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

// The attempt COUNT and the attempt RECORD answer different questions and must
// stay separable. § Delivery attempts derives `never_attempted` from the
// attempt record's absence, so counting an attempt must not manufacture a
// record — the two specs that carry that contract are "counts every attempt"
// and "counting alone leaves the item never_attempted".
var _ = Describe("Delivery attempt count", func() {
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

	push := func(attentionStore pkg.AttentionStore) *pkg.Item {
		item, err := attentionStore.Push(ctx, pkg.PushRequest{
			ProducerID:      pkg.ProducerID("session-a"),
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:session-a"),
			DedupKey:        pkg.DedupKey("count-a"),
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

	It("counts every attempt, monotonically, starting at one", func() {
		attentionStore := store()
		item := push(attentionStore)

		for want := 1; want <= 3; want++ {
			got, err := attentionStore.RecordAttemptCount(
				ctx,
				item.ItemID,
				pkg.DeliveryRefuse,
				"scripted",
			)
			Expect(err).To(BeNil())
			Expect(got).To(Equal(want))
		}
	})

	// ⚠️ The regression guard for § Delivery attempts. If the count were carried
	// on the attempt record, or a record written to hold it, this item would stop
	// reading `never_attempted` — and every count-carrying item would silently
	// lose the distinction the schema exists to preserve.
	It("counting alone leaves the item never_attempted", func() {
		attentionStore := store()
		item := push(attentionStore)

		_, err := attentionStore.RecordAttemptCount(
			ctx,
			item.ItemID,
			pkg.DeliveryRefuse,
			"scripted",
		)
		Expect(err).To(BeNil())
		_, err = attentionStore.RecordAttemptCount(ctx, item.ItemID, pkg.DeliveryRefuse, "scripted")
		Expect(err).To(BeNil())

		report, err := attentionStore.Delivery(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(report.Status).To(Equal(pkg.NeverAttemptedStatus))
	})

	// The other half: an OBSERVED outcome still writes the record, and the count
	// is unaffected by it. Counting and recording are independent facts.
	It("keeps the count independent of a recorded outcome", func() {
		attentionStore := store()
		item := push(attentionStore)

		_, err := attentionStore.RecordAttemptCount(
			ctx,
			item.ItemID,
			pkg.DeliveryRefuse,
			"scripted",
		)
		Expect(err).To(BeNil())
		_, err = attentionStore.RecordAttempt(
			ctx,
			item.ItemID,
			"attention-watcher",
			pkg.DeliveredOutcome,
		)
		Expect(err).To(BeNil())

		report, err := attentionStore.Delivery(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(report.Status).To(Equal(pkg.DeliveredStatus))

		got, err := attentionStore.RecordAttemptCount(
			ctx,
			item.ItemID,
			pkg.DeliveryRefuse,
			"scripted",
		)
		Expect(err).To(BeNil())
		Expect(got).To(Equal(2))
	})

	It("refuses a count for an unknown item rather than orphaning it", func() {
		_, err := store().RecordAttemptCount(ctx, pkg.ItemID("no-such-item"), pkg.DeliveryRefuse, "scripted")
		Expect(err).ToNot(BeNil())
	})
})
