// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build darwin && cgo

package pkg

import (
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsevents"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("FSEvents task dir watcher", func() {
	DescribeTable("mapFSEventPath",
		func(eventPath string, want string, wantOK bool) {
			got, ok := mapFSEventPath(eventPath, "/real/vault/25 Tasks", "/link/vault/25 Tasks")
			Expect(ok).To(Equal(wantOK))
			Expect(got).To(Equal(want))
		},
		Entry("device-relative path without leading slash",
			"real/vault/25 Tasks/A.md", "/link/vault/25 Tasks/A.md", true),
		Entry("absolute path", "/real/vault/25 Tasks/A.md", "/link/vault/25 Tasks/A.md", true),
		Entry("the watched directory itself", "real/vault/25 Tasks", "", false),
		Entry("a sibling directory", "real/vault/24 Goals/G.md", "", false),
		Entry("a prefix-sharing sibling", "real/vault/25 Tasks Archive/A.md", "", false),
	)

	// ⚠️ The vault is reached through a symlink on the operator's Mac, and FSEvents
	// reports real paths. Without the mapping every event is dropped and the index
	// silently falls back to the backstop window.
	It("delivers a write made through a symlinked directory under the symlinked path", func() {
		base, err := filepath.EvalSymlinks(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		realDir := filepath.Join(base, "real")
		Expect(os.Mkdir(realDir, 0o755)).To(Succeed())
		linkDir := filepath.Join(base, "link")
		Expect(os.Symlink(realDir, linkDir)).To(Succeed())

		watcher, err := newTaskDirWatcher(linkDir)
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = watcher.Close() }()

		want := filepath.Join(linkDir, "A.md")
		expectDelivered(watcher, want)
	})

	// ⚠️ FSEvents paths are absolute; a relative configured dir must still match.
	It("delivers writes when the watched dir is given as a relative path", func() {
		base, err := filepath.EvalSymlinks(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		GinkgoT().Chdir(base)
		Expect(os.Mkdir("tasks", 0o755)).To(Succeed())

		watcher, err := newTaskDirWatcher("tasks")
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = watcher.Close() }()

		expectDelivered(watcher, filepath.Join("tasks", "A.md"))
	})

	It("reports a drop-flagged batch as exactly one error and still relays its paths", func() {
		w := &fseventsTaskDirWatcher{
			dir:     "/link/t",
			realDir: "/real/t",
			events:  make(chan string, 8),
			errors:  make(chan error, 8),
			done:    make(chan struct{}),
		}
		ok := w.relayBatch([]fsevents.Event{
			{Path: "real/t", Flags: fsevents.MustScanSubDirs},
			{Path: "real/t", Flags: fsevents.KernelDropped},
			{Path: "real/t/A.md"},
		})
		Expect(ok).To(BeTrue())
		Expect(w.errors).To(HaveLen(1))
		Expect(<-w.errors).To(MatchError(errFSEventsDropped))
		Expect(w.events).To(Receive(Equal("/link/t/A.md")))
	})

	It("closes its event channel on Close", func() {
		watcher, err := newTaskDirWatcher(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		Expect(watcher.Close()).To(Succeed())
		Eventually(watcher.Events(), 2*time.Second).Should(BeClosed())
	})
})
