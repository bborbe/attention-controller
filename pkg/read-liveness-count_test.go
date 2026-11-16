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
// connected stream on every store change. Since a dead liveness subject no
// longer removes an item, the read consults the registry not at all.
//
// The probe counts the thing the defect is about — how many times a read
// consults the session registry — by wrapping the real checker in a fake that
// counts EVERY consult. ⚠️ It counts `IsLive` as well as `LiveSessions`, so a
// liveness probe re-introduced on the read path through either method is seen.

// countingSessionLiveness is a session-liveness checker that also lists the
// registry, counting every consult it receives.
//
// The real checker's `LiveSessions(ctx) (pkg.SessionIDs, bool)` is exported, so
// it is reached through an anonymous interface rather than a named one.
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

	// A board read does not consult the session registry at all: a dead
	// liveness subject removes nothing, so the read has no use for a listing.
	// ⚠️ Run with a populated registry and with an empty one (every producer
	// gone): neither may list, and the empty case must keep every ask.
	It("never consults the session registry on a board read", func() {
		for _, populated := range []bool{true, false} {
			dir := GinkgoT().TempDir()
			const count = 50
			if populated {
				for i := 0; i < count; i++ {
					writeRegistryEntry(dir, fmt.Sprintf("session-%d", i))
				}
			}
			checker := newCountingSessionLiveness(dir)
			store, db := newStore(checker)
			for i := 0; i < count; i++ {
				pushSessionItem(store, i)
			}
			checker.consults = 0

			items, err := store.ReadBoard(ctx)
			Expect(err).To(BeNil())
			Expect(items).To(HaveLen(count), "a dead subject alone must not remove an ask")
			Expect(checker.consults).To(Equal(0),
				"the read path must not list the registry — liveness no longer decides removal")

			Expect(db.Close()).To(BeNil())
		}
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

	// Regression guard for the removed per-read snapshot: a read must not consult
	// the session registry AT ALL, and the unreadable registry is the case that
	// used to be load-bearing.
	//
	// ⚠️ The old assertion here — "keeps every open item when the registry cannot
	// be read" — can no longer fail: nothing is pruned on liveness, so every read
	// keeps every open item whatever the registry says, and the check would pass
	// against a store that listed the registry a hundred times per read. What is
	// still falsifiable is the consult count, so that is what this pins. The
	// fail-safe itself (an unreadable registry renews rather than lapses) is
	// pinned where it now matters, in attention-store-dead-subject_test.go.
	It("consults the registry zero times on a read, even when it cannot be read", func() {
		dir := filepath.Join(GinkgoT().TempDir(), "missing")
		checker := newCountingSessionLiveness(dir)
		store, db := newStore(checker)

		const count = 3
		for i := 0; i < count; i++ {
			pushSessionItem(store, i)
		}
		checker.consults = 0

		items, err := store.ReadBoard(ctx)
		Expect(err).To(BeNil())
		Expect(items).To(HaveLen(count))
		Expect(checker.consults).To(Equal(0),
			"an unreadable registry must not be consulted by the read path either")

		Expect(db.Close()).To(BeNil())
	})
})
