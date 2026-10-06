// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/bborbe/run"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

// writeVaultFile writes one file under <vault>/<dir>/. Written as raw text
// rather than through a parser, so the fixture is the *file shape* the vault
// actually holds and a rename in the reader cannot make a fixture agree with
// itself.
func writeVaultFile(vault, dir, name, content string) {
	path := filepath.Join(vault, dir)
	Expect(os.MkdirAll(path, 0o750)).To(BeNil())
	Expect(os.WriteFile(filepath.Join(path, name), []byte(content), 0o600)).To(BeNil())
}

// writeVaultTask writes one task file under <vault>/25 Tasks/, the directory the
// index reads.
func writeVaultTask(vault, name, content string) {
	writeVaultFile(vault, "25 Tasks", name, content)
}

var _ = Describe("TaskIndex", func() {
	var ctx context.Context
	// clock is frozen in BeforeEach so the refresh window is measured from a
	// fixed instant; the refresh specs advance it with SetNow rather than
	// sleeping, per the repo's time-injection rule.
	var clock libtime.CurrentDateTime

	BeforeEach(func() {
		ctx = context.Background()
		clock = libtime.NewCurrentDateTime()
		clock.SetNow(clock.Now())
	})

	// Each entry builds its own vault and returns the directory the index is
	// built from, so the "does not exist" case can point the index at a path
	// nothing ever created.
	DescribeTable("resolves the task recorded for a session",
		func(build func(root string) string, sessionID, wantName, wantPath string, wantOK bool) {
			vault := build(GinkgoT().TempDir())

			task, ok := pkg.NewTaskIndex(ctx, vault, clock).Lookup(sessionID)

			Expect(ok).To(Equal(wantOK))
			Expect(task.Name).To(Equal(wantName))
			Expect(task.Path).To(Equal(wantPath))
		},
		Entry("a file with a valid claude_session_id",
			func(root string) string {
				writeVaultTask(root, "Fix the board.md",
					"---\nclaude_session_id: session-a\nstatus: active\n---\nbody\n")
				return root
			},
			"session-a", "Fix the board", "25 Tasks/Fix the board.md", true),
		Entry("a file with no claude_session_id",
			func(root string) string {
				writeVaultTask(root, "No Session.md", "---\ntitle: No Session\n---\n")
				return root
			},
			"session-a", "", "", false),
		Entry("a file with an empty claude_session_id",
			func(root string) string {
				writeVaultTask(root, "Empty.md", "---\nclaude_session_id:\n---\n")
				return root
			},
			"", "", "", false),
		Entry("a file whose claude_session_id is only whitespace",
			func(root string) string {
				writeVaultTask(root, "Blank.md", "---\nclaude_session_id:    \n---\n")
				return root
			},
			"", "", "", false),
		Entry("a file with no frontmatter block",
			func(root string) string {
				writeVaultTask(root, "Plain.md", "# Plain\n\nclaude_session_id: session-a\n")
				return root
			},
			"session-a", "", "", false),
		Entry("a file whose frontmatter block is never closed",
			func(root string) string {
				writeVaultTask(root, "Unclosed.md", "---\nclaude_session_id: session-a\n")
				return root
			},
			"session-a", "", "", false),
		Entry("a non-markdown file",
			func(root string) string {
				writeVaultTask(root, "Notes.txt", "---\nclaude_session_id: session-a\n---\n")
				return root
			},
			"session-a", "", "", false),
		Entry("two files sharing one id, one still in flight",
			func(root string) string {
				// The in-flight file sorts *first*, so a tie-break that simply
				// took the last file read would pick the completed one.
				writeVaultTask(root, "Newer Task.md",
					"---\nclaude_session_id: session-b\nstatus: active\n---\n")
				writeVaultTask(root, "Older Task.md",
					"---\nclaude_session_id: session-b\nstatus: completed\n---\n")
				return root
			},
			"session-b", "Newer Task", "25 Tasks/Newer Task.md", true),
		Entry("two files sharing one id, both terminal",
			func(root string) string {
				writeVaultTask(root, "Alpha Task.md",
					"---\nclaude_session_id: session-c\nstatus: completed\n---\n")
				writeVaultTask(root, "Beta Task.md",
					"---\nclaude_session_id: session-c\nstatus: aborted\n---\n")
				return root
			},
			"session-c", "Beta Task", "25 Tasks/Beta Task.md", true),
		Entry("a vault directory that does not exist",
			func(root string) string {
				return filepath.Join(root, "does-not-exist")
			},
			"session-a", "", "", false),
	)

	// The goal rung, driven through the same exported constructor. The fixtures
	// write the *file shape* the vault actually holds — raw frontmatter text,
	// never a value marshalled from a struct — so a rename in the reader cannot
	// make a fixture agree with itself.
	DescribeTable(
		"resolves the goal and the topic a task's goals name",
		func(build func(root string) string, sessionID, wantGoalName, wantGoalPath, wantTopicName, wantTopicPath string) {
			vault := build(GinkgoT().TempDir())

			task, _ := pkg.NewTaskIndex(ctx, vault, clock).Lookup(sessionID)

			Expect(task.GoalName).To(Equal(wantGoalName))
			Expect(task.GoalPath).To(Equal(wantGoalPath))
			Expect(task.TopicName).To(Equal(wantTopicName))
			Expect(task.TopicPath).To(Equal(wantTopicPath))
		},
		Entry("a task whose goals list names a goal a topic lists",
			func(root string) string {
				writeVaultTask(
					root,
					"Fix the board.md",
					"---\nclaude_session_id: session-a\ngoals:\n  - \"[[Fix the Board]]\"\n---\nbody\n",
				)
				writeVaultFile(root, "24 Goals", "Fix the Board.md",
					"---\ntitle: Fix the Board\n---\n")
				writeVaultFile(root, "23 Topics", "Attention Board Polish.md",
					"---\ntitle: Attention Board Polish\n---\n\n## Goals\n\n- [[Fix the Board]]\n")
				return root
			},
			"session-a",
			"Fix the Board", "24 Goals/Fix the Board.md",
			"Attention Board Polish", "23 Topics/Attention Board Polish.md"),
		Entry("a task whose goals is the inline empty form",
			func(root string) string {
				writeVaultTask(root, "Empty Goals.md",
					"---\nclaude_session_id: session-a\ngoals: []\n---\n")
				writeVaultFile(root, "24 Goals", "Fix the Board.md",
					"---\ntitle: Fix the Board\n---\n")
				return root
			},
			"session-a", "", "", "", ""),
		Entry("a task whose goals list names two entries",
			func(root string) string {
				// The first is the one rendered; the second must appear in no
				// field, so its goal file and its topic both exist and are both
				// reachable only if the wrong entry won.
				writeVaultTask(
					root,
					"Two Goals.md",
					"---\nclaude_session_id: session-a\ngoals:\n  - \"[[First Goal]]\"\n  - \"[[Second Goal]]\"\n---\n",
				)
				writeVaultFile(root, "24 Goals", "First Goal.md", "---\ntitle: First Goal\n---\n")
				writeVaultFile(root, "24 Goals", "Second Goal.md", "---\ntitle: Second Goal\n---\n")
				writeVaultFile(root, "23 Topics", "First Topic.md",
					"---\ntitle: First Topic\n---\n\n## Goals\n\n- [[First Goal]]\n")
				writeVaultFile(root, "23 Topics", "Second Topic.md",
					"---\ntitle: Second Topic\n---\n\n## Goals\n\n- [[Second Goal]]\n")
				return root
			},
			"session-a",
			"First Goal", "24 Goals/First Goal.md",
			"First Topic", "23 Topics/First Topic.md"),
		Entry("a task whose goals names a title with no file under 24 Goals",
			func(root string) string {
				writeVaultTask(root, "Missing Goal.md",
					"---\nclaude_session_id: session-a\ngoals:\n  - \"[[Missing Goal]]\"\n---\n")
				writeVaultFile(root, "23 Topics", "Missing Topic.md",
					"---\ntitle: Missing Topic\n---\n\n## Goals\n\n- [[Missing Goal]]\n")
				return root
			},
			"session-a", "", "", "", ""),
		Entry("a goal file that no topic lists",
			func(root string) string {
				// The dominant live case: a goal resolves and no topic does.
				writeVaultTask(root, "Lonely Goal.md",
					"---\nclaude_session_id: session-a\ngoals:\n  - \"[[Lonely Goal]]\"\n---\n")
				writeVaultFile(root, "24 Goals", "Lonely Goal.md",
					"---\ntitle: Lonely Goal\n---\n")
				return root
			},
			"session-a",
			"Lonely Goal", "24 Goals/Lonely Goal.md", "", ""),
		Entry("a goal named in a topic page's prose but not under its Goals heading",
			func(root string) string {
				writeVaultTask(root, "Prose Goal.md",
					"---\nclaude_session_id: session-a\ngoals:\n  - \"[[Prose Goal]]\"\n---\n")
				writeVaultFile(root, "24 Goals", "Prose Goal.md",
					"---\ntitle: Prose Goal\n---\n")
				writeVaultFile(
					root,
					"23 Topics",
					"Prose Topic.md",
					"---\ntitle: Prose Topic\n---\n\nWe should work on [[Prose Goal]] soon.\n\n## Goals\n\n- [[Some Other Goal]]\n\n## Notes\n\n- [[Prose Goal]]\n",
				)
				return root
			},
			"session-a",
			"Prose Goal", "24 Goals/Prose Goal.md", "", ""),
		Entry("a topic Goals section listing a title that exists only under 25 Tasks",
			func(root string) string {
				// ⚠️ The existence guard, and the only case that asserts it. The
				// section mixes goals and tasks, so an implementation that trusted
				// it would resolve a task title as a goal.
				writeVaultTask(root, "Shared Title.md", "---\nclaude_session_id: some-other\n---\n")
				writeVaultTask(root, "Goal Carrier.md",
					"---\nclaude_session_id: session-a\ngoals:\n  - \"[[Shared Title]]\"\n---\n")
				writeVaultFile(root, "23 Topics", "Mixer.md",
					"---\ntitle: Mixer\n---\n\n## Goals\n\n- [[Shared Title]]\n")
				return root
			},
			"session-a", "", "", "", ""),
		Entry("a single-quoted goals entry",
			func(root string) string {
				writeVaultTask(root, "Single Quoted.md",
					"---\nclaude_session_id: session-a\ngoals:\n  - '[[Quote Goal]]'\n---\n")
				writeVaultFile(root, "24 Goals", "Quote Goal.md", "---\ntitle: Quote Goal\n---\n")
				writeVaultFile(root, "23 Topics", "Quote Topic.md",
					"---\ntitle: Quote Topic\n---\n\n## Goals\n\n- [[Quote Goal]]\n")
				return root
			},
			"session-a",
			"Quote Goal", "24 Goals/Quote Goal.md",
			"Quote Topic", "23 Topics/Quote Topic.md"),
		Entry("a 4-space-indented goals entry",
			func(root string) string {
				writeVaultTask(root, "Indented.md",
					"---\nclaude_session_id: session-a\ngoals:\n    - \"[[Indent Goal]]\"\n---\n")
				writeVaultFile(root, "24 Goals", "Indent Goal.md",
					"---\ntitle: Indent Goal\n---\n")
				writeVaultFile(root, "23 Topics", "Indent Topic.md",
					"---\ntitle: Indent Topic\n---\n\n## Goals\n\n- [[Indent Goal]]\n")
				return root
			},
			"session-a",
			"Indent Goal", "24 Goals/Indent Goal.md",
			"Indent Topic", "23 Topics/Indent Topic.md"),
		Entry("a goals entry carrying an alias",
			func(root string) string {
				writeVaultTask(
					root,
					"Aliased.md",
					"---\nclaude_session_id: session-a\ngoals:\n  - \"[[Alias Goal|display]]\"\n---\n",
				)
				writeVaultFile(root, "24 Goals", "Alias Goal.md", "---\ntitle: Alias Goal\n---\n")
				writeVaultFile(root, "23 Topics", "Alias Topic.md",
					"---\ntitle: Alias Topic\n---\n\n## Goals\n\n- [[Alias Goal]]\n")
				return root
			},
			"session-a",
			"Alias Goal", "24 Goals/Alias Goal.md",
			"Alias Topic", "23 Topics/Alias Topic.md"),
		Entry("a bare-title goals entry with no wikilink brackets",
			func(root string) string {
				writeVaultTask(root, "Bare Title.md",
					"---\nclaude_session_id: session-a\ngoals:\n  - Bare Goal\n---\n")
				writeVaultFile(root, "24 Goals", "Bare Goal.md", "---\ntitle: Bare Goal\n---\n")
				return root
			},
			"session-a", "", "", "", ""),
		Entry("a topic page carrying no Goals heading",
			func(root string) string {
				writeVaultTask(root, "No Heading.md",
					"---\nclaude_session_id: session-a\ngoals:\n  - \"[[Heading Goal]]\"\n---\n")
				writeVaultFile(root, "24 Goals", "Heading Goal.md",
					"---\ntitle: Heading Goal\n---\n")
				writeVaultFile(root, "23 Topics", "No Heading Topic.md",
					"---\ntitle: No Heading Topic\n---\n\n- [[Heading Goal]]\n")
				return root
			},
			"session-a",
			"Heading Goal", "24 Goals/Heading Goal.md", "", ""),
		Entry("a topic page carrying a Goals subheading instead",
			func(root string) string {
				writeVaultTask(root, "Subheading.md",
					"---\nclaude_session_id: session-a\ngoals:\n  - \"[[Subheading Goal]]\"\n---\n")
				writeVaultFile(root, "24 Goals", "Subheading Goal.md",
					"---\ntitle: Subheading Goal\n---\n")
				writeVaultFile(root, "23 Topics", "Subheading Topic.md",
					"---\ntitle: Subheading Topic\n---\n\n### Goals\n\n- [[Subheading Goal]]\n")
				return root
			},
			"session-a",
			"Subheading Goal", "24 Goals/Subheading Goal.md", "", ""),
		Entry("two topic pages listing one goal, with a subdirectory and a non-markdown file",
			func(root string) string {
				// The first topic found wins — with os.ReadDir's sorted order that
				// is the lexicographically first topic path. The subdirectory and
				// the `.txt` file must be skipped rather than read.
				writeVaultTask(root, "Shared Goal.md",
					"---\nclaude_session_id: session-a\ngoals:\n  - \"[[Shared Goal]]\"\n---\n")
				writeVaultFile(root, "24 Goals", "Shared Goal.md",
					"---\ntitle: Shared Goal\n---\n")
				writeVaultFile(root, "23 Topics", "B Topic.md",
					"---\ntitle: B Topic\n---\n\n## Goals\n\n- [[Shared Goal]]\n")
				writeVaultFile(root, "23 Topics", "A Topic.md",
					"---\ntitle: A Topic\n---\n\n## Goals\n\n- [[Shared Goal]]\n")
				writeVaultFile(root, "23 Topics", "notes.txt", "not markdown\n")
				writeVaultFile(
					root,
					"23 Topics/Sub",
					"nested.md",
					"## Goals\n\n- [[Shared Goal]]\n",
				)
				return root
			},
			"session-a",
			"Shared Goal", "24 Goals/Shared Goal.md",
			"A Topic", "23 Topics/A Topic.md"),
		Entry("a topic directory holding an unreadable .md entry",
			func(root string) string {
				writeVaultTask(root, "Broken Link.md",
					"---\nclaude_session_id: session-a\ngoals:\n  - \"[[Broken Goal]]\"\n---\n")
				writeVaultFile(root, "24 Goals", "Broken Goal.md",
					"---\ntitle: Broken Goal\n---\n")
				writeVaultFile(root, "23 Topics", "Intact Topic.md",
					"---\ntitle: Intact Topic\n---\n\n## Goals\n\n- [[Broken Goal]]\n")
				Expect(os.Symlink(
					"does-not-exist.md",
					filepath.Join(root, "23 Topics", "Dangling.md"),
				)).To(BeNil())
				return root
			},
			"session-a",
			"Broken Goal", "24 Goals/Broken Goal.md",
			"Intact Topic", "23 Topics/Intact Topic.md"),
		Entry("a vault with no 24 Goals directory at all",
			func(root string) string {
				writeVaultTask(root, "No Goals Dir.md",
					"---\nclaude_session_id: session-a\ngoals:\n  - \"[[Any Goal]]\"\n---\n")
				return root
			},
			"session-a", "", "", "", ""),
		Entry("a task using the singular goal key",
			func(root string) string {
				writeVaultTask(root, "Singular.md",
					"---\nclaude_session_id: session-a\ngoal:\n  - \"[[Fix the Board]]\"\n---\n")
				writeVaultFile(root, "24 Goals", "Fix the Board.md",
					"---\ntitle: Fix the Board\n---\n")
				writeVaultFile(root, "23 Topics", "Attention Board Polish.md",
					"---\ntitle: Attention Board Polish\n---\n\n## Goals\n\n- [[Fix the Board]]\n")
				return root
			},
			"session-a", "", "", "", ""),
	)

	It("resolves no goal and no topic when the index build is cancelled", func() {
		vault := GinkgoT().TempDir()
		writeVaultTask(vault, "Cancelled.md",
			"---\nclaude_session_id: session-a\ngoals:\n  - \"[[Cancelled Goal]]\"\n---\n")
		writeVaultFile(vault, "24 Goals", "Cancelled Goal.md",
			"---\ntitle: Cancelled Goal\n---\n")
		writeVaultFile(vault, "23 Topics", "Cancelled Topic.md",
			"---\ntitle: Cancelled Topic\n---\n\n## Goals\n\n- [[Cancelled Goal]]\n")
		cancelled, cancel := context.WithCancel(ctx)
		cancel()

		task, _ := pkg.NewTaskIndex(cancelled, vault, clock).Lookup("session-a")

		// A cancelled build holds what it read before cancellation and no more,
		// so the goal rung — which reads before the task walk — contributes
		// nothing rather than failing.
		Expect(task.GoalName).To(BeEmpty())
		Expect(task.TopicName).To(BeEmpty())
	})
})

