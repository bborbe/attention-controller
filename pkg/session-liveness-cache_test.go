// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

// mutableClock is a CurrentDateTimeGetter whose reading is moved by hand, so
// the cache window can be crossed without a sleep. Every spec in this file
// drives the window through it — the alternative, real time, would make the
// specs both slow and flaky.
type mutableClock struct {
	mu  sync.Mutex
	now libtime.DateTime
	// nowCalls counts how many times Now has been read. The stampede spec uses
	// it to tell one collapsed listing from N separate ones, because `list` is
	// unexported and the clock is the only seam that observes it from
	// `package pkg_test`. Atomic because that spec reads the clock from N
	// goroutines at once.
	nowCalls atomic.Int64
}

func newMutableClock() *mutableClock {
	return &mutableClock{now: libtime.DateTimeFromUnixMicro(0)}
}

// Now returns the current reading.
func (c *mutableClock) Now() libtime.DateTime {
	c.nowCalls.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// NowCalls returns how many times Now has been called so far.
func (c *mutableClock) NowCalls() int64 {
	return c.nowCalls.Load()
}

// Advance moves the reading forward by the given duration.
func (c *mutableClock) Advance(duration libtime.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(duration)
}

// writeSessionEntry records one live session in the registry directory.
func writeSessionEntry(dir, sessionID string) {
	content := fmt.Sprintf(`{"sessionId":%q}`, sessionID)
	err := os.WriteFile(filepath.Join(dir, sessionID+".json"), []byte(content), 0600)
	Expect(err).To(BeNil())
}

// newCacheTestStore opens a temp-file DB and a store over the given checker.
//
// ⚠️ The DB close is registered as a DeferCleanup rather than left to each
// spec's last line: the consult-count spec opens one DB per loop iteration, so
// an assertion failing mid-loop would leak every DB opened before it. Cleanup
// runs however the spec exits, including on failure. boltkv's Close is
// idempotent — bbolt returns nil for an already-closed DB — so the existing
// specs' explicit Close still returns nil and their assertions are untouched.
func newCacheTestStore(
	ctx context.Context,
	checker pkg.SessionLivenessChecker,
) (pkg.AttentionStore, libkv.DB) {
	db, err := libboltkv.OpenTemp(ctx)
	Expect(err).To(BeNil())
	DeferCleanup(func() {
		Expect(db.Close()).To(BeNil())
	})
	store := pkg.NewAttentionStore(
		db,
		pkg.NewItemIDGenerator(),
		checker,
		libtime.NewCurrentDateTime(),
		libtime.Duration(15*60*1e9),
	)
	return store, db
}

// pushAbsentSessionItem pushes one open ask whose producer is a session that is
// NOT in the registry.
func pushAbsentSessionItem(ctx context.Context, store pkg.AttentionStore, index int) {
	sessionID := fmt.Sprintf("session-%d", index)
	_, err := store.Push(ctx, pkg.PushRequest{
		ProducerID:      pkg.ProducerID(sessionID),
		ProducerKind:    pkg.SessionProducerKind,
		LivenessRef:     pkg.LivenessRef("session:" + sessionID),
		DedupKey:        pkg.DedupKey(fmt.Sprintf("gate-%d", index)),
		InterruptClass:  "approve",
		Payload:         "deploy prod?",
		AnswerMechanism: pkg.MessageAnswerMechanism,
	})
	Expect(err).To(BeNil())
}

// countingFreshChecker is a checker that satisfies both the cached and the
// uncached listing capability, counting only the uncached one. It exists to
// measure how often the read path's miss guard re-lists.
type countingFreshChecker struct {
	sessions      pkg.SessionIDs
	freshConsults int
}

// IsLive answers from the fixed listing.
func (c *countingFreshChecker) IsLive(ctx context.Context, sessionID string) bool {
	return c.sessions.Contains(sessionID)
}

// LiveSessions serves the cached listing.
func (c *countingFreshChecker) LiveSessions(ctx context.Context) (pkg.SessionIDs, bool) {
	return c.sessions, true
}

// LiveSessionsFresh counts one consult and serves the same listing.
func (c *countingFreshChecker) LiveSessionsFresh(
	ctx context.Context,
) (pkg.SessionIDs, bool) {
	c.freshConsults++
	return c.sessions, true
}

var _ = Describe("Session registry cache", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	// The window is honoured: a listing taken inside it is reused, and one
	// taken past it is not.
	It("reuses one listing for the cache window and re-lists after it", func() {
		dir := GinkgoT().TempDir()
		writeSessionEntry(dir, "a")
		clock := newMutableClock()
		checker := pkg.NewSessionLivenessCheckerWithClock(dir, clock)
		snapshot := snapshotOf(checker)

		ids, readable := snapshot.LiveSessions(ctx)
		Expect(readable).To(BeTrue())
		Expect(ids).To(ConsistOf("a"))

		writeSessionEntry(dir, "b")
		ids, readable = snapshot.LiveSessions(ctx)
		Expect(readable).To(BeTrue())
		Expect(ids).To(ConsistOf("a"),
			"a listing taken inside the window must be reused, so the new entry is not seen")

		clock.Advance(libtime.Duration(2 * time.Second))
		ids, readable = snapshot.LiveSessions(ctx)
		Expect(readable).To(BeTrue())
		Expect(ids).To(ConsistOf("a", "b"),
			"a listing taken past the window must be re-read")
	})

	// A cached listing is reused across reads.
	It("serves a cached listing across reads", func() {
		dir := GinkgoT().TempDir()
		writeSessionEntry(dir, "a")
		clock := newMutableClock()
		checker := pkg.NewSessionLivenessCheckerWithClock(dir, clock)
		snapshot := snapshotOf(checker)

		first, readable := snapshot.LiveSessions(ctx)
		Expect(readable).To(BeTrue())
		Expect(first).To(ConsistOf("a"))

		writeSessionEntry(dir, "b")
		second, readable := snapshot.LiveSessions(ctx)
		Expect(readable).To(BeTrue())
		Expect(second).To(ConsistOf("a"),
			"the second read inside the window must reuse the first listing")
	})

	// ⚠️ The spec that would fail if the miss guard were dropped, and the whole
	// reason the window is safe: a session that registers inside the window is
	// absent from the cached listing, and answering "gone" from it would prune a
	// live item.
	It("re-lists on a miss, so a session registered inside the window is not gone", func() {
		dir := GinkgoT().TempDir()
		clock := newMutableClock()
		checker := pkg.NewSessionLivenessCheckerWithClock(dir, clock)

		ids, readable := snapshotOf(checker).LiveSessions(ctx)
		Expect(readable).To(BeTrue())
		Expect(ids).To(BeEmpty(), "the listing is now cached and holds nothing")

		writeSessionEntry(dir, "late")

		Expect(checker.IsLive(ctx, "late")).To(BeTrue(),
			"a session registered inside the window must not be reported gone")
	})

	// An unreadable registry proves nothing and reads as live, and the rule must
	// survive the cache: a cached unreadable listing keeps reading as live.
	It("reads an unreadable registry as live, through the cache", func() {
		dir := filepath.Join(GinkgoT().TempDir(), "missing")
		clock := newMutableClock()
		checker := pkg.NewSessionLivenessCheckerWithClock(dir, clock)

		Expect(checker.IsLive(ctx, "anything")).To(BeTrue())
		Expect(checker.IsLive(ctx, "anything")).To(BeTrue(),
			"a cached unreadable listing must still read as live")
	})

	// ⚠️ The guard fires at most once per read-session-liveness, and a read that
	// prunes holds two of them — the classification's and the prune's — so the
	// count is 2 however many items the read holds. A guard that fired per item
	// would count one per item here.
	It("consults the fresh listing at most once per read, not once per item", func() {
		for _, count := range []int{1, 5} {
			checker := &countingFreshChecker{}
			store, db := newCacheTestStore(ctx, checker)
			for i := 0; i < count; i++ {
				pushAbsentSessionItem(ctx, store, i)
			}

			items, err := store.ReadBoard(ctx)
			Expect(err).To(BeNil())
			Expect(items).To(BeEmpty(), "every producer is absent, so all are pruned")
			Expect(checker.freshConsults).To(Equal(2),
				"the guard must fire at most once per read, not once per item")

			Expect(db.Close()).To(BeNil())
		}
	})

	// ⚠️ The discriminator for the miss guard's ANSWER, not merely that it fired.
	// The consult-count spec above proves the guard RAN; this one proves the
	// listing it fetched is the one that DECIDES. The registry entry is written
	// WITHOUT moving the clock, so the cached listing predates it and the read's
	// first lookup must miss. Only the guard's fresh listing holds the id: an
	// implementation that called LiveSessionsFresh and threw the result away
	// would answer from the stale cached listing, classify the item as dead and
	// prune it — this spec fails against exactly that implementation.
	It(
		"uses the miss guard's fresh listing, so a session registered inside the window is kept",
		func() {
			dir := GinkgoT().TempDir() // empty: no session is registered yet
			clock := newMutableClock()
			checker := pkg.NewSessionLivenessCheckerWithClock(dir, clock)
			store, _ := newCacheTestStore(ctx, checker)

			// An ask (message, not ack), so a producer read as gone is prunable.
			pushAbsentSessionItem(ctx, store, 0)

			// Prime the cache with the empty listing. LiveSessions directly, not
			// ReadBoard: a read here would classify the item against the empty
			// registry and prune it before the spec's setup was complete.
			ids, readable := snapshotOf(checker).LiveSessions(ctx)
			Expect(readable).To(BeTrue())
			Expect(ids).To(BeEmpty(), "the listing is now cached and holds nothing")

			// Register the session WITHOUT advancing the clock: the cached listing
			// predates this entry, so the read's first lookup must miss it.
			writeSessionEntry(dir, "session-0")

			items, err := store.ReadBoard(ctx)
			Expect(err).To(BeNil())
			Expect(items).To(HaveLen(1),
				"only the miss guard's fresh listing holds the id; an answer taken from the cached listing would prune a live item")
		},
	)

	// ⚠️ The design claim the cache rests on: the mutex is held ACROSS the
	// listing, so N callers past the window collapse into ONE listing rather
	// than N. Every other spec drives a single goroutine, so nothing else tests
	// it.
	//
	// `list` is unexported, so the listing count is observed through the
	// injected clock: LiveSessions reads `s.now.Now()` once at entry and stamps
	// `s.cachedAt` from a SECOND read AFTER the single listing returns. One
	// collapsed stampede therefore costs N+1 clock reads — N entry reads plus
	// the one stamp — while N separate listings would cost 2N, one entry read
	// and one stamp each. The assertion below is N+1.
	It("collapses a stampede of concurrent callers into one listing", func() {
		dir := GinkgoT().TempDir()
		writeSessionEntry(dir, "a")
		clock := newMutableClock()
		checker := pkg.NewSessionLivenessCheckerWithClock(dir, clock)
		snapshotter := snapshotOf(checker)

		// Prime the cache, then step past the window so every caller is stale.
		_, readable := snapshotter.LiveSessions(ctx)
		Expect(readable).To(BeTrue())
		clock.Advance(libtime.Duration(2 * time.Second))

		const goroutines = 8
		before := clock.NowCalls()
		// All callers are released together and contend on the checker's lock,
		// so this is deterministic without a sleep: the start channel plus the
		// lock held across the listing is what collapses them.
		start := make(chan struct{})
		results := make([]pkg.SessionIDs, goroutines)
		readables := make([]bool, goroutines)
		var wg sync.WaitGroup
		wg.Add(goroutines)
		for i := 0; i < goroutines; i++ {
			go func(i int) {
				defer wg.Done()
				<-start
				results[i], readables[i] = snapshotter.LiveSessions(ctx)
			}(i)
		}
		close(start)
		wg.Wait()

		for i := 0; i < goroutines; i++ {
			Expect(readables[i]).To(BeTrue())
			Expect(results[i]).To(ConsistOf("a"),
				"every caller must receive the same listing")
		}
		// N entry reads + 1 stamp = N+1. A lock released across the listing
		// would let callers list separately, costing 2N.
		Expect(clock.NowCalls()-before).To(Equal(int64(goroutines+1)),
			"the lock must be held across the listing, collapsing N callers into one")
	})
})
