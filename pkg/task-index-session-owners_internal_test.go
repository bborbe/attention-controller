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
})
