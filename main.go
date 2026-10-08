// Copyright (c) 2025 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"net"
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
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/boardmetrics"
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
	SentryDSN       string `required:"false" arg:"sentry-dsn"               env:"SENTRY_DSN"               usage:"SentryDSN (empty disables error reporting)"                                                                                                            display:"length"`
	SentryProxy     string `required:"false" arg:"sentry-proxy"             env:"SENTRY_PROXY"             usage:"Sentry Proxy"`
	Listen          string `required:"true"  arg:"listen"                   env:"LISTEN"                   usage:"address to listen to"`
	DataDir         string `required:"true"  arg:"datadir"                  env:"DATADIR"                  usage:"data directory"`
	HeartbeatWindow string `required:"false" arg:"heartbeat-window"         env:"HEARTBEAT_WINDOW"         usage:"how stale a heartbeat:<path> mtime may be before the producer counts as finished"                                                                                       default:"15m"`
	// SessionHeartbeatWindow is how stale a SESSION heartbeat may be before the
	// session counts as gone. ⚠️ It is deliberately NOT HeartbeatWindow, and
	// the two must not be collapsed onto one number: this one bounds how long a
	// killed session can still read Live (the board's `Stale Nm` card depends
	// on it), while HeartbeatWindow protects a cron job that runs every ten
	// minutes from being declared dead. One number cannot serve both.
	//
	// 60s against the 30s post interval is one whole missed tick of slack.
	SessionHeartbeatWindow string `required:"false" arg:"session-heartbeat-window" env:"SESSION_HEARTBEAT_WINDOW" usage:"how stale a session heartbeat may be before the session counts as gone"                                                                                                 default:"60s"`
	// SessionHeartbeatDir is the heartbeat store directory. ⚠️ Empty resolves
	// to the EXISTING supervisor store (`~/.local/state/claude-supervisor/live`)
	// rather than to a new one — this endpoint extends that store, and the
	// supervisor scripts keep reading the same files.
	SessionHeartbeatDir string `required:"false" arg:"session-heartbeat-dir"    env:"SESSION_HEARTBEAT_DIR"    usage:"directory holding the session heartbeat store (empty resolves to the supervisor's existing store)"`
	AnsweredMaxAge      string `required:"false" arg:"answered-max-age"         env:"ANSWERED_MAX_AGE"         usage:"how old an answered item may be before the store closes it; must stay well above the slowest consumer's poll interval"                                                  default:"1h"`
	SessionsDir         string `required:"false" arg:"sessions-dir"             env:"SESSIONS_DIR"             usage:"directory holding the session registry used to resolve session:<id> liveness"`
	// SessionLiveness selects how producer liveness is resolved, and therefore
	// whether the store prunes an item whose producer it cannot find.
	//
	// ⚠️ `registry` (the default) is the host-local behaviour: the registry
	// directory is read and an item whose producer is gone is pruned. `off` wires
	// a checker that reports every session live, so nothing is ever pruned — the
	// cluster backend's mode, where the registry names sessions on one host and
	// every producer elsewhere reads as gone.
	//
	// ⚠️ An unrecognised value is a startup error, never a silent fallback. The
	// two modes differ in whether stored data is deleted, so guessing is the one
	// answer that is wrong whichever way it guesses.
	SessionLiveness   string `required:"false" arg:"session-liveness"         env:"SESSION_LIVENESS"         usage:"how producer liveness is resolved: 'registry' prunes an item whose producer is gone from the session registry; 'off' never prunes (a cluster backend)"                  default:"registry"`
	AttentionStateDir string `required:"false" arg:"attention-state-dir"      env:"ATTENTION_STATE_DIR"      usage:"directory holding the producers' event logs the page resolves item provenance from"`
	SpawnStateDir     string `required:"false" arg:"spawn-state-dir"          env:"SPAWN_STATE_DIR"          usage:"directory holding the supervisor's spawn ledger the page reads each session's headless/interactive mode from"`
	// VaultDir is the directory holding the vault whose task files record the
	// session each task belongs to. ⚠️ Deliberately without a `default:`, unlike
	// SessionsDir and AttentionStateDir: an unset vault is a legitimate state,
	// and defaulting it would point the board at a guessed path instead of
	// simply rendering no task names.
	VaultDir string `required:"false" arg:"vault-dir"                env:"VAULT_DIR"                usage:"directory holding the vault whose task files record the session each task belongs to (empty renders no task names)"`
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
	JumpListen string `required:"false" arg:"jump-listen"              env:"JUMP_LISTEN"              usage:"address of the legacy pane-addressed jump listener (empty disables it)"                                                                                                 default:"127.0.0.1:1337"`
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
	JumpTokenPath string `required:"false" arg:"jump-token-path"          env:"JUMP_TOKEN_PATH"          usage:"file holding the jump token the legacy pane-addressed jump route requires (empty resolves to ~/.claude/secrets/jump-token)"                            display:"length"`
	// AttentionStoreListen is the address of the second, cluster-reachable
	// listener. It serves the business API only, and every request to it must
	// carry the bearer token in AttentionStoreToken.
	//
	// ⚠️ Empty disables the listener entirely, which is a supported
	// configuration rather than a degenerate one — the same switch jump-listen
	// has, and the store then serves exactly as it does today. It is logged
	// rather than silent, so a client that stops reaching the store is traceable
	// to the setting that disabled it instead of to a bug.
	AttentionStoreListen string `required:"false" arg:"attention-store-listen"   env:"ATTENTION_STORE_LISTEN"   usage:"address of the second listener serving the business API behind a bearer token (empty disables it)"`
	// AttentionStoreToken is the bearer token every request to the second
	// listener must present.
	//
	// ⚠️ A credential: never logged, never rendered. display:"length" makes
	// argument.Parse()'s startup dump print this field's length rather than its
	// value.
	// ⚠️ Empty disables the second listener entirely — the store never serves
	// the API unauthenticated on a non-loopback address. That is the
	// fail-closed default, not a degenerate configuration.
	AttentionStoreToken string `required:"false" arg:"attention-store-token"    env:"ATTENTION_STORE_TOKEN"    usage:"bearer token every request to the second listener must present (empty disables the listener)"                                                          display:"length"`
	// AttentionUpstreamURL is the base URL of a PEER attention store whose open
	// items this instance merges into its own board.
	//
	// ⚠️ Empty disables federation entirely, which is a supported configuration
	// rather than a degenerate one: an instance with no peer serves exactly the
	// board it serves today. It is logged rather than silent, so a card that
	// stops appearing is traceable to the setting that disabled it instead of to
	// a bug — the same switch AttentionStoreListen has.
	//
	// ⚠️ It is the peer's BUSINESS API listener — the bearer-gated one — not its
	// board. The board listener renders HTML and is deliberately not exposed; the
	// business API is the surface a token can gate.
	AttentionUpstreamURL string `required:"false" arg:"attention-upstream-url"   env:"ATTENTION_UPSTREAM_URL"   usage:"base URL of a peer attention store whose open items are merged into this instance's board (empty disables federation)"`
	// AttentionUpstreamToken is the bearer token the peer's business API
	// requires.
	//
	// ⚠️ A credential: never logged, never rendered. display:"length" makes
	// argument.Parse()'s startup dump print this field's length rather than its
	// value.
	//
	// ⚠️ There is deliberately NO arg: tag. Its sibling AttentionStoreToken has
	// one, and the review of the change that added it flagged the consequence: an
	// argv-passed token is readable in the process's ps output by every local
	// user. This field has no operational need for a flag — the peer's address
	// and token are configured together in the deployment — so it is env-only
	// rather than repeating a known exposure for symmetry's sake.
	//
	// ⚠️ Empty disables federation, exactly as an empty URL does. The two halves
	// are required TOGETHER: a URL with no token would call a gated API that
	// answers 401, which is a configured-looking instance that silently federates
	// nothing.
	AttentionUpstreamToken string `required:"false"                                env:"ATTENTION_UPSTREAM_TOKEN" usage:"bearer token the peer attention store's business API requires (empty disables federation)"                                                             display:"length"`
	// TTSURL is the tts server's base URL. Optional: with no value the
	// read-aloud route is not registered and the page renders no read-aloud
	// control, so a host without a tts server serves the same page minus one
	// control rather than one that always fails.
	TTSURL          string            `required:"false" arg:"tts-url"                  env:"TTS_URL"                  usage:"base URL of the tts server the board's read-aloud control forwards to (empty disables it)"                                                                              default:"http://127.0.0.1:12000"`
	BuildGitVersion string            `required:"false" arg:"build-git-version"        env:"BUILD_GIT_VERSION"        usage:"Build Git version"                                                                                                                                                      default:"dev"`
	BuildGitCommit  string            `required:"false" arg:"build-git-commit"         env:"BUILD_GIT_COMMIT"         usage:"Build Git commit hash"                                                                                                                                                  default:"none"`
	BuildDate       *libtime.DateTime `required:"false" arg:"build-date"               env:"BUILD_DATE"               usage:"Build timestamp (RFC3339)"`
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

	// ⚠️ The federation wraps the NOTIFYING store, not the other way round, and
	// the order is the whole of the correctness here. A write the federation
	// proxies to the peer changes nothing locally, so it must not wake this
	// instance's live views; with the notifier INSIDE the federation, a proxied
	// answer leaves the local store untouched and signals nothing, which is
	// exactly right. Reversed, every federated write would fire a board re-read
	// for a store that did not move.
	store := a.federate(pkg.NewNotifyingAttentionStore(rawStore, notifier))

	// Built once per process and injected, so the two counters are registered on
	// the default registry — the one /metrics serves — exactly once. A second
	// registration of the same collector panics.
	boardMetrics := boardmetrics.NewMetrics(prometheus.DefaultRegisterer)

	return service.Run(
		ctx,
		a.createHTTPServer(sentryClient, db, store, notifier, boardMetrics),
	)

}

