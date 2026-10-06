// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"os"
	"path/filepath"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

// The index used to re-read the whole vault on a two-second timer. These specs
// pin the replacement: a watcher on the task directory is what keeps the index
// current, a render performs no rebuild at all, and the slow backstop window
// bounds the rest rather than driving it.
var _ = Describe("TaskIndexWatcher", func() {
	var ctx context.Context
	var clock libtime.CurrentDateTime

	BeforeEach(func() {
		ctx = context.Background()
		// Frozen, so the backstop window is measured from a fixed instant and the
		// specs advance it with SetNow rather than sleeping.
		clock = libtime.NewCurrentDateTime()
		clock.SetNow(clock.Now())
	})

	It("resolves a task file written while the watcher runs", func() {
		vault := GinkgoT().TempDir()
		// ⚠️ The directory must exist before the watcher starts: watcher.Add on an
		// absent directory fails, Run returns nil, and no event ever fires.
		tasksDir := filepath.Join(vault, "25 Tasks")
		Expect(os.MkdirAll(tasksDir, 0o750)).To(Succeed())

		index := pkg.NewTaskIndex(ctx, vault, clock)

		watchCtx, cancelWatch := context.WithCancel(ctx)
		defer cancelWatch()
		// The one goroutine these specs need: Run blocks until the context is
		// cancelled, so it runs concurrently and reports through a buffered
		// channel, matching the idiom the existing specs use.
		done := make(chan error, 1)
		go func() {
			done <- pkg.NewTaskIndexWatcher(index, vault).Run(watchCtx)
		}()

		// ⚠️ The task file is rewritten on every poll rather than written once
		// before the watcher starts. Run calls watcher.Add asynchronously, so a
		// single write issued immediately after the goroutine starts can land
		// before the watch is established — inotify reports no event for a file
		// created before the watch, and with the injected clock frozen the
		// five-minute backstop never fires, so the spec would time out rather than
		// fail loudly. Rewriting inside the poll guarantees an event once the watch
		// is up.
		var task pkg.Task
		Eventually(func() string {
			_ = os.WriteFile(filepath.Join(tasksDir, "Watched Task.md"),
				[]byte("---\nclaude_session_id: session-watched\n---\n"), 0o600)
			found, ok := index.Lookup("session-watched")
			if !ok {
				return ""
			}
			task = found
			return found.Name
		}, "10s", "50ms").Should(Equal("Watched Task"))

		Expect(task.Path).To(Equal("25 Tasks/Watched Task.md"))

		cancelWatch()
		Eventually(done, "5s").Should(Receive(BeNil()))
	})

	It("returns immediately when no vault is configured", func() {
		// An empty vaultDir is the ordinary case for a host with no vault: there
		// is nothing to watch, and joining "" with the task directory would
		// resolve a relative path rather than the vault's own.
		index := pkg.NewTaskIndex(ctx, "", clock)
		Expect(pkg.NewTaskIndexWatcher(index, "").Run(ctx)).To(Succeed())
	})

	It("keeps serving when the tasks directory cannot be watched", func() {
		// The vault exists but `25 Tasks/` does not — the state of a vault that
		// has not been populated yet. watcher.Add fails, and the watcher must
		// return nil rather than an error: a service that cannot watch its vault
		// still serves, and the index's backstop window keeps it converging.
		vault := GinkgoT().TempDir()
		index := pkg.NewTaskIndex(ctx, vault, clock)
		Expect(pkg.NewTaskIndexWatcher(index, vault).Run(ctx)).To(Succeed())
	})

	It("performs no rebuild on the render path", func() {
		vault := GinkgoT().TempDir()
		writeVaultTask(vault, "Steady.md", "---\nclaude_session_id: session-steady\n---\n")
		index := pkg.NewTaskIndex(ctx, vault, clock)

		counter, ok := index.(interface{ RebuildCount() int })
		Expect(ok).To(BeTrue(), "the task index must expose its rebuild count")
		Expect(counter.RebuildCount()).To(Equal(0), "the boot build is not a rebuild")

		// Many renders with the clock unadvanced: the common path is a map hit and
		// rebuilds nothing.
		for i := 0; i < 100; i++ {
			task, ok := index.Lookup("session-steady")
			Expect(ok).To(BeTrue())
			Expect(task.Name).To(Equal("Steady"))
		}
		Expect(counter.RebuildCount()).To(Equal(0),
			"a render inside the backstop window must not rebuild")
	})

	It("does not rebuild a lookup inside the backstop window", func() {
		vault := GinkgoT().TempDir()
		writeVaultTask(vault, "Steady.md", "---\nclaude_session_id: session-steady\n---\n")
		index := pkg.NewTaskIndex(ctx, vault, clock)

		counter, ok := index.(interface{ RebuildCount() int })
		Expect(ok).To(BeTrue(), "the task index must expose its rebuild count")

		// ⚠️ More than the two-second window this change replaces, less than the
		// five-minute backstop. The lower bound is load-bearing: an advance under
		// two seconds would also pass against the old window and prove nothing.
		clock.SetNow(clock.Now().Add(libtime.Duration(3 * 1e9)))

		task, ok := index.Lookup("session-steady")
		Expect(ok).To(BeTrue())
		Expect(task.Name).To(Equal("Steady"))
		Expect(counter.RebuildCount()).To(Equal(0),
			"a lookup inside the backstop window must not rebuild")
	})
})
