// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// WakeSubject is one session's resolved subject.
//
// ⚠️ It carries `Resolved` separately from `Subject` because "no record" and
// "a record naming nothing" are different facts. Both fall to the Fleet
// Manager, but a measurement that reported them as one would make a broken
// record look like a missing session.
type WakeSubject struct {
	// Subject is the goal or topic the session is anchored to, empty when the
	// record names none.
	Subject string
	// Fleet reports whether this session is a Fleet Manager registration.
	Fleet bool
	// Resolved reports whether a record was found at all.
	Resolved bool
}

// WakeOwnership is one (manager, item-producer) pair's subject facts.
//
// ⚠️ It is the whole cross-language contract. The fixture
// `testdata/wake-subject-predicate.json` is these two fields plus the expected
// verdict, so the Go job and the Python arms are tested against ONE file rather
// than against each other's prose — which is the only way "one rule" can be
// checked rather than asserted.
type WakeOwnership struct {
	// Manager is the receiving manager's resolved subject.
	Manager WakeSubject
	// Producer is the item's producer session's resolved subject.
	Producer WakeSubject
}

// Keeps reports whether the manager keeps a card whose producer resolved to
// Producer.
//
// The rule, and why every branch is the fail-open direction:
//
//   - an UNRESOLVED producer is unowned, not foreign — no manager can prove the
//     card is not theirs. It is kept by the Fleet Manager alone, because the
//     Fleet Manager is the one session whose job is the fleet-wide residue. This
//     is the Python filter's `route_unowned` rule, not a new one.
//   - a RESOLVED producer that names no subject is the same unowned class from
//     the other side: the record exists, it just names nothing.
//   - a manager with no resolved subject is not evidence of a foreign card, so
//     it keeps — the spawn-edge rule then decides, exactly as before.
//   - both resolved and equal — the card is this manager's, keep.
//   - both resolved and different — DROP. This is the only branch that drops,
//     and it drops on a POSITIVE foreign resolution, never on an absence.
func (w WakeOwnership) Keeps() bool {
	if !w.Producer.Resolved || w.Producer.Subject == "" {
		return w.Manager.Fleet
	}
	if w.Manager.Subject == "" {
		return true
	}
	return w.Manager.Subject == w.Producer.Subject
}

// WakeSubjectResolver resolves a session to the subject it is anchored to.
//
// ⚠️ Resolution is a CHAIN, not one field. Managers write a `worker-manager`
// record, but a wrapper-backed worker (measured 2026-11-21 on `a4633168`)
// writes none and carries an empty ledger `task`, so the session name on its
// context record is the only carrier. A single-source resolver would keep
// exactly the foreign cards this change exists to drop.
type WakeSubjectResolver interface {
	// Resolve returns the subject a session is anchored to. An unknown session
	// returns the zero WakeSubject, never an error: a missing record is a fact
	// about the fleet, not a failure of the pass.
	Resolve(ctx context.Context, sessionID string) WakeSubject
}

// wakeSubjectFleetBranch is the `branch` value a Fleet Manager registration
// carries. It is the same constant the Python filter's `FLEET_BRANCH` holds.
const wakeSubjectFleetBranch = "fleet"

// NewDefaultWakeSubjectResolver builds the resolver over the state directories
// the statusline and the managers already write.
func NewDefaultWakeSubjectResolver(
	stateDir string,
	contextDir string,
	vaultsRoot string,
) WakeSubjectResolver {
	return &wakeSubjectResolver{
		workerManagerDir: filepath.Join(stateDir, "worker-manager"),
		contextDir:       contextDir,
		vaultsRoot:       vaultsRoot,
		cache:            map[string]WakeSubject{},
	}
}

// wakeSubjectResolver reads the two records in order and caches per session: a
// pass asks once per session, and one session appears once per manager.
type wakeSubjectResolver struct {
	workerManagerDir string
	contextDir       string
	vaultsRoot       string
	cache            map[string]WakeSubject
}

// Resolve implements WakeSubjectResolver.
func (r *wakeSubjectResolver) Resolve(_ context.Context, sessionID string) WakeSubject {
	if sessionID == "" {
		return WakeSubject{}
	}
	if cached, ok := r.cache[sessionID]; ok {
		return cached
	}
	resolved := r.resolveWorkerManager(sessionID)
	if !resolved.Resolved {
		resolved = r.resolveContext(sessionID)
	}
	r.cache[sessionID] = resolved
	return resolved
}

