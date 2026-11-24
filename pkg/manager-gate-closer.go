// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"regexp"
	"strings"
)

// managerCloserPrefix opens the closer panel line a worker ends its turn on.
const managerCloserPrefix = "👤 You:"

// managerNothingVerb is the closer body that says nobody was asked anything.
const managerNothingVerb = "nothing"

// managerParkedVerb prefixes a closer body that names the event which resumes
// the work; until that event fires there is nothing for the operator to answer.
const managerParkedVerb = "later (on "

// managerCloserEntityPattern matches the HTML entities a pane emits inside a
// closer line. `&nbsp;` is six literal characters, not whitespace, so a plain
// TrimSpace leaves it in place and defeats the verb comparison.
var managerCloserEntityPattern = regexp.MustCompile(`(?i)&(?:nbsp|#160|#xa0);`)

// managerCloserBody returns the body of the LAST `👤 You:` line, and false when
// the text carries none. Last, not first: a worker may quote an earlier closer.
func managerCloserBody(text string) (string, bool) {
	body := ""
	found := false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, managerCloserPrefix) {
			continue
		}
		body = strings.TrimSpace(strings.TrimPrefix(trimmed, managerCloserPrefix))
		found = true
	}
	return body, found
}

// managerNormalizeCloser strips HTML entities and collapses whitespace before a
// verb comparison. Panes emit `👤 You: &nbsp;&nbsp;&nbsp;&nbsp;nothing`.
func managerNormalizeCloser(text string) string {
	replaced := managerCloserEntityPattern.ReplaceAllString(text, " ")
	return strings.Join(strings.Fields(replaced), " ")
}

// managerAskVerbs is the closed set of closer verbs that read as a live ask. A
// closer body opening with any of these asks the operator to act. The set is
// deliberately closed: an unseen verb reads as no ask, so a park is never
// manufactured for a turn that asked nothing.
var managerAskVerbs = []string{
	"pick",
	"approve:",
	"review:",
	"you run:",
}

// managerIsAsk reports whether a closer body is a live ask: one that opens with
// exactly one of the four verbs in managerAskVerbs. Not an ask: the `nothing`
// panel, a `later (on …)` parked wait, and every verb outside the four.
//
// ⚠️ It takes the RAW body and normalizes internally. That is why it is the only
// caller of managerNormalizeCloser: wrapping the argument in that helper would
// double-normalize and leave it with no caller.
func managerIsAsk(body string) bool {
	verb := strings.ToLower(managerNormalizeCloser(body))
	if verb == "" {
		return false
	}
	if strings.HasPrefix(verb, managerNothingVerb) {
		return false
	}
	if strings.HasPrefix(verb, managerParkedVerb) {
		return false
	}
	for _, ask := range managerAskVerbs {
		if strings.HasPrefix(verb, ask) {
			return true
		}
	}
	return false
}
