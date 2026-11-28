// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"net/http"
	stdtime "time"

	libboltkv "github.com/bborbe/boltkv"
	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/handler"
)

// The undeliverable marker is the board's only statement about delivery: an
// operator whose answer was dropped otherwise sees a card that looks exactly like
// one whose answer landed. These specs drive the REAL store through the board
// JSON read, so the delivery status each card carries is the one the store
// derived, not one a fake handed back.
var _ = Describe("the undeliverable marker in the board JSON", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var httpHandler http.Handler

	var now stdtime.Time
	setClock := func(when stdtime.Time) { now = when }

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		now = stdtime.Now().UTC()
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

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

		httpHandler = handler.NewAttentionBoardReadHandler(
			store,
			&mocks.ProvenanceResolver{},
			false,
			GinkgoT().TempDir(),
		)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

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

	It("marks a card whose answer failed to deliver", func() {
		item := answered("undeliverable-failed", "Which surface?")
		_, err := store.RecordAttempt(ctx, item.ItemID, "attention-watcher", pkg.FailedOutcome)
		Expect(err).To(BeNil())

		entry := boardEntryOf(httpHandler, item.ItemID)
		Expect(entry.Payload).To(Equal(item.Payload))
		Expect(entry.Board.Undeliverable).To(BeTrue())
	})

	It("does not mark a card whose answer was delivered", func() {
		item := answered("undeliverable-delivered", "Which surface?")
		_, err := store.RecordAttempt(ctx, item.ItemID, "attention-watcher", pkg.DeliveredOutcome)
		Expect(err).To(BeNil())

		entry := boardEntryOf(httpHandler, item.ItemID)
		Expect(entry.Board.Undeliverable).To(BeFalse())
	})

	It("marks a card with no attempt record after the trail", func() {
		item := answered("undeliverable-never", "Which surface?")
		report, err := store.Delivery(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(report.Status).To(Equal(pkg.NeverAttemptedStatus))

		Expect(boardEntryOf(httpHandler, item.ItemID).Board.Undeliverable).To(BeTrue())
	})

	It("serves the card without a marker when the delivery read fails", func() {
		failing := &mocks.AttentionStore{}
		failing.ReadBoardReturns(pkg.Items{
			{
				ItemID:    pkg.ItemID("probe-delivery-error"),
				Payload:   "Which surface?",
				State:     pkg.AnsweredState,
				CreatedAt: libtime.NewCurrentDateTime().Now(),
			},
		}, nil)
		failing.DeliveryReportsReturns(nil, errors.New(ctx, "delivery read failed"))
		provenance := &mocks.ProvenanceResolver{}
		provenance.ResolveReturns(pkg.Provenances{})

		entry := boardEntryOf(
			handler.NewAttentionBoardReadHandler(failing, provenance, false, GinkgoT().TempDir()),
			pkg.ItemID("probe-delivery-error"),
		)
		Expect(entry.Payload).To(Equal(pkg.Payload("Which surface?")))
		Expect(entry.State).To(Equal(pkg.AnsweredState))
		Expect(entry.Board.Undeliverable).To(BeFalse())
		Expect(entry.Board.DeliveryStatus).To(BeEmpty())
	})

	It("marks only the failed card when a delivered card sits beside it", func() {
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

		board := readBoardByID(httpHandler)
		Expect(board[failed.ItemID].Board.Undeliverable).To(BeTrue())
		Expect(board[delivered.ItemID].Board.Undeliverable).To(BeFalse())
	})

	It(
		"marks exactly the failed and never-attempted cards among the five outcomes, and never an open card",
		func() {
			// pre_trail_unknown is reachable only for an answer that predates the
			// delivery trail, so the clock is set before the epoch for that one card.
			setClock(pkg.DeliveryTrailEpoch.Add(-stdtime.Hour))
			preTrail := answered("undeliverable-pre-trail", "Pre-trail?")
			_, err := store.SettleAnswer(ctx, preTrail.ItemID)
			Expect(err).To(BeNil())
			setClock(stdtime.Now().UTC())

			delivered := answered("undeliverable-outcome-delivered", "Deliver?")
			_, err = store.RecordAttempt(
				ctx,
				delivered.ItemID,
				"attention-watcher",
				pkg.DeliveredOutcome,
			)
			Expect(err).To(BeNil())

			failed := answered("undeliverable-outcome-failed", "Fail?")
			_, err = store.RecordAttempt(ctx, failed.ItemID, "attention-watcher", pkg.FailedOutcome)
			Expect(err).To(BeNil())

			held := answered("undeliverable-declined", "Decline?")
			_, err = store.RecordAttempt(ctx, held.ItemID, "attention-watcher", pkg.HeldOutcome)
			Expect(err).To(BeNil())

			neverItem := answered("undeliverable-outcome-never", "Never?")

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

			// The fixture's own precondition: every card carries the status it is
			// meant to, so the assertions below cannot pass on a wrong fixture.
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

			board := readBoardByID(httpHandler)

			marked := 0
			for _, entry := range board {
				if entry.Board.Undeliverable {
					marked++
				}
			}
			Expect(marked).To(Equal(2))
			Expect(board[failed.ItemID].Board.Undeliverable).To(BeTrue())
			Expect(board[neverItem.ItemID].Board.Undeliverable).To(BeTrue())
			// The open card derives never_attempted too, and is still unmarked: it
			// has no answer to lose.
			Expect(board[openItem.ItemID].Board.DeliveryStatus).To(Equal("never_attempted"))
			Expect(board[openItem.ItemID].Board.Undeliverable).To(BeFalse())

			Expect(board[delivered.ItemID].Board.DeliveryOutcome).To(Equal("delivered"))
			Expect(board[failed.ItemID].Board.DeliveryOutcome).To(Equal("failed"))
			Expect(board[neverItem.ItemID].Board.DeliveryOutcome).To(Equal("never_attempted"))
			Expect(board[preTrail.ItemID].Board.DeliveryOutcome).To(Equal("pre_trail_unknown"))
			// A deliberate hold renders no outcome line at all.
			Expect(board[held.ItemID].Board.DeliveryStatus).To(Equal("held"))
			Expect(board[held.ItemID].Board.DeliveryOutcome).To(BeEmpty())
		},
	)
})
