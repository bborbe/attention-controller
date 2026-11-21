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

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
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
