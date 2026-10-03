// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

// This file is the probe for the read path's session-registry cost.
//
// The read used to list `~/.claude/sessions` once per open item: every
// `classifyForRead` and every `stillDead` re-check called the checker directly,
// and each call opened the registry, listed it and decoded its entries. A board
// read therefore cost one directory listing per open item, paid again by every
// connected stream on every store change.
//
// The probe counts the thing the defect is about — how many times a read
// consults the session registry — by wrapping the real checker in a fake that
// counts EVERY consult. ⚠️ It counts `IsLive` as well as `LiveSessions`, and
// that is deliberate rather than incidental: a counter that watched only
// `LiveSessions` would read 1 whether or not `stillDead` was left on the direct
// checker, because a `stillDead` left behind calls the fake's `IsLive` and never
// its `LiveSessions` — so the pruning spec below would pass over the very
// regression it exists to catch.

// countingSessionLiveness is a session-liveness checker that also satisfies the
// snapshot capability, counting every consult it receives.
//
// ⚠️ It lives in `package pkg_test` and so cannot name the unexported
// `sessionSnapshotter` interface. It satisfies it structurally by implementing
// `LiveSessions(ctx) (pkg.SessionIDs, bool)` — the method is exported, so
// satisfaction is by method set and needs no name. The real checker is reached
// through that same anonymous interface.
type countingSessionLiveness struct {
	consults    int
	checker     pkg.SessionLivenessChecker
	snapshotter interface {
		LiveSessions(ctx context.Context) (pkg.SessionIDs, bool)
	}
}

// IsLive counts one consult and delegates to the real checker.
func (c *countingSessionLiveness) IsLive(ctx context.Context, sessionID string) bool {
	c.consults++
	return c.checker.IsLive(ctx, sessionID)
}

// LiveSessions counts one consult and delegates to the real checker.
func (c *countingSessionLiveness) LiveSessions(ctx context.Context) (pkg.SessionIDs, bool) {
	c.consults++
	return c.snapshotter.LiveSessions(ctx)
}

// newCountingSessionLiveness wraps the real checker over sessionsDir.
func newCountingSessionLiveness(sessionsDir string) *countingSessionLiveness {
	checker := pkg.NewSessionLivenessChecker(sessionsDir)
	snapshotter, ok := checker.(interface {
		LiveSessions(ctx context.Context) (pkg.SessionIDs, bool)
	})
	Expect(ok).To(BeTrue(), "the real checker must offer the one-listing capability")
	return &countingSessionLiveness{checker: checker, snapshotter: snapshotter}
}

// snapshotOf returns the one-listing capability of a real checker. The
// interface is unexported in pkg, so it is named structurally here.
func snapshotOf(
	checker pkg.SessionLivenessChecker,
) interface {
	LiveSessions(ctx context.Context) (pkg.SessionIDs, bool)
} {
	snapshotter, ok := checker.(interface {
		LiveSessions(ctx context.Context) (pkg.SessionIDs, bool)
	})
	Expect(ok).To(BeTrue(), "the real checker must offer the one-listing capability")
	return snapshotter
}

var _ = Describe("Session liveness checker", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("answers IsLive from the registry and reads an unreadable one as live", func() {
		dir := GinkgoT().TempDir()
		Expect(os.WriteFile(
			filepath.Join(dir, "present.json"),
			[]byte(`{"sessionId":"present"}`),
			0600,
		)).To(BeNil())
		checker := pkg.NewSessionLivenessChecker(dir)

		Expect(checker.IsLive(ctx, "present")).To(BeTrue())
		Expect(checker.IsLive(ctx, "absent")).To(BeFalse())
		Expect(checker.IsLive(ctx, "")).To(BeFalse(), "the empty id is never live")

		unreadable := pkg.NewSessionLivenessChecker(filepath.Join(GinkgoT().TempDir(), "missing"))
		Expect(unreadable.IsLive(ctx, "present")).To(BeTrue(),
			"an unreadable registry cannot prove a session is gone")
	})

	It("collects only the ids a readable listing carries", func() {
		dir := GinkgoT().TempDir()
		Expect(os.WriteFile(
			filepath.Join(dir, "a.json"),
			[]byte(`{"sessionId":"a"}`),
			0600,
		)).To(BeNil())
		Expect(os.WriteFile(
			filepath.Join(dir, "b.json"),
			[]byte(`{"sessionId":"b"}`),
			0600,
		)).To(BeNil())
		// Skipped: not JSON, records no id, and not a .json name at all.
		Expect(os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{"), 0600)).To(BeNil())
		Expect(os.WriteFile(filepath.Join(dir, "empty.json"), []byte(`{}`), 0600)).To(BeNil())
		Expect(os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0600)).To(BeNil())

		ids, readable := snapshotOf(pkg.NewSessionLivenessChecker(dir)).LiveSessions(ctx)

		Expect(readable).To(BeTrue())
		Expect(ids).To(ConsistOf("a", "b"))
		Expect(ids.Contains("a")).To(BeTrue())
		Expect(ids.Contains("")).To(BeFalse())
	})

	It("reports an unreadable registry as not readable", func() {
		checker := pkg.NewSessionLivenessChecker(filepath.Join(GinkgoT().TempDir(), "missing"))
		_, readable := snapshotOf(checker).LiveSessions(ctx)
		Expect(readable).To(BeFalse())
	})
})