// resolveWorkerManager reads `<state>/worker-manager/<session>.json`.
//
// ⚠️ A `branch: fleet` record resolves even with an empty `subject`: the branch
// IS the fact the rule needs, and requiring a subject there would turn a live
// Fleet Manager into an unowned manager — which would then drop every unowned
// card instead of keeping it.
func (r *wakeSubjectResolver) resolveWorkerManager(sessionID string) WakeSubject {
	raw, err := os.ReadFile(filepath.Join(r.workerManagerDir, sessionID+".json"))
	if err != nil {
		return WakeSubject{}
	}
	var record struct {
		Subject string `json:"subject"`
		Branch  string `json:"branch"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return WakeSubject{}
	}
	subject := strings.TrimSpace(record.Subject)
	fleet := record.Branch == wakeSubjectFleetBranch
	return WakeSubject{Subject: subject, Fleet: fleet, Resolved: fleet || subject != ""}
}

// resolveContext reads `<context>/<session>.json`, takes the session name and
// resolves it to the task page's first goal.
//
// ⚠️ The task page is found by GLOB over the vaults root, never by a guessed
// vault: a context record names no vault, and a guessed vault would resolve a
// goal's name against the wrong vault — the wrong-guess failure
// `sweep-gate-subjects.py` records for retired vault names.
func (r *wakeSubjectResolver) resolveContext(sessionID string) WakeSubject {
	raw, err := os.ReadFile(filepath.Join(r.contextDir, sessionID+".json"))
	if err != nil {
		return WakeSubject{}
	}
	var record struct {
		SessionName string `json:"session_name"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return WakeSubject{}
	}
	name := strings.TrimSpace(record.SessionName)
	if name == "" {
		return WakeSubject{}
	}
	subject := r.goalForTask(name)
	return WakeSubject{Subject: subject, Resolved: subject != ""}
}

// goalForTask returns the first `goals:` entry of the named task page, or "".
func (r *wakeSubjectResolver) goalForTask(name string) string {
	if r.vaultsRoot == "" {
		return ""
	}
	matches, err := filepath.Glob(filepath.Join(r.vaultsRoot, "*", "25 Tasks", name+".md"))
	if err != nil {
		return ""
	}
	for _, match := range matches {
		if goal := firstGoal(match); goal != "" {
			return goal
		}
	}
	return ""
}

// firstGoal reads the frontmatter's first `goals:` entry.
//
// ⚠️ It is a line scan, not a YAML decode, because the frontmatter is
// vault-cli's own shape and a YAML parser in the store for one field is not
// worth the dependency. It stops at the end of the frontmatter and at the first
// non-list line after `goals:`, so a body bullet beginning `- [[` is never
// mistaken for a goal.
func firstGoal(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(raw), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return ""
	}
	inGoals := false
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			return ""
		}
		if trimmed == "goals:" {
			inGoals = true
			continue
		}
		if !inGoals {
			continue
		}
		if !strings.HasPrefix(trimmed, "- ") {
			return ""
		}
		return strings.Trim(strings.TrimPrefix(trimmed, "- "), "'\"[]")
	}
	return ""
}

// WithManagerWakeSubjectResolver attaches the subject resolver to the job.
//
// ⚠️ An option, not a constructor parameter, so the one registration site and
// every existing spec keep their call shape — and a nil resolver preserves the
// pre-change behaviour exactly, which is what lets the spawn-edge specs stay
// unchanged.
func WithManagerWakeSubjectResolver(resolver WakeSubjectResolver) ManagerWakeOption {
	return func(job *managerWakeJob) {
		job.subjects = resolver
	}
}

// wakeOwnership returns the pair's subject facts, or ok=false when no resolver
// is attached.
func (p *wakePass) wakeOwnership(manager string, producer string) (WakeOwnership, bool) {
	if p.job.subjects == nil {
		return WakeOwnership{}, false
	}
	return WakeOwnership{
		Manager:  p.job.subjects.Resolve(p.ctx, manager),
		Producer: p.job.subjects.Resolve(p.ctx, producer),
	}, true
}

// isFleetManager reports whether a session is a Fleet Manager registration.
//
// ⚠️ It falls back to true when no resolver is attached: the pre-change
// behaviour kept every unresolved card, and a job without the resolver must not
// silently start dropping them.
func (p *wakePass) isFleetManager(manager string) bool {
	if p.job.subjects == nil {
		return true
	}
	return p.job.subjects.Resolve(p.ctx, manager).Fleet
}
