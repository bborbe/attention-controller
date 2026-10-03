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

		sweep := runAnsweredSweep(store, libtime.Duration(time.Hour), time.Millisecond)
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
