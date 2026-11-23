// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

// stubTaskIndex answers Lookup from a fixed map. GoalScope needs nothing else
// from the index, so the other methods are unused and panic-free no-ops.
type stubTaskIndex struct {
	tasks map[string]pkg.Task
}

func (s *stubTaskIndex) Lookup(sessionID string) (pkg.Task, bool) {
	task, ok := s.tasks[sessionID]
	return task, ok
}

func (s *stubTaskIndex) SubjectTitles() []string { return nil }

func (s *stubTaskIndex) ApplyPaths(_ context.Context, _ []string) {}

func (s *stubTaskIndex) Reconcile(_ context.Context) {}

var _ = Describe("session goal scope", func() {
	var dir string

	writeGoal := func(name, sessionStamp string) {
		path := filepath.Join(dir, "24 Goals", name+".md")
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		body := "---\nstatus: in_progress\nclaude_session_id: " + sessionStamp + "\n---\n\n# Success Criteria\n"
		Expect(os.WriteFile(path, []byte(body), 0o644)).To(Succeed())
	}

	writeTopic := func(name, sessionStamp string, goals ...string) {
		path := filepath.Join(dir, "23 Topics", name+".md")
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		body := "---\npage_type: topic\nclaude_session_id: " + sessionStamp + "\n---\n\n## Goals\n\n"
		for _, goal := range goals {
			body += "- [[" + goal + "]] — closes a stage\n"
		}
		body += "\n## Related\n\n- [[NotAMember]]\n"
		Expect(os.WriteFile(path, []byte(body), 0o644)).To(Succeed())
	}

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
	})

	It("TestGoalScopeSessionResolved — separates no-task from task-with-no-goal", func() {
		tasks := &stubTaskIndex{tasks: map[string]pkg.Task{
			"w-no-goal": {Goals: nil},
		}}
		scope := pkg.NewGoalScope(dir, tasks)

		Expect(scope.SessionResolved("w-no-goal")).To(BeTrue())
		Expect(scope.SessionResolved("exit-observer-keeper")).To(BeFalse())
		Expect(scope.SessionResolved("")).To(BeFalse())
		Expect(pkg.NewGoalScope(dir, nil).SessionResolved("w-no-goal")).To(BeFalse())
	})

	It("TestGoalScopeResolvesTheManagersGoalFromItsPage", func() {
		writeGoal("Mine", "mgr-1")
		tasks := &stubTaskIndex{tasks: map[string]pkg.Task{
			"w-mine":    {Goals: []string{"Mine"}},
			"w-foreign": {Goals: []string{"Other"}},
		}}
		scope := pkg.NewGoalScope(dir, tasks)

		goal, foreign, ok := scope.Foreign("mgr-1", "w-mine")
		Expect(ok).To(BeTrue())
		Expect(foreign).To(BeFalse())
		Expect(goal).To(BeEmpty())

		goal, foreign, ok = scope.Foreign("mgr-1", "w-foreign")
		Expect(ok).To(BeTrue())
		Expect(foreign).To(BeTrue())
		Expect(goal).To(Equal("Other"))
	})

	It("TestGoalScopeReadsAQuotedStamp", func() {
		// The vault writes both spellings. A raw comparison under-fires on this
		// one and reads as "this manager has no goal" rather than as an error.
		writeGoal("Mine", `"mgr-1"`)
		tasks := &stubTaskIndex{tasks: map[string]pkg.Task{
			"w-foreign": {Goals: []string{"Other"}},
		}}
		_, foreign, ok := pkg.NewGoalScope(dir, tasks).Foreign("mgr-1", "w-foreign")
		Expect(ok).To(BeTrue())
		Expect(foreign).To(BeTrue())
	})

	It("TestGoalScopeResolvesATopicManagersGoalsFromItsTopicPage", func() {
		// A topic manager is stamped on a topic page, not a goal page. `goalOf`
		// scans only `24 Goals/`, so before this fallback it resolved no goal and
		// `Foreign` failed open for every card the topic manager received — it
		// inherited the whole fleet's traffic.
		writeTopic("Echo Sky Run", "topic-mgr", "Ship One Level", "World 1 Plays")
		writeGoal("Ship One Level", "")
		writeGoal("World 1 Plays", "")
		tasks := &stubTaskIndex{tasks: map[string]pkg.Task{
			"w-member":  {Goals: []string{"World 1 Plays"}},
			"w-foreign": {Goals: []string{"Someone Elses Goal"}},
		}}
		scope := pkg.NewGoalScope(dir, tasks)

		goal, foreign, ok := scope.Foreign("topic-mgr", "w-member")
		Expect(ok).To(BeTrue())
		Expect(foreign).To(BeFalse())
		Expect(goal).To(BeEmpty())

		goal, foreign, ok = scope.Foreign("topic-mgr", "w-foreign")
		Expect(ok).To(BeTrue())
		Expect(foreign).To(BeTrue())
		Expect(goal).To(Equal("Someone Elses Goal"))
	})

	It("TestGoalScopeFiltersNonGoalTitlesOutOfATopicPage", func() {
		// A topic page's `## Goals` section is documented as mixing goals and
		// tasks. Without the goal-file filter those task titles would count as the
		// manager's goals, the set would be non-empty, and the fail-open would never
		// fire — handing the topic manager every card in the fleet.
		writeTopic("Mixed Topic", "topic-mgr", "Some Task", "Another Task")
		writeGoal("A Real Goal", "")
		tasks := &stubTaskIndex{tasks: map[string]pkg.Task{
			"w-foreign": {Goals: []string{"Other"}},
		}}
		_, _, ok := pkg.NewGoalScope(dir, tasks).Foreign("topic-mgr", "w-foreign")
		Expect(ok).To(BeFalse())
	})

	It("TestGoalScopeKeepsFailingOpenWhenATopicPageDeclaresNoGoals", func() {
		writeTopic("Empty Topic", "topic-mgr")
		tasks := &stubTaskIndex{tasks: map[string]pkg.Task{
			"w-foreign": {Goals: []string{"Other"}},
		}}
		_, _, ok := pkg.NewGoalScope(dir, tasks).Foreign("topic-mgr", "w-foreign")
		Expect(ok).To(BeFalse())
	})

	It("TestGoalScopeFailsOpenOnEveryUnresolvableBranch", func() {
		writeGoal("Mine", "mgr-1")
		tasks := &stubTaskIndex{tasks: map[string]pkg.Task{
			"w-nogoal": {},
		}}
		scope := pkg.NewGoalScope(dir, tasks)

		// An unstamped manager.
		_, _, ok := scope.Foreign("nobody", "w-nogoal")
		Expect(ok).To(BeFalse())

		// A session anchoring no task.
		_, _, ok = scope.Foreign("mgr-1", "unknown")
		Expect(ok).To(BeFalse())

		// A task declaring no goal.
		_, _, ok = scope.Foreign("mgr-1", "w-nogoal")
		Expect(ok).To(BeFalse())

		// An unconfigured vault.
		_, _, ok = pkg.NewGoalScope("", tasks).Foreign("mgr-1", "w-nogoal")
		Expect(ok).To(BeFalse())

		// A vault directory that does not exist.
		_, _, ok = pkg.NewGoalScope(filepath.Join(dir, "absent"), tasks).
			Foreign("mgr-1", "w-nogoal")
		Expect(ok).To(BeFalse())
	})
})