// federate wraps the store so a peer attention store's items are visible on
// this instance's board, when a peer is configured.
//
// ⚠️ Both halves of the configuration are required together, and an empty
// either way disables the whole thing rather than half of it. A URL with no
// token would call a gated API that answers 401 — an instance that looks
// configured and federates nothing — so the two are checked as one setting.
//
// ⚠️ It is logged when disabled, mirroring addAttentionStoreAPIListener, so a
// card that stops appearing is traceable to the setting that disabled it
// instead of to a bug. The URL is logged; the token never is.
func (a *application) federate(store pkg.AttentionStore) pkg.AttentionStore {
	if a.AttentionUpstreamURL == "" || a.AttentionUpstreamToken == "" {
		glog.Warningf("attention federation disabled (upstream url or token is empty)")
		return store
	}
	glog.V(2).Infof("attention federation enabled, reading peer at %s", a.AttentionUpstreamURL)
	return pkg.NewFederatingAttentionStore(
		store,
		pkg.NewHTTPRemoteAttentionStore(a.AttentionUpstreamURL, a.AttentionUpstreamToken),
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
	livenessChecker, err := a.sessionLivenessChecker(ctx)
	if err != nil {
		return nil, err
	}
	return pkg.NewAttentionStore(
		db,
		pkg.NewItemIDGenerator(),
		livenessChecker,
		libtime.NewCurrentDateTime(),
		*heartbeatWindow,
	), nil
}

// createSessionHeartbeatStore builds the session-heartbeat store and resolves
// its window.
//
// ⚠️ The window is this store's OWN named setting and is deliberately not
// HeartbeatWindow. The two bound different things: this one bounds how long a
// killed session can still read Live, while HeartbeatWindow stops a cron job
// that runs every ten minutes from being declared dead. One number cannot serve
// both — see the config field.
func (a *application) createSessionHeartbeatStore(
	ctx context.Context,
) (pkg.SessionHeartbeatStore, libtime.Duration, error) {
	window, err := parseSessionHeartbeatWindow(ctx, a.SessionHeartbeatWindow)
	if err != nil {
		return nil, libtime.Duration(0), err
	}
	now := libtime.NewCurrentDateTime()
	if a.SessionHeartbeatDir != "" {
		return pkg.NewSessionHeartbeatStore(a.SessionHeartbeatDir, now), window, nil
	}
	store, err := pkg.NewSessionHeartbeatStoreFromEnv(ctx, now)
	if err != nil {
		return nil, libtime.Duration(0), err
	}
	return store, window, nil
}

// parseSessionHeartbeatWindow resolves the session-heartbeat staleness window
// and refuses a non-positive one.
//
// ⚠️ **A zero or negative window is a startup error, not a value to accept.**
// `libtime.ParseDuration` rejects unparseable input but accepts `0s` and `-5m`
// happily, and `IsFresh` compares `age <= window` — so either value reports
// EVERY row dead at once, which is a board showing every live session as
// finished. That is the same collapse the sibling `parseAnsweredMaxAge` refuses
// for its own flag, and the two duration flags in this file must hold the same
// line rather than one guarding and the other not.
//
// It is a function rather than inline statements, for the sibling's reason: the
// branch that rejects a non-positive bound is the highest-consequence line
// here, and a spec can reach it directly instead of standing up the whole HTTP
// path.
func parseSessionHeartbeatWindow(ctx context.Context, raw string) (libtime.Duration, error) {
	window, err := libtime.ParseDuration(ctx, raw)
	if err != nil {
		return 0, errors.Wrapf(ctx, err, "parse session heartbeat window '%s' failed", raw)
	}
	if *window <= 0 {
		return 0, errors.Errorf(
			ctx, "session heartbeat window must be positive, got '%s'", raw,
		)
	}
	return *window, nil
}

// registerSessionHeartbeatRoutes wires the session-heartbeat endpoints under
// /api/1.0/.
//
// ⚠️ The read is TWO routes and they answer different questions: the bare path
// lists every row (each carrying its own `live` flag, so a caller can count the
// live ones AND still see a session that recently died), while the
// /{sessionID} path answers one session and returns 404 for a row that does not
// exist. A caller that reads the 404 as `stale` collapses `absent` onto `stale`
// and renders a never-seen id the same as a killed session.
func registerSessionHeartbeatRoutes(
	router *mux.Router,
	store pkg.SessionHeartbeatStore,
	window libtime.Duration,
) {
	now := libtime.NewCurrentDateTime()
	router.Path("/api/1.0/session-heartbeat").
		Methods(http.MethodPost).
		Handler(factory.CreateSessionHeartbeatPostHandler(store))
	router.Path("/api/1.0/session-heartbeat").
		Methods(http.MethodGet).
		Handler(factory.CreateSessionHeartbeatListHandler(store, now, window))
	router.Path("/api/1.0/session-heartbeat/{sessionID}").
		Methods(http.MethodGet).
		Handler(factory.CreateSessionHeartbeatGetHandler(store, now, window))
}

// sessionLivenessChecker resolves the checker the store prunes through.
//
// ⚠️ `off` changes ONLY the store's checker. The page's provenance join reads
// the same registry through its own resolver and must keep working on a host,
// so the sessions-dir lookup is not short-circuited for the process — a cluster
// backend simply never consults it for liveness.
func (a *application) sessionLivenessChecker(
	ctx context.Context,
) (pkg.SessionLivenessChecker, error) {
	switch a.SessionLiveness {
	case "off":
		return pkg.NewAlwaysLiveSessionLivenessChecker(), nil
	case "", "registry":
		sessionsDir := a.SessionsDir
		if sessionsDir == "" {
			resolved, err := defaultSessionsDir(ctx)
			if err != nil {
				return nil, err
			}
			sessionsDir = resolved
		}
		return pkg.NewSessionLivenessChecker(sessionsDir), nil
	default:
		return nil, errors.Errorf(
			ctx,
			"unknown session-liveness '%s': expected 'registry' or 'off'",
			a.SessionLiveness,
		)
	}
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
	tasks pkg.TaskIndex,
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
	spawnDir := a.SpawnStateDir
	if spawnDir == "" {
		resolved, err := defaultSpawnStateDir(ctx)
		if err != nil {
			glog.Warningf("resolve spawn state dir failed: %v", err)
		}
		spawnDir = resolved
	}
	// ⚠️ The task index is built once in createHTTPServer and passed in here — it
	// is never built per page, and never a second time for the watcher. The vault
	// holds thousands of task files, and the page is served continuously by the
	// SSE stream, so the whole vault must not be re-read on every render. The
	// resolver and the watcher share ONE index, because two indexes would be two
	// independent maps that could disagree about the same session.
	return pkg.NewProvenanceResolver(
		pkg.NewEventLogReader(stateDir),
		sessionsDir,
		spawnDir,
		panes,
		tasks,
		libtime.NewCurrentDateTime(),
	)
}

// defaultSpawnStateDir resolves ~/.local/state/claude-supervisor/sessions, the
// directory holding the supervisor's spawn ledger.
func defaultSpawnStateDir(ctx context.Context) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.Wrap(ctx, err, "resolve home dir failed")
	}
	return filepath.Join(home, ".local", "state", "claude-supervisor", "sessions"), nil
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

