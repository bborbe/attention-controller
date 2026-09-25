// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	libboltkv "github.com/bborbe/boltkv"
	libhttp "github.com/bborbe/http"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/handler"
)

var _ = Describe("AttentionPageHandler", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var sessionLivenessChecker *mocks.SessionLivenessChecker
	var provenance *mocks.ProvenanceResolver
	var httpHandler http.Handler

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		// A real boltkv DB and a real store, not a fake of either: Read prunes
		// open items whose producer is not live, so a faked store would let the
		// page render fixtures that the production read path would have dropped.
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

		// The mock defaults to false. Left at its default, every fixture item is
		// pruned during Read and the page renders empty — the empty-store case
		// would pass vacuously and the positive assertions would fail. Pinning it
		// to true is what makes the pruning real rather than incidental.
		sessionLivenessChecker = &mocks.SessionLivenessChecker{}
		sessionLivenessChecker.IsLiveReturns(true)

		store = pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			sessionLivenessChecker,
			libtime.NewCurrentDateTime(),
			libtime.Duration(15*60*1e9),
		)

		// Left returning nil, so every existing case exercises the no-provenance
		// path — which is the degradation this page must keep: a row whose
		// provenance cannot be resolved renders no provenance line at all.
		provenance = &mocks.ProvenanceResolver{}

		httpHandler = handler.NewAttentionPageHandler(store, provenance)
	})

	AfterEach(func() {
		// bbolt's Close is idempotent, so the read-failure case closing the db
		// first does not turn this into a second-close failure.
		Expect(db.Close()).To(BeNil())
	})

	// pushRequest builds a declaration whose liveness model matches its producer
	// id and whose answer mechanism makes it a question rather than a report, so
	// the pruning Read performs is exercised for every fixture.
	pushRequest := func(producerID pkg.ProducerID, dedupKey pkg.DedupKey, payload pkg.Payload) pkg.PushRequest {
		return pkg.PushRequest{
			ProducerID:      producerID,
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:" + producerID.String()),
			DedupKey:        dedupKey,
			InterruptClass:  "approve",
			Payload:         payload,
			AnswerMechanism: pkg.MessageAnswerMechanism,
		}
	}

	// get renders the page and returns the recorder.
	get := func(method string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/", nil)
		resp := httptest.NewRecorder()
		httpHandler.ServeHTTP(resp, req)
		return resp
	}

	It("renders one row per open item, carrying every rendered field", func() {
		first, err := store.Push(ctx, pushRequest("producer-a", "gate-a", "deploy prod?"))
		Expect(err).To(BeNil())
		second, err := store.Push(ctx, pushRequest("producer-b", "gate-b", "approve the migration"))
		Expect(err).To(BeNil())

		resp := get("GET")
		Expect(resp.Code).To(Equal(http.StatusOK))
		Expect(resp.Header().Get("Content-Type")).To(Equal("text/html; charset=utf-8"))

		body := resp.Body.String()
		// Gomega has no count matcher, so the row count is counted directly.
		Expect(strings.Count(body, `data-item-id="`)).To(Equal(2))

		for _, item := range []*pkg.Item{first, second} {
			Expect(body).To(ContainSubstring(item.ProducerID.String()))
			Expect(body).To(ContainSubstring(item.ProducerKind.String()))
			Expect(body).To(ContainSubstring(item.Payload.String()))
			Expect(body).To(ContainSubstring(item.State.String()))
			Expect(body).To(ContainSubstring(item.CreatedAt.String()))
			Expect(body).To(ContainSubstring(`data-item-id="` + item.ItemID.String() + `"`))
		}
	})

	It("renders an empty page for an empty store", func() {
		resp := get("GET")

		Expect(resp.Code).To(Equal(http.StatusOK))
		Expect(strings.Count(resp.Body.String(), "data-item-id=")).To(Equal(0))
	})

	It("escapes producer-supplied text instead of emitting it raw", func() {
		// ProducerID is a free string validated only by NotEmptyString, so this is
		// the exact value the html/template boundary exists to neutralise.
		_, err := store.Push(
			ctx,
			pushRequest(`"><script>alert(1)</script>`, "gate-escape", "escaping fixture"),
		)
		Expect(err).To(BeNil())

		body := get("GET").Body.String()

		// The page legitimately carries its own <script> for the answer controls,
		// so the assertion is that the *injected* raw script is absent rather than
		// that no script element exists at all.
		Expect(body).NotTo(ContainSubstring("<script>alert(1)"))
		Expect(body).To(ContainSubstring("&lt;script&gt;"))
	})

	It("omits closed items while keeping open ones", func() {
		open, err := store.Push(ctx, pushRequest("producer-open", "gate-open", "still open"))
		Expect(err).To(BeNil())
		closed, err := store.Push(
			ctx,
			pushRequest("producer-closed", "gate-closed", "already done"),
		)
		Expect(err).To(BeNil())
		_, err = store.Close(ctx, closed.ItemID)
		Expect(err).To(BeNil())

		body := get("GET").Body.String()

		Expect(body).To(ContainSubstring(open.ProducerID.String()))
		Expect(body).NotTo(ContainSubstring(closed.ProducerID.String()))
	})

	// ⚠️ This spec previously asserted the page was inert — no <form>, no
	// <script>, no method="post" — which was the recorded design decision the
	// board reversal overturns. It asserts the *bounded* rule instead: answer
	// controls exist for `message` items and a `permission` item renders none,
	// because only the operator may answer a gate, and only in the session that
	// raised it. See the attention item schema § Answer routing, and the page
	// handler's own doc comment for why the reversal stops there.
	It("offers answer controls for a message item and none for a permission item", func() {
		message, err := store.Push(
			ctx,
			pushRequest("producer-readonly", "gate-readonly", "read me"),
		)
		Expect(err).To(BeNil())
		permission, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      "producer-gate",
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:producer-gate"),
			DedupKey:        "gate-permission",
			InterruptClass:  "approve",
			Payload:         "deploy prod?",
			AnswerMechanism: pkg.PermissionAnswerMechanism,
		})
		Expect(err).To(BeNil())

		// A non-empty page first, so the absence assertions below are made against
		// a rendered document rather than against an empty body.
		resp := get("GET")
		Expect(resp.Body.String()).NotTo(BeEmpty())
		body := resp.Body.String()

		// Scoped per row rather than page-wide: a page-wide grep would pass on a
		// page where the wrong item carried the controls.
		rowOf := func(itemID pkg.ItemID) string {
			start := strings.Index(body, `data-item-id="`+itemID.String()+`"`)
			Expect(start).To(BeNumerically(">=", 0))
			rest := body[start:]
			end := strings.Index(rest, "</li>")
			Expect(end).To(BeNumerically(">=", 0))
			return rest[:end]
		}

		Expect(rowOf(message.ItemID)).To(ContainSubstring("<form"))
		Expect(rowOf(permission.ItemID)).NotTo(ContainSubstring("<form"))
		Expect(rowOf(permission.ItemID)).NotTo(ContainSubstring("<button"))

		// HEAD is routed to this handler too; it is read-only and a link checker
		// or browser may issue it, so it is asserted rather than merely declared.
		Expect(get("HEAD").Code).To(Equal(http.StatusOK))
	})

	It("renders host, cwd, tool and pane as four required values on a resolving row", func() {
		item, err := store.Push(ctx, pushRequest("producer-prov", "gate-prov", "deploy prod?"))
		Expect(err).To(BeNil())
		provenance.ResolveReturns(pkg.Provenances{
			item.ItemID: pkg.Provenance{
				Host:         "burn",
				Cwd:          "/Users/bborbe/Documents/workspaces/attention-controller",
				Tool:         "AskUserQuestion",
				Pane:         "1140",
				PaneRecorded: true,
				Routable:     true,
			},
		})

		body := get("GET").Body.String()

		// Positive control first: a row that failed to render at all must not be
		// able to satisfy the assertions below.
		Expect(body).To(ContainSubstring(item.Payload.String()))
		Expect(body).To(ContainSubstring(item.ProducerID.String()))

		// All four, each in its own element. The pane is asserted as a required
		// value rather than a conditional one: an implementation that resolves
		// host, cwd and tool but never a pane fails here.
		Expect(body).To(ContainSubstring(`<span class="host">burn</span>`))
		Expect(
			body,
		).To(ContainSubstring(`<span class="cwd">/Users/bborbe/Documents/workspaces/attention-controller</span>`))
		Expect(body).To(ContainSubstring(`<span class="tool">AskUserQuestion</span>`))
		Expect(body).To(ContainSubstring(`<span class="pane">pane 1140</span>`))
	})

	It("marks an unvalidated pane unroutable, shows no pane id, and invents nothing", func() {
		item, err := store.Push(
			ctx,
			pushRequest("producer-unroutable", "gate-unroutable", "who owns this?"),
		)
		Expect(err).To(BeNil())
		// A pane was recorded but did not validate against the session, which is
		// § Silence 7's exact case: a recycled id that resolves to another
		// session's pane. Tool is absent, which is the common case for idle items.
		provenance.ResolveReturns(pkg.Provenances{
			item.ItemID: pkg.Provenance{
				Host:         "burn",
				Cwd:          "/tmp",
				PaneRecorded: true,
				Routable:     false,
			},
		})

		body := get("GET").Body.String()

		Expect(body).To(ContainSubstring(item.Payload.String()))
		Expect(body).To(ContainSubstring(`<span class="unroutable">unroutable</span>`))

		// The pane id is withheld entirely — not shown struck through, not shown
		// with a warning, not shown at all. Showing it invites the operator to
		// route to a pane that belongs to somebody else.
		Expect(body).NotTo(ContainSubstring(`class="pane"`))

		// An absent field renders blank, never a placeholder. A stand-in would be
		// an unresolvable value presented as resolved, which is the one failure
		// this task exists to avoid.
		Expect(body).NotTo(ContainSubstring(`class="tool"`))
		for _, placeholder := range []string{"unknown", "n/a", "N/A", "—", "??"} {
			Expect(body).NotTo(ContainSubstring(placeholder))
		}
	})

	It("renders every item with no provenance line when nothing resolves", func() {
		first, err := store.Push(
			ctx,
			pushRequest("producer-noprov-a", "gate-noprov-a", "first ask"),
		)
		Expect(err).To(BeNil())
		second, err := store.Push(
			ctx,
			pushRequest("producer-noprov-b", "gate-noprov-b", "second ask"),
		)
		Expect(err).To(BeNil())
		provenance.ResolveReturns(pkg.Provenances{})

		resp := get("GET")

		// The standalone claim, honoured rather than broken: no provenance source
		// is not an error and does not blank the page.
		Expect(resp.Code).To(Equal(http.StatusOK))
		body := resp.Body.String()
		Expect(strings.Count(body, `data-item-id="`)).To(Equal(2))
		for _, item := range []*pkg.Item{first, second} {
			Expect(body).To(ContainSubstring(item.ProducerID.String()))
			Expect(body).To(ContainSubstring(item.Payload.String()))
		}
		Expect(body).NotTo(ContainSubstring(`class="provenance"`))
	})

	It("returns the standard JSON error body when the read fails", func() {
		Expect(db.Close()).To(BeNil())

		resp := get("GET")

		Expect(resp.Code).To(Equal(http.StatusInternalServerError))

		// Decoded rather than substring-matched, so a body that merely resembles
		// the standard error shape does not pass. Only code and message are
		// asserted: details is omitempty and this error carries no data.
		var errorResponse libhttp.ErrorResponse
		Expect(json.NewDecoder(resp.Body).Decode(&errorResponse)).To(BeNil())
		Expect(errorResponse.Error.Code).To(Equal(libhttp.ErrorCodeInternal))
		Expect(errorResponse.Error.Message).To(ContainSubstring("read failed"))
	})
})
