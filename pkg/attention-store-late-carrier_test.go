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

// The attempt record is last-write-wins, so a carrier that arrives AFTER the
// gate is already settled overwrites a real delivery and the item reads
// `failed`.
//
// ⚠️ The direction is a false negative, never a false ANSWERED: the delivery
// really happened and the record loses it. Two after-the-fact consumers read
// the report and get it wrong — restart-precheck.py's card_is_live_channel
// (which would call a delivered card not delivered and release a gate that
// demonstrably released its worker), and a second CLI run printing
// NOT_DELIVERED for the same gate.
//
// Measured 2026-11-06 on card f2e9520d105f97d8911cc0f562f60454: the CLI read
// `delivered` (watcher:attention-watcher), and 15 seconds later the same item
// read `failed` (supervisor:attention-poll).
var _ = Describe("A settled delivery survives a late carrier write", func() {
	var ctx context.Context
	var db libkv.DB
	var now stdtime.Time

	store := func() pkg.AttentionStore {
		sessionLivenessChecker := &mocks.SessionLivenessChecker{}
		sessionLivenessChecker.IsLiveReturns(true)
		return pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			sessionLivenessChecker,
			libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
				return libtime.DateTime(now)
			}),
			libtime.Duration(15*60*1e9),
		)
	}

	push := func(attentionStore pkg.AttentionStore, dedupKey string) *pkg.Item {
		item, err := attentionStore.Push(ctx, pkg.PushRequest{
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

	BeforeEach(func() {
		ctx = context.Background()
		now = pkg.DeliveryTrailEpoch.Add(stdtime.Hour)
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	It("still reads delivered after a late carrier records a failure", func() {
		attentionStore := store()
		item := push(attentionStore, "gate-1")

		// The real delivery: the watcher reached the worker.
		_, err := attentionStore.RecordAttempt(
			ctx, item.ItemID, "watcher:attention-watcher", pkg.DeliveredOutcome,
		)
		Expect(err).To(BeNil())

		report, err := attentionStore.Delivery(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(report.Status).To(Equal(pkg.DeliveredStatus))

		// A redundant carrier arrives 15 seconds later, after the gate is
		// already settled, and reports its own failure.
		now = now.Add(15 * stdtime.Second)
		_, err = attentionStore.RecordAttempt(
			ctx, item.ItemID, "supervisor:attention-poll", pkg.FailedOutcome,
		)
		Expect(err).To(BeNil())

		report, err = attentionStore.Delivery(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(report.Status).To(Equal(pkg.DeliveredStatus),
			"a late carrier write must not overwrite a settled delivery")
		Expect(report.Carrier).To(Equal("watcher:attention-watcher"),
			"the report must still name the arm that delivered")

		// Both writes stay visible, each with its own writer and timestamp, so
		// "delivered then overwritten" is distinguishable from "never delivered".
		attempts, err := attentionStore.DeliveryAttempts(ctx)
		Expect(err).To(BeNil())
		stored := attempts[item.ItemID]
		Expect(stored.Carrier).To(Equal("supervisor:attention-poll"),
			"the last write is preserved")
		Expect(stored.Outcome).To(Equal(pkg.FailedOutcome))
		Expect(stored.AttemptedAt.Time().Equal(now)).To(BeTrue())
		Expect(stored.Settled).ShouldNot(BeNil(), "the delivery is preserved")
		Expect(stored.Settled.Carrier).To(Equal("watcher:attention-watcher"))
		deliveredAt := stored.Settled.AttemptedAt.Time()
		Expect(deliveredAt.Equal(now.Add(-15 * stdtime.Second))).To(BeTrue())
		Expect(stored.EffectiveOutcome()).To(Equal(pkg.DeliveredOutcome))
	})

	// The other direction must keep working: the first arm to reach the session
	// is the one whose outcome the reader needs, so failed-then-delivered is a
	// real recovery. A future refactor that froze the top-level fields whenever a
	// settlement existed would break exactly this.
	It("still upgrades a failed attempt when a later carrier delivers", func() {
		attentionStore := store()
		item := push(attentionStore, "gate-2")

		_, err := attentionStore.RecordAttempt(
			ctx, item.ItemID, "supervisor:attention-poll", pkg.FailedOutcome,
		)
		Expect(err).To(BeNil())

		report, err := attentionStore.Delivery(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(report.Status).To(Equal(pkg.FailedStatus))

		now = now.Add(15 * stdtime.Second)
		_, err = attentionStore.RecordAttempt(
			ctx, item.ItemID, "watcher:attention-watcher", pkg.DeliveredOutcome,
		)
		Expect(err).To(BeNil())

		report, err = attentionStore.Delivery(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(report.Status).To(Equal(pkg.DeliveredStatus),
			"failed-then-delivered is a real recovery and must keep reading as delivered")
		Expect(report.Carrier).To(Equal("watcher:attention-watcher"),
			"the report must name the arm that delivered")
	})
})
