// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The stamp cache exists because `IsLive` re-lists the registry on every miss,
// so an attention read whose items include a dead producer reads and decodes
// every registry file — 19 % of all allocation, measured 2026-11-04. A listing
// must therefore re-read only the files that changed.
var _ = Describe("session registry stamp cache", func() {
	var dir string
	var reads int

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
		reads = 0
		original := sessionRegistryFileReader
		DeferCleanup(func() { sessionRegistryFileReader = original })
		sessionRegistryFileReader = func(root *os.Root, name string) ([]byte, error) {
			reads++
			return root.ReadFile(name)
		}
	})

	writeEntry := func(name, sessionID string) string {
		path := filepath.Join(dir, name)
		body, err := json.Marshal(sessionRegistryEntry{SessionID: sessionID})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(path, body, 0o600)).To(Succeed())
		return path
	}

	newChecker := func() *sessionLivenessChecker {
		checker, ok := NewSessionLivenessCheckerWithClock(
			dir, libtime.NewCurrentDateTime(),
		).(*sessionLivenessChecker)
		Expect(ok).To(BeTrue(), "the constructor must build the stamping checker")
		return checker
	}

	It("re-reads nothing when no file changed", func() {
		writeEntry("1.json", "session-a")
		writeEntry("2.json", "session-b")
		checker := newChecker()

		first, readable := checker.LiveSessionsFresh(context.Background())
		Expect(readable).To(BeTrue())
		Expect(first).To(ConsistOf("session-a", "session-b"))
		Expect(reads).To(Equal(2))

		second, readable := checker.LiveSessionsFresh(context.Background())
		Expect(readable).To(BeTrue())
		Expect(second).To(ConsistOf("session-a", "session-b"))
		Expect(reads).To(Equal(2), "an unchanged listing must not re-read a file")
	})

	It("re-reads only the file that changed", func() {
		writeEntry("1.json", "session-a")
		writeEntry("2.json", "session-b")
		checker := newChecker()
		_, _ = checker.LiveSessionsFresh(context.Background())
		Expect(reads).To(Equal(2))

		// A rename rewrites one entry.
		writeEntry("2.json", "session-b2")
		ids, readable := checker.LiveSessionsFresh(context.Background())
		Expect(readable).To(BeTrue())
		Expect(ids).To(ConsistOf("session-a", "session-b2"))
		Expect(reads).To(Equal(3), "only the changed file is re-read")
	})

	It("drops an entry whose file is gone", func() {
		path := writeEntry("1.json", "session-a")
		checker := newChecker()
		_, _ = checker.LiveSessionsFresh(context.Background())

		Expect(os.Remove(path)).To(Succeed())
		ids, readable := checker.LiveSessionsFresh(context.Background())
		Expect(readable).To(BeTrue())
		Expect(ids).To(BeEmpty())
	})

	// ⚠️ The retry guarantee is the reason the failure path leaves an entry OUT
	// of `next` rather than stamping it: a stamped failure would be cached until
	// the file happened to change, so one transient read error would hide a live
	// session for as long as it lived. This spec is the only thing that pins that
	// choice — without it, "leave it out" and "stamp it empty" behave identically
	// on every passing listing.
	It("retries an entry it could not read instead of caching the failure", func() {
		writeEntry("1.json", "session-a")
		checker := newChecker()

		original := sessionRegistryFileReader
		DeferCleanup(func() { sessionRegistryFileReader = original })
		failed := false
		sessionRegistryFileReader = func(root *os.Root, name string) ([]byte, error) {
			reads++
			if !failed {
				failed = true
				return nil, os.ErrPermission
			}
			return root.ReadFile(name)
		}

		first, readable := checker.LiveSessionsFresh(context.Background())
		Expect(readable).To(BeTrue())
		Expect(first).To(BeEmpty(), "an unreadable entry contributes no id")

		second, readable := checker.LiveSessionsFresh(context.Background())
		Expect(readable).To(BeTrue())
		Expect(second).To(ConsistOf("session-a"),
			"a transient read failure must be retried, not cached until the file changes")
		Expect(reads).To(Equal(2), "the failed entry is read again on the next listing")
	})

	// ⚠️ An unparseable entry takes the same path as an unreadable one, and the
	// two are separate branches in entrySessionID — a decode error and an I/O
	// error. The cache must not turn either into a permanent absence.
	// ⚠️ The file is left VALID on disk and the failure is injected through the
	// reader, because rewriting it would change size and mtime and force a re-read
	// whatever the cache did with the failure. A spec that rewrites the file
	// passes against a cache that stamps the failed entry — it is a duplicate of
	// "re-reads only the file that changed", not a test of the retry.
	It("retries an entry it could not parse", func() {
		writeEntry("1.json", "session-a")
		checker := newChecker()

		original := sessionRegistryFileReader
		DeferCleanup(func() { sessionRegistryFileReader = original })
		failed := false
		sessionRegistryFileReader = func(root *os.Root, name string) ([]byte, error) {
			reads++
			if !failed {
				failed = true
				return []byte("{not json"), nil
			}
			return root.ReadFile(name)
		}

		first, readable := checker.LiveSessionsFresh(context.Background())
		Expect(readable).To(BeTrue())
		Expect(first).To(BeEmpty(), "an unparseable entry contributes no id")

		second, readable := checker.LiveSessionsFresh(context.Background())
		Expect(readable).To(BeTrue())
		Expect(second).To(ConsistOf("session-a"),
			"a parse failure must be retried even though the file never changed")
		Expect(reads).To(Equal(2), "the unparseable entry is read again on the next listing")
	})

	// ⚠️ entryMu and entryStamps are new shared state, and the default gate runs
	// `-race=false` (Makefile.precommit:32, opt-in via ENABLE_RACE), so the race
	// detector will not catch a mistake here. This spec cannot replace it, but it
	// does pin that concurrent listings all succeed and agree — the failure a
	// missing lock produces under load is a torn map, not a compile error.
	It("survives concurrent listings", func() {
		writeEntry("1.json", "session-a")
		writeEntry("2.json", "session-b")
		checker := newChecker()

		// ⚠️ The start barrier is load-bearing. Without it the scheduler can run
		// the goroutines back to back, the spec passes, and entryMu was never
		// contended — the false confidence the spec exists to prevent.
		// ⚠️ The done signal is a defer, not a send after the assertions. A failed
		// Expect panics, GinkgoRecover swallows it, and a goroutine that only
		// signalled on success would leave the main goroutine blocking until the
		// timeout — reporting a timeout instead of the assertion that actually
		// failed. Same shape as pkg/attention-store_test.go.
		const listings = 8
		results := make([]SessionIDs, listings)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < listings; i++ {
			wg.Add(1)
			go func(i int) {
				defer GinkgoRecover()
				defer wg.Done()
				<-start
				ids, readable := checker.LiveSessionsFresh(context.Background())
				Expect(readable).To(BeTrue())
				results[i] = ids
			}(i)
		}
		close(start)
		finished := make(chan struct{})
		go func() {
			wg.Wait()
			close(finished)
		}()
		Eventually(finished, "10s").Should(BeClosed())
		for i := 0; i < listings; i++ {
			Expect(results[i]).To(ConsistOf("session-a", "session-b"))
		}
	})

	It("answers live for a session that registers after a cached listing", func() {
		writeEntry("1.json", "session-a")
		checker := newChecker()
		Expect(checker.IsLive(context.Background(), "session-a")).To(BeTrue())
		Expect(checker.IsLive(context.Background(), "session-late")).To(BeFalse())

		writeEntry("2.json", "session-late")
		Expect(checker.IsLive(context.Background(), "session-late")).To(BeTrue(),
			"the fresh re-list must see a session that registered inside the window")
	})
})
