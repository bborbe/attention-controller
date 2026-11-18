// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	stderrors "errors"
	"time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

// The Python watcher pinned a never-fatal contract on its cycle report
// (scripts/test_attention_watcher_cycle_report.py): reporting a cycle must never
// terminate the watcher. It ran from the loop's `finally`, so an escaping error
// killed the process rather than costing one interval.
//
// The Go port has no outbound report to fail — the duration is observed locally
// — so the contract moves onto the loop itself. `DeliverOnce` returns the cycle
// error; `Run` must swallow it and tick again. The existing delivery specs drive
// `DeliverOnce` directly and so cannot see this: the loop is the only place the
// process would actually die, and it is the only thing these specs pin.
var _ = Describe("attention watcher delivery loop", func() {
	var (
		store *mocks.AttentionStore
		job   pkg.AttentionWatcherJob
	)

	BeforeEach(func() {
		store = &mocks.AttentionStore{}
		store.DeliveryReportsReturns(map[pkg.ItemID]*pkg.DeliveryReport{}, nil)
		store.RecordAttemptReturns(&pkg.DeliveryAttempt{}, nil)
		job = pkg.NewAttentionWatcherJob(
			store,
			&recordingCarrier{verdict: pkg.DeliveryOK},
			&fakeHeadlessDeliverer{},
			5*time.Millisecond,
			libtime.NewCurrentDateTime(),
		)
	})

	It("keeps cycling after a failed cycle and returns nil on cancellation", func() {
		// Every cycle fails at its first store read.
		store.DeliveryAttemptCountsReturns(nil, stderrors.New("store unavailable"))

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- job.Run(ctx) }()

		// More than one attempt proves the loop ticked again after the failure
		// rather than exiting on it.
		Eventually(
			store.DeliveryAttemptCountsCallCount,
			"2s",
			"5ms",
		).Should(BeNumerically(">", 1))

		cancel()

		var err error
		Eventually(done, "2s").Should(Receive(&err))
		// The cycle error must not escape as the loop's result: a caller that
		// reads it as a fatal stop is the failure this contract forbids.
		Expect(err).Should(BeNil())
	})

	It("delivers on a later cycle after an earlier one failed", func() {
		// The first cycle fails, every cycle after it succeeds and carries the
		// card. A loop that exited on the first error would never deliver.
		store.DeliveryAttemptCountsReturnsOnCall(
			0, nil, stderrors.New("store unavailable"),
		)
		store.ReadBoardReturns(pkg.Items{
			answeredItem("item-1", "operator", pkg.Decision("allow")),
		}, nil)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() { _ = job.Run(ctx) }()

		Eventually(store.RecordAttemptCallCount, "2s", "5ms").
			Should(BeNumerically(">", 0))

		cancel()
	})
})
