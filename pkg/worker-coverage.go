// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// workerCoverageTruncationMinPrefix is the length below which a registry name
// is not accepted as a truncation of a title.
//
// ⚠️ It mirrors `_TRUNCATION_MIN_PREFIX` in claude-supervisor's
// `fleet-board.py`, and the two must agree — a registry name is a *truncated*
// title (measured 46 characters on 2026-10-07, against a 108-character title),
// so the prefix branch is what makes coverage resolve at all. The floor is what
// keeps a short coincidental prefix from matching: a role name such as `boss`
// opens no title by accident, but it would open one by luck.
const workerCoverageTruncationMinPrefix = 20

// WorkerCoverage answers whether a worker's attention card has a live manager
// that could relay it.
//
// The board's default view shows manager-kind cards only, on the assumption
// that a worker's manager handles or relays the worker's question. That
// assumption had no check behind it: where no live manager covers the worker,
// the question is neither relayed nor shown, and the operator cannot answer what
// they cannot see. Measured 2026-11-05 — 9 live sessions parked on a waiting
// verb, 6 with no live covering manager, and 0 cards in the default view.
//
// ⚠️ Coverage is resolved from the three sources this process already holds,
// never from claude-supervisor's Python: the task index answers which goal and
// topic a worker's session belongs to, the session registry answers what the
// live sessions are called, and the liveness checker answers which are live. A
// cross-repo runtime dependency would let a refactor in another repository
// change this service's behaviour silently.
type WorkerCoverage interface {
	// Uncovered reports whether sessionID's card has no live covering manager.
	//
	// ⚠️ Every unprovable case answers false — covered — because the failure
	// directions are not symmetric. Reading an unreadable registry as "nobody
	// covers anyone" would put every worker card on the board the moment the
	// registry was missing, which is the same false alarm the liveness checker
	// already refuses. Only a readable registry holding no matching live
	// manager makes a card uncovered.
	Uncovered(ctx context.Context, sessionID string) bool
}

// NewWorkerCoverage returns a WorkerCoverage over the task index, the liveness
// checker and the session registry directory.
func NewWorkerCoverage(
	tasks TaskIndex,
	liveness SessionLivenessChecker,
	sessionsDir string,
) WorkerCoverage {
	return &workerCoverage{tasks: tasks, liveness: liveness, sessionsDir: sessionsDir}
}

type workerCoverage struct {
	tasks       TaskIndex
	liveness    SessionLivenessChecker
	sessionsDir string
}

// Uncovered reports whether this worker's card has no live covering manager.
func (c *workerCoverage) Uncovered(ctx context.Context, sessionID string) bool {
	if c == nil || c.tasks == nil || c.liveness == nil || sessionID == "" {
		return false
	}
	titles := c.coveredTitles(sessionID)
	if len(titles) == 0 {
		// No goal and no topic is no manager's subject. A worker the vault
		// cannot place is uncovered by construction, not by measurement.
		return true
	}
	names := readSessionNames(ctx, c.sessionsDir)
	if len(names) == 0 {
		// An unreadable registry proves nothing, and the board must not treat
		// "I could not look" as "nobody covers this worker".
		return false
	}
	for candidate, entry := range names {
		if candidate == sessionID {
			continue
		}
		if !c.liveness.IsLive(ctx, candidate) {
			continue
		}
		if matchesOneTitle(normalizeSessionName(entry.Name), titles) {
			return false
		}
	}
	return true
}

// coveredTitles returns the normalized goal and topic titles a live manager
// would have to name to cover this session. An empty set means the session
// anchors no goal and no topic.
func (c *workerCoverage) coveredTitles(sessionID string) map[string]struct{} {
	task, ok := c.tasks.Lookup(sessionID)
	if !ok {
		return nil
	}
	titles := make(map[string]struct{}, 2)
	for _, title := range []string{task.GoalName, task.TopicName} {
		if normalized := normalizeSessionName(title); normalized != "" {
			titles[normalized] = struct{}{}
		}
	}
	return titles
}

// matchesOneTitle reports whether key names exactly one entry of titles.
//
// ⚠️ Exact first, then a prefix gated on the length floor AND on identifying
// exactly one title. A guess about which title was truncated must never displace
// a certainty, and an ambiguous prefix must never become a confident match —
// the same two guards `_matches_one_title` applies, for the same reasons.
func matchesOneTitle(key string, titles map[string]struct{}) bool {
	if key == "" {
		return false
	}
	if _, ok := titles[key]; ok {
		return true
	}
	if len([]rune(key)) < workerCoverageTruncationMinPrefix {
		return false
	}
	prefixes := 0
	for title := range titles {
		if strings.HasPrefix(title, key) {
			prefixes++
		}
	}
	return prefixes == 1
}

// normalizeSessionName reduces a session name or a title to the form the two are
// compared in.
//
// ⚠️ It mirrors `_norm_session_name` in claude-supervisor's `fleet-board.py`,
// step for step: NFKC, trim, drop the leading decoration (`⚙ ` is a registry
// marker no title carries), collapse whitespace, casefold. The one deviation is
// that Go's case folding is `ToLower`, which differs from Python's `casefold`
// only for non-ASCII scripts — and the parity test over the live fleet is what
// would catch that, rather than a reader noticing.
func normalizeSessionName(text string) string {
	text = norm.NFKC.String(text)
	text = strings.TrimSpace(text)
	text = strings.TrimLeftFunc(text, isLeadingDecoration)
	text = strings.Join(strings.Fields(text), " ")
	return strings.ToLower(text)
}

// isLeadingDecoration reports whether r is decoration a registry name may carry
// in front of the title. Python's `[^\w(]` keeps word characters and `(`; this
// keeps the same set, so a name beginning with `(` is not stripped on one side
// and stripped on the other.
func isLeadingDecoration(r rune) bool {
	return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '('
}
