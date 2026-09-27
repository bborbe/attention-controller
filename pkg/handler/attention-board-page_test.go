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

// The board renders no ANSWER control on any class, from 2026-09-27. Before
// that date it rendered them for `message` items and none for any other class,
// and the negative half was the load-bearing one: an ANSWER control on a
// `permission` item would let the board answer a gate that only the operator
// may answer, in the session that raised it, which the schema calls permission
// laundering. See the attention item schema § Answer routing and silence 12.
//
// ⚠️ AMENDED 2026-09-27: this said "and nothing for every other class", which is
// no longer true. A `permission` row renders three controls that are NOT answers
// — the jump corner (navigation), the corner X (the clear), and the read-aloud
// control (a utility). The X closes the card without answering, so it launders
// nothing; see [[Attention Item Schema]] § The corner X. The read-aloud control
// records no answer either, and it renders on every row whose `Speak` is set:
// the template gate moved from `{{if and .Message .Speak}}` to `{{if .Speak}}`,
// so the class conjunct is gone and "a tts server is configured" is the only
// condition left. ⚠️ Three specs below used `NotTo("<button")` as a proxy for
// "carries no control" and have been amended for it — twice when the jump corner
// and the X landed, once more when the read-aloud control did. Assert on the
// ANSWER-control markers (`data-ack`, `<form`, `<input`), never on `<button`.
//
// ⚠️ AMENDED AGAIN 2026-09-27: a board answer no longer releases a gate — the
// consumer requires `resolved_by`, which the board's JavaScript never sends — so
// a `message` card is display-only and renders no ANSWER control either. Its
// wrapper is `<div class="answer-readonly">`, each option is a plain div rather
// than a `<label>` around an input, and the card ends with the `readonly-note`
// saying so. The ANSWER-control markers below therefore read the same on every
// class — present nowhere — and the specs that pinned the removed form, skip,
// radio, checkbox and free-text controls now pin their absence instead.
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

		It(
			"renders a read-only card instead of a form, a skip control and a free-text field",
			func() {
				body, item := renderPage(messageRequest(), pkg.Provenance{})
				block := rowBlock(body, item.ItemID)

				// ⚠️ AMENDED 2026-09-27 — the contract changed: a board answer no
				// longer releases a gate, so the message card is display-only. The
				// form, the skip control and the free-text field are gone; the
				// read-only note stands in their place.
				Expect(block).To(ContainSubstring(`class="answer-readonly"`))
				Expect(block).To(ContainSubstring(`class="readonly-note"`))
				Expect(block).To(ContainSubstring("A board answer no longer releases the gate."))
				Expect(block).NotTo(ContainSubstring("<form"))
				Expect(block).NotTo(ContainSubstring(`value="skip"`))
				Expect(block).NotTo(ContainSubstring(`type="text"`))
			},
		)

		It("renders no jump command", func() {
			body, item := renderPage(messageRequest(), pkg.Provenance{Pane: "1907"})
			Expect(rowBlock(body, item.ItemID)).NotTo(ContainSubstring("/supervisor:jump"))
		})

		// The control is icon-only, so its `aria-label` is the only name it has —
		// and the assertion reads that attribute, never a text node. A bare
		// `Read aloud` substring check would also pass on a text button carrying no
		// accessible name at all, which is the shape this change moved away from.
		It("renders an icon-only read-aloud control carrying its own accessible name", func() {
			body, item := renderPage(messageRequest(), pkg.Provenance{})
			block := rowBlock(body, item.ItemID)

			Expect(block).To(ContainSubstring("data-speak"))
			Expect(block).To(ContainSubstring(`aria-label="Read aloud"`))
			Expect(block).To(ContainSubstring(`title="Read aloud"`))
			Expect(block).To(ContainSubstring(`class="speak-icon"`))
			Expect(block).To(ContainSubstring(`aria-hidden="true"`))
			// Icon-only: the svg is the control's whole content, so the markup runs
			// name -> svg -> close with no text node anywhere inside the button.
			Expect(block).To(ContainSubstring(`title="Read aloud"><svg`))
			Expect(block).To(ContainSubstring(`</svg></button>`))
			// ⚠️ AMENDED 2026-09-27 — the contract changed: a board answer no
			// longer releases a gate, so a message card renders no actions row at
			// all. The read-aloud control is a utility rather than a decision, so
			// it sits in the card's corner and never in a row of answer controls —
			// and there is no longer a row for it to sit in.
			Expect(block).NotTo(ContainSubstring(`class="actions"`))
			Expect(block).NotTo(ContainSubstring("Submit answer"))
		})
	})

	Describe("a permission item", func() {
		It("renders zero answer controls", func() {
			body, item := renderPage(permissionRequest(), pkg.Provenance{})
			block := rowBlock(body, item.ItemID)

			Expect(block).NotTo(ContainSubstring("<form"))
			// ⚠️ AMENDED 2026-09-27 — a permission row now DOES render one
			// button: the jump corner, disabled, because the operator's ask is
			// that the control sit in the same place on every card, and with the
			// previous exception 2 of 77 live cards rendered none. What stays
			// true, and is what this spec is for, is that a permission row
			// carries no ANSWER control.
			Expect(block).NotTo(ContainSubstring("data-ack"))
			Expect(block).NotTo(ContainSubstring("<input"))
			Expect(block).To(ContainSubstring(`class="jump-corner"`))
			Expect(block).To(ContainSubstring(`disabled`))
			Expect(block).NotTo(ContainSubstring(`data-jump`))
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
			// ⚠️ AMENDED 2026-09-27 (second time) — this asserted
			// NotTo("<button"), a proxy for "carries no control". A permission
			// row now renders THREE buttons: the jump corner, the corner X, and
			// the read-aloud control. The proxy is what broke, not the property,
			// so it is replaced by the property: none of them is an ANSWER
			// control.
			Expect(block).NotTo(ContainSubstring("data-ack"))
			Expect(block).NotTo(ContainSubstring("<input"))
			Expect(block).NotTo(ContainSubstring(`value="skip"`))
		})

		// An unresolvable pane must render absent rather than as a stand-in: a
		// jump command naming no pane is a value presented as resolved that is
		// not. See the attention item schema § silence 7.
		It("renders no jump command when no pane resolved", func() {
			body, item := renderPage(permissionRequest(), pkg.Provenance{})
			Expect(rowBlock(body, item.ItemID)).NotTo(ContainSubstring("/supervisor:jump"))
		})

		// ⚠️ INVERTED 2026-09-27. This spec previously asserted the opposite —
		// that a permission row carried no read-aloud control even with a tts
		// server wired — and it was the guard holding the class exclusion in
		// place. The operator's ruling of 2026-09-27 is that permission cards
		// carry the read-aloud control too, so the spec now asserts the control
		// is PRESENT and keeps a negative clause for what a permission row must
		// still not carry: an answer control.
		// ⚠️ The `NotTo("<button")` assertion this spec also carried is DROPPED
		// rather than re-pointed: `data-speak` is the whole property, and the
		// `<button` proxy stopped meaning "no control" once a permission row
		// rendered buttons of its own — first the jump corner, then the X.
		It("renders the read-aloud control when read-aloud is enabled", func() {
			body, item := renderPage(permissionRequest(), pkg.Provenance{Pane: "1907"})
			block := rowBlock(body, item.ItemID)

			Expect(block).To(ContainSubstring("data-speak"))
			Expect(block).To(ContainSubstring(`aria-label="Read aloud"`))
			Expect(block).To(ContainSubstring(`class="speak-icon"`))
			// The negative half: the control is a utility, not an answer control.
			Expect(block).NotTo(ContainSubstring("<form"))
			Expect(block).NotTo(ContainSubstring("data-ack"))
			Expect(block).NotTo(ContainSubstring(`value="skip"`))
		})
	})

	// The two classes must not bleed into each other: a page carrying both is
	// the case where a page-wide grep would pass on the wrong row.
	// ⚠️ RENAMED 2026-09-27: this read "renders controls on the message row and
	// none on the permission row of the same page". "None" stopped being true
	// when the corner X landed on permission rows; the property that survives,
	// and the one the two-classes-must-not-bleed framing is for, is that the
	// permission row carries no ANSWER control.
	// ⚠️ AMENDED AGAIN 2026-09-27: a board answer no longer releases a gate, so
	// the message row's card is display-only and carries no answer control
	// either. The row-scoped split this spec guards is now "the message row
	// renders the read-only card, the permission row renders no card at all".
	It(
		"renders a read-only card on the message row and no card on the permission row of the same page",
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

			// ⚠️ AMENDED 2026-09-27: the message card is display-only, so the
			// positive assertion is the read-only wrapper and the negative one is
			// the form it replaced — both halves, so the spec fails if the form
			// comes back.
			Expect(rowBlock(body, message.ItemID)).To(ContainSubstring(`class="answer-readonly"`))
			Expect(rowBlock(body, message.ItemID)).NotTo(ContainSubstring("<form"))
			Expect(rowBlock(body, message.ItemID)).NotTo(ContainSubstring("<input"))
			Expect(rowBlock(body, permission.ItemID)).NotTo(ContainSubstring("<form"))
			// ⚠️ AMENDED 2026-09-27 (second time) — the `<button` proxy again.
			// The permission row renders the jump corner, the corner X and the
			// read-aloud control; what must not reach it is an ANSWER control.
			Expect(rowBlock(body, permission.ItemID)).To(ContainSubstring("data-speak"))
			Expect(rowBlock(body, permission.ItemID)).NotTo(ContainSubstring("data-ack"))
			Expect(rowBlock(body, permission.ItemID)).NotTo(ContainSubstring("<input"))
			Expect(rowBlock(body, permission.ItemID)).NotTo(ContainSubstring(`value="skip"`))
		},
	)

	// ⚠️ The negative control for the gate itself, added 2026-09-27. Dropping the
	// `Message` conjunct must not be achieved by dropping the `Speak` gate too:
	// with no tts server configured, no row renders a speaker at all. `Speak` is
	// set from the page-level `speakEnabled`, so the reachable instance of
	// `Speak == false` is a whole page, never a single card — a probe that cannot
	// produce a `Speak == false` row is measuring nothing.
	//
	// The positive clause is load-bearing: the absence alone is also satisfied by
	// a page that rendered no rows at all, so each row is asserted present first.
	It("renders no read-aloud control on any row when read-aloud is disabled", func() {
		message, err := store.Push(ctx, messageRequest())
		Expect(err).To(BeNil())
		permission, err := store.Push(ctx, permissionRequest())
		Expect(err).To(BeNil())
		provenance.ResolveReturns(pkg.Provenances{
			permission.ItemID: pkg.Provenance{Pane: "1907", PaneRecorded: true, Routable: true},
		})

		noSpeakHandler := handler.NewAttentionPageHandler(
			store,
			provenance,
			false,
			pkg.NewJumpTokenReader(""),
		)

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		resp := httptest.NewRecorder()
		noSpeakHandler.ServeHTTP(resp, req)
		Expect(resp.Code).To(Equal(http.StatusOK))
		body := resp.Body.String()

		// Positive clause first: both rows rendered, so the absence below is a
		// withheld control rather than an empty page.
		Expect(rowBlock(body, message.ItemID)).To(ContainSubstring("data-item-id"))
		Expect(rowBlock(body, permission.ItemID)).To(ContainSubstring("data-item-id"))

		// ⚠️ Asserted per ROW, never on the whole body: `data-speak` also appears
		// in the page's own script (`querySelectorAll('button[data-speak]')`), so a
		// body-wide substring check fails on a correctly-disabled page.
		Expect(rowBlock(body, message.ItemID)).NotTo(ContainSubstring("data-speak"))
		Expect(rowBlock(body, permission.ItemID)).NotTo(ContainSubstring("data-speak"))
	})

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

		// ⚠️ Added 2026-09-27 with the dimmed-record read-aloud fix. The dimmed
		// record card is a record, not a prompt, so it carries no control that
		// offers an answer — the read-aloud control included, because it asks the
		// tts server to read the QUESTION aloud, which is the act of a prompt.
		//
		// ⚠️ Both halves sit in ONE render, deliberately. The negative alone is
		// also satisfied by a page that rendered nothing, and a fix that removed
		// the control from EVERY row would satisfy the negative while breaking the
		// three legitimate carriers — so all three are asserted PRESENT in the
		// same document that asserts the dimmed cards are bare.
		//
		// ⚠️ Every assertion is scoped to the row's own block via rowBlock, never
		// the whole body: `data-speak` also appears in the page's own script
		// (`querySelectorAll('button[data-speak]')`), so a body-wide substring
		// check finds a copy whether the card renders the control or not.
		It(
			"withholds read-aloud from every dimmed card and keeps it on every open row, in one render",
			func() {
				// Two local builders: the outer permissionRequest() carries a fixed
				// dedup key, and the open permission row must coexist with the
				// dimmed one rather than dedup into it.
				permissionItem := func(
					dedupKey pkg.DedupKey,
					payload pkg.Payload,
				) pkg.PushRequest {
					producerID := pkg.ProducerID("producer-" + dedupKey.String())
					return pkg.PushRequest{
						ProducerID:      producerID,
						ProducerKind:    pkg.SessionProducerKind,
						LivenessRef:     pkg.LivenessRef("session:" + producerID.String()),
						DedupKey:        dedupKey,
						InterruptClass:  "approve",
						Payload:         payload,
						AnswerMechanism: pkg.PermissionAnswerMechanism,
					}
				}
				ackItem := func(dedupKey pkg.DedupKey, payload pkg.Payload) pkg.PushRequest {
					producerID := pkg.ProducerID("producer-" + dedupKey.String())
					return pkg.PushRequest{
						ProducerID:      producerID,
						ProducerKind:    pkg.SessionProducerKind,
						LivenessRef:     pkg.LivenessRef("session:" + producerID.String()),
						DedupKey:        dedupKey,
						InterruptClass:  "approve",
						Payload:         payload,
						AnswerMechanism: pkg.AckAnswerMechanism,
					}
				}

				dimmedMessage, err := store.Push(
					ctx,
					messageItem("speak-dimmed-msg", "Which surface?"),
				)
				Expect(err).To(BeNil())
				answer := pkg.Answer{Kind: pkg.TextAnswerKind, Value: "the board"}
				_, err = store.Answer(
					ctx,
					dimmedMessage.ItemID,
					"attention-board",
					"",
					"",
					&answer,
					nil,
					nil,
				)
				Expect(err).To(BeNil())

				dimmedPermission, err := store.Push(
					ctx,
					permissionItem("speak-dimmed-gate", "Deploy to prod?"),
				)
				Expect(err).To(BeNil())
				_, err = store.Answer(
					ctx,
					dimmedPermission.ItemID,
					"operator",
					"",
					pkg.AllowDecision,
					nil,
					nil,
					nil,
				)
				Expect(err).To(BeNil())

				openMessage, err := store.Push(ctx, messageItem("speak-open-msg", "Still open?"))
				Expect(err).To(BeNil())
				openAck, err := store.Push(
					ctx,
					ackItem("speak-open-ack", "the nightly sweep failed"),
				)
				Expect(err).To(BeNil())
				openPermission, err := store.Push(
					ctx,
					permissionItem("speak-open-gate", "Deploy to staging?"),
				)
				Expect(err).To(BeNil())

				body := render()

				// ⚠️ Positive clause first, on all five: every row is present and
				// carries its own body, so the withheld control below is a withheld
				// control and not a missing row.
				Expect(dimmedRow(body, dimmedMessage.ItemID)).To(BeTrue())
				Expect(rowBlock(body, dimmedMessage.ItemID)).To(ContainSubstring("record-answer"))
				Expect(dimmedRow(body, dimmedPermission.ItemID)).To(BeTrue())
				Expect(
					rowBlock(body, dimmedPermission.ItemID),
				).To(ContainSubstring("decision: allow"))
				Expect(dimmedRow(body, openMessage.ItemID)).To(BeFalse())
				Expect(dimmedRow(body, openAck.ItemID)).To(BeFalse())
				Expect(dimmedRow(body, openPermission.ItemID)).To(BeFalse())

				// SC1 + SC3 — no read-aloud control on either dimmed card. An `ack`
				// item never reaches `answered`, so no third dimmed case exists to
				// probe.
				Expect(rowBlock(body, dimmedMessage.ItemID)).NotTo(ContainSubstring("data-speak"))
				Expect(
					rowBlock(body, dimmedPermission.ItemID),
				).NotTo(ContainSubstring("data-speak"))

				// SC2 — the three legitimate carriers keep it, in this same load.
				Expect(rowBlock(body, openMessage.ItemID)).To(ContainSubstring("data-speak"))
				Expect(rowBlock(body, openAck.ItemID)).To(ContainSubstring("data-speak"))
				Expect(rowBlock(body, openPermission.ItemID)).To(ContainSubstring("data-speak"))
				Expect(
					rowBlock(body, openMessage.ItemID),
				).To(ContainSubstring(`aria-label="Read aloud"`))
			},
		)

		// SC4 — the fix must not smuggle some OTHER control onto the dimmed card
		// in the speaker's place. The schema's own list at line 234 names the form,
		// the option row, `Other…`, Dismiss, Next and the acknowledge control; the
		// form / Acknowledge / Dismiss / Next half is already asserted by the spec
		// above, so this adds the two it does not cover, plus the speaker.
		It("renders no option row and no Other field on the dimmed card", func() {
			item, err := store.Push(ctx, messageItem("dimmed-bare-1", "Which surface?"))
			Expect(err).To(BeNil())
			answer := pkg.Answer{Kind: pkg.TextAnswerKind, Value: "the board"}
			_, err = store.Answer(ctx, item.ItemID, "attention-board", "", "", &answer, nil, nil)
			Expect(err).To(BeNil())

			block := rowBlock(render(), item.ItemID)

			// Positive clause: the record body rendered, so the absences below are
			// a bare record rather than an empty row.
			Expect(block).To(ContainSubstring("record-question"))
			Expect(block).To(ContainSubstring("record-answer"))
			Expect(block).NotTo(ContainSubstring(`class="option"`))
			Expect(block).NotTo(ContainSubstring(`class="other"`))
			Expect(block).NotTo(ContainSubstring("data-speak"))
		})

		// Positive control: an open row on the same page must stay a prompt. A page
		// that dimmed every row would pass the negative assertions above and is
		// caught here.
		It(
			"leaves an open message item undimmed with its read-only card, in the same render",
			func() {
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
				// ⚠️ AMENDED 2026-09-27 — the contract changed: a board answer no
				// longer releases a gate, so the open row's card is display-only. Its
				// read-only card is what marks it a prompt rather than a record.
				Expect(rowBlock(body, open.ItemID)).To(ContainSubstring(`class="answer-readonly"`))
				Expect(rowBlock(body, open.ItemID)).NotTo(ContainSubstring("<form"))
			},
		)

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

	// The board's own view filter: a control at the top of the page that hides
	// the dimmed answered records, so the operator can ask what is left rather
	// than reading records beside open prompts. The filtering is client-side —
	// the page must not re-request the document — so what these specs pin is
	// the contract the script relies on: where the control sits, that it
	// defaults to the rendering the board had before it existed, and which
	// rows its selector matches. The filter's own behaviour in both positions
	// is measured on the running page, not here.
	Describe("the answerable filter", func() {
		renderAt := func(target string) string {
			req := httptest.NewRequest(http.MethodGet, target, nil)
			resp := httptest.NewRecorder()
			httpHandler.ServeHTTP(resp, req)
			Expect(resp.Code).To(Equal(http.StatusOK))
			return resp.Body.String()
		}
		render := func() string { return renderAt("/") }

		// dimmedRow reports whether the row for itemID carries the class the
		// filter selects on. The class sits in the opening <li> tag, before
		// data-item-id, so rowBlock — which starts at that attribute — would
		// not see it.
		dimmedRow := func(body string, itemID pkg.ItemID) bool {
			return strings.Contains(
				body,
				`class="item dimmed" data-item-id="`+itemID.String()+`"`,
			)
		}

		It("renders the control above the first card", func() {
			item, err := store.Push(ctx, messageRequest())
			Expect(err).To(BeNil())
			provenance.ResolveReturns(pkg.Provenances{item.ItemID: pkg.Provenance{}})

			body := render()
			control := strings.Index(body, `data-board-filter`)
			Expect(control).To(BeNumerically(">=", 0), "no filter control rendered")

			firstRow := strings.Index(body, `data-item-id="`)
			Expect(firstRow).To(BeNumerically(">=", 0), "no row rendered")
			Expect(control).To(BeNumerically("<", firstRow))

			// Positive control: the control sits below the board's own heading.
			// "At the top of the board" is a claim about the board, so a
			// document that put the control in the head or above the heading
			// would not satisfy it by being first of nothing.
			Expect(strings.Index(body, "<h1>")).To(BeNumerically("<", control))
		})

		It("defaults to off and leaves the dimmed record on the served page", func() {
			item, err := store.Push(ctx, messageRequest())
			Expect(err).To(BeNil())
			answer := pkg.Answer{Kind: pkg.TextAnswerKind, Value: "the board"}
			_, err = store.Answer(ctx, item.ItemID, "attention-board", "", "", &answer, nil, nil)
			Expect(err).To(BeNil())
			provenance.ResolveReturns(pkg.Provenances{item.ItemID: pkg.Provenance{}})

			body := render()
			Expect(
				body,
			).To(ContainSubstring(`data-board-filter role="switch" aria-checked="false"`))

			// The positive control for the default position: the record the
			// filter would hide is on the page to be hidden. A build that
			// defaulted the filter on, or stopped serving dimmed rows, fails
			// here — and the dimmed record is the detector this topic built, so
			// losing it silently is the failure this pins.
			Expect(dimmedRow(body, item.ItemID)).To(BeTrue())
		})

		It("renders the filter as a labelled switch rather than a bare icon", func() {
			item, err := store.Push(ctx, messageRequest())
			Expect(err).To(BeNil())
			provenance.ResolveReturns(pkg.Provenances{item.ItemID: pkg.Provenance{}})

			// The switch carries the STATE and the visible label carries the
			// MEANING. v0.18.0 rejected an icon-only read-aloud control because
			// it would have moved the control's meaning into an aria-label only
			// assistive tech sees, and this board's governing complaint was
			// cards whose controls were not discoverable at all — so the label
			// is pinned here rather than left to taste.
			body := render()
			Expect(body).To(ContainSubstring(`role="switch"`))
			Expect(body).To(ContainSubstring(`aria-checked="false"`))
			Expect(body).To(ContainSubstring(`class="switch-track"`))
			Expect(body).To(ContainSubstring(`class="switch-knob"`))
			Expect(body).To(ContainSubstring(`<span class="switch-label">Hide answered</span>`))

			// The state must not also be carried on a second attribute: a
			// control reporting both aria-pressed and aria-checked tells
			// assistive tech two different things about one state. Asserted
			// against the control's whole opening tag rather than against the
			// page, so a later board control that legitimately uses aria-pressed
			// does not break this spec for an unrelated reason.
			Expect(body).To(ContainSubstring(
				`<button type="button" class="board-filter" data-board-filter ` +
					`role="switch" aria-checked="false">`,
			))
		})

		It("carries the filter in the URL, and still serves the rows it hides", func() {
			// The operator asked for the view state to be addressable:
			// *"The hide button at the top should be a URL parameter, so a
			// reload of the page keeps the preview setting."* The server renders
			// the switch's initial position from that parameter, so the served
			// markup agrees with the URL the operator is looking at.
			item, err := store.Push(ctx, messageRequest())
			Expect(err).To(BeNil())
			answer := pkg.Answer{Kind: pkg.TextAnswerKind, Value: "the board"}
			_, err = store.Answer(ctx, item.ItemID, "attention-board", "", "", &answer, nil, nil)
			Expect(err).To(BeNil())
			provenance.ResolveReturns(pkg.Provenances{item.ItemID: pkg.Provenance{}})

			body := renderAt("/?hide=answered")
			Expect(body).To(ContainSubstring(`aria-checked="true"`))

			// ⚠️ The dimmed row is still SERVED. The filter is client-side and
			// the switch has to be able to restore what it hid, so a server that
			// dropped these rows would make the control one-way — the opposite
			// of what a view filter is.
			Expect(dimmedRow(body, item.ItemID)).To(BeTrue())
		})

		It("leaves the switch off for an absent or unrecognised value", func() {
			item, err := store.Push(ctx, messageRequest())
			Expect(err).To(BeNil())
			provenance.ResolveReturns(pkg.Provenances{item.ItemID: pkg.Provenance{}})

			// The value names the SET that is hidden, so anything that is not
			// that set leaves the default rendering alone rather than guessing
			// at an intent the URL did not state.
			Expect(renderAt("/")).To(ContainSubstring(`aria-checked="false"`))
			Expect(renderAt("/?hide=")).To(ContainSubstring(`aria-checked="false"`))
			Expect(renderAt("/?hide=closed")).To(ContainSubstring(`aria-checked="false"`))
		})

		It("reads the filter alongside other parameters rather than instead of them", func() {
			item, err := store.Push(ctx, messageRequest())
			Expect(err).To(BeNil())
			provenance.ResolveReturns(pkg.Provenances{item.ItemID: pkg.Provenance{}})

			// The operator's own example URL carries answer-form state too. The
			// filter is read from the same query string, so neither parameter
			// can displace the other.
			body := renderAt("/?text=y&kind=send&hide=answered")
			Expect(body).To(ContainSubstring(`aria-checked="true"`))
		})

		It("pins the script's own copy of the view parameter", func() {
			item, err := store.Push(ctx, messageRequest())
			Expect(err).To(BeNil())
			provenance.ResolveReturns(pkg.Provenances{item.ItemID: pkg.Provenance{}})

			// The parameter name and value exist twice — as Go consts and as
			// literals in the page's script — because the server renders the
			// switch's initial position while the script applies the filter and
			// writes the parameter back. Drift is silent in the direction that
			// matters: change the Go value and the server renders from one
			// parameter while the script writes another, so a reload stops
			// reproducing the view with every other spec still green. The
			// rendered body is already in hand, so the assertion costs nothing.
			body := render()
			Expect(body).To(ContainSubstring(`var HIDE_PARAM = 'hide';`))
			Expect(body).To(ContainSubstring(`var HIDE_ANSWERED = 'answered';`))
		})

		It("leaves an open permission card outside the filter's target set", func() {
			item, err := store.Push(ctx, permissionRequest())
			Expect(err).To(BeNil())
			provenance.ResolveReturns(pkg.Provenances{item.ItemID: pkg.Provenance{}})

			// The ruling keeps permission cards under both toggle positions:
			// they are unresolved and the operator is their only resolver. The
			// filter keys on the dimmed class, so an open permission row
			// carrying that class would be hidden — the ruling reversed by a
			// renderer rather than by a decision.
			body := render()
			Expect(dimmedRow(body, item.ItemID)).To(BeFalse())
			// Positive control: the row is on the page, so this cannot pass by
			// rendering nothing.
			Expect(body).To(ContainSubstring(`data-item-id="` + item.ItemID.String() + `"`))
		})

		It("renders the control on a board with no rows", func() {
			// A control that appeared and disappeared with the store's contents
			// would move under the operator's cursor. With nothing to filter it
			// is inert rather than absent.
			body := render()
			Expect(body).To(ContainSubstring(`data-board-filter`))
			Expect(body).To(ContainSubstring("Nothing needs attention."))
		})
	})
})
