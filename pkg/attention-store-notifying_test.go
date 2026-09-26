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

// The notifying store is a decorator, so its whole contract is "delegate, then
// signal on a write" — and the specs below are written against a faked inner
// store rather than a real boltkv one, because a real store would put its own
// validation between the assertion and the thing being asserted. The store's
// own behaviour is covered where it lives; what is pinned here is which calls
// signal and which must not.
//
// Every signal assertion is a direct buffer read rather than an Eventually or a
// Consistently. That is deliberate and it is stronger: Notify runs
// synchronously before the decorated call returns, so by the time the call has
// returned the buffer either holds a signal or does not. A windowed assertion
// would only be weaker, and an absence assertion over a window is exactly the
// shape that passes on a no-op.
var _ = Describe("Notifying attention store", func() {
	var ctx context.Context
	var inner *mocks.AttentionStore
	var notifier pkg.AttentionChangeNotifier
	var changes <-chan struct{}
	var unsubscribe func()
	var store pkg.AttentionStore

	BeforeEach(func() {
		ctx = context.Background()
		inner = &mocks.AttentionStore{}
		notifier = pkg.NewAttentionChangeNotifier()
		changes, unsubscribe = notifier.Subscribe()
		store = pkg.NewNotifyingAttentionStore(inner, notifier)
	})

	AfterEach(func() {
		unsubscribe()
	})

	It("signals after a push", func() {
		item := &pkg.Item{}
		inner.PushReturns(item, nil)

		returned, err := store.Push(ctx, pkg.PushRequest{})

		Expect(err).Should(BeNil())
		Expect(returned).Should(BeIdenticalTo(item))
		Expect(changes).Should(HaveLen(1))
	})

	It("signals after an answer", func() {
		item := &pkg.Item{}
		inner.AnswerReturns(item, nil)

		_, err := store.Answer(ctx, "item-1", "board", "session-1", "", nil, nil, nil)

		Expect(err).Should(BeNil())
		Expect(changes).Should(HaveLen(1))
	})

	It("signals after an escalation", func() {
		item := &pkg.Item{}
		inner.EscalateReturns(item, nil)

		_, err := store.Escalate(ctx, "item-1", "board")

		Expect(err).Should(BeNil())
		Expect(changes).Should(HaveLen(1))
	})

	It("signals after a close", func() {
		item := &pkg.Item{}
		inner.CloseReturns(item, nil)

		_, err := store.Close(ctx, "item-1", "board", nil)

		Expect(err).Should(BeNil())
		Expect(changes).Should(HaveLen(1))
	})

	It("does not signal after a rejected write", func() {
		// The rejection is the answer path's own: a second answer to an
		// already-answered item. Nothing changed, so nothing may wake a
		// subscriber to re-read an unchanged store.
		inner.AnswerReturns(nil, pkg.ErrAlreadyAnswered)

		_, err := store.Answer(ctx, "item-1", "board", "session-1", "", nil, nil, nil)

		Expect(err).Should(Equal(pkg.ErrAlreadyAnswered))
		Expect(changes).Should(BeEmpty())
	})

	It("does not signal after a get", func() {
		inner.GetReturns(&pkg.Item{}, nil)

		_, err := store.Get(ctx, "item-1")

		Expect(err).Should(BeNil())
		Expect(changes).Should(BeEmpty())
	})

	It("does not signal after a read", func() {
		inner.ReadReturns(pkg.Items{}, nil)

		_, err := store.Read(ctx)

		Expect(err).Should(BeNil())
		Expect(changes).Should(BeEmpty())
	})

	It("does not signal after a history read", func() {
		inner.HistoryReturns(pkg.Items{}, nil)

		_, err := store.History(ctx)

		Expect(err).Should(BeNil())
		Expect(changes).Should(BeEmpty())
	})

	Describe("against a real store", func() {
		var db libkv.DB

		BeforeEach(func() {
			var err error
			db, err = libboltkv.OpenTemp(ctx)
			Expect(err).Should(BeNil())

			sessionLivenessChecker := &mocks.SessionLivenessChecker{}
			sessionLivenessChecker.IsLiveReturns(true)

			store = pkg.NewNotifyingAttentionStore(
				pkg.NewAttentionStore(
					db,
					pkg.NewItemIDGenerator(),
					sessionLivenessChecker,
					libtime.NewCurrentDateTime(),
					libtime.Duration(15*60*1e9),
				),
				notifier,
			)
		})

		It("signals for a write that reached the store", func() {
			_, err := store.Push(ctx, pkg.PushRequest{
				ProducerID:      "producer-a",
				ProducerKind:    pkg.SessionProducerKind,
				LivenessRef:     pkg.LivenessRef("session:producer-a"),
				DedupKey:        "dedup-a",
				InterruptClass:  "approve",
				Payload:         "deploy prod?",
				AnswerMechanism: pkg.MessageAnswerMechanism,
			})

			Expect(err).Should(BeNil())
			Expect(changes).Should(HaveLen(1))
		})

		It("does not signal for a push the store rejected", func() {
			// No answer mechanism, so the request fails validation before the
			// store writes anything. The store's rejection is the point: the
			// decorator must signal on the write, not on the call.
			_, err := store.Push(ctx, pkg.PushRequest{
				ProducerID:   "producer-a",
				ProducerKind: pkg.SessionProducerKind,
				LivenessRef:  pkg.LivenessRef("session:producer-a"),
				DedupKey:     "dedup-a",
			})

			Expect(err).ShouldNot(BeNil())
			Expect(changes).Should(BeEmpty())
		})
	})
})
