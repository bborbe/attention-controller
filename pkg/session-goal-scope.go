// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/golang/glog"
)

// GoalScope answers one question the spawn edge cannot: does this session's work
// belong to a goal other than the one the manager serves?
//
// The spawn edge answers "who opened this" from LIVE stores, and every hop of it
// fails open, so the answer for one gate depended on the health of a manager that
// had nothing to do with it. Measured 2026-10-29: goal manager `49b471f1` was
// handed five gates raised by five sessions in four other goals in one afternoon.
// The vault answers "whose work is this" instead, and the stamps outlive the
// manager that wrote them.
//
// ⚠️ **It is a SECOND, INDEPENDENT input, never a replacement for the spawn
// edge.** The caller keeps the spawn edge's answer for every case this cannot
// resolve.
type GoalScope interface {
	// Foreign reports the goal that owns session when that goal is not the one
	// managerSession serves.
	//
	// ok is false when the question cannot be answered at all — an unconfigured
	// or unreadable vault, a manager session stamped on no goal page, a session
	// that anchors no task, or a task that declares no goal. ⚠️ **Every one of
	// those keeps the gate**: an unresolvable owner is not a reason to silence
	// it, which is the fail-open direction every other hop takes.
	Foreign(managerSession, session string) (goal string, foreign bool, ok bool)

	// ManagerResolved reports whether the vault names a goal for this manager
	// session.
	//
	// ⚠️ It separates two facts `Foreign`'s single `ok` folds together: "the
	// SESSION anchors no task" and "the MANAGER has no goal page". The first is
	// the unowned class the wake job may route; the second is a manager whose
	// scope could not be read, which must keep everything. Answering one with the
	// other would turn a missing manager page into a fleet-wide silence.
	ManagerResolved(managerSession string) bool

	// SessionResolved reports whether the task index knows this session at all.
	//
	// ⚠️ This is the fact that separates the cron class from every other
	// `ok=false`. `Foreign` answers `ok=false` for a session with no task AND for
	// a real task that declares no goal; only the FIRST is unowned. Routing the
	// second to the Fleet Manager would drop a genuine task's card for the very
	// manager that owns it.
	SessionResolved(session string) bool
}

// NewGoalScope returns a GoalScope reading vaultDir's goal and task pages.
//
// ⚠️ It re-reads the goal pages on every call rather than caching them. The
// vault holds a few dozen small files, this runs at most once per (manager, item)
// pair per pass, and a cache would go stale exactly when a new manager opens —
// the moment the answer matters most.
func NewGoalScope(vaultDir string, tasks TaskIndex) GoalScope {
	return &goalScope{vaultDir: vaultDir, tasks: tasks}
}

type goalScope struct {
	vaultDir string
	tasks    TaskIndex
}

// Foreign implements GoalScope.
func (s *goalScope) Foreign(managerSession, session string) (string, bool, bool) {
	if s.vaultDir == "" || managerSession == "" || session == "" || s.tasks == nil {
		return "", false, false
	}
	managerGoal := s.goalOf(managerSession)
	if managerGoal == "" {
		// An operator-started manager writes no task row, so its goal page is the
		// only place it is stamped — and an unreadable or unstamped vault leaves
		// the scope unresolved rather than empty. Excluding on that would be a
		// fleet-wide silence produced by a missing page.
		return "", false, false
	}
	task, ok := s.tasks.Lookup(session)
	if !ok {
		return "", false, false
	}
	if len(task.Goals) == 0 {
		// The task anchors a session but declares no goal. Distinct from foreign
		// on the Python side (`owner-unknown`), and here it simply keeps.
		return "", false, false
	}
	for _, goal := range task.Goals {
		if goal == managerGoal {
			return "", false, true
		}
	}
	return task.Goals[0], true, true
}

// ManagerResolved implements GoalScope.
func (s *goalScope) ManagerResolved(managerSession string) bool {
	if s.vaultDir == "" || managerSession == "" {
		return false
	}
	return s.goalOf(managerSession) != ""
}

// SessionResolved implements GoalScope.
func (s *goalScope) SessionResolved(session string) bool {
	if s.tasks == nil || session == "" {
		return false
	}
	_, ok := s.tasks.Lookup(session)
	return ok
}

// goalOf returns the title of the goal page stamped with sessionID, or "".
//
// ⚠️ Only an explicit `claude_session_id` counts, and an unreadable directory or
// file yields "". Several pages may carry the same stamp; the first in filename
// order wins, which is deterministic rather than meaningful — the vault writes
// one stamp per goal manager.
//
// ⚠️ The stamp is normalised through `sweepGateOwnerID`, not compared raw: the
// vault writes both `claude_session_id: abc` and `claude_session_id: "abc"`, and
// a raw comparison under-fires on the quoted form — silently, because an
// unmatched stamp reads as "this manager has no goal" rather than as an error.
func (s *goalScope) goalOf(sessionID string) string {
	dir := filepath.Join(s.vaultDir, goalDirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		glog.V(2).Infof("goal scope: read %s failed: %v", dir, err)
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			glog.V(2).Infof("goal scope: read %s failed: %v", entry.Name(), err)
			continue
		}
		if sweepGateOwnerID(frontmatterValue(content, taskSessionKey)) != sessionID {
			continue
		}
		return strings.TrimSuffix(entry.Name(), ".md")
	}
	return ""
}
