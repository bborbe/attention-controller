// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

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
