// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// These specs pin the ownership contract a task page's two session fields carry.
//
// A page records its `claude_session_id` stamp AND a `metrics_sessions` entry
// for every work-on that ran against it. The stamp is the last session to own
// the page; a session that ran work-on while another owner was live is recorded
// only in metrics_sessions. Reading the stamp alone left that producer with no
// task, so its cards resolved to no owner — the defect this change closes.
var _ = Describe("taskSessionIDs", func() {
	It("returns the stamp when the page records no metrics", func() {
		Expect(taskSessionIDs([]byte(
			"---\nclaude_session_id: session-stamp\n---\n",
		))).To(Equal([]string{"session-stamp"}))
	})

	It("returns a session recorded only in metrics_sessions", func() {
		Expect(taskSessionIDs([]byte(
			"---\n" +
				"claude_session_id: session-stamp\n" +
				"metrics_sessions:\n" +
				"    - session_id: session-worker\n" +
				"      started_at: \"2026-11-25T17:03:49+01:00\"\n" +
				"---\n",
		))).To(Equal([]string{"session-stamp", "session-worker"}))
	})

	It("de-duplicates a stamp that also appears in metrics_sessions, stamp first", func() {
		Expect(taskSessionIDs([]byte(
			"---\n" +
				"metrics_sessions:\n" +
				"    - session_id: session-a\n" +
				"      started_at: \"2026-11-25T17:03:49+01:00\"\n" +
				"    - session_id: session-b\n" +
				"      started_at: \"2026-11-25T18:02:49+01:00\"\n" +
				"claude_session_id: session-a\n" +
				"---\n",
		))).To(Equal([]string{"session-a", "session-b"}))
	})

	It("reads quoted ids, a bare item with an indented id, and stops at the next key", func() {
		Expect(taskSessionIDs([]byte(
			"---\n" +
				"claude_session_id: \"session-stamp\"\n" +
				"metrics_sessions:\n" +
				"    - session_id: \"session-quoted\"\n" +
				"    -\n" +
				"      session_id: session-indented\n" +
				"    - session_id: session-commented # note\n" +
				"goals:\n" +
				"    - '[[G]]'\n" +
				"---\n",
		))).To(Equal([]string{"session-stamp", "session-quoted", "session-indented", "session-commented"}))
	})

	It("returns nothing for the inline empty form", func() {
		Expect(taskSessionIDs([]byte("---\nmetrics_sessions: []\n---\n"))).To(BeEmpty())
	})

	It("returns nothing for a page that records no session", func() {
		Expect(taskSessionIDs([]byte("---\nstatus: in_progress\n---\n"))).To(BeEmpty())
	})
})

