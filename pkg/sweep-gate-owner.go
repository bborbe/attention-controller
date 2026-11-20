// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// sweepGateOwnerSession resolves the session that owns `subject`: the
// `claude_session_id` on the subject's own page.
//
// ⚠️ Why it exists. The owner filter routes a card by the session the item
// names — `evaluate()` calls `session_for_pane()`, then `verdict()` — and a
// `sweep-gate` card names the cron, which resolves no owner. The class is
// therefore fixed by the producer's identity: every such card is routed to the
// Fleet Manager, which can act on none of them, and no property of the subject
// can change it. Naming the subject's goal manager instead is what puts the
// card in front of the manager who can act on it.
//
// An empty result is a real answer, not a failure: a topic page, or a goal page
// carrying no `claude_session_id`, resolves no owner, and the card then posts
// under the constant `sweep-gate` and reads `unowned-fleet` — the degrade the
// board shows rather than a silence.
//
// ⚠️ It reads the id and does not judge it: a stale id left by a manager that
// has since exited is passed through as-is, and the liveness decision stays
// where it already lives — the owner filter, which resolves the named session
// against the ledger. Second-guessing it here would put a second liveness rule
// in a second language.
func sweepGateOwnerSession(subject SweepGateSubject) string {
	// ⚠️ The two page folders, not `subject.Branch`: the enumerator reports the
	// branch as a KIND (`topic`), which is not a directory name.
	for _, dir := range []string{goalDirName, topicDirName} {
		content, err := os.ReadFile(filepath.Join(subject.Vault, dir, subject.Name+".md"))
		if err != nil {
			continue
		}
		if id := sweepGateOwnerID(frontmatterValue(content, taskSessionKey)); id != "" {
			return id
		}
	}
	return ""
}

// sweepGateOwnerID accepts a frontmatter scalar only when it is plausibly a
// session id.
//
// `frontmatterValue` returns the raw scalar, so an explicit YAML null reads as
// the literal `null` and a tilde as `~`; either would otherwise be posted as a
// producer id and route the card to a session that never existed. The check is
// deliberately loose — reject the null spellings, a leading `-` (which an
// argument parser would read as a flag) and any embedded whitespace — rather
// than pinning a UUID shape, because the id's shape is the vault's to define.
func sweepGateOwnerID(value string) string {
	id := strings.Trim(strings.TrimSpace(value), `"'`)
	switch strings.ToLower(id) {
	case "", "null", "~":
		return ""
	}
	if strings.HasPrefix(id, "-") || strings.ContainsAny(id, " \t\r\n") {
		return ""
	}
	return id
}

// sweepGateLiveRegistryDir is the session registry the owner filter reads.
//
// ⚠️ It must be the SAME source the filter uses. `gate-owner-filter.py` builds
// its live set from `live_ids()` over `~/.claude/sessions/*.json`; a liveness
// rule that read a different store would clear owners the filter considers
// live, which is the opposite of what this change is for.
func sweepGateLiveRegistryDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "sessions")
}

// sweepGateLiveSession reports whether `sessionID` has a heartbeat in the live
// store at `liveDir`.
//
// ⚠️ Why the card cares. The owner filter's hop 3b drops a card as
// `peer-manager-own` only when the named session is a manager **and** is in the
// live set; a named session with no heartbeat falls through to
// `route_to_fleet()` and reads `unowned-fleet` — deliberately, because a dead
// manager cannot act on its own gate. So naming a dead manager buys nothing and
// misattributes the card: the row reads unroutable while claiming an owner.
// Naming only live sessions leaves that fail-open untouched and makes the
// attribution mean what it says.
// ⚠️ It mirrors `live_ids()` exactly: glob `*.json`, parse, take `sessionId` or
// `session_id`. The filenames are pids, not session ids, so a filename match
// would be wrong.
func sweepGateLiveSession(registryDir, sessionID string) bool {
	if sessionID == "" || registryDir == "" {
		return false
	}
	paths, err := filepath.Glob(filepath.Join(registryDir, "*.json"))
	if err != nil {
		return false
	}
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(content, &rec); err != nil {
			continue
		}
		sid, _ := rec["sessionId"].(string)
		if sid == "" {
			sid, _ = rec["session_id"].(string)
		}
		if sid == sessionID {
			return true
		}
	}
	return false
}

// sweepGateProducerID is the producer id a card is posted under: the owner
// session when the subject resolves one, the constant otherwise.
func sweepGateProducerID(owner string) string {
	if owner != "" {
		return owner
	}
	return sweepGateCardProducerID
}

// sweepGateOwnerLabel renders an owner for the log line, so an unresolved
// subject is named rather than leaving an empty field that reads as truncation.
func sweepGateOwnerLabel(owner string) string {
	if owner == "" {
		return "unresolved"
	}
	return owner
}
