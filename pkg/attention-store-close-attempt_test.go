// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

// Every close path that is not the delivery arm must leave a terminal delivery
// record, because the read surface derives `never_attempted` from an absent
// record — "no arm ever picked this answer up" — and that is indistinguishable
// from an answer that was answered, then retired by a closer that carries
// nothing. These specs pin the record on each of the three close paths, pin that
// a real delivery is never downgraded, and pin that nothing is written while the
// card is still mid-delivery.
var _ = Describe("AttentionStore close-path delivery record", func() {
	var ctx context.Context
	var db libkv.DB
	var clock libtime.CurrentDateTime
	var store pkg.AttentionStore

	const (
		heartbeatWindow = libtime.Duration(15 * 60 * 1e9)
		oneHour         = libtime.Duration(60 * 60 * 1e9)
		twoHours        = libtime.Duration(2 * 60 * 60 * 1e9)
	)

	push := func(producer pkg.ProducerID, dedup pkg.DedupKey) pkg.ItemID {
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      producer,
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:" + producer.String()),
			DedupKey:        dedup,
			InterruptClass:  "approve",
			Payload:         "deploy prod?",
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		Expect(err).To(BeNil())
		return item.ItemID
	}

	answer := func(producer pkg.ProducerID, dedup pkg.DedupKey) pkg.ItemID {
		id := push(producer, dedup)
		_, err := store.Answer(
			ctx,
			id,
			"attention-board",
			"session",
			pkg.Decision(""),
			nil,
			nil,
			nil,
		)
		Expect(err).To(BeNil())
		return id
	}

	delivery := func(id pkg.ItemID) *pkg.DeliveryReport {
		report, err := store.Delivery(ctx, id)
		Expect(err).To(BeNil())
		return report
	}

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

		liveness := &mocks.SessionLivenessChecker{}
		liveness.IsLiveReturns(true)

		clock = libtime.NewCurrentDateTime()
		store = pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			liveness,
			clock,
			heartbeatWindow,
			pkg.WithLostAnswers(&countingLostAnswers{}),
		)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	It("records the close when the explicit close route retires an answered card", func() {
		id := answer("producer-close", "key-close")

		closed, err := store.Close(ctx, id, "", nil)
		Expect(err).To(BeNil())
		Expect(closed.ClosedAt).NotTo(BeNil())

		report := delivery(id)
		Expect(report.Status).To(Equal(pkg.HeldStatus))
		Expect(report.Carrier).To(Equal("watcher:gate-resolved"))
		// The record is stamped from the same instant as the close, which is what
		// makes it one write at the close transition rather than a later poll.
		// BeTemporally, not Equal: the record is read back from the store, so it
		// carries no monotonic reading and no location, and `==` on time.Time
		// compares both.
		Expect(report.AttemptedAt).NotTo(BeNil())
		Expect(report.AttemptedAt.Time()).To(BeTemporally("==", closed.ClosedAt.Time()))
	})

	It("records the close when the answered sweep retires an answered card", func() {
		id := answer("producer-sweep-record", "key-sweep-record")
		clock.SetNow(clock.Now().Add(twoHours))

		closed, err := store.SweepAnswered(ctx, oneHour)
		Expect(err).To(BeNil())
		Expect(closed).To(Equal(1))

		report := delivery(id)
		Expect(report.Status).To(Equal(pkg.HeldStatus))
		Expect(report.Carrier).To(Equal("watcher:gate-resolved"))
	})

	It("does not downgrade a delivered record when the card is closed afterwards", func() {
		id := answer("producer-delivered", "key-delivered")
		_, err := store.RecordAttempt(ctx, id, "watcher:attention-watcher", pkg.DeliveredOutcome)
		Expect(err).To(BeNil())

		_, err = store.Close(ctx, id, "", nil)
		Expect(err).To(BeNil())

		report := delivery(id)
		Expect(report.Status).To(Equal(pkg.DeliveredStatus))
		Expect(report.Carrier).To(Equal("watcher:attention-watcher"))
	})

	It("does not downgrade a failed record from a real attempt", func() {
		id := answer("producer-failed", "key-failed")
		_, err := store.RecordAttempt(ctx, id, "watcher:attention-watcher", pkg.FailedOutcome)
		Expect(err).To(BeNil())

		_, err = store.Close(ctx, id, "", nil)
		Expect(err).To(BeNil())

		report := delivery(id)
		Expect(report.Status).To(Equal(pkg.FailedStatus))
		Expect(report.Carrier).To(Equal("watcher:attention-watcher"))
	})

	It("records nothing for a card closed before it was ever answered", func() {
		id := push("producer-open", "key-open")

		_, err := store.Close(ctx, id, "", nil)
		Expect(err).To(BeNil())

		report := delivery(id)
		Expect(report.Status).To(Equal(pkg.NeverAttemptedStatus))
		Expect(report.Carrier).To(BeEmpty())
	})

	// SC4: a card still mid-delivery must not be recorded early — the record
	// belongs to the close transition, and an answered-but-open card is still
	// waiting for an arm to carry it.
	It("records nothing while an answered card is still open", func() {
		id := answer("producer-open-answer", "key-open-answer")

		report := delivery(id)
		Expect(report.Status).To(Equal(pkg.NeverAttemptedStatus))
		Expect(report.Carrier).To(BeEmpty())
	})
})
