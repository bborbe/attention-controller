// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"time"

	libboltkv "github.com/bborbe/boltkv"
	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

// The HOLD: an answered card stays in the board's open set until an arm observes
// delivery, so an answer whose delivery never landed cannot make the board show a
// parked worker as unblocked.
//
// ⚠️ The first spec is the one that stops a build that never holds: before the
// hold, Answer moved the card straight to AnsweredState, so a build that reverted
// the change would fail it. SC3 requires exactly that reachability assertion.
var _ = Describe("Answer hold", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

		liveness := &mocks.SessionLivenessChecker{}
		liveness.IsLiveReturns(true)

		store = pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			liveness,
			libtime.NewCurrentDateTime(),
			libtime.Duration(15*60*1e9),
		)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	push := func() *pkg.Item {
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      "session-a",
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:session-a"),
			DedupKey:        "hold-gate",
			InterruptClass:  "approve",
			Payload:         "deploy prod?",
			AnswerMechanism: pkg.PermissionAnswerMechanism,
		})
		Expect(err).To(BeNil())
		return item
	}

	answer := func(item *pkg.Item) *pkg.Item {
		got, err := store.Answer(
			ctx, item.ItemID, "attention-board", "", pkg.AllowDecision, nil, nil, nil,
		)
		Expect(err).To(BeNil())
		return got
	}

	It("keeps an answered card OPEN until delivery is observed", func() {
		item := push()
		got := answer(item)

		// A build that never holds moves this to AnsweredState here and fails.
		Expect(got.State).To(Equal(pkg.OpenState))
		Expect(got.AnsweredAt).NotTo(BeNil())
	})

	It("settles the card to answered on SettleAnswer", func() {
		item := push()
		answer(item)

		settled, err := store.SettleAnswer(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(settled.State).To(Equal(pkg.AnsweredState))
	})

	It("is idempotent when the card is already answered", func() {
		item := push()
		answer(item)
		_, err := store.SettleAnswer(ctx, item.ItemID)
		Expect(err).To(BeNil())

		again, err := store.SettleAnswer(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(again.State).To(Equal(pkg.AnsweredState))
	})

	It("refuses a second answer while the card is held", func() {
		item := push()
		answer(item)

		_, err := store.Answer(
			ctx, item.ItemID, "attention-board", "", pkg.DenyDecision, nil, nil, nil,
		)
		Expect(err).NotTo(BeNil())
		Expect(errors.Is(err, pkg.ErrAlreadyAnswered)).To(BeTrue())
	})

	It("closes an undelivered open card carrying the FAILED marker", func() {
		item := push()
		answer(item)

		// The arm writes the undeliverable `failed` record at the bound, then
		// closes — the board renders the visible marker only for `failed`
		// (DeliveryReport.Status == failed), never for `held`. So the close must
		// keep the `failed` record, which recordCloseAttempt does (it never
		// overwrites an existing attempt).
		_, err := store.RecordAttempt(
			ctx,
			item.ItemID,
			"attention-watcher:undeliverable",
			pkg.FailedOutcome,
		)
		Expect(err).To(BeNil())

		closed, err := store.CloseUndelivered(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(closed.State).To(Equal(pkg.ClosedState))

		report, err := store.Delivery(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(
			report.Status,
		).To(Equal(pkg.FailedStatus), "SC4: the close must carry the visible undeliverable marker")
	})

	It("records held on a bare close with no prior attempt", func() {
		item := push()
		answer(item)

		closed, err := store.CloseUndelivered(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(closed.State).To(Equal(pkg.ClosedState))

		// No arm looked, so the close backfills `held` — an honest record, but NOT
		// the SC4 marker. The bound path writes `failed` first (the spec above).
		report, err := store.Delivery(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(report.Status).To(Equal(pkg.HeldStatus))
	})

	It("pins the delivery bound inside SC4's range", func() {
		// SC4 requires the bound outlast the supervisor's 35 s delivery wait and
		// stay within its 600 s ceiling. The constant must not drift out of that
		// window when the retry backoff is retuned.
		Expect(pkg.AttentionDeliveryBound).To(BeNumerically(">=", 35*time.Second))
		Expect(pkg.AttentionDeliveryBound).To(BeNumerically("<=", 600*time.Second))
	})

	It("keeps a held card in the board's open set", func() {
		item := push()
		answer(item)

		// ReadBoard is the board's read. The held card must still be returned —
		// the answered twin suppression must not drop it against its own record.
		board, err := store.ReadBoard(ctx)
		Expect(err).To(BeNil())
		found := false
		for _, row := range board {
			if row.ItemID == item.ItemID {
				found = true
			}
		}
		Expect(found).To(BeTrue(), "held card must stay on the board")
	})
})
