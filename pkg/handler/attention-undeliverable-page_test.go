// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	stdtime "time"

	libboltkv "github.com/bborbe/boltkv"
	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/boardmetrics"
	"github.com/bborbe/attention-controller/pkg/handler"
)

// The undeliverable marker is the board's only statement about delivery: before
// it, an operator whose answer was dropped saw a card that looked exactly like
// one whose answer arrived, because the board rendered nothing about the
// attempt at all.
//
// ⚠️ Every absence assertion is scoped to the card's own ROW and never to the
// page body. The marker's class is also a selector in the page's stylesheet, so
// a body-wide `ContainSubstring` matches the CSS rule and stays green whatever
// the row renders — the same trap the filter-switch specs document for
// `aria-checked`. And every absence case carries a positive control (the row's
// own payload), so a page that dropped the row cannot pass it vacuously.
var _ = Describe("the undeliverable marker on the served page", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var httpHandler http.Handler

	// now is the store's clock. It is MOVABLE so a fixture's answered_at can be
	// placed on either side of DeliveryTrailEpoch: the pre-trail case cannot be
	// produced by a wall clock, because every item such a store creates sits after
	// the epoch.
	var now stdtime.Time
	setClock := func(when stdtime.Time) { now = when }

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		// The clock's initial reading is "now", so every existing case keeps its
		// meaning: an answered card is still after DeliveryTrailEpoch.
		now = stdtime.Now().UTC()
		// A real boltkv DB and a real store, not a fake of either: the marker is
		// rendered from the store's own Delivery read, so the fixture has to be
		// written through the same write path the attempting arm uses.
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

		// The mock defaults to false. Left at its default, every open fixture item
		// is pruned during Read and the page renders empty — every absence
		// assertion below would then pass vacuously.
		sessionLivenessChecker := &mocks.SessionLivenessChecker{}
		sessionLivenessChecker.IsLiveReturns(true)

		store = pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			sessionLivenessChecker,
			libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
				return libtime.DateTime(now)
			}),
			libtime.Duration(15*60*1e9),
		)

		// Left returning nil, so every case exercises the no-provenance path: the
		// marker must not depend on provenance resolving.
		provenance := &mocks.ProvenanceResolver{}

		httpHandler = handler.NewAttentionPageHandler(
			store,
			provenance,
			false,
			GinkgoT().TempDir(),
			testBuildIdentity,
			boardmetrics.NewMetrics(prometheus.NewRegistry()),
		)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	// answered pushes one `message` item and answers it, so the card is a dimmed
	// record — the shape a failed delivery actually decorates. The dedup key is a
	// parameter because a single page carries several cases.
	answered := func(dedupKey pkg.DedupKey, payload pkg.Payload) *pkg.Item {
		producerID := pkg.ProducerID("producer-" + dedupKey.String())
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      producerID,
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:" + producerID.String()),
			DedupKey:        dedupKey,
			InterruptClass:  "pick",
			Payload:         payload,
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		Expect(err).To(BeNil())
		answer := pkg.Answer{Kind: pkg.TextAnswerKind, Value: "the board"}
		_, err = store.Answer(ctx, item.ItemID, "attention-board", "", "", &answer, nil, nil)
		Expect(err).To(BeNil())
		return item
	}

	// rowBlock returns the rendered HTML for one item's row, so an assertion
	// about the marker is scoped to that item rather than to the whole page — and,
	// for the absences, to a subtree the stylesheet cannot reach.
	rowBlock := func(body string, itemID pkg.ItemID) string {
		start := strings.Index(body, `data-item-id="`+itemID.String()+`"`)
		Expect(start).To(BeNumerically(">=", 0), "row for %s not found", itemID)
		rest := body[start:]
		end := strings.Index(rest, "</li>")
		Expect(end).To(BeNumerically(">=", 0))
		return rest[:end]
	}

	// render serves the board once and returns the document, so a case can push
	// several items and assert on the single page they share.
	render := func() string {
		req := httptest.NewRequest(http.MethodGet, "/?producer_kind=all", nil)
		resp := httptest.NewRecorder()
		httpHandler.ServeHTTP(resp, req)
		Expect(resp.Code).To(Equal(http.StatusOK))
		return resp.Body.String()
	}

	// panelOf slices a row at its info panel, the idiom the card-info spec uses,
	// so a panel assertion cannot pass on a line rendered elsewhere in the row.
	panelOf := func(row string) string {
		at := strings.Index(row, `<div class="info-panel"`)
		Expect(at).To(BeNumerically(">=", 0), "info panel not found in row")
		return row[at:]
	}

	// The exact markup the marker renders, written as a hand-written literal
	// rather than built from the production constant: a shared builder would
	// agree with itself whatever it produced.
	const marker = `<div class="delivery-undeliverable">undeliverable</div>`

	It("renders the marker on the row of a card whose answer failed to deliver", func() {
		item := answered("undeliverable-failed", "Which surface?")
		_, err := store.RecordAttempt(ctx, item.ItemID, "attention-watcher", pkg.FailedOutcome)
		Expect(err).To(BeNil())

		row := rowBlock(render(), item.ItemID)
		// The positive control: the row rendered at all, and it is the card this
		// case answered.
		Expect(row).To(ContainSubstring(item.Payload.String()))
		Expect(row).To(ContainSubstring(marker))
	})

	It("renders no marker on a card whose answer was delivered", func() {
		item := answered("undeliverable-delivered", "Which surface?")
		_, err := store.RecordAttempt(ctx, item.ItemID, "attention-watcher", pkg.DeliveredOutcome)
		Expect(err).To(BeNil())

		row := rowBlock(render(), item.ItemID)
		Expect(row).To(ContainSubstring(item.Payload.String()))
		Expect(row).NotTo(ContainSubstring("delivery-undeliverable"))
	})

	It("renders the marker on a card with no attempt record after the trail", func() {
		item := answered("undeliverable-never", "Which surface?")
		// Pin the fixture's meaning: no attempt record on an item answered after
		// the trail shipped derives `never_attempted`, not `failed`.
		report, err := store.Delivery(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(report.Status).To(Equal(pkg.NeverAttemptedStatus))

		row := rowBlock(render(), item.ItemID)
		Expect(row).To(ContainSubstring(item.Payload.String()))
		// ⚠️ This is the assertion the widening inverts. It used to assert no
		// marker, pinning the old `failed`-only predicate; that predicate is now
		// the defect, because the silent half of undelivered answers was the
		// larger one.
		Expect(row).To(ContainSubstring(marker))
	})

	// ⚠️ A mocked store is deliberate here and is the only reachable construction
	// of this input: a real store's Delivery fails only on a bbolt error, which
	// this spec cannot inject. The property under test is the page's fail-soft
	// behaviour — one card's unreadable status must not take the page down — and
	// the fake's Delivery is the only seam that can produce the failure.
	It("renders the card without a marker when the delivery read fails", func() {
		failing := &mocks.AttentionStore{}
		failing.ReadBoardReturns(pkg.Items{
			{
				ItemID:    pkg.ItemID("probe-delivery-error"),
				Payload:   "Which surface?",
				State:     pkg.AnsweredState,
				CreatedAt: libtime.NewCurrentDateTime().Now(),
			},
		}, nil)
		failing.DeliveryReturns(nil, errors.New(ctx, "delivery read failed"))
		provenance := &mocks.ProvenanceResolver{}
		provenance.ResolveReturns(pkg.Provenances{})
		handlerUnderTest := handler.NewAttentionPageHandler(
			failing,
			provenance,
			false,
			GinkgoT().TempDir(),
			testBuildIdentity,
			boardmetrics.NewMetrics(prometheus.NewRegistry()),
		)

		req := httptest.NewRequest(http.MethodGet, "/?producer_kind=all", nil)
		resp := httptest.NewRecorder()
		handlerUnderTest.ServeHTTP(resp, req)

		Expect(resp.Code).To(Equal(http.StatusOK))
		row := rowBlock(resp.Body.String(), pkg.ItemID("probe-delivery-error"))
		// The positive control: the card still rendered, and rendered as its own
		// record rather than as an error page.
		Expect(row).To(ContainSubstring("Which surface?"))
		Expect(row).To(ContainSubstring("record-answer"))
		Expect(row).NotTo(ContainSubstring("delivery-undeliverable"))
	})

	It("marks only the failed card's row when a delivered card sits beside it", func() {
		failed := answered("undeliverable-beside-failed", "Deploy prod?")
		delivered := answered("undeliverable-beside-delivered", "Restart dev?")
		_, err := store.RecordAttempt(ctx, failed.ItemID, "attention-watcher", pkg.FailedOutcome)
		Expect(err).To(BeNil())
		_, err = store.RecordAttempt(
			ctx,
			delivered.ItemID,
			"attention-watcher",
			pkg.DeliveredOutcome,
		)
		Expect(err).To(BeNil())

		body := render()
		Expect(rowBlock(body, failed.ItemID)).To(ContainSubstring(marker))
		Expect(rowBlock(body, delivered.ItemID)).NotTo(ContainSubstring("delivery-undeliverable"))
	})

	It(
		"marks exactly the failed and never-attempted rows among the five outcomes, and never an open card",
		func() {
			// The pre-trail card is created on a clock before the trail epoch, then the
			// clock returns to now so the remaining fixtures are all after it.
			setClock(pkg.DeliveryTrailEpoch.Add(-stdtime.Hour))
			preTrail := answered("undeliverable-pre-trail", "Pre-trail?")
			setClock(stdtime.Now().UTC())

			delivered := answered("undeliverable-outcome-delivered", "Deliver?")
			_, err := store.RecordAttempt(
				ctx,
				delivered.ItemID,
				"attention-watcher",
				pkg.DeliveredOutcome,
			)
			Expect(err).To(BeNil())

			failed := answered("undeliverable-outcome-failed", "Fail?")
			_, err = store.RecordAttempt(ctx, failed.ItemID, "attention-watcher", pkg.FailedOutcome)
			Expect(err).To(BeNil())

			// ⚠️ The dedup key and payload must not contain "held": the panel renders
			// Item.ProducerID, which answered derives as "producer-" + dedupKey, and the
			// held assertion below forbids the word anywhere in the panel.
			held := answered("undeliverable-declined", "Decline?")
			_, err = store.RecordAttempt(ctx, held.ItemID, "attention-watcher", pkg.HeldOutcome)
			Expect(err).To(BeNil())

			neverItem := answered("undeliverable-outcome-never", "Never?")

			// The open card is pushed and never answered. Delivery derives
			// never_attempted for it too (the created_at fallback), so this row is what
			// proves the AnsweredAt guard suppresses the marker.
			openProducer := pkg.ProducerID("producer-undeliverable-open")
			openItem, err := store.Push(ctx, pkg.PushRequest{
				ProducerID:      openProducer,
				ProducerKind:    pkg.SessionProducerKind,
				LivenessRef:     pkg.LivenessRef("session:" + openProducer.String()),
				DedupKey:        "undeliverable-open",
				InterruptClass:  "pick",
				Payload:         "Open?",
				AnswerMechanism: pkg.MessageAnswerMechanism,
			})
			Expect(err).To(BeNil())

			// Pin each fixture's derived status, so the matrix cannot pass on a card
			// that was actually attempted (or, for the open card, on an absence of
			// status rather than on the guard).
			for itemID, want := range map[pkg.ItemID]pkg.DeliveryStatus{
				delivered.ItemID: pkg.DeliveredStatus,
				failed.ItemID:    pkg.FailedStatus,
				held.ItemID:      pkg.HeldStatus,
				neverItem.ItemID: pkg.NeverAttemptedStatus,
				preTrail.ItemID:  pkg.PreTrailUnknownStatus,
				openItem.ItemID:  pkg.NeverAttemptedStatus,
			} {
				report, deliveryErr := store.Delivery(ctx, itemID)
				Expect(deliveryErr).To(BeNil())
				Expect(report.Status).To(Equal(want), "delivery status for %s", itemID)
			}

			body := render()

			// (a) exactly the failed and never-attempted rows carry the marker.
			Expect(rowBlock(body, failed.ItemID)).To(ContainSubstring(marker))
			Expect(rowBlock(body, neverItem.ItemID)).To(ContainSubstring(marker))

			// (b) the other three answered outcomes carry none.
			Expect(
				rowBlock(body, delivered.ItemID),
			).NotTo(ContainSubstring("delivery-undeliverable"))
			Expect(rowBlock(body, held.ItemID)).NotTo(ContainSubstring("delivery-undeliverable"))
			Expect(
				rowBlock(body, preTrail.ItemID),
			).NotTo(ContainSubstring("delivery-undeliverable"))

			// (c) exactly two markers over all six rows. ⚠️ Never count over the body:
			// the class is also a stylesheet selector, so a body-wide count is vacuous.
			total := 0
			for _, itemID := range []pkg.ItemID{
				delivered.ItemID,
				failed.ItemID,
				held.ItemID,
				neverItem.ItemID,
				preTrail.ItemID,
				openItem.ItemID,
			} {
				total += strings.Count(rowBlock(body, itemID), "delivery-undeliverable")
			}
			Expect(total).To(Equal(2))

			// (d) the four reader-visible outcomes name themselves in the panel. Assert
			// the whole element, never the bare word: the delivered row's timeline
			// already carries the stage label "delivered".
			Expect(panelOf(rowBlock(body, delivered.ItemID))).
				To(ContainSubstring(`<div class="delivery-outcome">delivered</div>`))
			Expect(panelOf(rowBlock(body, failed.ItemID))).
				To(ContainSubstring(`<div class="delivery-outcome">failed</div>`))
			Expect(panelOf(rowBlock(body, neverItem.ItemID))).
				To(ContainSubstring(`<div class="delivery-outcome">never_attempted</div>`))
			Expect(panelOf(rowBlock(body, preTrail.ItemID))).
				To(ContainSubstring(`<div class="delivery-outcome">pre_trail_unknown</div>`))

			// (e) a held card renders no delivery-outcome line and no held label at all.
			heldPanel := panelOf(rowBlock(body, held.ItemID))
			Expect(heldPanel).NotTo(ContainSubstring("delivery-outcome"))
			Expect(strings.Contains(strings.ToLower(heldPanel), "held")).To(BeFalse())

			// (f) the open card carries no marker. This is the assertion the sixth
			// fixture exists for: the other five are all answered, so they cannot tell a
			// guarded predicate from an unguarded one.
			Expect(
				rowBlock(body, openItem.ItemID),
			).NotTo(ContainSubstring("delivery-undeliverable"))
		},
	)
})
