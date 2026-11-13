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

// DeliveryAttempts is the whole-population read the stage-delta histogram uses.
// Its contract is narrower than Delivery's — it answers "what was recorded",
// never "what does that mean" — and these specs pin the two properties a caller
// depends on: the map is always usable, and it is keyed by the same item id
// Delivery takes.
var _ = Describe("Delivery attempts bulk read", func() {
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

	It("returns a usable empty map when nothing was ever attempted", func() {
		attempts, err := store().DeliveryAttempts(ctx)

		Expect(err).To(BeNil())
		Expect(attempts).ShouldNot(BeNil())
		Expect(attempts).To(BeEmpty())
	})

	It("keys each record by the item id Delivery takes", func() {
		attentionStore := store()
		item := push(attentionStore, "gate-1")
		_, err := attentionStore.RecordAttempt(
			ctx, item.ItemID, "supervisor:attention-next", pkg.DeliveredOutcome,
		)
		Expect(err).To(BeNil())

		attempts, err := attentionStore.DeliveryAttempts(ctx)
		Expect(err).To(BeNil())
		Expect(attempts).To(HaveLen(1))

		attempt, ok := attempts[item.ItemID]
		Expect(ok).To(BeTrue(), "the map must be keyed by the id Delivery accepts")
		Expect(attempt.Outcome).To(Equal(pkg.DeliveredOutcome))
		Expect(attempt.Carrier).To(Equal("supervisor:attention-next"))
		Expect(attempt.AttemptedAt.Time().IsZero()).To(BeFalse())
	})

	It("returns every record, including outcomes that are not a delivery", func() {
		attentionStore := store()
		delivered := push(attentionStore, "gate-1")
		held := push(attentionStore, "gate-2")
		_, err := attentionStore.RecordAttempt(
			ctx, delivered.ItemID, "supervisor:attention-next", pkg.DeliveredOutcome,
		)
		Expect(err).To(BeNil())
		_, err = attentionStore.RecordAttempt(
			ctx, held.ItemID, "supervisor:attention-next", pkg.HeldOutcome,
		)
		Expect(err).To(BeNil())

		attempts, err := attentionStore.DeliveryAttempts(ctx)
		Expect(err).To(BeNil())
		Expect(attempts).To(HaveLen(2))
		// ⚠️ The read reports what was recorded, not what it means. Deciding
		// that only a delivered outcome counts as the delivery is the caller's
		// rule, and a read that filtered here would hide the record a future
		// caller needs.
		Expect(attempts[held.ItemID].Outcome).To(Equal(pkg.HeldOutcome))
	})
})
