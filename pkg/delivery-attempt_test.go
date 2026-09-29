// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	stdtime "time"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

// The delivery trail answers "did my answer reach the session?" in one query.
// Its load-bearing claim is that the two ABSENT-record cases are told apart —
// *no arm ever looked* and *this predates the trail* are different answers, and
// a record that renders them identically fails the question it was built for.
// That is what most of these specs pin.
var _ = Describe("Delivery trail", func() {
	var ctx context.Context
	var db libkv.DB

	// storeAt builds a store whose clock is FIXED, so an item's CreatedAt lands
	// deliberately on one side of DeliveryTrailEpoch. A store reading the wall
	// clock could not exercise the epoch boundary at all: every item it created
	// would sit after the epoch, and the pre-trail case would be untestable
	// rather than merely untested.
	storeAt := func(when stdtime.Time) pkg.AttentionStore {
		sessionLivenessChecker := &mocks.SessionLivenessChecker{}
		sessionLivenessChecker.IsLiveReturns(true)
		return pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			sessionLivenessChecker,
			libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
				return libtime.DateTime(when)
			}),
			libtime.Duration(15*60*1e9),
		)
	}

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	push := func(store pkg.AttentionStore, dedupKey string) *pkg.Item {
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      pkg.ProducerID("session-a"),
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:session-a"),
			DedupKey:        pkg.DedupKey(dedupKey),
			InterruptClass:  "approve",
			Payload:         "deploy prod?",
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		Expect(err).To(BeNil())
		return item
	}

	It("reads never_attempted for an item created after the trail with no record", func() {
		store := storeAt(pkg.DeliveryTrailEpoch.Add(stdtime.Hour))
		item := push(store, "gate-1")

		report, err := store.Delivery(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(report.Status).To(Equal(pkg.NeverAttemptedStatus))
		Expect(report.Carrier).To(BeEmpty())
		Expect(report.AttemptedAt).To(BeNil())
	})

	// The criterion, not a refinement of it: an absent record must not be the
	// only encoding of both states, so these two specs must disagree.
	It("reads pre_trail_unknown for an item created before the trail, NOT never_attempted", func() {
		store := storeAt(pkg.DeliveryTrailEpoch.Add(-stdtime.Hour))
		item := push(store, "gate-1")

		report, err := store.Delivery(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(report.Status).To(Equal(pkg.PreTrailUnknownStatus))
		Expect(report.Status).NotTo(Equal(pkg.NeverAttemptedStatus))
	})

	It("reads delivered and names the carrier once an arm records a success", func() {
		store := storeAt(pkg.DeliveryTrailEpoch.Add(stdtime.Hour))
		item := push(store, "gate-1")

		attempt, err := store.RecordAttempt(
			ctx, item.ItemID, "supervisor:attention-next", pkg.DeliveredOutcome,
		)
		Expect(err).To(BeNil())
		// Stamped from the store's own clock inside the write, never supplied by
		// the caller — the same rule escalated_at carries.
		Expect(attempt.AttemptedAt.Time()).NotTo(BeZero())

		report, err := store.Delivery(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(report.Status).To(Equal(pkg.DeliveredStatus))
		Expect(report.Carrier).To(Equal("supervisor:attention-next"))
		Expect(report.AttemptedAt).NotTo(BeNil())
	})

	It("distinguishes attempted-and-failed from never-attempted", func() {
		store := storeAt(pkg.DeliveryTrailEpoch.Add(stdtime.Hour))
		failedItem := push(store, "gate-failed")
		neverItem := push(store, "gate-never")

		_, err := store.RecordAttempt(
			ctx, failedItem.ItemID, "supervisor:attention-next", pkg.FailedOutcome,
		)
		Expect(err).To(BeNil())

		failed, err := store.Delivery(ctx, failedItem.ItemID)
		Expect(err).To(BeNil())
		never, err := store.Delivery(ctx, neverItem.ItemID)
		Expect(err).To(BeNil())

		Expect(failed.Status).To(Equal(pkg.FailedStatus))
		Expect(never.Status).To(Equal(pkg.NeverAttemptedStatus))
		// Two distinct outcomes, neither reading as the other.
		Expect(failed.Status).NotTo(Equal(never.Status))
	})

	// ⚠️ A close is a clear, not an answer. An item cleared from the board with
	// no answer routed nowhere, so nothing was delivered — reading `closed` as
	// delivered would repeat the overstatement this record exists to remove.
	It("does not read a closed item with no record as delivered", func() {
		store := storeAt(pkg.DeliveryTrailEpoch.Add(stdtime.Hour))
		item := push(store, "gate-1")

		_, err := store.Close(ctx, item.ItemID, "attention-board", nil)
		Expect(err).To(BeNil())

		report, err := store.Delivery(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(report.Status).To(Equal(pkg.NeverAttemptedStatus))
		Expect(report.Status).NotTo(Equal(pkg.DeliveredStatus))
	})

	// An attempt against an unknown item would be a record the read surface
	// could never return — it joins the record to the item's own CreatedAt — so
	// it is refused rather than stored.
	It("refuses an attempt against an unknown item", func() {
		store := storeAt(pkg.DeliveryTrailEpoch.Add(stdtime.Hour))

		_, err := store.RecordAttempt(
			ctx, pkg.ItemID("no-such-item"), "supervisor:attention-next", pkg.DeliveredOutcome,
		)
		Expect(err).NotTo(BeNil())
		Expect(err.Error()).To(ContainSubstring("not found"))
	})

	It("refuses an outcome that is neither delivered nor failed", func() {
		store := storeAt(pkg.DeliveryTrailEpoch.Add(stdtime.Hour))
		item := push(store, "gate-1")

		_, err := store.RecordAttempt(ctx, item.ItemID, "some-arm", pkg.DeliveryOutcome("maybe"))
		Expect(err).NotTo(BeNil())

		// The refused write left no record behind, so the item still reads as
		// never attempted rather than as a partial write.
		report, err := store.Delivery(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(report.Status).To(Equal(pkg.NeverAttemptedStatus))
	})

	It("reports not-found for an unknown item rather than an empty status", func() {
		store := storeAt(pkg.DeliveryTrailEpoch.Add(stdtime.Hour))

		_, err := store.Delivery(ctx, pkg.ItemID("no-such-item"))
		Expect(err).NotTo(BeNil())
		Expect(err.Error()).To(ContainSubstring("not found"))
	})
})
