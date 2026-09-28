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
	"github.com/bborbe/attention-controller/pkg/buildidentity"
	"github.com/bborbe/attention-controller/pkg/factory"
)

func main() {
	app := &application{}
	os.Exit(service.Main(context.Background(), app, &app.SentryDSN, &app.SentryProxy))
}

type application struct {
	// Optional, unlike the notification-controller precedent: Sentry is error
	// reporting for a deployed stage, and requiring it made a local run
	// impossible without a teamvault-resolved DSN. An empty DSN disables error
	// reporting rather than failing startup. This repo has no deployed stage
	// yet, so nothing here depends on the flag; a future deploy supplies
	// SENTRY_DSN from its own secret.
	SentryDSN         string `required:"false" arg:"sentry-dsn"          env:"SENTRY_DSN"          usage:"SentryDSN (empty disables error reporting)"                                                                                 display:"length"`
	SentryProxy       string `required:"false" arg:"sentry-proxy"        env:"SENTRY_PROXY"        usage:"Sentry Proxy"`
	Listen            string `required:"true"  arg:"listen"              env:"LISTEN"              usage:"address to listen to"`
	DataDir           string `required:"true"  arg:"datadir"             env:"DATADIR"             usage:"data directory"`
	HeartbeatWindow   string `required:"false" arg:"heartbeat-window"    env:"HEARTBEAT_WINDOW"    usage:"how stale a heartbeat:<path> mtime may be before the producer counts as finished"                                                            default:"15m"`
	SessionsDir       string `required:"false" arg:"sessions-dir"        env:"SESSIONS_DIR"        usage:"directory holding the session registry used to resolve session:<id> liveness"`
	AttentionStateDir string `required:"false" arg:"attention-state-dir" env:"ATTENTION_STATE_DIR" usage:"directory holding the producers' event logs the page resolves item provenance from"`
	// VaultDir is the directory holding the vault whose task files record the
	// session each task belongs to. ⚠️ Deliberately without a `default:`, unlike
	// SessionsDir and AttentionStateDir: an unset vault is a legitimate state,
	// and defaulting it would point the board at a guessed path instead of
	// simply rendering no task names.
	VaultDir string `required:"false" arg:"vault-dir"           env:"VAULT_DIR"           usage:"directory holding the vault whose task files record the session each task belongs to (empty renders no task names)"`
	// JumpListen is the address of the legacy pane-addressed jump listener.
	//
	// ⚠️ It is NOT the fleet-jump server's origin any more — that server is
	// being retired by the fold, and this process now serves the route itself on
	// the port its callers already use. `claude-supervisor/scripts/jump-link.py`
	// emits `http://127.0.0.1:1337/jump?pane=<N>&t=<token>` into nine manager
	// surfaces, so the default keeps those links working with no change on their
	// side; that is what makes this a one-service fold rather than a cross-repo
	// migration.
	//
	// ⚠️ Empty disables the legacy listener entirely. That is the switch for the
	// day the last consumer is re-pointed, and it is a configuration change
	// rather than a code change on purpose.
	JumpListen string `required:"false" arg:"jump-listen"         env:"JUMP_LISTEN"         usage:"address of the legacy pane-addressed jump listener (empty disables it)"                                                                      default:"127.0.0.1:1337"`
	// JumpTokenPath is the file holding the jump token the legacy pane-addressed
	// route requires.
	// Empty resolves to ~/.claude/secrets/jump-token, the same path
	// claude-supervisor's jump-link.py reads, so the two surfaces cannot drift
	// onto different tokens. ⚠️ A credential: never logged, never rendered.
	//
	// ⚠️ The field holds the token's *path*, not the token, so it is not itself
	// secret material. It carries display:"length" anyway: argument.Parse()
	// dumps the config at startup, the field name matches the secret-shaped
	// rule, and the tag costs nothing but a less useful startup line. Both the
	// local review funnel and the bot flagged the omission, and a tag that is
	// correct for a credential-adjacent field is the cheaper default.
	JumpTokenPath string `required:"false" arg:"jump-token-path"     env:"JUMP_TOKEN_PATH"     usage:"file holding the jump token the legacy pane-addressed jump route requires (empty resolves to ~/.claude/secrets/jump-token)" display:"length"`
	// TTSURL is the tts server's base URL. Optional: with no value the
	// read-aloud route is not registered and the page renders no read-aloud
	// control, so a host without a tts server serves the same page minus one
	// control rather than one that always fails.
	TTSURL          string            `required:"false" arg:"tts-url"             env:"TTS_URL"             usage:"base URL of the tts server the board's read-aloud control forwards to (empty disables it)"                                                   default:"http://127.0.0.1:12000"`
	BuildGitVersion string            `required:"false" arg:"build-git-version"   env:"BUILD_GIT_VERSION"   usage:"Build Git version"                                                                                                                           default:"dev"`
	BuildGitCommit  string            `required:"false" arg:"build-git-commit"    env:"BUILD_GIT_COMMIT"    usage:"Build Git commit hash"                                                                                                                       default:"none"`
	BuildDate       *libtime.DateTime `required:"false" arg:"build-date"          env:"BUILD_DATE"          usage:"Build timestamp (RFC3339)"`
}