// isLoopbackListen reports whether a `host:port` listen address binds to a
// loopback interface. It is the gate on mounting the pprof endpoints.
//
// ⚠️ An empty host (":18080") is NOT loopback — it binds every interface — and
// `net.SplitHostPort` returns it as "", so it is refused explicitly rather than
// read as "unspecified, therefore local". A malformed or unresolvable address is
// likewise false: the caller's failure mode for "cannot prove loopback" is to
// withhold the debug surface, not to expose it.
//
// ⚠️ The name `localhost` is RESOLVED rather than trusted. A hosts file or
// resolver mapping it to a routable address would otherwise make this report
// loopback while `libhttp.NewServer` binds publicly — publishing argv, which is
// the exact failure this gate exists to prevent. The lookup is affordable
// because registration happens once per process, not per request.
func isLoopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		resolved, resolveErr := net.ResolveIPAddr("ip", host)
		if resolveErr != nil {
			return false
		}
		ip = resolved.IP
	}
	return ip.IsLoopback()
}

// registerPprofIfLoopback mounts the pprof endpoints on router when listen is a
// loopback address, and reports whether it did.
//
// ⚠️ It is a named function rather than an inline `if` at the call site so the
// gate and the registration can be exercised together against a real router.
// The failure this guards is silent in both directions — a gate that wrongly
// reports "not loopback" disables profiling in production with only a log line,
// and a registration that silently no-ops looks identical to a working one
// until someone tries to take a profile.
func registerPprofIfLoopback(router *mux.Router, listen string) bool {
	if !isLoopbackListen(listen) {
		glog.Warningf(
			"pprof endpoints NOT mounted: listen address %q is not loopback, and /debug/pprof/cmdline would publish this process's argv (which can carry -attention-store-token) to every interface bound",
			listen,
		)
		return false
	}
	// Logged on the mounted branch too, so both halves of the gate are
	// diagnosable from default-verbosity logs — the skip already logs, and a
	// silent success reads identically to a registration that never ran.
	glog.Infof("pprof endpoints mounted on loopback listen address %q", listen)
	libhttp.RegisterPprof(router)
	return true
}

