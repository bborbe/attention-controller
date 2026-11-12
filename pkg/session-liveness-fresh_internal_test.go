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
	"time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The fresh listing ran on every `IsLive` miss, and a dead producer misses
// forever: 33 % of all CPU on 2026-11-05, every stack ending in
// `LiveSessionsFresh → list`. These specs pin the reuse that removes it, and the
// two guarantees the reuse must not break — a newly registered session is seen
// at once, and an unreadable entry is retried rather than cached.
var _ = Describe("session registry fresh listing", func() {
	var dir string
	var listings int
	var now libtime.DateTime
	var clockMu sync.Mutex

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
		listings = 0
		now = libtime.NewCurrentDateTime().Now()
		original := sessionRegistryLister
		DeferCleanup(func() { sessionRegistryLister = original })
		sessionRegistryLister = func(ctx context.Context, s *sessionLivenessChecker) (SessionIDs, bool, bool) {
			listings++
			return s.listResolved(ctx)
		}
	})

	advance := func(d libtime.Duration) {
		clockMu.Lock()
		defer clockMu.Unlock()
		now = now.Add(d)
	}

	writeEntry := func(name, sessionID string) {
		body, err := json.Marshal(sessionRegistryEntry{SessionID: sessionID})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(dir, name), body, 0o600)).To(Succeed())
	}

	newChecker := func() *sessionLivenessChecker {
		clock := libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
			clockMu.Lock()
			defer clockMu.Unlock()
			return now
		})
		checker, ok := NewSessionLivenessCheckerWithClock(dir, clock).(*sessionLivenessChecker)
		Expect(ok).To(BeTrue(), "the constructor must build the stamping checker")
		return checker
	}

	It("does not re-list an unchanged registry inside the window", func() {
		writeEntry("1.json", "session-a")
		checker := newChecker()

		for i := 0; i < 50; i++ {
			Expect(checker.IsLive(context.Background(), "session-dead")).To(BeFalse())
		}
		Expect(listings).To(Equal(1),
			"fifty misses on a dead producer must cost one listing, not fifty")
	})

	It("re-lists once the window has passed", func() {
		writeEntry("1.json", "session-a")
		checker := newChecker()
		Expect(checker.IsLive(context.Background(), "session-dead")).To(BeFalse())
		Expect(listings).To(Equal(1))

		advance(freshListingWindow)
		Expect(checker.IsLive(context.Background(), "session-dead")).To(BeFalse())
		Expect(listings).To(Equal(2))
	})

	// ⚠️ This is the guarantee the fresh path exists for, and the clock is held
	// still on purpose: inside the window, only the directory mtime can make a
	// new session visible.
	It("sees a session that registers inside the window, through the directory mtime", func() {
		writeEntry("1.json", "session-a")
		checker := newChecker()
		Expect(checker.IsLive(context.Background(), "session-late")).To(BeFalse())

		// Directory mtime resolution is filesystem-dependent; make sure the
		// creation below lands on a different stamp.
		time.Sleep(20 * time.Millisecond)
		writeEntry("2.json", "session-late")
		Expect(checker.IsLive(context.Background(), "session-late")).To(BeTrue(),
			"a newly created registry file must be seen without waiting out the window")
	})

	It("does not cache a listing that had an unreadable entry", func() {
		writeEntry("1.json", "session-a")
		checker := newChecker()

		original := sessionRegistryFileReader
		DeferCleanup(func() { sessionRegistryFileReader = original })
		failed := false
		sessionRegistryFileReader = func(root *os.Root, name string) ([]byte, error) {
			if !failed {
				failed = true
				return nil, os.ErrPermission
			}
			return root.ReadFile(name)
		}

		first, readable := checker.LiveSessionsFresh(context.Background())
		Expect(readable).To(BeTrue())
		Expect(first).To(BeEmpty(), "the entry was unreadable, so the listing is empty")

		second, readable := checker.LiveSessionsFresh(context.Background())
		Expect(readable).To(BeTrue())
		Expect(second).To(ConsistOf("session-a"),
			"the failed entry must be retried inside the window, not served from cache")
		Expect(listings).To(Equal(2), "an incomplete listing is never reused")
	})
})
