// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// expectDelivered rewrites want until the watcher reports it, so the spec does
// not depend on how long the backend takes to start seeing writes. Any other
// path received in between is drained, but a path outside want's directory
// fails the spec.
func expectDelivered(watcher taskDirWatcher, want string) {
	dir := filepath.Dir(want)
	Eventually(func(g Gomega) {
		g.Expect(os.WriteFile(want, []byte(time.Now().String()), 0o600)).To(Succeed())
		select {
		case got := <-watcher.Events():
			g.Expect(filepath.Dir(got)).To(Equal(dir), "event outside the watched dir")
			g.Expect(got).To(Equal(want))
		case <-time.After(250 * time.Millisecond):
			g.Expect("no event").To(Equal(want))
		}
	}, 10*time.Second).Should(Succeed())
}

// These run on every platform: whichever backend this build selects must
// deliver a write and must shut down cleanly with nobody reading.
var _ = Describe("task dir watcher (platform backend)", func() {
	It("delivers a write under the watched dir", func() {
		dir, err := filepath.EvalSymlinks(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		watcher, err := newTaskDirWatcher(dir)
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = watcher.Close() }()

		expectDelivered(watcher, filepath.Join(dir, "A.md"))
	})

	// ⚠️ The heartbeat store posts atomically — CreateTemp then Rename into the
	// watched dir — so the final name must be reported for a rename, not only for
	// a plain write.
	It("reports the final name of a temp-file-then-rename write", func() {
		dir, err := filepath.EvalSymlinks(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		watcher, err := newTaskDirWatcher(dir)
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = watcher.Close() }()

		want := filepath.Join(dir, "s.json")
		Eventually(func(g Gomega) {
			tmp, err := os.CreateTemp(dir, ".s-*.tmp")
			g.Expect(err).NotTo(HaveOccurred())
			_, _ = tmp.WriteString(time.Now().String())
			g.Expect(tmp.Close()).To(Succeed())
			g.Expect(os.Rename(tmp.Name(), want)).To(Succeed())
			deadline := time.After(250 * time.Millisecond)
			for {
				select {
				case got := <-watcher.Events():
					if got == want {
						return
					}
				case <-deadline:
					g.Expect("no event for the final name").To(Equal(want))
					return
				}
			}
		}, 10*time.Second).Should(Succeed())
	})

	It("returns from Close and closes Events with a full buffer and no reader", func() {
		dir := GinkgoT().TempDir()
		watcher, err := newTaskDirWatcher(dir)
		Expect(err).NotTo(HaveOccurred())
		for i := 0; i < 600; i++ {
			Expect(
				os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d.md", i)), []byte("x"), 0o600),
			).To(Succeed())
		}
		time.Sleep(500 * time.Millisecond)

		closed := make(chan struct{})
		go func() { _ = watcher.Close(); close(closed) }()
		Eventually(closed, 5*time.Second).Should(BeClosed())
		Eventually(func() bool {
			for {
				select {
				case _, ok := <-watcher.Events():
					if !ok {
						return true
					}
				default:
					return false
				}
			}
		}, 5*time.Second).Should(BeTrue())
	})
})
