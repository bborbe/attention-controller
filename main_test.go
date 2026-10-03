// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
)

var _ = Describe("runAnsweredSweep", func() {
	var ctx context.Context
	var store *mocks.AttentionStore

	BeforeEach(func() {
		ctx = context.Background()
		store = &mocks.AttentionStore{}
		store.SweepAnsweredReturns(0, nil)
	})

	It("returns on cancellation without sweeping", func() {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()

		// ⚠️ A long interval, not a short one, and the assertion is why: this spec
		// proves that cancellation returns nil WITHOUT sweeping, and a 1 ms ticker
		// races it — ctx.Done() is ready immediately but ticker.C becomes ready a
		// millisecond after NewTicker, so a goroutine descheduled past that boundary
		// leaves Go choosing between two ready cases at random and the zero-call
		// assertion fails on a loaded runner. A second keeps the tick branch
		// unreachable for the whole spec without weakening what it tests.
		sweep := runAnsweredSweep(store, libtime.Duration(time.Hour), time.Second)
		Expect(sweep(cancelled)).To(Succeed())
		Expect(store.SweepAnsweredCallCount()).To(Equal(0))
	})

	It("keeps ticking after a failed sweep instead of returning the error", func() {
		// ⚠️ A failed sweep costs decode work; it does not lose an item. Returning
		// the error would take the process down through the shared runner and drop
		// the board over something the board survives, so the loop must swallow it
		// and try again on the next tick.
		store.SweepAnsweredReturnsOnCall(0, 0, errors.New(ctx, "sweep exploded"))

		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			done <- runAnsweredSweep(store, libtime.Duration(time.Hour), time.Millisecond)(runCtx)
		}()

		// Two calls is the assertion that matters: the first failed and the loop
		// came back for a second, rather than unwinding with the error.
		Eventually(store.SweepAnsweredCallCount, "2s").Should(BeNumerically(">=", 2))
		cancel()
		Eventually(done, "2s").Should(Receive(BeNil()))
	})
})

var _ = Describe("parseAnsweredMaxAge", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("accepts a positive value inside the ceiling", func() {
		got, err := parseAnsweredMaxAge(ctx, "1h")
		Expect(err).To(BeNil())
		Expect(time.Duration(got)).To(Equal(time.Hour))
	})

	It("rejects zero", func() {
		// ⚠️ The highest-consequence branch in this change. The sweep computes its
		// cutoff as now minus maxAge, so a zero makes EVERY answered item due on the
		// first tick and closes the whole backlog at once — destroying every verdict
		// a consumer had not read yet, which is the outcome the bound exists to
		// prevent.
		_, err := parseAnsweredMaxAge(ctx, "0s")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("must be positive"))
	})

	It("rejects a negative value", func() {
		_, err := parseAnsweredMaxAge(ctx, "-1h")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("must be positive"))
	})

	It("rejects a value above the ceiling, which would reinstate the unbounded index", func() {
		_, err := parseAnsweredMaxAge(ctx, "8760h")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("exceeds"))
	})

	It("rejects an unparseable value", func() {
		_, err := parseAnsweredMaxAge(ctx, "not-a-duration")
		Expect(err).To(HaveOccurred())
	})
})
