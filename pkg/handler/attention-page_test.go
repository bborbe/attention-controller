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

		// An empty token path never resolves, so the page renders no Jump
		// button — the fail-soft path, which is what a host with no fleet-jump
		// server looks like. The button's own cases live in attention-jump_test.
		httpHandler = handler.NewAttentionPageHandler(
			store,
			provenance,
			false,
			pkg.NewJumpTokenReader(""),
		)
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

	// rowOf returns the rendered HTML for one item's row, so an assertion about
	// the card is scoped to that item rather than to the whole page: a page-wide
	// grep would pass on a page where the wrong item carried the controls.
	rowOf := func(body string, itemID pkg.ItemID) string {
		start := strings.Index(body, `data-item-id="`+itemID.String()+`"`)
		Expect(start).To(BeNumerically(">=", 0), "row for %s not found", itemID)
		rest := body[start:]
		end := strings.Index(rest, "</li>")
		Expect(end).To(BeNumerically(">=", 0))
		return rest[:end]
	}

	// messageRequest builds a `message` declaration whose question shape the
	// caller supplies, so the card's control can be driven from the declared
	// cardinality rather than from the option count. The fixtures above carry no
	// options at all, which is what keeps their cases about the row rather than
	// about the card.
	messageRequest := func(
		dedupKey pkg.DedupKey,
		cardinality pkg.AnswerCardinality,
	) pkg.PushRequest {
		producerID := pkg.ProducerID("producer-" + dedupKey.String())
		return pkg.PushRequest{
			ProducerID:        producerID,
			ProducerKind:      pkg.SessionProducerKind,
			LivenessRef:       pkg.LivenessRef("session:" + producerID.String()),
			DedupKey:          dedupKey,
			InterruptClass:    "pick",
			Payload:           "Which vault-cleanup chores should I queue for this week?",
			AnswerMechanism:   pkg.MessageAnswerMechanism,
			AnswerCardinality: cardinality,
			Options: pkg.AnswerOptions{
				{
					Label:       "Dead-link sweep",
					Description: "Scan the vault for broken wikilinks.",
					Recommended: true,
				},
				{Label: "Archive 2025 daily notes"},
			},
		}
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
		_, err = store.Close(ctx, closed.ItemID, "", nil)
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
		Expect(rowOf(body, message.ItemID)).To(ContainSubstring("<form"))
		Expect(rowOf(body, permission.ItemID)).NotTo(ContainSubstring("<form"))
		Expect(rowOf(body, permission.ItemID)).NotTo(ContainSubstring("<button"))

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

	// The card's control is driven by the producer's declared cardinality and
	// never by the option count: a one-option question and a many-option
	// single-pick question carry lists of different lengths and ask for different
	// things, so a card that read the length would render a checkbox for a
	// question admitting one answer. See the attention item schema and the page
	// handler's own doc comment.
	Describe("the answer card", func() {
		It("renders a checkbox per option when the question takes several picks", func() {
			item, err := store.Push(
				ctx,
				messageRequest("gate-multiple", pkg.MultipleAnswerCardinality),
			)
			Expect(err).To(BeNil())

			block := rowOf(get("GET").Body.String(), item.ItemID)

			Expect(block).To(ContainSubstring(`type="checkbox"`))
			Expect(block).NotTo(ContainSubstring(`type="radio"`))
		})

		It("renders a radio button per option when the question takes one pick", func() {
			item, err := store.Push(
				ctx,
				messageRequest("gate-single", pkg.SingleAnswerCardinality),
			)
			Expect(err).To(BeNil())

			block := rowOf(get("GET").Body.String(), item.ItemID)

			Expect(block).To(ContainSubstring(`type="radio"`))
			Expect(block).NotTo(ContainSubstring(`type="checkbox"`))
		})

		// An absent cardinality is what every item pushed before the field
		// existed carries, and the schema reads it as single. Asserted rather than
		// assumed, because the opposite reading would render a checkbox for a
		// question that admits one answer.
		It("renders a radio button per option when the cardinality is absent", func() {
			item, err := store.Push(ctx, messageRequest("gate-absent", ""))
			Expect(err).To(BeNil())

			block := rowOf(get("GET").Body.String(), item.ItemID)

			Expect(block).To(ContainSubstring(`type="radio"`))
			Expect(block).NotTo(ContainSubstring(`type="checkbox"`))
		})

		It("renders the cardinality hint and marks the recommended option", func() {
			item, err := store.Push(
				ctx,
				messageRequest("gate-hint", pkg.MultipleAnswerCardinality),
			)
			Expect(err).To(BeNil())

			block := rowOf(get("GET").Body.String(), item.ItemID)

			Expect(block).To(ContainSubstring("(pick any number)"))
			Expect(block).To(ContainSubstring(`class="recommended"`))
			Expect(block).To(ContainSubstring("(Recommended)"))
		})

		It("renders an option's muted description under its label", func() {
			item, err := store.Push(
				ctx,
				messageRequest("gate-description", pkg.SingleAnswerCardinality),
			)
			Expect(err).To(BeNil())

			block := rowOf(get("GET").Body.String(), item.ItemID)

			Expect(block).To(ContainSubstring(`class="option-desc"`))
			Expect(block).To(ContainSubstring("Scan the vault for broken wikilinks."))
			// An option carrying no description renders its label alone rather
			// than an empty line, so the two options differ in this block.
			Expect(strings.Count(block, `class="option-desc"`)).To(Equal(1))
		})

		It("renders one tab and one panel per question of a multi-question item", func() {
			item, err := store.Push(ctx, pkg.PushRequest{
				ProducerID:      "producer-two-questions",
				ProducerKind:    pkg.SessionProducerKind,
				LivenessRef:     pkg.LivenessRef("session:producer-two-questions"),
				DedupKey:        "gate-two-questions",
				InterruptClass:  "pick",
				Payload:         "Vault cleanup",
				AnswerMechanism: pkg.MessageAnswerMechanism,
				Questions: pkg.Questions{
					{
						Tab:         "Chores",
						Payload:     "Which chores should I queue?",
						Cardinality: pkg.MultipleAnswerCardinality,
						Options:     pkg.AnswerOptions{{Label: "Dead-link sweep"}},
					},
					{
						Tab:         "Priority",
						Payload:     "Which one comes first?",
						Cardinality: pkg.SingleAnswerCardinality,
						Options:     pkg.AnswerOptions{{Label: "Dead-link sweep"}},
					},
				},
			})
			Expect(err).To(BeNil())

			block := rowOf(get("GET").Body.String(), item.ItemID)

			// Both tabs, both panels, and both questions' payloads — a card that
			// rendered only the first question would fail here.
			Expect(block).To(ContainSubstring(`data-tab="Chores"`))
			Expect(block).To(ContainSubstring(`data-tab="Priority"`))
			Expect(block).To(ContainSubstring(`data-question="Chores"`))
			Expect(block).To(ContainSubstring(`data-question="Priority"`))
			Expect(block).To(ContainSubstring("Which chores should I queue?"))
			Expect(block).To(ContainSubstring("Which one comes first?"))

			// The item's own payload is the card's title on a multi-question item
			// rather than a question, and it renders beside the questions rather
			// than instead of them.
			Expect(block).To(ContainSubstring("Vault cleanup"))

			// The first tab is the open one, and the second panel ships in the
			// document already hidden, so a click reveals a panel that is present
			// rather than fetching one.
			Expect(block).To(ContainSubstring(`class="tab active" data-tab="Chores"`))
			// The second panel carries its own cardinality, which is what the script
			// reads to choose between the value and values carriers, and ships in
			// the document already hidden so a click reveals a panel that is
			// present rather than fetching one.
			Expect(
				block,
			).To(ContainSubstring(`data-question="Priority" data-multi-pick="false" hidden`))

			// Each question carries its own control: the multi-pick tab renders a
			// checkbox and the single-pick tab a radio button, on one card.
			Expect(block).To(ContainSubstring(`type="checkbox"`))
			Expect(block).To(ContainSubstring(`type="radio"`))

			// The wire shape follows the tab strip. The script reads this attribute
			// to decide between `answer` and `answers`, so a card rendering tabs
			// while reporting false would post the wrong field.
			Expect(block).To(ContainSubstring(`data-multi="true"`))
		})

		It("renders no tab strip and answers by `answer` on a single-question item", func() {
			item, err := store.Push(
				ctx,
				messageRequest("gate-no-tabs", pkg.SingleAnswerCardinality),
			)
			Expect(err).To(BeNil())

			block := rowOf(get("GET").Body.String(), item.ItemID)

			Expect(block).To(ContainSubstring(`data-multi="false"`))
			Expect(block).NotTo(ContainSubstring(`data-tab=`))
			Expect(block).NotTo(ContainSubstring(`class="tabs"`))
			Expect(block).NotTo(ContainSubstring(`class="card-title"`))
		})

		It("renders Dismiss and Submit answer, and no card on a permission row", func() {
			message, err := store.Push(
				ctx,
				messageRequest("gate-buttons", pkg.SingleAnswerCardinality),
			)
			Expect(err).To(BeNil())
			permission, err := store.Push(ctx, pkg.PushRequest{
				ProducerID:      "producer-gate-buttons",
				ProducerKind:    pkg.SessionProducerKind,
				LivenessRef:     pkg.LivenessRef("session:producer-gate-buttons"),
				DedupKey:        "gate-buttons-permission",
				InterruptClass:  "approve",
				Payload:         "deploy prod?",
				AnswerMechanism: pkg.PermissionAnswerMechanism,
			})
			Expect(err).To(BeNil())

			body := get("GET").Body.String()
			block := rowOf(body, message.ItemID)

			// The Dismiss value is what the script reads as the skip, so it is
			// asserted rather than left to the label.
			Expect(block).To(ContainSubstring(`value="skip"`))
			Expect(block).To(ContainSubstring("Dismiss"))
			// The submit control is named for what it does rather than for an
			// advance: `Next` was inherited from the Paseo reference card, where
			// it means *advance to the next card*, while this control submits the
			// whole card. See [[Attention Item Schema]] § Answer routing.
			Expect(block).To(ContainSubstring("Submit answer"))

			// The card is one `if .Message` away from a permission row, so the
			// negative case is asserted beside the positive one rather than only
			// page-wide.
			permissionBlock := rowOf(body, permission.ItemID)
			Expect(permissionBlock).NotTo(ContainSubstring("<form"))
			Expect(permissionBlock).NotTo(ContainSubstring("<button"))
			Expect(permissionBlock).NotTo(ContainSubstring("<input"))
		})

		// The answer arm's terminal-state branch. The failure it exists for is an
		// item that left the queue between the render and the answer, and the
		// operator's report was of exactly that: a raw JSON body printed into a
		// failed note, and a card left in front of them for an item that no longer
		// existed. The script is inline and has no unit harness, so the assertions
		// are on what it ships — the code it recognises, the human line it writes,
		// and the return to the queue that follows. The browser click-through in
		// the task's Definition of Done is the half this cannot supply.
		It("recognises a closed item and returns the operator to the queue", func() {
			body := get("GET").Body.String()

			// The code is read out of the store's envelope rather than matched in
			// the raw body, so the branch fires on the classification and not on a
			// substring some other failure's message happens to contain.
			Expect(body).To(ContainSubstring("failure.code !== 'ITEM_CLOSED'"))
			Expect(body).To(ContainSubstring("failure.details.closed_at"))
			Expect(body).To(ContainSubstring("left the queue before this answer arrived"))

			// The line is shown AND the queue is returned to. A branch that
			// reloaded without showing would swallow the outcome into a reload,
			// which this file's own rule forbids; one that showed without
			// reloading would leave the stale card, which is the defect.
			Expect(body).To(ContainSubstring("showNote(form, failure.message, true)"))
			Expect(body).
				To(ContainSubstring("window.setTimeout(function () { window.location.reload(); }, 2500)"))
		})
	})

	// ackRequest builds a report-only declaration. An `ack` item is a condition
	// report, so it declares no options and no cardinality, and its liveness
	// model is a session so the pruning Read keeps it while the session is live.
	ackRequest := func(dedupKey pkg.DedupKey) pkg.PushRequest {
		producerID := pkg.ProducerID("producer-" + dedupKey.String())
		return pkg.PushRequest{
			ProducerID:      producerID,
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:" + producerID.String()),
			DedupKey:        dedupKey,
			InterruptClass:  "approve",
			Payload:         "the nightly sweep failed",
			AnswerMechanism: pkg.AckAnswerMechanism,
		}
	}

	It("renders the acknowledge control on an ack row, and on no other class", func() {
		ack, err := store.Push(ctx, ackRequest("report-ack"))
		Expect(err).To(BeNil())
		message, err := store.Push(ctx, pushRequest("producer-m", "gate-m", "deploy prod?"))
		Expect(err).To(BeNil())

		body := get("GET").Body.String()

		// The positive case: a report-only item carries a control where it used
		// to carry none, which is the defect this change exists to fix.
		ackBlock := rowOf(body, ack.ItemID)
		Expect(ackBlock).To(ContainSubstring("data-ack"))
		Expect(ackBlock).To(ContainSubstring("Acknowledge"))
		// It carries no answer form, and no Other field: an ack item asks
		// nothing, so the message card's controls would describe a choice it
		// does not offer.
		Expect(ackBlock).NotTo(ContainSubstring("<form"))
		Expect(ackBlock).NotTo(ContainSubstring(`class="other"`))

		// The negative control: the control is derived from the mechanism, so a
		// message row must not gain one. Without this the positive case would
		// pass on a page that rendered one identical control for every class.
		messageBlock := rowOf(body, message.ItemID)
		Expect(messageBlock).NotTo(ContainSubstring("data-ack"))
	})

	It("keeps a permission row free of every control, acknowledge included", func() {
		permission, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      "producer-perm",
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:producer-perm"),
			DedupKey:        "gate-perm",
			InterruptClass:  "approve",
			Payload:         "approve the deploy",
			AnswerMechanism: pkg.PermissionAnswerMechanism,
		})
		Expect(err).To(BeNil())

		block := rowOf(get("GET").Body.String(), permission.ItemID)

		// A permission item is approve-shaped and only the operator may answer
		// it in the session that raised it, so a control here would be the
		// permission laundering the schema forbids — the acknowledge control
		// included, which is why the ack branch must not reach it.
		Expect(block).NotTo(ContainSubstring("data-ack"))
		Expect(block).NotTo(ContainSubstring("<form"))
		Expect(block).NotTo(ContainSubstring("<button"))
		Expect(block).NotTo(ContainSubstring("<input"))
	})

	// The corner X. It is one affordance whose act is the mechanism's own
	// dominant act — an alias of Dismiss on a message card, of Acknowledge on an
	// ack card — so it adds no transition and no field. See [[Attention Item
	// Schema]] § Answer routing, § The corner X. The browser click-through in the
	// task's Definition of Done is the half these cannot supply.
	Describe("the corner X", func() {
		It("renders on a message row and dispatches the Dismiss the card already carries", func() {
			message, err := store.Push(
				ctx,
				messageRequest("corner-x-message", pkg.SingleAnswerCardinality),
			)
			Expect(err).To(BeNil())

			body := get("GET").Body.String()
			block := rowOf(body, message.ItemID)

			Expect(block).To(ContainSubstring("data-corner-x"))
			Expect(block).To(ContainSubstring(`aria-label="Skip this item"`))

			// The X's act is the Dismiss's act, and the assertion is on the
			// mechanism rather than on the label: the card already carries the
			// skip submit, so the X dispatches it instead of posting a second
			// write that merely agrees with it.
			Expect(block).To(ContainSubstring(`value="skip"`))
			Expect(body).To(ContainSubstring("button[data-corner-x]"))
			Expect(body).To(ContainSubstring(`form.querySelector('button[value=skip]')`))
		})

		It("renders on an ack row and shares the acknowledge close", func() {
			ack, err := store.Push(ctx, ackRequest("corner-x-ack"))
			Expect(err).To(BeNil())

			body := get("GET").Body.String()
			block := rowOf(body, ack.ItemID)

			Expect(block).To(ContainSubstring("data-corner-x"))
			Expect(block).To(ContainSubstring("data-ack"))

			// The ack card carries no form, so the X reaches the close path by
			// the named function both controls share rather than by dispatching
			// a submit that does not exist here.
			Expect(block).NotTo(ContainSubstring("<form"))
			Expect(body).To(ContainSubstring("function closeAck(row)"))
			Expect(body).To(ContainSubstring("closeAck(row);"))
		})

		// The criterion that fails if the X is rendered unconditionally. Without
		// it the two cases above would pass on a page that put an X on every
		// card, which is exactly what the operator's ruling forbids.
		It("renders no corner X on a permission row", func() {
			permission, err := store.Push(ctx, pkg.PushRequest{
				ProducerID:      "producer-corner-x-perm",
				ProducerKind:    pkg.SessionProducerKind,
				LivenessRef:     pkg.LivenessRef("session:producer-corner-x-perm"),
				DedupKey:        "corner-x-permission",
				InterruptClass:  "approve",
				Payload:         "approve the deploy",
				AnswerMechanism: pkg.PermissionAnswerMechanism,
			})
			Expect(err).To(BeNil())

			block := rowOf(get("GET").Body.String(), permission.ItemID)

			// A permission card stays jump-link-only with zero answer controls.
			// The X is an answer control on every mechanism that renders it, so
			// it is excluded here with the rest.
			Expect(block).NotTo(ContainSubstring("data-corner-x"))
			Expect(block).NotTo(ContainSubstring("<button"))
		})

		// The positive control read from the other end: a regression that
		// stripped the X from the whole page must fail here rather than pass
		// silently on the permission case alone.
		It("renders the X on both message and ack rows in the same run", func() {
			message, err := store.Push(
				ctx,
				messageRequest("corner-x-both-m", pkg.SingleAnswerCardinality),
			)
			Expect(err).To(BeNil())
			ack, err := store.Push(ctx, ackRequest("corner-x-both-a"))
			Expect(err).To(BeNil())

			body := get("GET").Body.String()
			Expect(rowOf(body, message.ItemID)).To(ContainSubstring("data-corner-x"))
			Expect(rowOf(body, ack.ItemID)).To(ContainSubstring("data-corner-x"))
		})
	})
})
