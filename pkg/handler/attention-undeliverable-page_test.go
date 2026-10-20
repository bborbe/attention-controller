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

	BeforeEach(func() {
		ctx = context.Background()
		var err error
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
			libtime.NewCurrentDateTime(),
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
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		resp := httptest.NewRecorder()
		httpHandler.ServeHTTP(resp, req)
		Expect(resp.Code).To(Equal(http.StatusOK))
		return resp.Body.String()
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

	It("renders no marker on a card with no attempt record", func() {
		item := answered("undeliverable-never", "Which surface?")
		// Pin the fixture's meaning: no attempt record on an item answered after
		// the trail shipped derives `never_attempted`, not `failed`.
		report, err := store.Delivery(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(report.Status).To(Equal(pkg.NeverAttemptedStatus))

		row := rowBlock(render(), item.ItemID)
		Expect(row).To(ContainSubstring(item.Payload.String()))
		Expect(row).NotTo(ContainSubstring("delivery-undeliverable"))
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

		req := httptest.NewRequest(http.MethodGet, "/", nil)
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
})
