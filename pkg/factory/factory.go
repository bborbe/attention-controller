// Copyright (c) 2025 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory

import (
	"net/http"

	libsentry "github.com/bborbe/sentry"
	libtime "github.com/bborbe/time"

	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/buildidentity"
	"github.com/bborbe/attention-controller/pkg/handler"
)

// CreateAttentionPushHandler creates the handler a producer calls to declare
// that a human is needed.
func CreateAttentionPushHandler(store pkg.AttentionStore) http.Handler {
	return handler.NewAttentionPushHandler(store)
}

// CreateAttentionReadHandler creates the handler every arm reads.
func CreateAttentionReadHandler(store pkg.AttentionStore) http.Handler {
	return handler.NewAttentionReadHandler(store)
}

// CreateSessionHeartbeatPostHandler creates the handler a session's MCP timer
// calls to declare that it is still alive.
func CreateSessionHeartbeatPostHandler(store pkg.SessionHeartbeatStore) http.Handler {
	return handler.NewSessionHeartbeatPostHandler(store)
}

// CreateSessionHeartbeatGetHandler creates the handler that answers one
// session's liveness.
func CreateSessionHeartbeatGetHandler(
	store pkg.SessionHeartbeatStore,
	now libtime.CurrentDateTimeGetter,
	window libtime.Duration,
) http.Handler {
	return handler.NewSessionHeartbeatGetHandler(store, now, window)
}

// CreateSessionHeartbeatListHandler creates the handler that answers every
// session's liveness at once.
func CreateSessionHeartbeatListHandler(
	store pkg.SessionHeartbeatStore,
	now libtime.CurrentDateTimeGetter,
	window libtime.Duration,
) http.Handler {
	return handler.NewSessionHeartbeatListHandler(store, now, window)
}

// CreateAttentionAnswerHandler creates the handler that applies an answer as an
// atomic compare-and-set.
func CreateAttentionAnswerHandler(store pkg.AttentionStore) http.Handler {
	return handler.NewAttentionAnswerHandler(store)
}

// CreateAttentionEscalateHandler creates the handler that records which
// session is carrying an item, as an atomic compare-and-set.
func CreateAttentionEscalateHandler(store pkg.AttentionStore) http.Handler {
	return handler.NewAttentionEscalateHandler(store)
}

// CreateAttentionCloseHandler creates the handler that applies
// open -> closed and answered -> closed.
func CreateAttentionCloseHandler(store pkg.AttentionStore) http.Handler {
	return handler.NewAttentionCloseHandler(store)
}

// CreateAttentionHistoryHandler creates the handler that returns every item
// regardless of state, for counting resolutions rather than rendering.
func CreateAttentionHistoryHandler(store pkg.AttentionStore) http.Handler {
	return handler.NewAttentionHistoryHandler(store)
}

// CreateAttentionGetHandler creates the handler that returns a single item by
// id, whatever its state.
func CreateAttentionGetHandler(store pkg.AttentionStore) http.Handler {
	return handler.NewAttentionGetHandler(store)
}

// CreateAttentionAttemptGetHandler creates the handler that answers "did this
// item's answer reach the session?" in one query, returning the derived
// delivery status rather than the raw record.
func CreateAttentionAttemptGetHandler(store pkg.AttentionStore) http.Handler {
	return handler.NewAttentionAttemptGetHandler(store)
}

// CreateAttentionAttemptRecordHandler creates the handler the attempting arm
// calls to record which arm tried and whether it delivered.
func CreateAttentionAttemptRecordHandler(store pkg.AttentionStore) http.Handler {
	return handler.NewAttentionAttemptRecordHandler(store)
}

// CreateAttentionPageHandler creates the read-only HTML page an operator opens
// to see what currently needs attention, without Claude Code, vault-cli or the
// task system.
//
// The resolver is injected rather than constructed here: `pkg/factory` is pure
// plumbing with no business logic, and which directory the provenance is read
// from is a decision `main` owns alongside the session registry's.
//
// speakEnabled gates the read-aloud control. It is passed rather than derived
// from ttsURL so the page and the route agree by construction: a control that
// renders while its endpoint is unrouted is a value presented as working that
// is not.
//
// ⚠️ The jump token is no longer a parameter. The Jump button is gated on the
// resolved pane alone, because the endpoint it points at performs the jump
// in-process and reads no token — see handler.NewAttentionPageHandler.
//
// vaultDir is the configured vault directory, threaded to the handler so a card
// can link to the vault task its session is anchored to. It is passed rather
// than read from an environment here for the same reason the provenance
// directories are: which vault a host serves is a decision `main` owns, and an
// empty directory renders no task link rather than failing.
// buildIdentity is the running binary's own provenance, threaded to the page so
// its footer can say which build is being read. It is passed rather than read
// here for the same reason vaultDir is: it is a fact about the process, and
// `pkg/factory` is pure plumbing that decides nothing.
//
// metrics is threaded through so the page reports its requests through the one
// process-wide instance, which is built once in `main` on the registry /metrics
// already serves. It is passed rather than constructed here for the same reason
// the stream handler's is: `pkg/factory` is pure plumbing, and a second instance
// would count every request on a private registry the deployed /metrics cannot
// see.
func CreateAttentionPageHandler(
	store pkg.AttentionStore,
	provenance pkg.ProvenanceResolver,
	speakEnabled bool,
	vaultDir string,
	buildIdentity buildidentity.Identity,
	metrics pkg.Metrics,
) http.Handler {
	return handler.NewAttentionPageHandler(
		store,
		provenance,
		speakEnabled,
		vaultDir,
		buildIdentity,
		metrics,
	)
}

