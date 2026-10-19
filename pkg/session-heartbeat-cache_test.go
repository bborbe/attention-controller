// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	stdtime "time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

// The cache exists for one measured defect: a per-request directory scan cost
// ~0.95 s under load, serialised concurrent readers, and pushed a 5 s client
// timeout into the spawn path. These specs pin the two halves of the fix — reads
// never touch disk after the first load, and the watcher keeps the snapshot
// current off the request path — plus the retention bound that keeps the
// directory from growing without limit.
var _ = Describe("Session heartbeat cache", func() {
	var ctx context.Context
	var dir string
	var now stdtime.Time

	clock := func() libtime.CurrentDateTimeGetter {
		return libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
			return libtime.DateTime(now)
		})
	}

	// writeRow writes a heartbeat row directly to the directory, the way a
	// legacy Node writer does — not through Post, so the watcher (not the write
	// path) has to notice it.
	writeRow := func(sessionID string, at stdtime.Time) {
		row := map[string]any{
			"sessionId": sessionID,
			"at":        at.UTC().Format(stdtime.RFC3339Nano),
			"source":    "mcp-timer",
		}
		content, err := json.Marshal(row)
		Expect(err).To(BeNil())
		Expect(os.WriteFile(filepath.Join(dir, sessionID+".json"), content, 0o600)).To(Succeed())
	}

	// runWatcher starts Run and returns a function that stops it and waits for
	// the nil it returns on cancellation.
	runWatcher := func(store pkg.SessionHeartbeatService) func() {
		watchCtx, cancelWatch := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			done <- store.Run(watchCtx)
		}()
		return func() {
			cancelWatch()
			Eventually(done, "5s").Should(Receive(BeNil()))
		}
	}

	BeforeEach(func() {
		ctx = context.Background()
		now = stdtime.Date(2026, 10, 19, 22, 0, 0, 0, stdtime.UTC)
		dir = GinkgoT().TempDir()
	})

	It("serves List from memory, not from a per-request disk scan", func() {
		store := pkg.NewSessionHeartbeatStore(dir, clock(), libtime.Duration(0))
		writeRow("session-a", now)

		listed, err := store.List(ctx)
		Expect(err).To(BeNil())
		Expect(listed).To(HaveLen(1))

		// ⚠️ The proof is the DELETED file, not a latency number: with no
		// watcher running, a store that re-scanned per request would return an
		// empty list here, and the snapshot returns the row it already loaded.
		Expect(os.Remove(filepath.Join(dir, "session-a.json"))).To(Succeed())

		listed, err = store.List(ctx)
		Expect(err).To(BeNil())
		Expect(listed).To(HaveLen(1))
		Expect(listed[0].SessionID).To(Equal("session-a"))
	})

	It("makes a posted heartbeat visible on the next List without a watcher", func() {
		store := pkg.NewSessionHeartbeatStore(dir, clock(), libtime.Duration(0))

		// Warm the snapshot first, so this exercises Post's cache update rather
		// than the lazy load re-reading the file.
		listed, err := store.List(ctx)
		Expect(err).To(BeNil())
		Expect(listed).To(BeEmpty())

		Expect(store.Post(ctx, pkg.SessionHeartbeat{
			SessionID: "5f2a1c34-0000-4000-8000-000000000002",
			Location:  pkg.LocalSessionHeartbeatLocation,
			State:     pkg.IdleSessionHeartbeatState,
			Source:    pkg.MCPTimerSessionHeartbeatSource,
		})).To(Succeed())

		listed, err = store.List(ctx)
		Expect(err).To(BeNil())
		Expect(listed).To(HaveLen(1))
		Expect(listed[0].SessionID).To(Equal("5f2a1c34-0000-4000-8000-000000000002"))
	})

	It("applies a row another writer creates while the watcher runs", func() {
		store := pkg.NewSessionHeartbeatStore(dir, clock(), libtime.Duration(0))
		stop := runWatcher(store)
		defer stop()

		// ⚠️ Rewritten inside the poll, matching the task-index watcher's spec:
		// Run adds its watch asynchronously, so a single write issued before the
		// watch is established would report no event and the spec would time out
		// rather than fail loudly.
		Eventually(func() int {
			writeRow("session-external", now)
			listed, err := store.List(ctx)
			if err != nil {
				return -1
			}
			return len(listed)
		}, "10s", "50ms").Should(Equal(1))
	})

	It("drops a row whose file disappears while the watcher runs", func() {
		store := pkg.NewSessionHeartbeatStore(dir, clock(), libtime.Duration(0))
		writeRow("session-removed", now)
		listed, err := store.List(ctx)
		Expect(err).To(BeNil())
		Expect(listed).To(HaveLen(1))

		stop := runWatcher(store)
		defer stop()

		Expect(os.Remove(filepath.Join(dir, "session-removed.json"))).To(Succeed())
		Eventually(func() int {
			listed, err := store.List(ctx)
			if err != nil {
				return -1
			}
			return len(listed)
		}, "10s", "50ms").Should(Equal(0))
	})

	It("prunes rows past the retention bound and keeps fresh ones", func() {
		retention := libtime.Duration(7 * 24 * stdtime.Hour)
		store := pkg.NewSessionHeartbeatStore(dir, clock(), retention)
		writeRow("session-old", now.Add(-8*24*stdtime.Hour))
		writeRow("session-fresh", now.Add(-stdtime.Hour))

		stop := runWatcher(store)
		defer stop()

		Eventually(func() int {
			listed, err := store.List(ctx)
			if err != nil {
				return -1
			}
			return len(listed)
		}, "10s", "50ms").Should(Equal(1))

		_, err := os.Stat(filepath.Join(dir, "session-old.json"))
		Expect(os.IsNotExist(err)).To(BeTrue())
		_, err = os.Stat(filepath.Join(dir, "session-fresh.json"))
		Expect(err).To(BeNil())

		listed, err := store.List(ctx)
		Expect(err).To(BeNil())
		Expect(listed[0].SessionID).To(Equal("session-fresh"))
	})

	It("reports an unreadable store as an error, never an empty list", func() {
		// A path that is a FILE, not a directory: ReadDir fails with ENOTDIR,
		// which is not the not-exist case that legitimately means "nothing yet".
		notADir := filepath.Join(dir, "not-a-dir")
		Expect(os.WriteFile(notADir, []byte("x"), 0o600)).To(Succeed())

		store := pkg.NewSessionHeartbeatStore(notADir, clock(), libtime.Duration(0))
		listed, err := store.List(ctx)
		Expect(err).NotTo(BeNil())
		Expect(listed).To(BeNil())
	})
})