func (a *application) createHTTPServer(
	sentryClient libsentry.Client,
	db libkv.DB,
	store pkg.AttentionStore,
	notifier pkg.AttentionChangeNotifier,
	boardMetrics pkg.Metrics,
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
		panes := pkg.NewWeztermPaneLister(libtime.NewCurrentDateTime())
		// ⚠️ One index, two consumers: the resolver reads it on every render and
		// the watcher rebuilds it on every vault change. Built here rather than
		// inside createProvenanceResolver so the watcher below can be handed the
		// same instance — two indexes would be two independent maps that could
		// disagree about the same session.
		tasks := pkg.NewTaskIndex(ctx, a.VaultDir, libtime.NewCurrentDateTime())
		watcher := pkg.NewTaskIndexWatcher(tasks, a.VaultDir)
		provenance := a.createProvenanceResolver(ctx, panes, tasks)
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
		// pprof is mounted first, ahead of every business route. Gorilla mux
		// matches in registration order and these are PathPrefix routes: the `/`
		// page route below is an exact Path and so would not shadow them, but
		// registering the debug block first removes the question rather than
		// relying on that distinction holding as routes are added.
		//
		// ⚠️ Mounted ONLY on a loopback listen address. The endpoints are
		// unauthenticated, and `/cmdline` publishes this process's argv — and
		// `AttentionStoreToken` is declared `arg:"attention-store-token"`
		// alongside its env backing, so a plist passing the bearer token on argv
		// would publish it to whoever can reach the listener. Loopback binding is
		// what makes the debug surface acceptable, so an address that is not
		// loopback does not get it: it logs and skips rather than silently
		// widening the exposure. A non-loopback deployment can still take a
		// profile, but has to build an instrumented instance to do it — the cost
		// this gate accepts in exchange for never publishing argv.
		registerPprofIfLoopback(router, a.Listen)
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
				store, provenance, a.TTSURL != "", a.VaultDir, buildIdentity, boardMetrics))

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
				boardMetrics,
			))

		// Business routes live under /api/1.0/, never in the admin block above.
		registerAttentionAPIRoutes(router, store, a.TTSURL)

		// The session-heartbeat surface.
		//
		// ⚠️ It is registered on THIS router only, deliberately not inside
		// registerAttentionAPIRoutes. That function is shared with the
		// cluster-reachable bearer-token listener, and this task's scope is local
		// and headless liveness only — the store is a directory on this host, so
		// serving it on a remote-reachable listener would widen the surface
		// without widening what the store can actually answer. The pod task that
		// adds a non-Mac poster owns that decision.
		sessionHeartbeatStore, sessionHeartbeatWindow, err := a.createSessionHeartbeatStore(ctx)
		if err != nil {
			return err
		}
		registerSessionHeartbeatRoutes(router, sessionHeartbeatStore, sessionHeartbeatWindow)

		// ⚠️ Two listeners, one process — this is the fold's whole claim, and it
		// is why SC1's evidence is `lsof` naming ONE pid on both ports. Both run
		// under one context, so a failure in either takes the process down and
		// launchd restarts both: a half-up state — board serving, jumps dead — is
		// exactly the two-lifecycle problem the fold exists to remove.
		// Five slots, not two: the legacy jump listener is a second long-running
		// function under the same context, the bearer-token-gated business-API
		// listener is a third, the task index watcher is a fourth, and the
		// answered sweep is a fifth.
		//
		// ⚠️ It does NOT share the listeners' failure semantics, and saying so here
		// matters because the difference is deliberate: the sweep returns nil on
		// cancellation and logs-and-continues on a failed sweep, because a sweep
		// that fails costs decode work and loses nothing — taking the process down
		// for it would drop the board over something the board survives. The third
		// slot is for the goroutine, not for a shared failure path. The watcher's
		// slot is the same story: it returns nil on cancellation and logs-and-
		// continues when it cannot watch at all, because an unwatchable vault
		// costs a slower index and loses nothing.
		runner := run.NewConcurrentRunner(5)
		defer runner.Close()

		glog.V(2).Infof("starting http server listen on %s", a.Listen)
		runner.Add(ctx, libhttp.NewServer(a.Listen, router).Run)
		if err := a.addLegacyJumpListener(ctx, runner, jumpTokens, activator); err != nil {
			return err
		}
		if err := a.addAttentionStoreAPIListener(ctx, runner, store); err != nil {
			return err
		}
		runner.Add(ctx, watcher.Run)

		// ⚠️ This sweep is what keeps the live index bounded. The index admits
		// `answered` items — liveIndexWorthy is `State != ClosedState` — so an
		// answered item that is never closed is decoded on every read for the life
		// of the store. Measured 2026-10-03: 2,547 answered against 16 open, so
		// every read decoded ~2,563 items and ~3.4 MB to return 16, on an API every
		// supervisor polls twice every 2 s.
		answeredMaxAge, err := parseAnsweredMaxAge(ctx, a.AnsweredMaxAge)
		if err != nil {
			return err
		}
		runner.Add(ctx, runAnsweredSweep(store, answeredMaxAge, answeredSweepInterval))

		return runner.Run(ctx)
	}
}