var _ = Describe("Read session-registry cost", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	// newStore opens a real temp-file DB and an attention store over it.
	newStore := func(checker pkg.SessionLivenessChecker) (pkg.AttentionStore, libkv.DB) {
		db, err := libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())
		store := pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			checker,
			libtime.NewCurrentDateTime(),
			libtime.Duration(15*60*1e9),
		)
		return store, db
	}

	// writeRegistryEntry records one live session in the registry directory.
	writeRegistryEntry := func(dir, sessionID string) {
		content := fmt.Sprintf(`{"sessionId":%q}`, sessionID)
		err := os.WriteFile(filepath.Join(dir, sessionID+".json"), []byte(content), 0600)
		Expect(err).To(BeNil())
	}

	// pushSessionItem pushes one open item whose producer is the given session.
	pushSessionItem := func(store pkg.AttentionStore, index int) {
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

	// A board read lists the registry exactly once, whatever the open-item count.
	// ⚠️ Run at N=1 and N=50: at a single item a per-item listing returns the
	// same figure as a shared one, so only the larger case can tell them apart.
	It("lists the session registry once per board read, whatever the open count", func() {
		for _, count := range []int{1, 50} {
			dir := GinkgoT().TempDir()
			for i := 0; i < count; i++ {
				writeRegistryEntry(dir, fmt.Sprintf("session-%d", i))
			}
			checker := newCountingSessionLiveness(dir)
			store, db := newStore(checker)
			for i := 0; i < count; i++ {
				pushSessionItem(store, i)
			}

			items, err := store.ReadBoard(ctx)
			Expect(err).To(BeNil())
			Expect(items).To(HaveLen(count))
			Expect(checker.consults).To(Equal(1),
				"the read listed the registry once per open item instead of once per read")

			Expect(db.Close()).To(BeNil())
		}
	})

	// ⚠️ The pruning case, and the one that catches `stillDead` being left on the
	// direct checker. Every producer is gone and every item is an ask (message,
	// not ack), so `classifyForRead` marks them for removal and `pruneDead`
	// re-checks each one through `stillDead`. A read that re-listed the registry
	// per pruned item would count 1 + count here.
	It("does not re-list the registry for the items a read prunes", func() {
		dir := GinkgoT().TempDir()
		checker := newCountingSessionLiveness(dir)
		store, db := newStore(checker)

		const count = 5
		for i := 0; i < count; i++ {
			pushSessionItem(store, i)
		}

		items, err := store.ReadBoard(ctx)
		Expect(err).To(BeNil())
		Expect(
			items,
		).To(BeEmpty(), "every producer is gone and every item was asked, so all are pruned")
		Expect(checker.consults).To(Equal(1),
			"the prune re-listed the registry once per dead item instead of reusing the read's snapshot")

		Expect(db.Close()).To(BeNil())
	})

	// A read that tests no session-model item must not list the registry at all:
	// the snapshot is taken lazily, on the first session-liveness lookup.
	It("lists the registry zero times when no item declares a session", func() {
		dir := GinkgoT().TempDir()
		checker := newCountingSessionLiveness(dir)
		store, db := newStore(checker)

		heartbeat := filepath.Join(GinkgoT().TempDir(), "heartbeat")
		Expect(os.WriteFile(heartbeat, []byte("beat"), 0600)).To(BeNil())

		const count = 3
		for i := 0; i < count; i++ {
			_, err := store.Push(ctx, pkg.PushRequest{
				ProducerID:      pkg.ProducerID(fmt.Sprintf("producer-%d", i)),
				ProducerKind:    pkg.SessionProducerKind,
				LivenessRef:     pkg.LivenessRef("heartbeat:" + heartbeat),
				DedupKey:        pkg.DedupKey(fmt.Sprintf("beat-%d", i)),
				InterruptClass:  "approve",
				Payload:         "deploy prod?",
				AnswerMechanism: pkg.MessageAnswerMechanism,
			})
			Expect(err).To(BeNil())
		}

		items, err := store.ReadBoard(ctx)
		Expect(err).To(BeNil())
		Expect(items).To(HaveLen(count))
		Expect(checker.consults).To(Equal(0),
			"a read of heartbeat-only items must not list the session registry")

		Expect(db.Close()).To(BeNil())
	})

	// An unreadable registry proves nothing, so it reads as live and every open
	// item is kept rather than swept. The rule is load-bearing and must survive
	// the snapshot: a read that could not see the registry keeps everything.
	It("keeps every open item when the registry cannot be read", func() {
		dir := filepath.Join(GinkgoT().TempDir(), "missing")
		checker := newCountingSessionLiveness(dir)
		store, db := newStore(checker)

		const count = 3
		for i := 0; i < count; i++ {
			pushSessionItem(store, i)
		}

		items, err := store.ReadBoard(ctx)
		Expect(err).To(BeNil())
		Expect(items).To(HaveLen(count), "an unreadable registry must not prune anything")

		Expect(db.Close()).To(BeNil())
	})
})
