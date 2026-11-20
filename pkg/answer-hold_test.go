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

	It("settles an undelivered card to answered, carrying the FAILED marker", func() {
		item := push()
		answer(item)

		// The arm writes the undeliverable `failed` record at the bound, then the
		// card is SETTLED to answered (not closed) — the board renders the visible
		// marker only for a dimmed answered record with DeliveryReport.Status ==
		// failed, and a closed card is not on the board at all.
		_, err := store.RecordAttempt(
			ctx,
			item.ItemID,
			"attention-watcher:undeliverable",
			pkg.FailedOutcome,
		)
		Expect(err).To(BeNil())

		settled, err := store.SettleAnswer(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(settled.State).To(Equal(pkg.AnsweredState))

		report, err := store.Delivery(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(
			report.Status,
		).To(Equal(pkg.FailedStatus), "SC4: the settled card must carry the visible undeliverable marker")
	})

	It("pins the delivery bound inside SC4's range", func() {
		// SC4 requires the bound outlast the supervisor's 35 s delivery wait and
		// stay within its 600 s ceiling. The constant must not drift out of that
		// window when the retry backoff is retuned.
		Expect(pkg.AttentionDeliveryBound).To(BeNumerically(">=", 35*time.Second))
		Expect(pkg.AttentionDeliveryBound).To(BeNumerically("<=", 600*time.Second))
	})

	It("survives the answered sweep's redundant-re-raise pass", func() {
		item := push()
		answer(item)

		// SweepAnswered's redundant-re-raise pass closes an open item whose ask is
		// in the durable answered index. A held card's own Answer wrote that entry,
		// so without the AnsweredAt guard it would be closed here, before delivery.
		_, err := store.SweepAnswered(ctx, libtime.Duration(1*3600*1e9))
		Expect(err).To(BeNil())

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(
			got.State,
		).To(Equal(pkg.OpenState), "a held card must not be closed as a redundant re-raise")
	})

	It("keeps a held card on the board even past its own expires_at", func() {
		past := libtime.DateTime(time.Now().Add(-1 * time.Hour))
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      "session-a",
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:session-a"),
			DedupKey:        "hold-expiry",
			InterruptClass:  "approve",
			Payload:         "deploy prod?",
			AnswerMechanism: pkg.PermissionAnswerMechanism,
			ExpiresAt:       &past,
		})
		Expect(err).To(BeNil())
		answer(item)

		// The expiry prune must not drop a held card before the watcher closes it with
		// the undeliverable marker; otherwise a producer TTL shorter than the bound
		// would strand the card with no marker.
		board, err := store.ReadBoard(ctx)
		Expect(err).To(BeNil())
		found := false
		for _, row := range board {
			if row.ItemID == item.ItemID {
				found = true
			}
		}
		Expect(found).To(BeTrue(), "a held card must not be pruned past expires_at")
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
