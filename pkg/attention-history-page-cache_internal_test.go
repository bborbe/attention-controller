// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"sync"
	"time"

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
	var now libtime.DateTime

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

		now = libtime.NewCurrentDateTime().Now()
		clock := libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime { return now })

		built, ok := NewAttentionStore(
			db,
			NewItemIDGenerator(),
			alwaysLiveChecker{},
			clock,
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
			limit int,
			offset int,
			filter historyMatcher,
		) ([]byte, libtime.DateTime, error) {
			walks++
			return a.collectHistoryPageJSON(ctx, limit, offset, filter)
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

	// ⚠️ This is the spec that keeps the cache worth having. The answered sweep
	// ticks every minute — the SAME cadence the history pollers use — and most
	// ticks close nothing. An unconditional invalidate there would clear the
	// cache as fast as it fills, and every other spec in this file would still
	// pass because they push before reading.
	It("keeps the cached page across a sweep that closes nothing", func() {
		push("session-a", "gate-1")
		_, err := store.History(ctx, 10, 0)
		Expect(err).To(BeNil())
		Expect(walks).To(Equal(1))

		closed, err := store.SweepAnswered(ctx, libtime.Duration(15*60*1e9))
		Expect(err).To(BeNil())
		Expect(closed).To(Equal(0), "nothing is due, so the sweep must write nothing")

		_, err = store.History(ctx, 10, 0)
		Expect(err).To(BeNil())
		Expect(walks).To(Equal(1),
			"a sweep that closed nothing must not drop the cached page")
	})

	// ⚠️ The cache key cannot carry time, but the walk drops an OPEN item once it
	// expires. An expiry is not a write, so no invalidate arrives — the entry
	// carries the EARLIEST deadline among its OPEN items instead, and past that
	// instant it is dropped and the read re-walks. Two items with different
	// deadlines pin that it is the earliest and not the last.
	It("drops an item that expires while its page is cached", func() {
		pushExpiring := func(dedupKey string, in libtime.Duration) {
			expiresAt := now.Add(in)
			_, err := store.Push(ctx, PushRequest{
				ProducerID:      ProducerID("session-a"),
				ProducerKind:    SessionProducerKind,
				LivenessRef:     LivenessRef("session:session-a"),
				DedupKey:        DedupKey(dedupKey),
				InterruptClass:  "approve",
				Payload:         Payload("still needed?"),
				AnswerMechanism: MessageAnswerMechanism,
				ExpiresAt:       &expiresAt,
			})
			Expect(err).To(BeNil())
		}
		pushExpiring("gate-expiring-soon", libtime.Duration(60*1e9))
		pushExpiring("gate-expiring-later", libtime.Duration(120*1e9))

		first, err := store.History(ctx, 10, 0)
		Expect(err).To(BeNil())
		Expect(first).To(HaveLen(2))
		Expect(walks).To(Equal(1))

		// Past the FIRST deadline but not the second: the horizon must have fired.
		now = now.Add(libtime.Duration(90 * 1e9))

		second, err := store.History(ctx, 10, 0)
		Expect(err).To(BeNil())
		Expect(walks).To(Equal(2), "the expiry horizon has passed, so the page is re-walked")
		Expect(second).To(HaveLen(1),
			"only the item whose deadline passed may drop; the later one must remain")
		Expect(second[0].DedupKey).To(Equal(DedupKey("gate-expiring-later")))
	})

	It("re-walks after an answer, so the new state is visible", func() {
		push("session-a", "gate-1")
		page, err := store.History(ctx, 10, 0)
		Expect(err).To(BeNil())
		Expect(page).To(HaveLen(1))
		Expect(walks).To(Equal(1))

		_, err = store.Answer(
			ctx,
			page[0].ItemID,
			"operator",
			"operator",
			Decision("approve"),
			nil,
			nil,
			nil,
		)
		Expect(err).To(BeNil())

		_, err = store.History(ctx, 10, 0)
		Expect(err).To(BeNil())
		Expect(walks).To(Equal(2), "an answer must drop the cached page")
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

	// ⚠️ The cache holds the ENCODED page, so a hit must return the identical
	// bytes. If it returned a re-encoded body the decode would be back on the hot
	// path, which is the whole cost the cache exists to remove.
	It("returns the same body on a cache hit", func() {
		push("session-a", "gate-1")
		push("session-a", "gate-2")

		first, err := store.HistoryJSON(ctx, 10, 0)
		Expect(err).To(BeNil())
		Expect(walks).To(Equal(1))

		second, err := store.HistoryJSON(ctx, 10, 0)
		Expect(err).To(BeNil())
		Expect(walks).To(Equal(1), "a repeat read must not re-walk")
		Expect(second).To(Equal(first), "a hit must serve the cached bytes unchanged")
	})

	// ⚠️ This is the spec that closes the GC spiral: a page larger than the budget
	// is served but never retained, so no polling pattern can park a page on the
	// heap. Without it the budget is decorative — the entry would be stored and
	// only evicted by the next store.
	It("does not cache a page larger than the byte budget", func() {
		originalBudget := historyPageCacheBudget
		DeferCleanup(func() { historyPageCacheBudget = originalBudget })
		historyPageCacheBudget = 1

		push("session-a", "gate-1")

		_, err := store.HistoryJSON(ctx, 10, 0)
		Expect(err).To(BeNil())
		Expect(walks).To(Equal(1))
		Expect(store.historyPage.size()).To(Equal(0), "an oversized page must not be retained")

		_, err = store.HistoryJSON(ctx, 10, 0)
		Expect(err).To(BeNil())
		Expect(walks).To(Equal(2), "nothing was cached, so the page is re-walked")
	})

	// ⚠️ The budget is a TOTAL, not a per-entry cap: filling past it must evict the
	// least recently used page, or a caller issuing several distinct queries holds
	// every one of them on the heap.
	It("evicts the least recently used page so the total stays inside the budget", func() {
		originalBudget := historyPageCacheBudget
		DeferCleanup(func() { historyPageCacheBudget = originalBudget })

		push("session-a", "gate-1")
		push("session-a", "gate-2")

		full, err := store.HistoryJSON(ctx, 10, 0)
		Expect(err).To(BeNil())
		Expect(walks).To(Equal(1))

		// A budget that fits exactly the page just read and nothing else.
		historyPageCacheBudget = len(full)

		_, err = store.HistoryJSON(ctx, 1, 0)
		Expect(err).To(BeNil())
		Expect(walks).To(Equal(2))
		Expect(store.historyPage.size()).To(BeNumerically("<=", historyPageCacheBudget),
			"the total must never exceed the budget")

		_, err = store.HistoryJSON(ctx, 10, 0)
		Expect(err).To(BeNil())
		Expect(walks).To(Equal(3), "the older page was evicted, so it must be re-walked")
	})

	// ⚠️ The pollers tick on the same cadence, so a miss is routinely several
	// misses at once. Without the flight map each of them walks the index and the
	// cache is refilled N times for one answer.
	It("walks once when many callers ask for the same page at once", func() {
		push("session-a", "gate-1")
		push("session-a", "gate-2")

		arrived := make(chan struct{}, 12)
		release := make(chan struct{})
		originalCollector := historyPageCollector
		DeferCleanup(func() { historyPageCollector = originalCollector })
		historyPageCollector = func(
			a *attentionStore,
			ctx context.Context,
			limit int,
			offset int,
			filter historyMatcher,
		) ([]byte, libtime.DateTime, error) {
			walks++
			arrived <- struct{}{}
			<-release
			return a.collectHistoryPageJSON(ctx, limit, offset, filter)
		}

		bodies := make([][]byte, 12)
		errs := make([]error, 12)
		var wg sync.WaitGroup
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				bodies[i], errs[i] = store.HistoryJSON(ctx, 10, 0)
			}(i)
		}
		// One caller must own the walk; the rest join its flight.
		<-arrived
		close(release)
		wg.Wait()

		for i := range errs {
			Expect(errs[i]).To(BeNil())
			Expect(bodies[i]).To(Equal(bodies[0]))
		}
		Expect(walks).To(Equal(1), "concurrent callers for one page must share one walk")
	})

	// ⚠️ A joiner must be releasable by its OWN cancellation. Without that arm it
	// inherits the owner's lifetime, so a client that gave up stays parked on a
	// build it no longer wants — and the request that would have served it never
	// returns.
	It("lets a joiner whose request was cancelled stop waiting", func() {
		push("session-a", "gate-1")

		arrived := make(chan struct{}, 1)
		release := make(chan struct{})
		originalCollector := historyPageCollector
		DeferCleanup(func() { historyPageCollector = originalCollector })
		historyPageCollector = func(
			a *attentionStore,
			ctx context.Context,
			limit int,
			offset int,
			filter historyMatcher,
		) ([]byte, libtime.DateTime, error) {
			walks++
			arrived <- struct{}{}
			<-release
			return a.collectHistoryPageJSON(ctx, limit, offset, filter)
		}

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = store.HistoryJSON(ctx, 10, 0)
		}()
		<-arrived

		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		_, err := store.HistoryJSON(cancelled, 10, 0)
		Expect(err).To(MatchError(ContainSubstring("context canceled")))

		close(release)
		wg.Wait()
		Expect(walks).To(Equal(1), "the cancelled joiner must not start its own walk")
	})

	// ⚠️ The budget bounds what is retained; this bounds what is in flight. A
	// burst of DISTINCT pages is the case the budget alone cannot cover — each
	// miss holds its own buffer until it is stored.
	It("caps how many pages are built at once", func() {
		originalSlots := store.historyPage.decodes
		DeferCleanup(func() { store.historyPage.decodes = originalSlots })
		store.historyPage.decodes = make(chan struct{}, 1)

		push("session-a", "gate-1")
		push("session-a", "gate-2")
		push("session-a", "gate-3")

		var mu sync.Mutex
		inFlight, peak := 0, 0
		originalCollector := historyPageCollector
		DeferCleanup(func() { historyPageCollector = originalCollector })
		historyPageCollector = func(
			a *attentionStore,
			ctx context.Context,
			limit int,
			offset int,
			filter historyMatcher,
		) ([]byte, libtime.DateTime, error) {
			mu.Lock()
			inFlight++
			if inFlight > peak {
				peak = inFlight
			}
			mu.Unlock()
			time.Sleep(20 * time.Millisecond)
			body, validUntil, err := a.collectHistoryPageJSON(ctx, limit, offset, filter)
			mu.Lock()
			inFlight--
			mu.Unlock()
			return body, validUntil, err
		}

		var wg sync.WaitGroup
		for _, limit := range []int{1, 2, 3} {
			wg.Add(1)
			go func(limit int) {
				defer wg.Done()
				_, _ = store.HistoryJSON(ctx, limit, 0)
			}(limit)
		}
		wg.Wait()

		Expect(peak).To(Equal(1), "a decode limit of 1 must serialise the walks")
	})
})
