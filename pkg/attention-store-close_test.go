// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

// The Close transition's specs live here rather than in attention-store_test.go
// because that file sits at the repository's 2000-line ceiling. The suite setup
// is repeated rather than shared: each file's Describe owns its own store, and a
// shared fixture would couple their lifecycles.

import (
	"context"

	libboltkv "github.com/bborbe/boltkv"
	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

var _ = Describe("AttentionStore Close", func() {
	var ctx context.Context
	var store pkg.AttentionStore

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		// boltkv, not memorykv: see attention-store_test.go for why the
		// compare-and-set this suite proves needs the real backend.
		db, err := libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())
		DeferCleanup(func() {
			Expect(db.Close()).To(BeNil())
		})
		sessionLivenessChecker := &mocks.SessionLivenessChecker{}
		sessionLivenessChecker.IsLiveReturns(true)
		store = pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			sessionLivenessChecker,
			libtime.NewCurrentDateTime(),
			libtime.Duration(15*60*1e9),
		)
	})

	// pushRequest is the same builder attention-store_test.go carries; it is
	// repeated here for the reason the setup is.
	pushRequest := func(producerID pkg.ProducerID, dedupKey pkg.DedupKey) pkg.PushRequest {
		return pkg.PushRequest{
			ProducerID:      producerID,
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:" + producerID.String()),
			DedupKey:        dedupKey,
			InterruptClass:  "approve",
			Payload:         "deploy prod?",
			AnswerMechanism: pkg.MessageAnswerMechanism,
		}
	}

	Describe("Close", func() {
		It("applies open -> closed", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())
			closed, err := store.Close(ctx, item.ItemID, "", nil)
			Expect(err).To(BeNil())
			Expect(closed.State).To(Equal(pkg.ClosedState))
			Expect(closed.ClosedAt).NotTo(BeNil())
		})

		It("applies answered -> closed", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())
			_, err = store.Answer(ctx, item.ItemID, "telegram", "", "", nil, nil, nil)
			Expect(err).To(BeNil())
			closed, err := store.Close(ctx, item.ItemID, "", nil)
			Expect(err).To(BeNil())
			Expect(closed.State).To(Equal(pkg.ClosedState))
		})

		It("rejects closed -> closed", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())
			_, err = store.Close(ctx, item.ItemID, "", nil)
			Expect(err).To(BeNil())
			_, err = store.Close(ctx, item.ItemID, "", nil)
			Expect(err).NotTo(BeNil())
			Expect(errors.Is(err, pkg.ErrIllegalTransition)).To(BeTrue())
		})

		// pushPermission pushes a `permission` item: a gate the operator alone may
		// answer. It is a question-shaped declaration with the permission mechanism,
		// exactly as the "returns every pushed item while every producer is live"
		// spec builds one.
		pushPermission := func(dedupKey pkg.DedupKey) *pkg.Item {
			request := pushRequest("session-a", dedupKey)
			request.AnswerMechanism = pkg.PermissionAnswerMechanism
			item, err := store.Push(ctx, request)
			Expect(err).To(BeNil())
			return item
		}

		It("refuses an arm-caused close and leaves the gate open", func() {
			item := pushPermission("gate-permission-1")

			_, err := store.Close(ctx, item.ItemID, "attention-board", nil)
			Expect(errors.Is(err, pkg.ErrPermissionCloseWithoutDecision)).To(BeTrue())
			// The two refusals must stay distinguishable: `open -> closed` is a row
			// of the table, so "illegal transition" would name the wrong thing.
			Expect(errors.Is(err, pkg.ErrIllegalTransition)).To(BeFalse())

			// The read-back is the point: a guard that returned an error while the
			// write still landed would be strictly worse than none.
			unchanged, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(unchanged.State).To(Equal(pkg.OpenState))
			Expect(unchanged.ClosedAt).To(BeNil())
			Expect(unchanged.AnsweredAt).To(BeNil())
			Expect(unchanged.AnsweredBy).To(BeEmpty())
			Expect(unchanged.AnsweredClient).To(BeNil())
		})

		It("still answers a permission item that carries a decision", func() {
			item := pushPermission("gate-permission-2")

			_, err := store.Close(ctx, item.ItemID, "attention-board", nil)
			Expect(errors.Is(err, pkg.ErrPermissionCloseWithoutDecision)).To(BeTrue())

			// The observable that separates a refusal from a consumption: the gate
			// is still answerable after the refused close.
			answered, err := store.Answer(
				ctx,
				item.ItemID,
				"operator",
				"",
				pkg.DenyDecision,
				nil,
				nil,
				nil,
			)
			Expect(err).To(BeNil())
			Expect(answered.AnsweredAt).NotTo(BeNil())
			Expect(answered.Decision).To(Equal(pkg.DenyDecision))
			settled, err := store.SettleAnswer(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(settled.State).To(Equal(pkg.AnsweredState))
		})

		It("still lets a producer withdraw its own item, which names no arm", func() {
			item := pushPermission("gate-permission-3")

			closed, err := store.Close(ctx, item.ItemID, "", nil)
			Expect(err).To(BeNil())
			Expect(closed.State).To(Equal(pkg.ClosedState))
			Expect(closed.ClosedAt).NotTo(BeNil())
			Expect(closed.AnsweredBy).To(BeEmpty())
		})

		It("leaves the arm-caused close of a non-permission item untouched", func() {
			request := pushRequest("session-a", "gate-ack-1")
			request.AnswerMechanism = pkg.AckAnswerMechanism
			item, err := store.Push(ctx, request)
			Expect(err).To(BeNil())

			closed, err := store.Close(ctx, item.ItemID, "attention-board", nil)
			Expect(err).To(BeNil())
			Expect(closed.State).To(Equal(pkg.ClosedState))
			Expect(closed.AnsweredBy).To(Equal("attention-board"))
			Expect(closed.AnsweredAt).To(BeNil())
		})

		// ⚠️ Added 2026-11-21 with the dimmed-card corner X. A HELD card is
		// `open` + answered_at: the store holds an answer it has not delivered,
		// and the board renders it dimmed. The X closes it, and the close must
		// NOT be treated as an acknowledgement — the arm that answered must
		// survive, and a `permission` card must not be refused as an unanswered
		// gate, because it already carries its decision.
		It("closes a held permission card and keeps the arm that answered", func() {
			item := pushPermission("gate-permission-held")
			_, err := store.Answer(
				ctx,
				item.ItemID,
				"attention-watch-auto",
				"",
				pkg.AllowDecision,
				nil,
				nil,
				nil,
			)
			Expect(err).To(BeNil())

			held, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(held.State).To(Equal(pkg.OpenState))
			Expect(held.AnsweredAt).NotTo(BeNil())
			Expect(held.Decision).To(Equal(pkg.AllowDecision))

			closed, err := store.Close(ctx, item.ItemID, "attention-board", nil)
			Expect(err).To(BeNil())
			Expect(closed.State).To(Equal(pkg.ClosedState))
			Expect(closed.AnsweredBy).To(Equal("attention-watch-auto"))
			Expect(closed.Decision).To(Equal(pkg.AllowDecision))
			Expect(closed.AnsweredAt).NotTo(BeNil())
		})

		// ⚠️ Added 2026-11-22 with the close-path fix. The event feed's
		// producer-side clear and the producer-withdrawal route both close by
		// dedup key naming NO arm. On a held card that abandons an answer the
		// delivery arm has not carried yet, so it is refused — the hold owns
		// that transition. Measured live 2026-11-22: nine permission cards left
		// the open set `retrying` and unmarked this way.
		It("refuses a close naming no arm on a held card", func() {
			item := pushPermission("gate-permission-held-noarm")
			_, err := store.Answer(
				ctx,
				item.ItemID,
				"attention-watch-auto",
				"",
				pkg.AllowDecision,
				nil,
				nil,
				nil,
			)
			Expect(err).To(BeNil())

			_, err = store.Close(ctx, item.ItemID, "", nil)
			Expect(errors.Is(err, pkg.ErrHeldCardCloseNeedsArm)).To(BeTrue())
			// `open -> closed` is a row of the table, so "illegal transition"
			// would name the wrong thing.
			Expect(errors.Is(err, pkg.ErrIllegalTransition)).To(BeFalse())

			// The read-back is the point: a refusal whose write still landed
			// would be strictly worse than none.
			held, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(held.State).To(Equal(pkg.OpenState))
			Expect(held.ClosedAt).To(BeNil())
			Expect(held.AnsweredAt).NotTo(BeNil())
		})

		// ⚠️ The other half of the fix. The corner X may still close a held card
		// (the operator's 2026-11-21 ruling), but the close is terminal, so a
		// still-`retrying` answer is now abandoned for good. The record must say
		// so with the undeliverable marker rather than reading "still trying"
		// forever.
		It("marks a still-retrying answer abandoned when an arm closes a held card", func() {
			item := pushPermission("gate-permission-held-retrying")
			_, err := store.Answer(
				ctx,
				item.ItemID,
				"attention-watch-auto",
				"",
				pkg.AllowDecision,
				nil,
				nil,
				nil,
			)
			Expect(err).To(BeNil())
			_, err = store.RecordAttempt(
				ctx,
				item.ItemID,
				"watcher:attention-watcher:retrying",
				pkg.RetriedOutcome,
			)
			Expect(err).To(BeNil())

			closed, err := store.Close(ctx, item.ItemID, "attention-board", nil)
			Expect(err).To(BeNil())
			Expect(closed.State).To(Equal(pkg.ClosedState))

			report, err := store.Delivery(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(report.Status).To(Equal(pkg.FailedStatus))
		})
	})
})