// answeredMaxAgeCeiling is the largest max age the store accepts.
//
// ⚠️ The lower bound protects verdicts; this upper bound protects the invariant
// the flag exists for. The max age is what keeps the live index bounded, so a
// very large value reinstates exactly the unbounded index this change removes.
// At the measured arrival rate — 2,547 answered over ~9.5 days, ~11/h — a day
// leaves ~264 items in the index, which is still small; a year would leave
// ~96,000 and be indistinguishable from having no bound at all.
const answeredMaxAgeCeiling = 24 * time.Hour

// parseAnsweredMaxAge validates the configured answered max age.
//
// ⚠️ Both bounds are load-bearing and neither is a formality. The sweep computes
// its cutoff as now minus maxAge, so a zero or negative value makes EVERY
// answered item due on the first tick and closes the whole backlog at once —
// destroying every verdict a consumer had not read yet, which is the exact
// outcome the max age exists to prevent. And an unbounded value reinstates the
// unbounded live index the max age exists to remove.
//
// It is a function rather than four inline statements so the bounds can be
// tested directly: the branch that rejects a zero is the highest-consequence
// line in this change, and it is unreachable from the HTTP path a spec would
// otherwise have to stand up.
func parseAnsweredMaxAge(ctx context.Context, raw string) (libtime.Duration, error) {
	maxAge, err := libtime.ParseDuration(ctx, raw)
	if err != nil {
		return 0, errors.Wrapf(ctx, err, "parse answered max age '%s' failed", raw)
	}
	if *maxAge <= 0 {
		return 0, errors.Errorf(ctx, "answered max age must be positive, got '%s'", raw)
	}
	if time.Duration(*maxAge) > answeredMaxAgeCeiling {
		return 0, errors.Errorf(
			ctx,
			"answered max age '%s' exceeds the %s ceiling, which would reinstate the unbounded live index the bound exists to remove",
			raw,
			answeredMaxAgeCeiling,
		)
	}
	return *maxAge, nil
}

