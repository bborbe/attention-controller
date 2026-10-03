// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

// The notifier exists because nothing below it can report a change: the store
// declares no watch hook, and github.com/bborbe/kv v1.21.16 exposes no
// subscribe API at all. Change detection is added in process instead, and these
// specs pin the two properties a live view depends on — a subscriber is woken
// by a write, and a subscriber that has not drained yet never stalls the writer
// trying to wake it.
//
// The coalescing specs are the load-bearing ones. The notifier carries no
// payload, so a subscriber re-reads the store when it wakes; that is what makes
// dropping a signal safe, and it is why the buffered channel may hold one
// signal rather than one per write.
var _ = Describe("Attention change notifier", func() {
	var notifier pkg.AttentionChangeNotifier

	BeforeEach(func() {
		notifier = pkg.NewAttentionChangeNotifier()
	})

	It("signals a subscriber when the store changes", func() {
		changes, unsubscribe := notifier.Subscribe()
		defer unsubscribe()

		notifier.Notify()

		Eventually(changes).Should(Receive())
	})

	It("signals again once the subscriber has drained", func() {
		changes, unsubscribe := notifier.Subscribe()
		defer unsubscribe()

		notifier.Notify()
		Eventually(changes).Should(Receive())

		notifier.Notify()
		Eventually(changes).Should(Receive())
	})

	It("signals every subscriber", func() {
		first, unsubscribeFirst := notifier.Subscribe()
		defer unsubscribeFirst()
		second, unsubscribeSecond := notifier.Subscribe()
		defer unsubscribeSecond()

		notifier.Notify()

		Eventually(first).Should(Receive())
		Eventually(second).Should(Receive())
	})

	It("leaves exactly one signal buffered however many writes go unread", func() {
		changes, unsubscribe := notifier.Subscribe()
		defer unsubscribe()

		for i := 0; i < 100; i++ {
			notifier.Notify()
		}

		// The send is non-blocking, so a hundred writes against a subscriber
		// that never reads neither stall the writer nor queue a hundred
		// signals. A blocking send would hang this loop rather than fail the
		// assertion, which is the one honest way to state the property.
		Expect(changes).Should(HaveLen(1))
	})

	It("closes the subscriber's channel when it unsubscribes", func() {
		changes, unsubscribe := notifier.Subscribe()

		unsubscribe()

		Eventually(changes).Should(BeClosed())
	})

	It("tolerates unsubscribing twice", func() {
		_, unsubscribe := notifier.Subscribe()

		unsubscribe()

		Expect(unsubscribe).ShouldNot(Panic())
	})

	It("stops signalling a subscriber that unsubscribed", func() {
		changes, unsubscribe := notifier.Subscribe()
		unsubscribe()

		notifier.Notify()

		Consistently(changes).ShouldNot(Receive())
	})

	It("does not panic when nothing is subscribed", func() {
		Expect(notifier.Notify).ShouldNot(Panic())
	})

	// The generation is what lets one render be shared by every stream. Two
	// subscribers woken by the same write must read the SAME value, because
	// that equality is how a shared renderer tells "this change has already
	// been rendered for" from "this change is still owed" — the difference
	// between one render per change and one per stream per change.
	It("counts writes, so every subscriber woken by one write reads the same value", func() {
		first, unsubscribeFirst := notifier.Subscribe()
		defer unsubscribeFirst()
		second, unsubscribeSecond := notifier.Subscribe()
		defer unsubscribeSecond()

		Expect(notifier.Generation()).Should(Equal(uint64(0)))

		notifier.Notify()
		Eventually(first).Should(Receive())
		Eventually(second).Should(Receive())

		Expect(notifier.Generation()).Should(Equal(uint64(1)))

		notifier.Notify()
		Eventually(first).Should(Receive())
		Eventually(second).Should(Receive())

		Expect(notifier.Generation()).Should(Equal(uint64(2)))
	})

	// ⚠️ Subscribing is not a write, so it must not move the generation. A
	// generation that advanced on attach would make every newly attached stream
	// believe a change was outstanding, and it would render for one that had
	// already been rendered for.
	It("does not advance the generation when a subscriber attaches or detaches", func() {
		_, unsubscribe := notifier.Subscribe()

		Expect(notifier.Generation()).Should(Equal(uint64(0)))

		unsubscribe()

		Expect(notifier.Generation()).Should(Equal(uint64(0)))
	})
})
