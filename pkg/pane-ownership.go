// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"strings"
	"unicode"
)

// StripStatusGlyph removes Claude Code's leading status glyph from a name.
//
// Claude Code prefixes a pane title with a status glyph (`✳ ◐ ◑ ◒ ◓ ⠿ …`) and
// a session's own name may carry one too (`⚙ …`), so the strip is applied to
// *both* sides of the ownership comparison. Measured live: the pane title reads
// `◐ ⚙ Let the Login Middleware …` while the registry's name for that same
// session reads `⚙ Let the Login Middleware …` — both strip to the same string.
//
// This mirrors `strip_status_glyph()` in claude-supervisor's `who-needs-me.py`
// (the reader), deliberately and character for character: the reader and the
// page must not drift on what "the name" is, or the same session would be
// routable in the terminal and unroutable in the browser.
func StripStatusGlyph(text string) string {
	runes := []rune(strings.TrimSpace(text))
	for len(runes) > 0 {
		first := runes[0]
		if unicode.IsLetter(first) || unicode.IsDigit(first) ||
			strings.ContainsRune("/~._-", first) {
			break
		}
		runes = []rune(strings.TrimLeftFunc(string(runes[1:]), unicode.IsSpace))
	}
	return string(runes)
}

// OwnsPane reports whether the pane carrying paneID is provably this session's
// pane — the rule that decides whether a pane id may be shown at all.
//
// ⚠️ This is [[Attention Item Schema]] § Silence 7's rule, and it is the one
// check that distinguishes a correct rendering from a plausible one. A pane id
// written at event time is not proof it is still this session's: WezTerm
// renumbers and reuses ids across tab moves and restarts, so a stale lookup
// returns *another* session's pane — a wrong answer wearing the appearance of a
// resolved one, which the schema calls strictly worse than a blank.
//
// Existence is necessary but not sufficient, and pane existence alone is the
// check that leaked: a headless worker inherits its spawner's `WEZTERM_PANE`,
// so its item carries the spawner's pane id, which exists, and the row rendered
// a confident route to the wrong tab. Ownership is therefore proven by the
// name — the pane's title against the session's current name, both glyph-stripped.
//
// A *mismatch* is required, not merely an absence. When either side has no name
// to compare, ownership is unprovable rather than disproven, and this returns
// true: reporting an unprovable pane as unroutable would strip provenance from
// every session the registry cannot speak for, which is a different failure and
// not the one this rule exists to catch.
//
// Mirrors `is_routable()` in claude-supervisor's `who-needs-me.py`; that file is
// the rule and this is its second reader.
func OwnsPane(panes map[int]Pane, paneID int, sessionName string) bool {
	pane, exists := panes[paneID]
	if !exists {
		return false
	}
	if sessionName == "" {
		return true
	}
	title := StripStatusGlyph(pane.Title)
	if title == "" {
		return true
	}
	return title == StripStatusGlyph(sessionName)
}
