// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// These specs pin the incremental path's own contract, which the external specs
// cannot reach: the bound is a COUNT of file opens and bytes read, and the
// reader that produces it is an unexported seam. Counting is the only way to
// assert the claim — re-deriving the cost from the source would be restating
// the implementation rather than testing it.
//
// It lives in `package pkg` because the seam is unexported. Both packages
// compile into the same test binary, so these specs register with the suite the
// rest of the package runs under.
var _ = Describe("TaskIndex incremental apply", func() {
	var ctx context.Context
	var vault string
	var tasksDir string
	var clock libtime.CurrentDateTime

	// countTaskFileReads installs a counter over the package's task-file reader
	// and returns a function reporting the opens and bytes accumulated so far.
	// It must be called AFTER construction: the boot walk reads through the same
	// seam, and counting it would attribute 200 filler files to the one change
	// under test.
	countTaskFileReads := func() func() (int, int) {
		var mu sync.Mutex
		var opens, bytes int
		original := taskFileReader
		DeferCleanup(func() { taskFileReader = original })
		taskFileReader = func(root *os.Root, name string) ([]byte, error) {
			content, err := original(root, name)
			if err == nil {
				mu.Lock()
				opens++
				bytes += len(content)
				mu.Unlock()
			}
			return content, err
		}
		return func() (int, int) {
			mu.Lock()
			defer mu.Unlock()
			return opens, bytes
		}
	}

	writeTask := func(name, sessionID string) {
		Expect(os.WriteFile(
			filepath.Join(tasksDir, name),
			[]byte(fmt.Sprintf("---\nclaude_session_id: %s\n---\n", sessionID)),
			0o600,
		)).To(Succeed())
	}

	rebuildCount := func(index TaskIndex) int {
		counter, ok := index.(interface{ RebuildCount() int })
		Expect(ok).To(BeTrue(), "the task index must expose its rebuild count")
		return counter.RebuildCount()
	}

	BeforeEach(func() {
		ctx = context.Background()
		// Frozen, so the backstop window is measured from a fixed instant and no
		// lookup in these specs reaches it by accident.
		clock = libtime.NewCurrentDateTime()
		clock.SetNow(clock.Now())

		vault = GinkgoT().TempDir()
		tasksDir = filepath.Join(vault, "25 Tasks")
		Expect(os.MkdirAll(tasksDir, 0o750)).To(Succeed())

		// A populated vault, so a wholesale read would be visible in the count
		// rather than merely absent from a two-file fixture.
		for i := 0; i < 200; i++ {
			writeTask(fmt.Sprintf("Filler %03d.md", i), "session-filler")
		}
	})

	It("reads one file, not the vault, for one changed task file", func() {
		index := NewTaskIndex(ctx, vault, clock)
		reads := countTaskFileReads()

		writeTask("Changed.md", "session-changed")
		index.ApplyPaths(ctx, []string{"25 Tasks/Changed.md"})

		opens, bytes := reads()
		// ⚠️ The bound is the criterion, and the filler count is what makes it
		// discriminating: a wholesale read answers this with 201, not 1.
		Expect(opens).To(Equal(1),
			"one changed file must cost one file open, against 201 for a whole-vault read")
		Expect(bytes).To(BeNumerically("<=", 200*1024),
			"one changed file must read at most 200 KB")

		task, ok := index.Lookup("session-changed")
		Expect(ok).To(BeTrue())
		Expect(task.Name).To(Equal("Changed"))
		Expect(task.Path).To(Equal("25 Tasks/Changed.md"))

		Expect(rebuildCount(index)).To(Equal(0),
			"an incremental apply is not a rebuild")
	})

	It("reads nothing for a path that is not a task file", func() {
		index := NewTaskIndex(ctx, vault, clock)
		reads := countTaskFileReads()

		index.ApplyPaths(ctx, []string{
			"25 Tasks/nested/Deep.md",
			"25 Tasks/Notes.txt",
			"24 Goals/A goal.md",
			"",
		})

		opens, _ := reads()
		Expect(opens).To(Equal(0), "only a `.md` directly under the task dir is a task file")
	})

	It("drops the previous session when a task file's session id changes", func() {
		writeTask("Anchored.md", "session-before")
		index := NewTaskIndex(ctx, vault, clock)

		_, ok := index.Lookup("session-before")
		Expect(ok).To(BeTrue())

		writeTask("Anchored.md", "session-after")
		index.ApplyPaths(ctx, []string{"25 Tasks/Anchored.md"})

		_, stale := index.Lookup("session-before")
		Expect(stale).To(BeFalse(),
			"the old session must not keep resolving to a file that no longer names it")

		task, ok := index.Lookup("session-after")
		Expect(ok).To(BeTrue())
		Expect(task.Name).To(Equal("Anchored"))
	})

	It("drops the entry when the task file is deleted", func() {
		writeTask("Doomed.md", "session-doomed")
		index := NewTaskIndex(ctx, vault, clock)

		_, ok := index.Lookup("session-doomed")
		Expect(ok).To(BeTrue())

		Expect(os.Remove(filepath.Join(tasksDir, "Doomed.md"))).To(Succeed())
		index.ApplyPaths(ctx, []string{"25 Tasks/Doomed.md"})

		_, ok = index.Lookup("session-doomed")
		Expect(ok).To(BeFalse(), "a deleted task file must not keep resolving")
	})

	It("agrees with a full walk on the tie-break between files sharing a session", func() {
		// ⚠️ Two files, one session, one of them terminal: the boot walk picks
		// the in-flight file. The incremental path must reach the same answer
		// from the candidate set alone, without re-reading the other file.
		Expect(os.WriteFile(
			filepath.Join(tasksDir, "A In Flight.md"),
			[]byte("---\nclaude_session_id: session-shared\nstatus: in_progress\n---\n"),
			0o600,
		)).To(Succeed())
		Expect(os.WriteFile(
			filepath.Join(tasksDir, "B Done.md"),
			[]byte("---\nclaude_session_id: session-shared\nstatus: completed\n---\n"),
			0o600,
		)).To(Succeed())
		index := NewTaskIndex(ctx, vault, clock)

		task, ok := index.Lookup("session-shared")
		Expect(ok).To(BeTrue())
		Expect(task.Name).To(Equal("A In Flight"))

		// Touching the terminal file must not move the winner onto it.
		Expect(os.WriteFile(
			filepath.Join(tasksDir, "B Done.md"),
			[]byte("---\nclaude_session_id: session-shared\nstatus: completed\n# edited\n---\n"),
			0o600,
		)).To(Succeed())
		index.ApplyPaths(ctx, []string{"25 Tasks/B Done.md"})

		task, ok = index.Lookup("session-shared")
		Expect(ok).To(BeTrue())
		Expect(task.Name).To(Equal("A In Flight"),
			"an in-flight file outranks a terminal one whatever the update order")

		// Once both are terminal, the lexicographically last path wins — the
		// same rule the boot walk applies.
		Expect(os.WriteFile(
			filepath.Join(tasksDir, "A In Flight.md"),
			[]byte("---\nclaude_session_id: session-shared\nstatus: completed\n---\n"),
			0o600,
		)).To(Succeed())
		index.ApplyPaths(ctx, []string{"25 Tasks/A In Flight.md"})

		task, ok = index.Lookup("session-shared")
		Expect(ok).To(BeTrue())
		Expect(task.Name).To(Equal("B Done"),
			"with every candidate terminal the lexicographically last path wins")
	})

	It("reconciles without re-reading unchanged task files", func() {
		index := NewTaskIndex(ctx, vault, clock)
		reads := countTaskFileReads()

		index.Reconcile(ctx)

		opens, _ := reads()
		Expect(opens).To(Equal(0),
			"a reconcile over an unchanged vault must read no task file at all")

		// ⚠️ And it must still find a change no event named — the case the whole
		// safety net exists for.
		writeTask("Missed.md", "session-missed")
		index.Reconcile(ctx)

		opens, _ = reads()
		Expect(opens).To(Equal(1),
			"a reconcile reads the one file whose stamp moved and nothing else")

		task, ok := index.Lookup("session-missed")
		Expect(ok).To(BeTrue())
		Expect(task.Name).To(Equal("Missed"))
	})

	It("re-resolves a reused entry's goal when the goal rung gains the file", func() {
		// A task naming a goal that does not exist yet resolves no goal; once
		// the goal file lands, a reconcile must resolve it WITHOUT re-reading the
		// task file, which is unchanged. This is the one field on a reused entry
		// that depends on something outside the task file.
		Expect(os.WriteFile(
			filepath.Join(tasksDir, "Points At Goal.md"),
			[]byte("---\nclaude_session_id: session-goal\ngoals:\n    - '[[Late Goal]]'\n---\n"),
			0o600,
		)).To(Succeed())
		index := NewTaskIndex(ctx, vault, clock)

		task, ok := index.Lookup("session-goal")
		Expect(ok).To(BeTrue())
		Expect(task.GoalName).To(BeEmpty(), "the goal file does not exist yet")

		goalsDir := filepath.Join(vault, "24 Goals")
		Expect(os.MkdirAll(goalsDir, 0o750)).To(Succeed())
		Expect(os.WriteFile(
			filepath.Join(goalsDir, "Late Goal.md"),
			[]byte("---\npage_type: goal\n---\n"),
			0o600,
		)).To(Succeed())

		index.Reconcile(ctx)

		task, ok = index.Lookup("session-goal")
		Expect(ok).To(BeTrue())
		Expect(task.GoalName).To(Equal("Late Goal"),
			"a reconcile must re-resolve the goal rung for a task file it did not re-read")
		Expect(task.GoalPath).To(Equal("24 Goals/Late Goal.md"))
	})
})