// answeredSweepInterval is how often the store is asked to close answered items
// past their max age. It is short because the sweep is cheap when nothing is due
// — it scans the live index, which the bound itself keeps small — and a long
// interval would let a burst of answers sit in the index for a whole interval
// after their age had passed.
const answeredSweepInterval = time.Minute

// answeredSweepTimeout bounds one sweep. It is well under the interval so a slow
// sweep cannot push one tick into the next, and far above the few milliseconds a
// sweep of the bounded index actually takes.
const answeredSweepTimeout = 30 * time.Second

// runAnsweredSweep closes answered items older than maxAge on a ticker.
//
// ⚠️ It runs in its own goroutine and calls the store's own Update — never a step
// inside a read, because the read path stays read-only.
// The interval is a parameter rather than the constant read directly, so a spec
// can drive the tick without waiting a minute for it — the loop's error-and-
// continue branch is otherwise unreachable in a test.
func runAnsweredSweep(
	store pkg.AttentionStore,
	maxAge libtime.Duration,
	interval time.Duration,
) run.Func {
	return func(ctx context.Context) error {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				// ⚠️ A deadline per tick, because the reasoning below assumes a sweep
				// always returns and a store call that never returns would make that
				// false: this loop would block for good, no further tick would fire,
				// ctx.Done() would be unreachable while the call is in flight, and the
				// bound this whole change installs would be silently disabled.
				//
				// It bounds what the store's own work may take and turns the failure
				// into a log line. It cannot pre-empt a bbolt transaction that has
				// already stopped honouring its context — bbolt checks the context as
				// it starts, not mid-commit — so for that one case a log is all that
				// can honestly be offered, and saying so is better than implying the
				// deadline guarantees the loop keeps ticking.
				sweepCtx, cancel := context.WithTimeout(ctx, answeredSweepTimeout)
				closed, err := store.SweepAnswered(sweepCtx, maxAge)
				cancel()
				if err != nil {
					// Logged, not fatal: a failed sweep costs decode work, it does
					// not lose an item, and taking the process down for it would
					// drop the board over something the board survives.
					glog.Errorf("sweep answered failed: %v", err)
					continue
				}
				if closed > 0 {
					glog.V(2).Infof(
						"swept %d answered items older than %s",
						closed,
						time.Duration(maxAge),
					)
				}
			}
		}
	}
}

