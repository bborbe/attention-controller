// Copyright (c) 2025 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package factory

import (
	"net/http"

	libsentry "github.com/bborbe/sentry"

	"github.com/bborbe/attention-controller/pkg"
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
// jumpTokens gates the Jump button on the same principle: the page renders the
// button only while the token the redirect needs is readable.
func CreateAttentionPageHandler(
	store pkg.AttentionStore,
	provenance pkg.ProvenanceResolver,
	speakEnabled bool,
	jumpTokens pkg.JumpTokenReader,
) http.Handler {
	return handler.NewAttentionPageHandler(store, provenance, speakEnabled, jumpTokens)
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
// speakEnabled and jumpTokens are threaded through so a row arriving over the
// stream renders exactly as the same row does on a fresh page load. A row that
// dropped either would be a control that disappears when the board updates
// itself.
func CreateAttentionStreamHandler(
	store pkg.AttentionStore,
	notifier pkg.AttentionChangeNotifier,
	provenance pkg.ProvenanceResolver,
	speakEnabled bool,
	jumpTokens pkg.JumpTokenReader,
) http.Handler {
	return handler.NewAttentionStreamHandler(
		store,
		notifier,
		provenance,
		jumpTokens,
		speakEnabled,
	)
}

// CreateAttentionJumpHandler creates the endpoint the board's Jump button calls
// to hand an item back to the session that raised it.
//
// jumpBaseURL is the fleet-jump server's origin and jumpCaller is what performs
// the jump against it; the handler adds the pane and the token, so the token
// never appears in a rendered page or in a response.
func CreateAttentionJumpHandler(
	store pkg.AttentionStore,
	provenance pkg.ProvenanceResolver,
	jumpTokens pkg.JumpTokenReader,
	jumpCaller pkg.JumpCaller,
	jumpBaseURL string,
) http.Handler {
	return handler.NewAttentionJumpHandler(store, provenance, jumpTokens, jumpCaller, jumpBaseURL)
}

// CreateAttentionSpeakHandler creates the handler that reads an item aloud
// through the tts server at ttsURL.
func CreateAttentionSpeakHandler(store pkg.AttentionStore, ttsURL string) http.Handler {
	return handler.NewAttentionSpeakHandler(store, ttsURL)
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