// CreateAttentionStreamHandler creates the board's live channel: the
// server-sent event stream the page subscribes to so a changed row reaches an
// open board without a reload.
//
// It takes the notifier as well as the store because the two are separate
// halves of one mechanism — the store signals, the stream listens — and the
// notifier is passed in rather than constructed here for the same reason the
// provenance resolver is: `pkg/factory` is pure plumbing, and a notifier built
// here would be a second instance nothing writes to.
//
// speakEnabled is threaded through so a row arriving over the stream renders
// exactly as the same row does on a fresh page load. A row that dropped it
// would be a control that disappears when the board updates itself.
//
// vaultDir is threaded through on the same principle, and it is the second
// value that must match the page's: a row whose task link were built from a
// different vault name — or from none at all — would be the same card rendering
// differently depending on whether it arrived by load or by stream, which is
// exactly the drift this handler exists to prevent.
//
// metrics is threaded through so the renderer reports through the one
// process-wide instance, which is built once in `main` on the registry /metrics
// already serves. It is passed rather than constructed here for the same reason
// the notifier is: `pkg/factory` is pure plumbing, and a second instance would
// be a second registration of the same collector.
func CreateAttentionStreamHandler(
	store pkg.AttentionStore,
	notifier pkg.AttentionChangeNotifier,
	provenance pkg.ProvenanceResolver,
	speakEnabled bool,
	vaultDir string,
	metrics pkg.Metrics,
) http.Handler {
	return handler.NewAttentionStreamHandler(
		store,
		notifier,
		provenance,
		speakEnabled,
		vaultDir,
		metrics,
	)
}

// CreateAttentionJumpHandler creates the endpoint the board's Jump button calls
// to hand an item back to the session that raised it.
//
// The activator performs the jump in-process, so no token and no fleet-jump
// origin are threaded through here: the board's path owns the whole jump, which
// is the point of the fold.
func CreateAttentionJumpHandler(
	store pkg.AttentionStore,
	provenance pkg.ProvenanceResolver,
	activator pkg.PaneActivator,
) http.Handler {
	return handler.NewAttentionJumpHandler(store, provenance, activator)
}

// CreateLegacyJumpHandler creates the pane-addressed jump route the manager
// layer's links target: `GET /jump?pane=<N>&t=<token>`.
//
// ⚠️ It is the compatibility surface the fold keeps rather than moves.
// `claude-supervisor/scripts/jump-link.py` emits pane-addressed links into nine
// manager-layer surfaces, so retiring the route would break every manager's
// jump link; serving it from this process instead leaves those consumers
// untouched and makes one process answer for both the card and its jump
// control. The token stays required here because a pane-addressed GET is
// reachable by any page the operator visits, and this is the surface with no
// item id to fall back on.
func CreateLegacyJumpHandler(
	jumpTokens pkg.JumpTokenReader,
	activator pkg.PaneActivator,
) http.Handler {
	return handler.NewLegacyJumpHandler(jumpTokens, activator)
}

// CreateLegacyHealthHandler creates the liveness probe for the legacy
// pane-addressed jump listener.
//
// It is separate from CreateHealthzHandler because the two answer different
// contracts on different ports: this one reproduces the Python jump server's
// plain-text `ok`, while `/healthz` is the board's JSON liveness response.
func CreateLegacyHealthHandler() http.Handler {
	return handler.NewLegacyHealthHandler()
}

// CreateAttentionSpeakHandler creates the handler that reads an item aloud
// through the tts server at ttsURL.
func CreateAttentionSpeakHandler(store pkg.AttentionStore, ttsURL string) http.Handler {
	return handler.NewAttentionSpeakHandler(store, ttsURL)
}

// CreateAttentionCancelHandler creates the handler that stops a reading this
// board started, through the tts server at ttsURL.
//
// It takes no store, unlike its speak sibling: the cancel is addressed by the
// tts message id the board's own /speak response returned, so there is nothing
// to look up. Reading the store here would add a failure mode rather than
// remove one — an item answered or closed between the speak and the stop would
// 404 and leave the operator unable to stop a reading they can hear.
func CreateAttentionCancelHandler(ttsURL string) http.Handler {
	return handler.NewAttentionCancelHandler(ttsURL)
}

// CreateTestLoglevelHandler creates an HTTP handler that tests different glog verbosity levels.
func CreateTestLoglevelHandler() http.Handler {
	return handler.NewTestLoglevelHandler()
}

// CreateSentryAlertHandler creates an HTTP handler that sends test alerts to Sentry.
func CreateSentryAlertHandler(sentryClient libsentry.Client) http.Handler {
	return handler.NewSentryAlertHandler(sentryClient)
}

// CreateHealthzHandler creates an HTTP handler that serves the canonical
// `/healthz` liveness response (HTTP 200, body `{"status":"ok"}`,
// Content-Type: application/json).
func CreateHealthzHandler() http.Handler {
	return handler.NewHealthzHandler()
}

// CreateBearerTokenHandler wraps next so that every request reaching it must
// present the configured bearer token in the Authorization header.
//
// It is the enforcement seam of the second, cluster-reachable listener: that
// listener registers its routes through the board's own business-route
// function, so the route inventory cannot drift, and this middleware is what
// makes reaching that inventory require the token.
func CreateBearerTokenHandler(next http.Handler, token string) http.Handler {
	return handler.NewBearerTokenHandler(next, token)
}
