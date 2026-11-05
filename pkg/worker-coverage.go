// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Worker coverage answers whether a session's attention card has a live
// manager that could relay it.
//
// The board's default view shows manager-kind cards only, on the assumption
// that a worker's manager handles or relays the worker's question. That
// assumption had no check behind it: where no live manager covers the worker,
// the question is neither relayed nor shown, and the operator cannot answer what
// they cannot see. Measured 2026-11-05 — 9 live sessions parked on a waiting
// verb and 0 cards in the default view.
//
// ⚠️ THE RULE IS claude-supervisor's, NOT OURS. `fleet-board.py`'s
// `build_grouping` parents every session, and this file answers one question
// about that tree: is the session parented to a manager other than the root?
// It is re-derived here from sources this process already holds — the task
// index, the session registry and the liveness checker — rather than by
// running the Python, because a cross-repo runtime dependency would let a
// refactor elsewhere change this service silently. A rule with two homes
// drifts, so the parity spec runs both over the live fleet and asserts
// identical verdicts; see worker-coverage-parity_test.go.
//
// Mirrored, step for step:
//   - A manager is a session whose registry name resolves to a goal or topic
//     page (`manager_subject` sources 2 and 3): the name, or the name less a
//     trailing ` Manager`, exactly and case-insensitively; else a unique
//     normalized prefix of at least workerCoverageTruncationMinPrefix runes
//     across goals and topics together.
//   - A worker is covered when a live manager's subject is one of its task's
//     declared goals (`manager_on_chain`). The goal's TOPIC is deliberately not
//     a rung — a topic manager never parents a worker.
//   - A manager's own card, and a session with no task, have no relaying parent
//     above them, so both are uncovered.
//
// Known gaps, each counted by the parity spec rather than assumed harmless:
// a task's hand-authored `managed_by`, a task listed directly in a topic's
// `## Goals`, a manager detected only by tab colour or by its loop record
// (`manager_subject` source 1), and sessions anchored in another vault.

// workerCoverageTruncationMinPrefix is the length below which a registry name
// is not accepted as a truncation of a title.
//
// ⚠️ It mirrors `_TRUNCATION_MIN_PREFIX` in `fleet-board.py`, and the two must
// agree.
const workerCoverageTruncationMinPrefix = 20

// managerSuffix mirrors `_MANAGER_SUFFIX`: a manager session is named after its
// subject with ` Manager` appended, and the subject is what has to match.
var managerSuffix = regexp.MustCompile(`(?i)\s+manager$`)

// subjectIndex resolves a session name to the goal or topic it names.
type subjectIndex struct {
	// exact maps a lowered, trimmed title to itself.
	exact map[string]string
	// normalized maps normalizeSessionName(title) to the title.
	normalized map[string]string
}

// newSubjectIndex builds the index over every goal and topic title.
func newSubjectIndex(titles []string) subjectIndex {
	index := subjectIndex{
		exact:      make(map[string]string, len(titles)),
		normalized: make(map[string]string, len(titles)),
	}
	for _, title := range titles {
		index.exact[strings.ToLower(strings.TrimSpace(title))] = title
		index.normalized[normalizeSessionName(title)] = title
	}
	return index
}

// subject returns the title a session name names, or "" when it names none.
func (s subjectIndex) subject(name string) string {
	stripped := managerSuffix.ReplaceAllString(name, "")
	for _, candidate := range []string{name, stripped} {
		if title, ok := s.exact[strings.ToLower(strings.TrimSpace(candidate))]; ok {
			return title
		}
	}
	// ⚠️ Exactly ONE hit across both keys and both tables, so an ambiguous
	// prefix stays unresolved rather than becoming a confident guess.
	hits := map[string]struct{}{}
	for _, key := range []string{normalizeSessionName(name), normalizeSessionName(stripped)} {
		if title := s.prefixTitle(key); title != "" {
			hits[title] = struct{}{}
		}
	}
	if len(hits) != 1 {
		return ""
	}
	for title := range hits {
		return title
	}
	return ""
}

// prefixTitle mirrors `_prefix_title`: the single title key unambiguously
// prefixes, or "".
func (s subjectIndex) prefixTitle(key string) string {
	if len([]rune(key)) < workerCoverageTruncationMinPrefix {
		return ""
	}
	found := ""
	for normalized, title := range s.normalized {
		if !strings.HasPrefix(normalized, key) {
			continue
		}
		if found != "" {
			return ""
		}
		found = title
	}
	return found
}

// liveManagerSubjects returns the lowered subject title of every live session
// whose name resolves to a goal or topic, keyed by session id.
func liveManagerSubjects(
	names map[string]sessionName,
	index subjectIndex,
	isLive func(sessionID string) bool,
) map[string]string {
	subjects := map[string]string{}
	for sessionID, entry := range names {
		title := index.subject(entry.Name)
		if title == "" || !isLive(sessionID) {
			continue
		}
		subjects[sessionID] = strings.ToLower(title)
	}
	return subjects
}

// workerUncovered reports whether sessionID's card has no live manager that
// could relay it.
//
// ⚠️ An empty registry answers false — covered — because the failure
// directions are not symmetric. Reading "I could not look" as "nobody covers
// anyone" would put every worker card on the board the moment the registry was
// unreadable, the same false alarm the liveness checker already refuses.
func workerUncovered(
	sessionID string,
	task Task,
	hasTask bool,
	names map[string]sessionName,
	index subjectIndex,
	managers map[string]string,
) bool {
	if sessionID == "" || len(names) == 0 {
		return false
	}
	if entry, ok := names[sessionID]; ok && index.subject(entry.Name) != "" {
		// A manager is parented to the root, never to a peer manager.
		return true
	}
	if !hasTask || len(task.Goals) == 0 {
		return true
	}
	for candidate, subject := range managers {
		if candidate == sessionID {
			continue
		}
		for _, goal := range task.Goals {
			if strings.ToLower(goal) == subject {
				return false
			}
		}
	}
	return true
}

// normalizeSessionName mirrors `_norm_session_name`, step for step: NFKC,
// trim, drop the leading decoration (`⚙ ` is a registry marker no title
// carries), collapse whitespace, casefold.
//
// ⚠️ The one deviation is case folding: Go's ToLower differs from Python's
// casefold only outside ASCII, and the parity spec is what catches it.
func normalizeSessionName(text string) string {
	text = norm.NFKC.String(text)
	text = strings.TrimSpace(text)
	text = strings.TrimLeftFunc(text, isLeadingDecoration)
	text = strings.Join(strings.Fields(text), " ")
	return strings.ToLower(text)
}

// isLeadingDecoration mirrors Python's `[^\w(]`: it keeps letters, digits,
// `_` and `(`, so a name opening with `(` is stripped on neither side.
func isLeadingDecoration(r rune) bool {
	return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '('
}
