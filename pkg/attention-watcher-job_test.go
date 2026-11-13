// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

// recordingCarrier is a scripted PaneCarrier that records what it was asked to
// carry and returns a fixed verdict.
type recordingCarrier struct {
	calls   []string
	verdict pkg.DeliveryVerdict
}

func (c *recordingCarrier) TypeIntoPane(
	_ context.Context,
	delivery *pkg.PaneDelivery,
	text string,
	_ bool,
) (pkg.DeliveryVerdict, string) {
	c.calls = append(c.calls, delivery.StoreItemID+"="+text)
	return c.verdict, "scripted"
}

// answeredItem builds an answered card for a session.
func answeredItem(id string, answeredBy string, decision pkg.Decision) pkg.Item {
	answeredAt := libtime.NewCurrentDateTime().Now()
	return pkg.Item{
		ItemID:      pkg.ItemID(id),
		LivenessRef: pkg.LivenessRef("session:11111111-2222-3333-4444-555555555555"),
		State:       pkg.AnsweredState,
		AnsweredAt:  &answeredAt,
		AnsweredBy:  answeredBy,
		Decision:    decision,
	}
}

var _ = Describe("attention watcher delivery job", func() {
	var (
		store   *mocks.AttentionStore
		carrier *recordingCarrier
		job     pkg.AttentionWatcherJob
	)

	BeforeEach(func() {
		store = &mocks.AttentionStore{}
		store.DeliveryAttemptsReturns(map[pkg.ItemID]pkg.DeliveryAttempt{}, nil)
		store.RecordAttemptReturns(&pkg.DeliveryAttempt{}, nil)
		carrier = &recordingCarrier{verdict: pkg.DeliveryOK}
		job = pkg.NewAttentionWatcherJob(store, carrier, time.Hour)
	})

	It("carries an answered permission card and records delivered", func() {
		store.ReadBoardReturns(pkg.Items{
			answeredItem("item-1", "operator", pkg.Decision("allow")),
		}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())

		Expect(carrier.calls).To(ConsistOf("item-1=allow"))
		Expect(store.RecordAttemptCallCount()).To(Equal(1))
		_, _, _, outcome := store.RecordAttemptArgsForCall(0)
		Expect(outcome).To(Equal(pkg.DeliveredOutcome))
	})

	It("skips a card the originate arm settled", func() {
		store.ReadBoardReturns(pkg.Items{
			answeredItem("item-1", "attention-watcher", pkg.Decision("allow")),
		}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(carrier.calls).To(BeEmpty())
	})

	It("skips a card already recorded as delivered", func() {
		store.ReadBoardReturns(pkg.Items{
			answeredItem("item-1", "operator", pkg.Decision("allow")),
		}, nil)
		store.DeliveryAttemptsReturns(map[pkg.ItemID]pkg.DeliveryAttempt{
			"item-1": {Outcome: pkg.DeliveredOutcome},
		}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(carrier.calls).To(BeEmpty())
	})

	It("records a failed attempt when the carrier refuses", func() {
		carrier.verdict = pkg.DeliveryRefuse
		store.ReadBoardReturns(pkg.Items{
			answeredItem("item-1", "operator", pkg.Decision("deny")),
		}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())

		Expect(store.RecordAttemptCallCount()).To(Equal(1))
		_, _, _, outcome := store.RecordAttemptArgsForCall(0)
		Expect(outcome).To(Equal(pkg.FailedOutcome))
	})

	It("records nothing on a retry verdict", func() {
		carrier.verdict = pkg.DeliveryRetry
		store.ReadBoardReturns(pkg.Items{
			answeredItem("item-1", "operator", pkg.Decision("allow")),
		}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(store.RecordAttemptCallCount()).To(Equal(0))
	})

	It("skips an open card", func() {
		item := answeredItem("item-1", "operator", pkg.Decision("allow"))
		item.State = pkg.OpenState
		store.ReadBoardReturns(pkg.Items{item}, nil)
		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(carrier.calls).To(BeEmpty())
	})
})
