// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// alwaysLiveChecker stands in for the counterfeiter mock, which cannot be used
// here: `mocks` imports this package, so importing it from a `package pkg` spec
// file is an import cycle.
type alwaysLiveChecker struct{}

func (alwaysLiveChecker) IsLive(ctx context.Context, sessionID string) bool { return true }

// The history page cache exists because the endpoint was the process's largest
// single allocator: on 2026-11-07 a lifetime `alloc_space` profile put
// `collectHistoryPage` at 67.9 % cumulative (464 GB of 683 GB over 10.5 h), with
// ~22 pollers each re-decoding the full 10,000-row page every 60 s. These specs
// pin the two halves of the contract that make the reuse safe — a repeat read
// does not re-walk, and any write makes the next read walk again.
//
// They live in `package pkg` because they count walks through the
// `historyPageCollector` seam, which is unexported.
var _ = Describe("history page cache", func() {
	var ctx context.Context
	var db libkv.DB
	var store *attentionStore
	var walks int

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

		built, ok := NewAttentionStore(
			db,
			NewItemIDGenerator(),
			alwaysLiveChecker{},
			libtime.NewCurrentDateTime(),
			libtime.Duration(15*60*1e9),
		).(*attentionStore)
		Expect(ok).To(BeTrue(), "the constructor must build the concrete store")
		store = built

		walks = 0
		original := historyPageCollector
		DeferCleanup(func() { historyPageCollector = original })
		historyPageCollector = func(
			a *attentionStore,
			ctx context.Context,
			tx libkv.Tx,
			limit int,
			offset int,
			filter historyMatcher,
		) (Items, error) {
			walks++
			return a.collectHistoryPage(ctx, tx, limit, offset, filter)
		}
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	push := func(producerID, dedupKey string) {
		_, err := store.Push(ctx, PushRequest{
			ProducerID:      ProducerID(producerID),
			ProducerKind:    SessionProducerKind,
			LivenessRef:     LivenessRef("session:" + producerID),
			DedupKey:        DedupKey(dedupKey),
			InterruptClass:  "approve",
			Payload:         Payload(fmt.Sprintf("gate %s?", dedupKey)),
			AnswerMechanism: MessageAnswerMechanism,
		})
		Expect(err).To(BeNil())
	}

	It("serves a repeat page without re-walking the index", func() {
		push("session-a", "gate-1")
		push("session-a", "gate-2")

		first, err := store.History(ctx, 10, 0)
		Expect(err).To(BeNil())
		Expect(first).To(HaveLen(2))
		Expect(walks).To(Equal(1))

		second, err := store.History(ctx, 10, 0)
		Expect(err).To(BeNil())
		Expect(second).To(HaveLen(2))
		Expect(walks).To(Equal(1), "an unchanged repeat read must not re-walk the index")
	})

	It("re-walks after a write, so the new item is visible", func() {
		push("session-a", "gate-1")
		_, err := store.History(ctx, 10, 0)
		Expect(err).To(BeNil())
		Expect(walks).To(Equal(1))

		push("session-a", "gate-2")
		after, err := store.History(ctx, 10, 0)
		Expect(err).To(BeNil())
		Expect(walks).To(Equal(2), "a write must drop the cached page")
		Expect(after).To(HaveLen(2), "the cached page must not outlive the write")
	})

	It("does not serve one page to a different bound", func() {
		push("session-a", "gate-1")
		push("session-a", "gate-2")
		push("session-a", "gate-3")

		one, err := store.History(ctx, 1, 0)
		Expect(err).To(BeNil())
		Expect(one).To(HaveLen(1))

		three, err := store.History(ctx, 3, 0)
		Expect(err).To(BeNil())
		Expect(three).To(HaveLen(3), "a different limit is a different page")
		Expect(walks).To(Equal(2))
	})

	It("does not serve a filtered page to an unfiltered read", func() {
		push("session-a", "gate-1")
		push("session-b", "gate-2")

		unfiltered, err := store.History(ctx, 10, 0)
		Expect(err).To(BeNil())
		Expect(unfiltered).To(HaveLen(2))

		filtered, err := store.History(ctx, 10, 0, HistoryFilter{
			Producers: []ProducerID{"session-b"},
		})
		Expect(err).To(BeNil())
		Expect(filtered).To(HaveLen(1), "the filter is part of the cache key")
		Expect(filtered[0].ProducerID).To(Equal(ProducerID("session-b")))
		Expect(walks).To(Equal(2))
	})
})