// registerAttentionAPIRoutes wires the JSON business endpoints under
// /api/1.0/attention. The push entry point takes a producer's declaration;
// nothing scrapes a pane, a hook event or a rendered closer line.
//
// They are registered in one place because gorilla mux matches in registration
// order and the literal paths must precede the /{itemID} routes: registered
// after them, `/api/1.0/attention/history` would resolve as an item whose id is
// literally `history`. Keeping the block whole is what makes that ordering
// visible; scattering the calls across the caller is how it gets broken
// silently, and the failure is a 404 on a working endpoint rather than a
// compile error.
func registerAttentionAPIRoutes(
	router *mux.Router,
	store pkg.AttentionStore,
	ttsURL string,
) {
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
	// The delivery trail. The GET is the one query that answers "did this
	// item's answer reach the session?"; the POST is called by the arm that
	// ATTEMPTS delivery — never by the arm that records the answer, which
	// resolves a target and delivers nothing.
	router.Path("/api/1.0/attention/{itemID}/attempt").
		Methods(http.MethodGet).
		Handler(factory.CreateAttentionAttemptGetHandler(store))
	router.Path("/api/1.0/attention/{itemID}/attempt").
		Methods(http.MethodPost).
		Handler(factory.CreateAttentionAttemptRecordHandler(store))
	// Read-aloud is routed only when a tts server is configured, and the page
	// renders its control on the same condition. A control that renders while
	// its endpoint is unrouted is a value presented as working that is not.
	if ttsURL != "" {
		router.Path("/api/1.0/attention/{itemID}/speak").
			Methods(http.MethodPost).
			Handler(factory.CreateAttentionSpeakHandler(store, ttsURL))
		// The stop half of the same control, on the same condition: a
		// toggle whose stop endpoint is unrouted is a control that looks
		// like it can be stopped and cannot.
		router.Path("/api/1.0/attention/{itemID}/cancel").
			Methods(http.MethodPost).
			Handler(factory.CreateAttentionCancelHandler(ttsURL))
	}
	// Single-item read, distinct from the render path above: an arm reads
	// open items, but a caller checking a transition's outcome (or the
	// loser of an answer or escalation race reading back) needs the item
	// whatever state it is in.
	router.Path("/api/1.0/attention/{itemID}").
		Methods(http.MethodGet).
		Handler(factory.CreateAttentionGetHandler(store))
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

// createAttentionStoreAPIHandler builds the second, cluster-reachable listener's
// handler.
//
// ⚠️ The bearer check applies to every route it registers: reaching the business
// API on this listener requires the token. The routes come from
// registerAttentionAPIRoutes — the same function the board's own router calls —
// so the two listeners cannot drift on the route inventory or on its
// load-bearing ordering.
//
// ⚠️ The check is applied with router.Use rather than by wrapping the returned
// router. gorilla/mux's Use middleware runs only for a route that matched, so a
// request to a path this listener does not serve (`/`, `/metrics`, `/healthz`,
// `/readiness`) falls through to mux's not-found handler and answers 404.
// Wrapping the whole router would answer 401 for `/` instead, which would mean
// this listener advertised a board it does not serve.
func (a *application) createAttentionStoreAPIHandler(
	store pkg.AttentionStore,
	token string,
) http.Handler {
	router := mux.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return factory.CreateBearerTokenHandler(next, token)
	})
	registerAttentionAPIRoutes(router, store, a.TTSURL)
	return router
}

