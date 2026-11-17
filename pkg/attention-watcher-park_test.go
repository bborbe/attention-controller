// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	stdtime "time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

// The backoff and the park are what stop the deliver arm's hot loop. The park is
// a LIVENESS gate, never a verdict class: `refuse` and `no-pane` both describe a
// session whose process may come back, so the discriminating pair is "dead
// session -> no attempt" against "live session -> attempt".
var _ = Describe("attention watcher delivery backoff and park", func() {
	var (
		store   *mocks.AttentionStore
		carrier *recordingCarrier
		job     pkg.AttentionWatcherJob
		now     stdtime.Time
	)

	BeforeEach(func() {
		store = &mocks.AttentionStore{}
		store.DeliveryAttemptsReturns(map[pkg.ItemID]pkg.DeliveryAttempt{}, nil)
		store.RecordAttemptReturns(&pkg.DeliveryAttempt{}, nil)
		store.RecordAttemptCountReturns(1, nil)
		carrier = &recordingCarrier{verdict: pkg.DeliveryOK}
		now = stdtime.Date(2026, 11, 16, 12, 0, 0, 0, stdtime.UTC)
		job = pkg.NewAttentionWatcherJob(
			store,
			carrier,
			&fakeHeadlessDeliverer{},
			stdtime.Hour,
			libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
				return libtime.DateTime(now)
			}),
		)
	})

	It("does not re-attempt an item before the backoff has elapsed", func() {
		store.ReadBoardReturns(pkg.Items{
			answeredItem("item-1", "operator", pkg.Decision("allow")),
		}, nil)
		store.DeliveryAttemptCountsReturns(map[pkg.ItemID]pkg.DeliveryAttemptCount{
			"item-1": {
				ItemID:          "item-1",
				Count:           1,
				LastAttemptedAt: libtime.DateTime(now.Add(-stdtime.Minute)),
			},
		}, nil)

		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(carrier.calls).To(BeEmpty())
	})

	It("re-attempts an item once the backoff has elapsed", func() {
		store.ReadBoardReturns(pkg.Items{
			answeredItem("item-1", "operator", pkg.Decision("allow")),
		}, nil)
		store.DeliveryAttemptCountsReturns(map[pkg.ItemID]pkg.DeliveryAttemptCount{
			"item-1": {
				ItemID: "item-1",
				Count:  1,
				LastAttemptedAt: libtime.DateTime(
					now.Add(-pkg.DeliveryAttemptBackoff - stdtime.Minute),
				),
			},
		}, nil)

		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(carrier.calls).To(ConsistOf("item-1=allow"))
	})

	It("parks an item that reached the threshold, naming its verdict and reason", func() {
		carrier.verdict = pkg.DeliveryRefuse
		store.ReadBoardReturns(pkg.Items{
			answeredItem("item-1", "operator", pkg.Decision("deny")),
		}, nil)
		store.RecordAttemptCountReturns(pkg.DeliveryAttemptParkThreshold, nil)

		Expect(job.DeliverOnce(context.Background())).To(Succeed())

		Expect(store.ParkDeliveryCallCount()).To(Equal(1))
		_, park := store.ParkDeliveryArgsForCall(0)
		Expect(park.ItemID).To(Equal(pkg.ItemID("item-1")))
		Expect(park.Attempts).To(Equal(pkg.DeliveryAttemptParkThreshold))
		Expect(park.Verdict).To(Equal(pkg.DeliveryRefuse))
		Expect(park.Reason).To(Equal("scripted"))
	})

	It("does not park an item below the threshold", func() {
		carrier.verdict = pkg.DeliveryRefuse
		store.ReadBoardReturns(pkg.Items{
			answeredItem("item-1", "operator", pkg.Decision("deny")),
		}, nil)
		store.RecordAttemptCountReturns(pkg.DeliveryAttemptParkThreshold-1, nil)

		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(store.ParkDeliveryCallCount()).To(Equal(0))
	})

	It("does not attempt a parked item whose session is dead", func() {
		store.ReadBoardReturns(pkg.Items{
			answeredItem("item-1", "operator", pkg.Decision("allow")),
		}, nil)
		store.DeliveryParksReturns(map[pkg.ItemID]pkg.DeliveryPark{
			"item-1": {
				ItemID:   "item-1",
				Verdict:  pkg.DeliveryRefuse,
				Reason:   "session ended its turn and its process exited",
				Attempts: pkg.DeliveryAttemptParkThreshold,
			},
		}, nil)
		store.IsSessionLiveReturns(false)

		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(carrier.calls).To(BeEmpty())
		Expect(store.ClearDeliveryParkCallCount()).To(Equal(0))
	})

	It("re-opens a parked item whose session came back", func() {
		carrier.verdict = pkg.DeliveryRefuse
		store.ReadBoardReturns(pkg.Items{
			answeredItem("item-1", "operator", pkg.Decision("allow")),
		}, nil)
		store.DeliveryParksReturns(map[pkg.ItemID]pkg.DeliveryPark{
			"item-1": {
				ItemID:   "item-1",
				Verdict:  pkg.DeliveryNoPane,
				Reason:   "session is pane-less and its process exited — resumable",
				Attempts: pkg.DeliveryAttemptParkThreshold,
			},
		}, nil)
		store.IsSessionLiveReturns(true)

		Expect(job.DeliverOnce(context.Background())).To(Succeed())
		Expect(store.ClearDeliveryParkCallCount()).To(Equal(1))
		Expect(carrier.calls).To(ConsistOf("item-1=allow"))
	})
})
