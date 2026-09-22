// Copyright (c) 2025 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"time"

	libboltkv "github.com/bborbe/boltkv"
	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"
	libkv "github.com/bborbe/kv"
	"github.com/bborbe/log"
	libmetrics "github.com/bborbe/metrics"
	"github.com/bborbe/run"
	libsentry "github.com/bborbe/sentry"
	"github.com/bborbe/service"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/factory"
)

func main() {
	app := &application{}
	os.Exit(service.Main(context.Background(), app, &app.SentryDSN, &app.SentryProxy))
}

type application struct {
	// Optional, unlike the notification-controller precedent: Sentry is error
	// reporting for the deployed stage, and requiring it made a local run
	// impossible without a teamvault-resolved DSN. An empty DSN disables error
	// reporting rather than failing startup. The deployed manifests still
	// supply SENTRY_DSN from the secret, so prod behaviour is unchanged.
	SentryDSN         string            `required:"false" arg:"sentry-dsn"          env:"SENTRY_DSN"          usage:"SentryDSN (empty disables error reporting)"                                         display:"length"`
	SentryProxy       string            `required:"false" arg:"sentry-proxy"        env:"SENTRY_PROXY"        usage:"Sentry Proxy"`
	Listen            string            `required:"true"  arg:"listen"              env:"LISTEN"              usage:"address to listen to"`
	DataDir           string            `required:"true"  arg:"datadir"             env:"DATADIR"             usage:"data directory"`
	HeartbeatWindow   string            `required:"false" arg:"heartbeat-window"    env:"HEARTBEAT_WINDOW"    usage:"how stale a heartbeat:<path> mtime may be before the producer counts as finished"                    default:"15m"`
	SessionsDir       string            `required:"false" arg:"sessions-dir"        env:"SESSIONS_DIR"        usage:"directory holding the session registry used to resolve session:<id> liveness"`
	AttentionStateDir string            `required:"false" arg:"attention-state-dir" env:"ATTENTION_STATE_DIR" usage:"directory holding the producers' event logs the page resolves item provenance from"`
	BuildGitVersion   string            `required:"false" arg:"build-git-version"   env:"BUILD_GIT_VERSION"   usage:"Build Git version"                                                                                   default:"dev"`
	BuildGitCommit    string            `required:"false" arg:"build-git-commit"    env:"BUILD_GIT_COMMIT"    usage:"Build Git commit hash"                                                                               default:"none"`
	BuildDate         *libtime.DateTime `required:"false" arg:"build-date"          env:"BUILD_DATE"          usage:"Build timestamp (RFC3339)"`
}

func (a *application) Run(ctx context.Context, sentryClient libsentry.Client) error {
	libmetrics.NewBuildInfoMetrics().SetBuildInfo(a.BuildGitVersion, a.BuildGitCommit, a.BuildDate)

	db, err := libboltkv.OpenDir(ctx, a.DataDir)
	if err != nil {
		return errors.Wrap(ctx, err, "open db failed")
	}
	defer db.Close()

	store, err := a.createAttentionStore(ctx, db)
	if err != nil {
		return err
	}

	return service.Run(
		ctx,
		a.createHTTPServer(sentryClient, db, store),
	)

}

// createAttentionStore builds the attention store.
//
// ⚠️ Two values here resolve recorded schema silences rather than reading them
// from the schema: the heartbeat window (the schema requires the check but
// declares no field carrying it) and the session registry the session model is
// resolved against (the schema names no source). Both are documented on
// [[Attention Item Schema]] § Silences found while implementing, and both are
// injectable so a later step can replace them without touching the store.
func (a *application) createAttentionStore(
	ctx context.Context,
	db libkv.DB,
) (pkg.AttentionStore, error) {
	heartbeatWindow, err := libtime.ParseDuration(ctx, a.HeartbeatWindow)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "parse heartbeat window '%s' failed", a.HeartbeatWindow)
	}
	sessionsDir := a.SessionsDir
	if sessionsDir == "" {
		sessionsDir, err = defaultSessionsDir(ctx)
		if err != nil {
			return nil, err
		}
	}
	return pkg.NewAttentionStore(
		db,
		pkg.NewItemIDGenerator(),
		pkg.NewSessionLivenessChecker(sessionsDir),
		libtime.NewCurrentDateTime(),
		*heartbeatWindow,
	), nil
}

// defaultSessionsDir resolves ~/.claude/sessions.
func defaultSessionsDir(ctx context.Context) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.Wrap(ctx, err, "resolve home dir failed")
	}
	return filepath.Join(home, ".claude", "sessions"), nil
}