var _ = Describe("TaskIndex session owners", func() {
	var ctx context.Context
	var vault string
	var tasksDir string
	var clock libtime.CurrentDateTime

	writePage := func(body string) {
		Expect(os.WriteFile(
			filepath.Join(tasksDir, "Claimed Page.md"),
			[]byte(body),
			0o600,
		)).To(Succeed())
	}

	BeforeEach(func() {
		ctx = context.Background()
		clock = libtime.NewCurrentDateTime()
		clock.SetNow(clock.Now())

		vault = GinkgoT().TempDir()
		tasksDir = filepath.Join(vault, "25 Tasks")
		Expect(os.MkdirAll(tasksDir, 0o750)).To(Succeed())
	})

	It("resolves a session recorded only in metrics_sessions to its page", func() {
		writePage(
			"---\n" +
				"claude_session_id: session-stamp\n" +
				"metrics_sessions:\n" +
				"    - session_id: session-worker\n" +
				"      started_at: \"2026-11-25T17:03:49+01:00\"\n" +
				"---\n",
		)

		index := NewTaskIndex(ctx, vault, clock)

		task, ok := index.Lookup("session-worker")
		Expect(ok).To(BeTrue(),
			"a session the page records in metrics_sessions must resolve to that page")
		Expect(task.Name).To(Equal("Claimed Page"))
		Expect(task.Path).To(Equal("25 Tasks/Claimed Page.md"))

		stamped, ok := index.Lookup("session-stamp")
		Expect(ok).To(BeTrue(), "the stamp must keep resolving to its own page")
		Expect(stamped.Name).To(Equal("Claimed Page"))
	})

	It("drops a metrics-only session when a rewrite no longer records it", func() {
		writePage(
			"---\n" +
				"claude_session_id: session-stamp\n" +
				"metrics_sessions:\n" +
				"    - session_id: session-worker\n" +
				"---\n",
		)
		index := NewTaskIndex(ctx, vault, clock)
		_, ok := index.Lookup("session-worker")
		Expect(ok).To(BeTrue())

		writePage("---\nclaude_session_id: session-stamp\n---\n")
		index.ApplyPaths(ctx, []string{"25 Tasks/Claimed Page.md"})

		_, stale := index.Lookup("session-worker")
		Expect(stale).To(BeFalse(),
			"a session removed from metrics_sessions must not keep resolving")

		_, stamped := index.Lookup("session-stamp")
		Expect(stamped).To(BeTrue(), "the stamp must survive the rewrite")
	})

	It("drops every session when the page is deleted", func() {
		writePage(
			"---\n" +
				"claude_session_id: session-stamp\n" +
				"metrics_sessions:\n" +
				"    - session_id: session-worker\n" +
				"---\n",
		)
		index := NewTaskIndex(ctx, vault, clock)

		Expect(os.Remove(filepath.Join(tasksDir, "Claimed Page.md"))).To(Succeed())
		index.ApplyPaths(ctx, []string{"25 Tasks/Claimed Page.md"})

		_, stamped := index.Lookup("session-stamp")
		Expect(stamped).To(BeFalse())
		_, worker := index.Lookup("session-worker")
		Expect(worker).To(BeFalse(), "a deleted page must not keep any of its sessions")
	})

	It("prefers the page that stamps a session over one that only lists it", func() {
		// "A Stamp.md" sorts before "B Mention.md" and both are in flight, so
		// only the stamp rank separates them: without it the later path wins and
		// the session's own page loses its mapping.
		Expect(os.WriteFile(
			filepath.Join(tasksDir, "A Stamp.md"),
			[]byte("---\nclaude_session_id: session-shared\nstatus: in_progress\n---\n"),
			0o600,
		)).To(Succeed())
		Expect(os.WriteFile(
			filepath.Join(tasksDir, "B Mention.md"),
			[]byte("---\nclaude_session_id: session-other\nstatus: in_progress\n"+
				"metrics_sessions:\n    - session_id: session-shared\n---\n"),
			0o600,
		)).To(Succeed())

		index := NewTaskIndex(ctx, vault, clock)

		task, ok := index.Lookup("session-shared")
		Expect(ok).To(BeTrue())
		Expect(task.Name).To(Equal("A Stamp"),
			"the page that stamps the session must outrank one that only lists it")
	})

	It("applies the stamp rank to a quoted claude_session_id", func() {
		// The vault writes both `claude_session_id: abc` and `"abc"`. A quoted
		// stamp must normalise to the same id the metrics entry carries, or the
		// stamp rank silently never applies.
		Expect(os.WriteFile(
			filepath.Join(tasksDir, "A Stamp.md"),
			[]byte("---\nclaude_session_id: \"session-shared\"\nstatus: in_progress\n---\n"),
			0o600,
		)).To(Succeed())
		Expect(os.WriteFile(
			filepath.Join(tasksDir, "B Mention.md"),
			[]byte("---\nclaude_session_id: session-other\nstatus: in_progress\n"+
				"metrics_sessions:\n    - session_id: session-shared\n---\n"),
			0o600,
		)).To(Succeed())

		index := NewTaskIndex(ctx, vault, clock)

		task, ok := index.Lookup("session-shared")
		Expect(ok).To(BeTrue())
		Expect(task.Name).To(Equal("A Stamp"),
			"a quoted stamp must normalise and still outrank a metrics-only mention")
	})

	It("keeps the in-flight page over a terminal page that stamps the session", func() {
		// Terminal-first is the older rule and still wins: the session is
		// anchored to the page it is still working, even when a completed page
		// carries its stamp.
		Expect(os.WriteFile(
			filepath.Join(tasksDir, "A Done Stamp.md"),
			[]byte("---\nclaude_session_id: session-shared\nstatus: completed\n---\n"),
			0o600,
		)).To(Succeed())
		Expect(os.WriteFile(
			filepath.Join(tasksDir, "B Live Mention.md"),
			[]byte("---\nclaude_session_id: session-other\nstatus: in_progress\n"+
				"metrics_sessions:\n    - session_id: session-shared\n---\n"),
			0o600,
		)).To(Succeed())

		index := NewTaskIndex(ctx, vault, clock)

		task, ok := index.Lookup("session-shared")
		Expect(ok).To(BeTrue())
		Expect(task.Name).To(Equal("B Live Mention"),
			"an in-flight page outranks a terminal one before the stamp rank is consulted")
	})

	It("reverts a shared session to its remaining page when the stamped page is deleted", func() {
		Expect(os.WriteFile(
			filepath.Join(tasksDir, "A Stamp.md"),
			[]byte("---\nclaude_session_id: session-shared\nstatus: in_progress\n---\n"),
			0o600,
		)).To(Succeed())
		Expect(os.WriteFile(
			filepath.Join(tasksDir, "B Mention.md"),
			[]byte("---\nclaude_session_id: session-other\nstatus: in_progress\n"+
				"metrics_sessions:\n    - session_id: session-shared\n---\n"),
			0o600,
		)).To(Succeed())

		index := NewTaskIndex(ctx, vault, clock)
		first, ok := index.Lookup("session-shared")
		Expect(ok).To(BeTrue())
		Expect(first.Name).To(Equal("A Stamp"))

		Expect(os.Remove(filepath.Join(tasksDir, "A Stamp.md"))).To(Succeed())
		index.ApplyPaths(ctx, []string{"25 Tasks/A Stamp.md"})

		second, ok := index.Lookup("session-shared")
		Expect(ok).To(BeTrue(),
			"a session shared by two pages must keep resolving while one remains")
		Expect(second.Name).To(Equal("B Mention"),
			"with the stamped page gone, the remaining mention page takes over")
	})
})
