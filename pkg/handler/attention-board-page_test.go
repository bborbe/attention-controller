// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/handler"
)

// The board renders answer controls for `message` items and nothing for every
// other class. The negative half is the load-bearing one: a control on a
// `permission` item would let the board answer a gate that only the operator
// may answer, in the session that raised it, which the schema calls permission
// laundering. See the attention item schema § Answer routing and silence 12.
var _ = Describe("Attention page board controls", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var provenance *mocks.ProvenanceResolver
	var httpHandler http.Handler

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

		sessionLivenessChecker := &mocks.SessionLivenessChecker{}
		sessionLivenessChecker.IsLiveReturns(true)

		store = pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			sessionLivenessChecker,
			libtime.NewCurrentDateTime(),
			libtime.Duration(15*60*1e9),
		)

		provenance = &mocks.ProvenanceResolver{}
		// Empty token path: no Jump button, so these cases keep exercising the
		// answer controls alone.
		httpHandler = handler.NewAttentionPageHandler(
			store,
			provenance,
			true,
			pkg.NewJumpTokenReader(""),
		)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	// renderPage pushes the given declaration, resolves the given provenance for
	// it, and returns the rendered document.
	renderPage := func(request pkg.PushRequest, resolved pkg.Provenance) (string, *pkg.Item) {
		item, err := store.Push(ctx, request)
		Expect(err).To(BeNil())
		provenance.ResolveReturns(pkg.Provenances{item.ItemID: resolved})

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		resp := httptest.NewRecorder()
		httpHandler.ServeHTTP(resp, req)
		Expect(resp.Code).To(Equal(http.StatusOK))
		return resp.Body.String(), item
	}

	// rowBlock returns the rendered HTML for one item's row, so an assertion
	// about controls is scoped to that item rather than to the whole page. A
	// page-wide grep would pass on a page where the wrong item carried the
	// controls.
	rowBlock := func(body string, itemID pkg.ItemID) string {
		start := strings.Index(body, `data-item-id="`+itemID.String()+`"`)
		Expect(start).To(BeNumerically(">=", 0), "row for %s not found", itemID)
		rest := body[start:]
		end := strings.Index(rest, "</li>")
		Expect(end).To(BeNumerically(">=", 0))
		return rest[:end]
	}

	messageRequest := func() pkg.PushRequest {
		return pkg.PushRequest{
			ProducerID:      "session-a",
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:session-a"),
			DedupKey:        "question-1",
			InterruptClass:  "pick",
			Payload:         "Which surface should the answer land on?",
			Context:         "The board is the store's own page.",
			AnswerMechanism: pkg.MessageAnswerMechanism,
			Options: pkg.AnswerOptions{
				{Label: "the board", Recommended: true},
				{Label: "the tab"},
			},
		}
	}

	permissionRequest := func() pkg.PushRequest {
		return pkg.PushRequest{
			ProducerID:      "session-b",
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:session-b"),
			DedupKey:        "gate-1",
			InterruptClass:  "approve",
			Payload:         "Deploy to prod?",
			AnswerMechanism: pkg.PermissionAnswerMechanism,
		}
	}

	Describe("a message item", func() {
		It(
			"renders the question, the context and the options with the recommendation marked",
			func() {
				body, item := renderPage(messageRequest(), pkg.Provenance{})
				block := rowBlock(body, item.ItemID)

				Expect(block).To(ContainSubstring("Which surface should the answer land on?"))
				Expect(block).To(ContainSubstring("The board is the store&#39;s own page."))
				Expect(block).To(ContainSubstring("the board"))
				Expect(block).To(ContainSubstring("the tab"))
				Expect(block).To(ContainSubstring(`class="recommended"`))
			},
		)

		It("renders a form, a skip control and a free-text field", func() {
			body, item := renderPage(messageRequest(), pkg.Provenance{})
			block := rowBlock(body, item.ItemID)

			Expect(block).To(ContainSubstring("<form"))
			Expect(block).To(ContainSubstring(`value="skip"`))
			Expect(block).To(ContainSubstring(`type="text"`))
		})

		It("renders no jump command", func() {
			body, item := renderPage(messageRequest(), pkg.Provenance{Pane: "1907"})
			Expect(rowBlock(body, item.ItemID)).NotTo(ContainSubstring("/supervisor:jump"))
		})

		It("renders a read-aloud control", func() {
			body, item := renderPage(messageRequest(), pkg.Provenance{})
			block := rowBlock(body, item.ItemID)

			Expect(block).To(ContainSubstring("data-speak"))
			Expect(block).To(ContainSubstring("Read aloud"))
		})
	})

	Describe("a permission item", func() {
		It("renders zero answer controls", func() {
			body, item := renderPage(permissionRequest(), pkg.Provenance{})
			block := rowBlock(body, item.ItemID)

			Expect(block).NotTo(ContainSubstring("<form"))
			Expect(block).NotTo(ContainSubstring("<button"))
		})

		It("renders the copyable jump command when a pane resolved", func() {
			body, item := renderPage(permissionRequest(), pkg.Provenance{
				Pane:         "1907",
				PaneRecorded: true,
				Routable:     true,
			})
			block := rowBlock(body, item.ItemID)

			Expect(block).To(ContainSubstring("/supervisor:jump 1907"))
			Expect(block).NotTo(ContainSubstring("<form"))
			Expect(block).NotTo(ContainSubstring("<button"))
		})

		// An unresolvable pane must render absent rather than as a stand-in: a
		// jump command naming no pane is a value presented as resolved that is
		// not. See the attention item schema § silence 7.
		It("renders no jump command when no pane resolved", func() {
			body, item := renderPage(permissionRequest(), pkg.Provenance{})
			Expect(rowBlock(body, item.ItemID)).NotTo(ContainSubstring("/supervisor:jump"))
		})

		// Read-aloud is configured on this suite's page, so this is the case that
		// matters: even with a tts server wired, a permission row carries no
		// control at all. The read-aloud button renders on `message` rows only,
		// which is what keeps SC2's grep clean for `<form>` and `<button>` on a
		// permission block.
		It("renders no read-aloud control even when read-aloud is enabled", func() {
			body, item := renderPage(permissionRequest(), pkg.Provenance{Pane: "1907"})
			block := rowBlock(body, item.ItemID)

			Expect(block).NotTo(ContainSubstring("data-speak"))
			Expect(block).NotTo(ContainSubstring("<button"))
		})
	})

	// The two classes must not bleed into each other: a page carrying both is
	// the case where a page-wide grep would pass on the wrong row.
	It(
		"renders controls on the message row and none on the permission row of the same page",
		func() {
			message, err := store.Push(ctx, messageRequest())
			Expect(err).To(BeNil())
			permission, err := store.Push(ctx, permissionRequest())
			Expect(err).To(BeNil())
			provenance.ResolveReturns(pkg.Provenances{
				permission.ItemID: pkg.Provenance{Pane: "1907", PaneRecorded: true, Routable: true},
			})

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			resp := httptest.NewRecorder()
			httpHandler.ServeHTTP(resp, req)
			body := resp.Body.String()

			Expect(rowBlock(body, message.ItemID)).To(ContainSubstring("<form"))
			Expect(rowBlock(body, permission.ItemID)).NotTo(ContainSubstring("<form"))
			Expect(rowBlock(body, permission.ItemID)).NotTo(ContainSubstring("<button"))
		},
	)

	// The dimmed record is the answered item's card: it carries what was
	// recorded and offers nothing to act on. The positive controls are the
	// load-bearing half — a page that rendered no rows at all, or that dimmed
	// every row, would pass the negative assertions alone.
	Describe("an answered item", func() {
		// render serves the board once and returns the document, so a case can
		// push several items and assert on the single page they share.
		render := func() string {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			resp := httptest.NewRecorder()
			httpHandler.ServeHTTP(resp, req)
			Expect(resp.Code).To(Equal(http.StatusOK))
			return resp.Body.String()
		}

		// dimmedRow reports whether the row for itemID carries the dimmed class.
		// The class sits in the opening <li> tag, before data-item-id, so rowBlock
		// — which starts at that attribute — would not see it.
		dimmedRow := func(body string, itemID pkg.ItemID) bool {
			return strings.Contains(
				body,
				`class="item dimmed" data-item-id="`+itemID.String()+`"`,
			)
		}

		// messageItem builds a `message` declaration with a caller-supplied key and
		// payload, so two rows can coexist on one page without dedup collapsing
		// them into one.
		messageItem := func(dedupKey pkg.DedupKey, payload pkg.Payload) pkg.PushRequest {
			producerID := pkg.ProducerID("producer-" + dedupKey.String())
			return pkg.PushRequest{
				ProducerID:      producerID,
				ProducerKind:    pkg.SessionProducerKind,
				LivenessRef:     pkg.LivenessRef("session:" + producerID.String()),
				DedupKey:        dedupKey,
				InterruptClass:  "pick",
				Payload:         payload,
				AnswerMechanism: pkg.MessageAnswerMechanism,
			}
		}

		It("renders a dimmed card carrying the recorded answer", func() {
			item, err := store.Push(ctx, messageItem("answered-1", "Which surface?"))
			Expect(err).To(BeNil())
			answer := pkg.Answer{Kind: pkg.TextAnswerKind, Value: "the board"}
			_, err = store.Answer(ctx, item.ItemID, "attention-board", "", "", &answer, nil, nil)
			Expect(err).To(BeNil())

			body := render()
			Expect(dimmedRow(body, item.ItemID)).To(BeTrue())

			block := rowBlock(body, item.ItemID)
			Expect(block).To(ContainSubstring("record-answer"))
			Expect(block).To(ContainSubstring("answered: the board"))
		})

		It(
			"renders no form and no acknowledge, dismiss or next control on the dimmed card",
			func() {
				item, err := store.Push(ctx, messageItem("answered-2", "Which surface?"))
				Expect(err).To(BeNil())
				answer := pkg.Answer{Kind: pkg.TextAnswerKind, Value: "the board"}
				_, err = store.Answer(
					ctx,
					item.ItemID,
					"attention-board",
					"",
					"",
					&answer,
					nil,
					nil,
				)
				Expect(err).To(BeNil())

				block := rowBlock(render(), item.ItemID)
				Expect(block).NotTo(ContainSubstring("<form"))
				Expect(block).NotTo(ContainSubstring("Acknowledge"))
				Expect(block).NotTo(ContainSubstring("Dismiss"))
				Expect(block).NotTo(ContainSubstring("Next"))
			},
		)

		// Positive control: an open row on the same page must stay a prompt. A page
		// that dimmed every row would pass the negative assertions above and is
		// caught here.
		It("leaves an open message item undimmed with its answer form, in the same render", func() {
			answered, err := store.Push(ctx, messageItem("answered-3", "Answered question?"))
			Expect(err).To(BeNil())
			answer := pkg.Answer{Kind: pkg.TextAnswerKind, Value: "the board"}
			_, err = store.Answer(
				ctx,
				answered.ItemID,
				"attention-board",
				"",
				"",
				&answer,
				nil,
				nil,
			)
			Expect(err).To(BeNil())
			open, err := store.Push(ctx, messageItem("open-3", "Open question?"))
			Expect(err).To(BeNil())

			body := render()

			Expect(dimmedRow(body, answered.ItemID)).To(BeTrue())
			Expect(rowBlock(body, answered.ItemID)).NotTo(ContainSubstring("<form"))

			Expect(dimmedRow(body, open.ItemID)).To(BeFalse())
			Expect(rowBlock(body, open.ItemID)).To(ContainSubstring("<form"))
		})

		// Positive control alongside: the open row must be on the page, so an empty
		// document cannot pass this by rendering nothing.
		It("omits a closed item from the board entirely", func() {
			open, err := store.Push(ctx, messageItem("open-4", "Still open?"))
			Expect(err).To(BeNil())
			closed, err := store.Push(ctx, messageItem("closed-4", "Already closed?"))
			Expect(err).To(BeNil())
			_, err = store.Close(ctx, closed.ItemID, "", nil)
			Expect(err).To(BeNil())

			body := render()
			Expect(body).To(ContainSubstring(`data-item-id="` + open.ItemID.String() + `"`))
			Expect(body).NotTo(ContainSubstring(`data-item-id="` + closed.ItemID.String() + `"`))
		})

		It("renders the decision on a dimmed permission item", func() {
			item, err := store.Push(ctx, permissionRequest())
			Expect(err).To(BeNil())
			_, err = store.Answer(
				ctx,
				item.ItemID,
				"operator",
				"",
				pkg.AllowDecision,
				nil,
				nil,
				nil,
			)
			Expect(err).To(BeNil())

			body := render()
			Expect(dimmedRow(body, item.ItemID)).To(BeTrue())

			block := rowBlock(body, item.ItemID)
			Expect(block).To(ContainSubstring("decision: allow"))
			// A renderer reading `answer` for every mechanism would show this blank.
			Expect(block).NotTo(ContainSubstring("no decision recorded"))
		})

		// The per-kind matrix the store records: a text answer, an option answer, a
		// skip, and a permission item's allow and deny. A table because the rule is
		// "read the field the mechanism names", asserted once per kind.
		DescribeTable("renders the recorded answer for each answer kind",
			func(request pkg.PushRequest, answerItem func(itemID pkg.ItemID), expected string) {
				item, err := store.Push(ctx, request)
				Expect(err).To(BeNil())
				answerItem(item.ItemID)

				body := render()
				Expect(dimmedRow(body, item.ItemID)).To(BeTrue())

				block := rowBlock(body, item.ItemID)
				Expect(block).To(ContainSubstring("record-answer"))
				Expect(block).To(ContainSubstring(expected))
			},
			Entry("a text answer",
				messageItem("kind-text", "Q?"),
				func(itemID pkg.ItemID) {
					answer := pkg.Answer{Kind: pkg.TextAnswerKind, Value: "a free-text answer"}
					_, err := store.Answer(
						ctx,
						itemID,
						"attention-board",
						"",
						"",
						&answer,
						nil,
						nil,
					)
					Expect(err).To(BeNil())
				}, "a free-text answer"),
			Entry("an option answer",
				messageItem("kind-option", "Q?"),
				func(itemID pkg.ItemID) {
					answer := pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "the board"}
					_, err := store.Answer(
						ctx,
						itemID,
						"attention-board",
						"",
						"",
						&answer,
						nil,
						nil,
					)
					Expect(err).To(BeNil())
				}, "the board"),
			Entry("a skip",
				messageItem("kind-skip", "Q?"),
				func(itemID pkg.ItemID) {
					answer := pkg.Answer{Kind: pkg.SkipAnswerKind}
					_, err := store.Answer(
						ctx,
						itemID,
						"attention-board",
						"",
						"",
						&answer,
						nil,
						nil,
					)
					Expect(err).To(BeNil())
				}, "skipped"),
			Entry("an allow decision",
				permissionRequest(),
				func(itemID pkg.ItemID) {
					_, err := store.Answer(
						ctx,
						itemID,
						"operator",
						"",
						pkg.AllowDecision,
						nil,
						nil,
						nil,
					)
					Expect(err).To(BeNil())
				}, "decision: allow"),
			Entry("a deny decision",
				permissionRequest(),
				func(itemID pkg.ItemID) {
					_, err := store.Answer(
						ctx,
						itemID,
						"operator",
						"",
						pkg.DenyDecision,
						nil,
						nil,
						nil,
					)
					Expect(err).To(BeNil())
				}, "decision: deny"),
		)

		// The `answers` branch: one entry per tab, rendered for the operator to read
		// back. Separate from the table because it asserts two questions on one card
		// rather than one answer kind.
		It("renders every tab's answer on a dimmed multi-question item", func() {
			request := messageItem("multi-1", "Two questions?")
			request.Questions = pkg.Questions{
				{Tab: "left", Payload: "Left?"},
				{Tab: "right", Payload: "Right?"},
			}
			item, err := store.Push(ctx, request)
			Expect(err).To(BeNil())
			_, err = store.Answer(
				ctx,
				item.ItemID,
				"attention-board",
				"",
				"",
				nil,
				pkg.Answers{
					{Question: "left", Answer: pkg.Answer{Kind: pkg.TextAnswerKind, Value: "one"}},
					{Question: "right", Answer: pkg.Answer{Kind: pkg.TextAnswerKind, Value: "two"}},
				},
				nil,
			)
			Expect(err).To(BeNil())

			block := rowBlock(render(), item.ItemID)
			Expect(block).To(ContainSubstring("left: one"))
			Expect(block).To(ContainSubstring("right: two"))
		})
	})
})