// addAttentionStoreAPIListener registers the second, cluster-reachable listener
// serving the business API behind a bearer token.
//
// ⚠️ An empty address or an empty token disables the listener, and both are
// supported configurations rather than degenerate ones: the store then serves
// exactly as it does today. The disable is logged rather than silent, so a
// client that stops reaching the store is traceable to the setting that disabled
// it instead of to a bug.
//
// ⚠️ The check is fail-closed: an unset token never falls back to serving the
// API unauthenticated on a non-loopback address. Both checks return before any
// runner.Add, so neither an unconfigured host nor a host with no token binds
// anything.
func (a *application) addAttentionStoreAPIListener(
	ctx context.Context,
	runner run.ConcurrentRunner,
	store pkg.AttentionStore,
) error {
	if a.AttentionStoreListen == "" {
		glog.Warningf("attention store api listener disabled (attention-store-listen is empty)")
		return nil
	}
	if a.AttentionStoreToken == "" {
		glog.Warningf("attention store api listener disabled (attention-store-token is empty)")
		return nil
	}
	glog.V(2).Infof("starting attention store api server listen on %s", a.AttentionStoreListen)
	runner.Add(ctx, libhttp.NewServer(
		a.AttentionStoreListen,
		a.createAttentionStoreAPIHandler(store, a.AttentionStoreToken),
	).Run)
	return nil
}
