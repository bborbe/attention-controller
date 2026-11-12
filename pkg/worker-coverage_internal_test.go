// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// These specs pin the coverage rule's own shape. They are NOT the parity
// evidence — fixtures encode the same assumption on both sides of the check —
// and that is worker-coverage-parity_test.go, which runs claude-supervisor's
// own grouping over the live fleet.
var _ = Describe("worker coverage", func() {
	const (
		goalTitle  = "Strategy Prototypes Run Unattended in the Cluster"
		otherGoal  = "Every AgentAssigned Record Reaches a Terminal State"
		topicTitle = "Development Pipeline Throughput"
	)
	index := newSubjectIndex([]string{goalTitle, otherGoal, topicTitle})

	DescribeTable("normalizeSessionName",
		func(in, want string) {
			Expect(normalizeSessionName(in)).To(Equal(want))
		},
		Entry("strips the registry's gear marker", "⚙ Fix the Board", "fix the board"),
		Entry("strips stacked status glyphs", "◐ ⚙ Fix the Board", "fix the board"),
		Entry("collapses inner whitespace", "Fix   the\tBoard", "fix the board"),
		Entry("keeps a leading parenthesis", "(MVP) Fix", "(mvp) fix"),
		Entry("folds NFKC compatibility forms", "Ｆｉｘ", "fix"),
	)

	DescribeTable("subjectIndex.subject",
		func(name, want string) {
			Expect(index.subject(name)).To(Equal(want))
		},
		Entry("an exact goal title", goalTitle, goalTitle),
		Entry("case-insensitively", "strategy prototypes run unattended in the cluster", goalTitle),
		Entry("the subject less a trailing Manager — the shape every manager has",
			goalTitle+" Manager", goalTitle),
		Entry("a topic manager", topicTitle+" Manager", topicTitle),
		Entry("a registry-truncated manager name",
			"Strategy Prototypes Run Unattended in the Clu", goalTitle),
		Entry("a gear-marked truncated name", "⚙ Every AgentAssigned Record Reac", otherGoal),
		Entry("a prefix below the length floor", "Strategy", ""),
		Entry("a task-titled worker names no subject", "Fix the Board", ""),
		Entry("the empty name", "", ""),
	)

	It("refuses a prefix shared by two titles rather than guessing", func() {
		ambiguous := newSubjectIndex([]string{
			"Reduce the Resource Footprint of Go Services",
			"Reduce the Resource Footprint of Python Services",
		})
		Expect(ambiguous.subject("Reduce the Resource Footprint of")).To(BeEmpty())
	})

	Describe("workerUncovered", func() {
		const (
			worker  = "worker-session"
			manager = "manager-session"
			topicM  = "topic-manager-session"
		)
		var (
			names    map[string]string
			managers map[string]string
			task     Task
		)
		BeforeEach(func() {
			names = map[string]string{
				worker:  "⚙ Fix the Board",
				manager: goalTitle + " Manager",
				topicM:  topicTitle + " Manager",
			}
			alive := func(context.Context, string) bool { return true }
			managers = liveManagerSubjects(context.Background(), names, index, alive)
			task = Task{
				Name:      "Fix the Board",
				GoalName:  goalTitle,
				Goals:     []string{goalTitle},
				TopicName: topicTitle,
			}
		})

		It("is covered when a live manager names the task's goal", func() {
			Expect(workerUncovered(worker, task, true, names, index, managers)).To(BeFalse())
		})
		It("is covered by a later goal in the task's goals list", func() {
			task.Goals = []string{otherGoal, goalTitle}
			Expect(workerUncovered(worker, task, true, names, index, managers)).To(BeFalse())
		})
		It("is uncovered when the only live manager is the goal's TOPIC manager", func() {
			// fleet-board's manager_on_chain: a topic manager never parents a
			// worker, so the goal's topic is not a covering rung.
			delete(managers, manager)
			Expect(workerUncovered(worker, task, true, names, index, managers)).To(BeTrue())
		})
		It("is uncovered when the goal's manager is not live", func() {
			dead := func(_ context.Context, id string) bool { return id != manager }
			managers = liveManagerSubjects(context.Background(), names, index, dead)
			Expect(workerUncovered(worker, task, true, names, index, managers)).To(BeTrue())
		})
		It("is uncovered when the session anchors no task", func() {
			Expect(workerUncovered(worker, Task{}, false, names, index, managers)).To(BeTrue())
		})
		It("is uncovered when the task declares no existing goal", func() {
			task.Goals = nil
			Expect(workerUncovered(worker, task, true, names, index, managers)).To(BeTrue())
		})
		It("is uncovered for a manager's own card — no manager sits above it", func() {
			Expect(workerUncovered(manager, task, true, names, index, managers)).To(BeTrue())
		})
		It("is covered when the registry is unreadable, so the board is never flooded", func() {
			Expect(
				workerUncovered(worker, task, true, map[string]string{}, index, managers),
			).To(BeFalse())
		})
		It("is covered for an item naming no session", func() {
			Expect(workerUncovered("", task, true, names, index, managers)).To(BeFalse())
		})
	})
})