// createProvenanceResolver builds the resolver the page joins item provenance
// from.
//
// ⚠️ Both directories are optional and an unresolved one is not fatal. A store
// running for k8s agents, cron jobs or dark-factory runs has neither a Claude
// Code state directory nor WezTerm, and it must still serve every item: the
// resolver reads nothing, every row renders with no provenance line, and the
// page is exactly what it was before this change. That degradation is the
// honest scoping of the page's standalone claim — it still works without Claude
// Code, but it works with provenance absent rather than without looking.
func (a *application) createProvenanceResolver(ctx context.Context) pkg.ProvenanceResolver {
	stateDir := a.AttentionStateDir
	if stateDir == "" {
		resolved, err := defaultAttentionStateDir(ctx)
		if err != nil {
			// Not fatal: an unresolvable home directory means no provenance
			// source, which is the same state as a host that has none.
			glog.Warningf("resolve attention state dir failed: %v", err)
		}
		stateDir = resolved
	}
	sessionsDir := a.SessionsDir
	if sessionsDir == "" {
		resolved, err := defaultSessionsDir(ctx)
		if err != nil {
			glog.Warningf("resolve sessions dir failed: %v", err)
		}
		sessionsDir = resolved
	}
	return pkg.NewProvenanceResolver(stateDir, sessionsDir, pkg.NewWeztermPaneLister())
}

// defaultAttentionStateDir resolves ~/.claude/state/attention, the directory
// holding each producer's event log.
func defaultAttentionStateDir(ctx context.Context) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.Wrap(ctx, err, "resolve home dir failed")
	}
	return filepath.Join(home, ".claude", "state", "attention"), nil
}

func (a *application) createHTTPServer(
	sentryClient libsentry.Client,
	db libkv.DB,
	store pkg.AttentionStore,
) run.Func {
	return func(ctx context.Context) error {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		// Hoisted rather than inlined into the handler call below, matching how
		// the store is built once in Run and passed down. Two constructors
		// nested at a call site read as wiring that happened by accident.
		provenance := a.createProvenanceResolver(ctx)

		router := mux.NewRouter()
		router.Path("/healthz").Handler(factory.CreateHealthzHandler())
		router.Path("/readiness").Handler(libhttp.NewPrintHandler("OK"))
		router.Path("/metrics").Handler(promhttp.Handler())
		router.Path("/resetdb").Handler(libkv.NewResetHandler(db, cancel))
		router.Path("/resetbucket/{BucketName}").Handler(libkv.NewResetBucketHandler(db, cancel))
		router.Path("/setloglevel/{level}").
			Handler(log.NewSetLoglevelHandler(ctx, log.NewLogLevelSetter(2, 5*time.Minute)))
		router.Path("/gc").Handler(libhttp.NewGarbageCollectorHandler())
		router.Path("/testloglevel").Handler(factory.CreateTestLoglevelHandler())
		router.Path("/sentryalert").Handler(factory.CreateSentryAlertHandler(sentryClient))
		// The attention page sits at / rather than under /api/1.0/ because it
		// renders HTML for a human rather than JSON for an API client — it is
		// the store's operator-facing surface, not a business endpoint. It is
		// read-only, so GET and HEAD are the only methods routed here; without
		// .Methods, gorilla mux would route POST and DELETE to it as well.
		router.Path("/").
			Methods(http.MethodGet, http.MethodHead).
			Handler(factory.CreateAttentionPageHandler(store, provenance))

		// Business routes live under /api/1.0/, never in the admin block above.
		// The push entry point takes a producer's declaration; nothing scrapes
		// a pane, a hook event or a rendered closer line.
		router.Path("/api/1.0/attention").
			Methods(http.MethodPost).
			Handler(factory.CreateAttentionPushHandler(store))
		router.Path("/api/1.0/attention").
			Methods(http.MethodGet).
			Handler(factory.CreateAttentionReadHandler(store))
		router.Path("/api/1.0/attention/{itemID}/answer").
			Methods(http.MethodPost).
			Handler(factory.CreateAttentionAnswerHandler(store))
		// Escalation is not a transition: the item stays open, and this route
		// records which session is carrying it. First to stamp wins; the loser
		// reads the item back rather than stamping over it.
		router.Path("/api/1.0/attention/{itemID}/escalate").
			Methods(http.MethodPost).
			Handler(factory.CreateAttentionEscalateHandler(store))
		router.Path("/api/1.0/attention/{itemID}/close").
			Methods(http.MethodPost).
			Handler(factory.CreateAttentionCloseHandler(store))
		// Single-item read, distinct from the render path above: an arm reads
		// open items, but a caller checking a transition's outcome (or the
		// loser of an answer or escalation race reading back) needs the item
		// whatever state it is in.
		router.Path("/api/1.0/attention/{itemID}").
			Methods(http.MethodGet).
			Handler(factory.CreateAttentionGetHandler(store))

		glog.V(2).Infof("starting http server listen on %s", a.Listen)
		return libhttp.NewServer(
			a.Listen,
			router,
		).Run(ctx)
	}
}