func (a *application) Run(ctx context.Context, sentryClient libsentry.Client) error {
	libmetrics.NewBuildInfoMetrics().SetBuildInfo(a.BuildGitVersion, a.BuildGitCommit, a.BuildDate)

	db, err := libboltkv.OpenDir(ctx, a.DataDir)
	if err != nil {
		return errors.Wrap(ctx, err, "open db failed")
	}
	defer db.Close()

	rawStore, err := a.createAttentionStore(ctx, db)
	if err != nil {
		return err
	}

	// One notifier, passed both ways: wrapped into the store so a successful
	// write signals it, and into the server so the board's live channel can
	// subscribe to it. Two instances would be a channel nothing writes to.
	notifier := pkg.NewAttentionChangeNotifier()
	store := pkg.NewNotifyingAttentionStore(rawStore, notifier)

	return service.Run(
		ctx,
		a.createHTTPServer(sentryClient, db, store, notifier),
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
// ⚠️ All three directories are optional and an unresolved one is not fatal. A
// store running for k8s agents, cron jobs or dark-factory runs has neither a
// Claude Code state directory nor WezTerm, and it must still serve every item:
// the resolver reads nothing, every row renders with no provenance line, and
// the page is exactly what it was before this change. That degradation is the
// honest scoping of the page's standalone claim — it still works without Claude
// Code, but it works with provenance absent rather than without looking.
//
// The vault is the same story: no vault configured means no task name resolved,
// not a startup failure.
func (a *application) createProvenanceResolver(
	ctx context.Context,
	panes pkg.PaneLister,
) pkg.ProvenanceResolver {
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
	// ⚠️ The task index is built here, once, and handed to the resolver — never
	// built per page. The vault holds thousands of task files, and the page is
	// served continuously by the SSE stream.
	return pkg.NewProvenanceResolver(
		stateDir,
		sessionsDir,
		panes,
		pkg.NewTaskIndex(ctx, a.VaultDir),
	)
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

// defaultJumpTokenPath resolves ~/.claude/secrets/jump-token, the file
// claude-supervisor's jump-link.py reads. The path is shared rather than
// duplicated so the board and the managers' handovers cannot drift onto
// different tokens.
func defaultJumpTokenPath(ctx context.Context) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.Wrap(ctx, err, "resolve home dir failed")
	}
	return filepath.Join(home, ".claude", "secrets", "jump-token"), nil
}

// createJumpTokenReader builds the reader the page and the redirect share.
//
// ⚠️ An unresolvable path is not fatal. The page then renders no Jump button
// and the redirect refuses, which is the same state as a host with no
// fleet-jump server — taking the store down over an optional affordance would
// be the wrong trade, exactly as it is for the provenance directories.
func (a *application) createJumpTokenReader(ctx context.Context) pkg.JumpTokenReader {
	path := a.JumpTokenPath
	if path == "" {
		resolved, err := defaultJumpTokenPath(ctx)
		if err != nil {
			glog.Warningf("resolve jump token path failed: %v", err)
		}
		path = resolved
	}
	return pkg.NewJumpTokenReader(path)
}

// createPaneActivator builds the capability both jump routes go through.
//
// ⚠️ The lister is passed in rather than built here, because the same listing
// already serves the provenance resolver: two listers would be two subprocess
// paths whose failure semantics could drift, and the activator's whole
// correctness rests on resolving a pane against the LIVE list before touching
// the terminal.
func (a *application) createPaneActivator(panes pkg.PaneLister) pkg.PaneActivator {
	return pkg.NewWeztermPaneActivator(panes)
}

func (a *application) createHTTPServer(
	sentryClient libsentry.Client,
	db libkv.DB,
	store pkg.AttentionStore,
	notifier pkg.AttentionChangeNotifier,
) run.Func {
	return func(ctx context.Context) error {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		// Hoisted rather than inlined into the handler call below, matching how
		// the store is built once in Run and passed down. Two constructors
		// nested at a call site read as wiring that happened by accident.
		//
		// ⚠️ One pane lister, two consumers. The provenance resolver reads it to
		// decide whether a row has a jump target, and the activator reads it to
		// refuse a stale pane id before touching the terminal. Two listers would
		// be two subprocess paths whose failure semantics could drift — and the
		// activator's correctness rests on resolving against the LIVE list.
		panes := pkg.NewWeztermPaneLister()
		provenance := a.createProvenanceResolver(ctx, panes)
		jumpTokens := a.createJumpTokenReader(ctx)
		activator := a.createPaneActivator(panes)

		// Read once, from the binary's own build info, and handed to the page so
		// its footer can answer "which build am I looking at" from the surface the
		// operator is already reading.
		//
		// ⚠️ Read HERE rather than inside the handler, for the same reason the
		// store is built once in Run: it is a constant of the process, and a
		// handler that re-read it per request would re-derive a constant while
		// making the footer untestable.
		//
		// ⚠️ Deliberately NOT read from the repo. A repo read at render time
		// answers "which commit is the checkout at" — a different question, and
		// one that agrees with the binary right up until the two diverge, which is
		// exactly when the footer has to be right.
		buildIdentity := buildidentity.Read()

		router := mux.NewRouter()
		registerAdminRoutes(ctx, router, db, cancel, sentryClient)
		// The Jump button's target: a path on this board, answered in-process.
		// Registered ahead of the page's own route, and GET/HEAD only, because a
		// navigation rather than a business call.
		router.Path("/jump/{itemID}").
			Methods(http.MethodGet, http.MethodHead).
			Handler(factory.CreateAttentionJumpHandler(store, provenance, activator))
		// The attention page sits at / rather than under /api/1.0/ because it
		// renders HTML for a human rather than JSON for an API client — it is
		// the store's operator-facing surface, not a business endpoint. It is
		// read-only, so GET and HEAD are the only methods routed here; without
		// .Methods, gorilla mux would route POST and DELETE to it as well.
		router.Path("/").
			Methods(http.MethodGet, http.MethodHead).
			Handler(factory.CreateAttentionPageHandler(
				store, provenance, a.TTSURL != "", a.VaultDir, buildIdentity))

		// The board's live channel. It is registered here, ahead of the
		// `/api/1.0/attention/{itemID}` route below, because gorilla mux matches
		// in registration order: registered after it, this path would resolve as
		// an item whose id is literally `stream`.
		router.Path("/api/1.0/attention/stream").
			Methods(http.MethodGet).
			Handler(factory.CreateAttentionStreamHandler(
				store,
				notifier,
				provenance,
				a.TTSURL != "",
				a.VaultDir,
			))

		// Business routes live under /api/1.0/, never in the admin block above.
		// The push entry point takes a producer's declaration; nothing scrapes
		// a pane, a hook event or a rendered closer line.
		router.Path("/api/1.0/attention").
			Methods(http.MethodPost).
			Handler(factory.CreateAttentionPushHandler(store))
		router.Path("/api/1.0/attention").
			Methods(http.MethodGet).
			Handler(factory.CreateAttentionReadHandler(store))
		// Every item regardless of state, for counting what resolved and what
		// escalated. Registered before /{itemID} so "history" is never read as
		// an item id.
		router.Path("/api/1.0/attention/history").
			Methods(http.MethodGet).
			Handler(factory.CreateAttentionHistoryHandler(store))
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
		// Read-aloud is routed only when a tts server is configured, and the page
		// renders its control on the same condition. A control that renders while
		// its endpoint is unrouted is a value presented as working that is not.
		if a.TTSURL != "" {
			router.Path("/api/1.0/attention/{itemID}/speak").
				Methods(http.MethodPost).
				Handler(factory.CreateAttentionSpeakHandler(store, a.TTSURL))
			// The stop half of the same control, on the same condition: a
			// toggle whose stop endpoint is unrouted is a control that looks
			// like it can be stopped and cannot.
			router.Path("/api/1.0/attention/{itemID}/cancel").
				Methods(http.MethodPost).
				Handler(factory.CreateAttentionCancelHandler(a.TTSURL))
		}
		// Single-item read, distinct from the render path above: an arm reads
		// open items, but a caller checking a transition's outcome (or the
		// loser of an answer or escalation race reading back) needs the item
		// whatever state it is in.
		router.Path("/api/1.0/attention/{itemID}").
			Methods(http.MethodGet).
			Handler(factory.CreateAttentionGetHandler(store))

		// ⚠️ Two listeners, one process — this is the fold's whole claim, and it
		// is why SC1's evidence is `lsof` naming ONE pid on both ports. Both run
		// under one context, so a failure in either takes the process down and
		// launchd restarts both: a half-up state — board serving, jumps dead — is
		// exactly the two-lifecycle problem the fold exists to remove.
		runner := run.NewConcurrentRunner(2)
		defer runner.Close()

		glog.V(2).Infof("starting http server listen on %s", a.Listen)
		runner.Add(ctx, libhttp.NewServer(a.Listen, router).Run)
		if err := a.addLegacyJumpListener(ctx, runner, jumpTokens, activator); err != nil {
			return err
		}

		return runner.Run(ctx)
	}
}

// registerAdminRoutes wires the board router's admin and diagnostics endpoints.
//
// ⚠️ Extracted from createHTTPServer for length alone — nothing here is new, and
// the grouping is the repo's own: `/healthz`, `/readiness`, `/metrics`, the
// store's reset routes, the log-level switch, `/gc` and the two probe handlers
// are operational surfaces rather than business ones. Keeping them out of the
// business-route block below is what makes the two kinds of route tellable
// apart at a glance, and it is why `/api/1.0/...` starts where it does.
func registerAdminRoutes(
	ctx context.Context,
	router *mux.Router,
	db libkv.DB,
	cancel context.CancelFunc,
	sentryClient libsentry.Client,
) {
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
}

// addLegacyJumpListener registers the pane-addressed jump server, when one is
// configured.
//
// It is its own method rather than a block inside createHTTPServer because that
// function is already at the linter's length bound, and because the listener is
// a distinct concern: the board's router and this one share a process, an
// activator and a context, and nothing else.
//
// ⚠️ An empty address disables the listener, and that is a supported
// configuration rather than a degenerate one — it is the switch for the day the
// last `jump-link.py` consumer is re-pointed. It is logged rather than silent,
// so a jump link that stops working is traceable to the setting that disabled
// it instead of to a bug.
func (a *application) addLegacyJumpListener(
	ctx context.Context,
	runner run.ConcurrentRunner,
	jumpTokens pkg.JumpTokenReader,
	activator pkg.PaneActivator,
) error {
	if a.JumpListen == "" {
		glog.Warningf("legacy jump listener disabled (jump-listen is empty)")
		return nil
	}
	// A separate router, not the board's: a pane-addressed GET that any visited
	// page can fire has no business sharing an origin with the board's own API.
	// `/health` is carried over because the Python server it replaces answered
	// it, and a listener with no liveness probe is one that cannot be observed.
	legacyRouter := mux.NewRouter()
	legacyRouter.Path("/health").Handler(factory.CreateLegacyHealthHandler())
	legacyRouter.Path("/jump").
		Methods(http.MethodGet, http.MethodHead).
		Handler(factory.CreateLegacyJumpHandler(jumpTokens, activator))
	glog.V(2).Infof("starting legacy jump server listen on %s", a.JumpListen)
	runner.Add(ctx, libhttp.NewServer(a.JumpListen, legacyRouter).Run)
	return nil
}