// The index used to read the vault once, at construction, so every task created
// afterwards was invisible for the process's lifetime. These specs pin the
// bounded re-read that fixes it: a task file written after the build resolves on
// a later lookup, and only once the injected clock has passed the window.
var _ = Describe("TaskIndex refresh", func() {
	var ctx context.Context
	var clock libtime.CurrentDateTime

	BeforeEach(func() {
		ctx = context.Background()
		// Frozen, so the refresh window is measured from a fixed instant and the
		// specs advance it with SetNow rather than sleeping.
		clock = libtime.NewCurrentDateTime()
		clock.SetNow(clock.Now())
	})

	// advanceClock moves the injected clock past the five-minute backstop window,
	// so the next Lookup rebuilds the index from the vault.
	advanceClock := func() {
		clock.SetNow(clock.Now().Add(libtime.Duration(6 * time.Minute)))
	}

	It("resolves a task written after the index was built, once the window lapses", func() {
		vault := GinkgoT().TempDir()
		index := pkg.NewTaskIndex(ctx, vault, clock)

		// The vault is empty at construction, so the session resolves nothing.
		_, ok := index.Lookup("session-late")
		Expect(ok).To(BeFalse())

		writeVaultTask(vault, "Late Task.md",
			"---\nclaude_session_id: session-late\n---\n")

		// ⚠️ Inside the window the new file is not observed. This is the assertion
		// that makes the bound real rather than a no-op: a Lookup that re-read the
		// vault unconditionally would resolve here and fail this line.
		_, ok = index.Lookup("session-late")
		Expect(ok).To(BeFalse())

		advanceClock()
		task, ok := index.Lookup("session-late")
		Expect(ok).To(BeTrue())
		Expect(task.Name).To(Equal("Late Task"))
		Expect(task.Path).To(Equal("25 Tasks/Late Task.md"))
	})

	It("resolves the goal and the topic for a task written after the build", func() {
		// ⚠️ The whole reason the refresh re-runs all three index steps rather than
		// the task walk alone: the goal rung and the topic rung are built before the
		// walk, and a task naming a goal that did not exist at construction would
		// resolve no goal if only the walk were re-run.
		vault := GinkgoT().TempDir()
		index := pkg.NewTaskIndex(ctx, vault, clock)

		writeVaultTask(vault, "New Goal Task.md",
			"---\nclaude_session_id: session-late-goal\ngoals:\n  - \"[[New Goal]]\"\n---\n")
		writeVaultFile(vault, "24 Goals", "New Goal.md", "---\ntitle: New Goal\n---\n")
		writeVaultFile(vault, "23 Topics", "New Topic.md",
			"---\ntitle: New Topic\n---\n\n## Goals\n\n- [[New Goal]]\n")

		advanceClock()
		task, ok := index.Lookup("session-late-goal")
		Expect(ok).To(BeTrue())
		Expect(task.GoalName).To(Equal("New Goal"))
		Expect(task.GoalPath).To(Equal("24 Goals/New Goal.md"))
		Expect(task.TopicName).To(Equal("New Topic"))
		Expect(task.TopicPath).To(Equal("23 Topics/New Topic.md"))
	})

	It("yields no entry and no error when the tasks directory is unreadable", func() {
		// The soft-failure rule survives the refresh: a vault with no `25 Tasks/`
		// resolves nothing rather than failing, and a rebuild that reads no tasks
		// installs an empty index rather than returning an error.
		vault := GinkgoT().TempDir()
		index := pkg.NewTaskIndex(ctx, vault, clock)

		advanceClock()
		task, ok := index.Lookup("session-none")
		Expect(ok).To(BeFalse())
		Expect(task.Name).To(BeEmpty())
	})

	It("keeps serving earlier entries when a refresh is cancelled", func() {
		vault := GinkgoT().TempDir()
		writeVaultTask(vault, "Kept.md", "---\nclaude_session_id: session-kept\n---\n")
		buildCtx, cancel := context.WithCancel(ctx)
		index := pkg.NewTaskIndex(buildCtx, vault, clock)

		task, ok := index.Lookup("session-kept")
		Expect(ok).To(BeTrue())
		Expect(task.Name).To(Equal("Kept"))

		// The build context is cancelled and the vault changes before the window
		// lapses. The refresh the next Lookup triggers stops early, and its partial
		// index must be discarded rather than installed — so the earlier entry
		// keeps resolving instead of being replaced by an empty index.
		cancel()
		writeVaultTask(vault, "New.md", "---\nclaude_session_id: session-new\n---\n")
		advanceClock()

		task, ok = index.Lookup("session-kept")
		Expect(ok).To(BeTrue())
		Expect(task.Name).To(Equal("Kept"))
		_, ok = index.Lookup("session-new")
		Expect(ok).To(BeFalse())
	})

	It("keeps serving earlier entries when a refresh cannot read the tasks directory", func() {
		// ⚠️ The data-loss window this spec closes. The build fails soft on I/O, so
		// an unreadable `25 Tasks/` yields no entries rather than an error — and
		// before the read report existed, that empty rebuild was installed anyway,
		// replacing a fully resolved index with an empty one. The directory is
		// removed rather than chmod 000: absence is how the repo's other specs
		// produce an unreadable `25 Tasks/`, and a permission bit is not enforced
		// when the suite runs as root.
		vault := GinkgoT().TempDir()
		writeVaultTask(vault, "Kept.md", "---\nclaude_session_id: session-kept\n---\n")
		index := pkg.NewTaskIndex(ctx, vault, clock)

		task, ok := index.Lookup("session-kept")
		Expect(ok).To(BeTrue())
		Expect(task.Name).To(Equal("Kept"))

		// The tasks directory is gone before the window lapses, so the next
		// Lookup's rebuild reads no tasks. Its result must be discarded rather
		// than installed — the earlier entry keeps resolving instead of being
		// replaced by an empty index.
		Expect(os.RemoveAll(filepath.Join(vault, "25 Tasks"))).To(Succeed())
		advanceClock()

		task, ok = index.Lookup("session-kept")
		Expect(ok).To(BeTrue(),
			"a failed task walk must not wipe the previously resolved index")
		Expect(task.Name).To(Equal("Kept"))
		Expect(task.Path).To(Equal("25 Tasks/Kept.md"))
	})

	It("does not race when lookups run against a refresh", func() {
		// ⚠️ make precommit runs with -race=false, so a data race between a
		// refresh's map swap and a concurrent Lookup would not be reported here.
		// This case drives many lookups through one index while the window has
		// already lapsed, so the winner's rebuild and its swap race the reads — the
		// RWMutex guard is exercised under contention rather than only serially.
		vault := GinkgoT().TempDir()
		writeVaultTask(vault, "Conc.md", "---\nclaude_session_id: session-conc\n---\n")
		index := pkg.NewTaskIndex(ctx, vault, clock)

		// Lapse the window once before the fan-out, so every goroutine's first
		// lookup finds the index stale and observes the same lapsed window. Only
		// the first to claim the token rebuilds; the rest are served the index
		// already installed.
		advanceClock()

		// Results are collected per goroutine and asserted after the fan-out:
		// Ginkgo assertions are not safe to make from the runner's goroutines.
		// Each goroutine writes its own slot, so the slice needs no lock.
		const workers = 8
		names := make([]string, workers)
		oks := make([]bool, workers)

		funcs := make([]run.Func, 0, workers)
		for i := 0; i < workers; i++ {
			funcs = append(funcs, func(ctx context.Context) error {
				task, ok := index.Lookup("session-conc")
				names[i] = task.Name
				oks[i] = ok
				return nil
			})
		}
		Expect(run.CancelOnFirstErrorWait(ctx, funcs...)).To(BeNil())
		for i := 0; i < workers; i++ {
			Expect(oks[i]).To(BeTrue())
			Expect(names[i]).To(Equal("Conc"))
		}
	})

	It("runs exactly one rebuild when concurrent lookups observe a lapsed window", func() {
		// ⚠️ The guard the render loop depends on. Lookup is reached once per item
		// from the resolver's render loop, so when the window lapses every
		// concurrent render would otherwise start its own full-vault rebuild —
		// over 8,000 task files read synchronously inside each request. The
		// staleness check and the token claim happen in ONE critical section, so a
		// burst of lookups that all observe the lapsed window triggers exactly one
		// rebuild: the winner rebuilds synchronously, and the rest are served the
		// index already installed rather than queueing behind the read.
		//
		// ⚠️ The count is read off the index directly, not through the injected
		// clock: both the staleness check and the install read the clock and the
		// interleaving is nondeterministic, so counting clock reads would be flaky.
		vault := GinkgoT().TempDir()
		writeVaultTask(vault, "Conc.md", "---\nclaude_session_id: session-conc\n---\n")
		index := pkg.NewTaskIndex(ctx, vault, clock)

		counter, ok := index.(interface{ RefreshCount() int })
		Expect(ok).To(BeTrue(), "the task index must expose its refresh count")
		Expect(counter.RefreshCount()).To(Equal(0), "the boot build is not a rebuild")

		// Written after the boot build, so a lookup that resolves it proves the
		// winner's rebuild was installed rather than merely started.
		writeVaultTask(vault, "Late.md", "---\nclaude_session_id: session-late\n---\n")

		// Lapse the window once before the fan-out, so every goroutine's first
		// lookup observes the same lapsed window.
		advanceClock()

		// Results are collected per goroutine and asserted after the fan-out:
		// Ginkgo assertions are not safe to make from the runner's goroutines.
		const workers = 8
		oks := make([]bool, workers)
		funcs := make([]run.Func, 0, workers)
		for i := 0; i < workers; i++ {
			funcs = append(funcs, func(ctx context.Context) error {
				_, ok := index.Lookup("session-conc")
				oks[i] = ok
				return nil
			})
		}
		Expect(run.CancelOnFirstErrorWait(ctx, funcs...)).To(BeNil())

		Expect(counter.RefreshCount()).To(Equal(1),
			"concurrent lookups past the window started more than one rebuild")
		for i := 0; i < workers; i++ {
			Expect(oks[i]).To(BeTrue(),
				"a lookup served no index while a rebuild was in flight")
		}

		// The winner's rebuild is installed, so a task written after the boot build
		// resolves on a later lookup.
		task, ok := index.Lookup("session-late")
		Expect(ok).To(BeTrue())
		Expect(task.Name).To(Equal("Late"))
		Expect(counter.RefreshCount()).To(Equal(1),
			"a later lookup inside the fresh window must not rebuild again")
	})
})
