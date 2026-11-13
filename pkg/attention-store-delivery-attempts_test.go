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

// DeliveryAttempts is the bulk read a population-wide collector uses: one read
// transaction for every attempt record, keyed by item id, so a metrics join
// needs no per-item Delivery call. The specs below pin the real store's shape
// and both decorators' forwarding rules.
var _ = Describe("Delivery attempts", func() {
	var ctx context.Context
	var db libkv.DB
	var now stdtime.Time
	storeAt := func() pkg.AttentionStore {
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

	It("returns a non-nil empty map for a store with no attempts", func() {
		store := storeAt()

		attempts, err := store.DeliveryAttempts(ctx)

		Expect(err).To(BeNil())
		Expect(attempts).NotTo(BeNil())
		Expect(attempts).To(BeEmpty())
	})

	It("returns every recorded attempt keyed by item id", func() {
		store := storeAt()
		item := push(store, "gate-1")
		_, err := store.RecordAttempt(ctx, item.ItemID, "carrier", pkg.DeliveredOutcome)
		Expect(err).To(BeNil())

		attempts, err := store.DeliveryAttempts(ctx)

		Expect(err).To(BeNil())
		Expect(attempts).To(HaveLen(1))
		attempt, ok := attempts[item.ItemID]
		Expect(ok).To(BeTrue())
		Expect(attempt.ItemID).To(Equal(item.ItemID))
		Expect(attempt.Outcome).To(Equal(pkg.DeliveredOutcome))
		Expect(attempt.Carrier).To(Equal("carrier"))
		Expect(attempt.AttemptedAt.Time()).NotTo(BeZero())
	})
})

var _ = Describe("Federating delivery attempts", func() {
	var ctx context.Context
	var local *mocks.AttentionStore
	var remote *mocks.RemoteAttentionStore
	var store pkg.AttentionStore

	BeforeEach(func() {
		ctx = context.Background()
		local = &mocks.AttentionStore{}
		remote = &mocks.RemoteAttentionStore{}
		store = pkg.NewFederatingAttentionStoreWithClock(local, remote, newMutableClock())
	})

	// ⚠️ AC8: the delivery trail is the LOCAL arm's bookkeeping, so a
	// federated card contributes nothing here — the peer is never asked.
	It("reads only the local attempts and never the peer", func() {
		local.DeliveryAttemptsReturns(map[pkg.ItemID]pkg.DeliveryAttempt{}, nil)
		remote.ReadReturns(pkg.Items{{ItemID: "peer-only"}}, nil)

		attempts, err := store.DeliveryAttempts(ctx)

		Expect(err).To(BeNil())
		Expect(attempts).To(BeEmpty())
		Expect(local.DeliveryAttemptsCallCount()).To(Equal(1))
		Expect(remote.ReadCallCount()).To(Equal(0))
		_, ok := attempts[pkg.ItemID("peer-only")]
		Expect(ok).To(BeFalse())
	})
})

var _ = Describe("Notifying delivery attempts", func() {
	var ctx context.Context
	var inner *mocks.AttentionStore
	var notifier *mocks.AttentionChangeNotifier
	var store pkg.AttentionStore

	BeforeEach(func() {
		ctx = context.Background()
		inner = &mocks.AttentionStore{}
		notifier = &mocks.AttentionChangeNotifier{}
		store = pkg.NewNotifyingAttentionStore(inner, notifier)
	})

	It("returns the map unchanged and does not signal", func() {
		want := map[pkg.ItemID]pkg.DeliveryAttempt{
			"item-1": {ItemID: "item-1", Carrier: "carrier", Outcome: pkg.DeliveredOutcome},
		}
		inner.DeliveryAttemptsReturns(want, nil)

		got, err := store.DeliveryAttempts(ctx)

		Expect(err).To(BeNil())
		Expect(got).To(Equal(want))
		Expect(inner.DeliveryAttemptsCallCount()).To(Equal(1))
		// A read must not wake subscribers.
		Expect(notifier.NotifyCallCount()).To(Equal(0))
	})
})
