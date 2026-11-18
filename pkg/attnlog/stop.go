// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package attnlog

import (
	"regexp"
	"strings"
)

// The Stop branch's closer contract.
//
// ⚠️ The fence walker is deliberately the same one `pkg.UnforwardedClosers`
// reads independently: an UNFENCED `👤 You:` line wins; a fenced closer counts
// only when its block is a one-line closer or a closer panel (a state glyph
// beside the closer). A quoted panel that merely *discusses* a closer is not
// emitted, which is the whole reason the walker tracks fences.
var (
	// closerLinePattern is the `👤 You:` line, tolerant of emphasis and of the
	// colon written detached or the name padded.
	closerLinePattern = regexp.MustCompile(`^[*_\s]*👤\s*You\s*[*_]*\s*:\s*[*_]*\s*:?(.*)$`)
	// closerDecorationPattern is the emphasis a session wraps a closer in.
	closerDecorationPattern = regexp.MustCompile("[`*_]")
	// stateGlyphLinePattern marks a closer panel: a state line beside the
	// closer, which makes a fenced block an emitted panel rather than a quote.
	stateGlyphLinePattern = regexp.MustCompile(`^[*_\s]*(🟢|🟡|🔴|🔵|⚪|⏰)`)
	// parkedCloserPattern is a closer that names the event it waits for.
	parkedCloserPattern = regexp.MustCompile(`(?i)^later\s*\(on\b`)
	// noReplyCloserPattern is a closer that explicitly asks for no reply.
	noReplyCloserPattern = regexp.MustCompile(
		`(?i)(^|[—–]\s*)(no reply( needed)?|nothing needed|no action needed)(\s+[A-Za-z0-9_]{1,12})?[.!]?\s*$`,
	)
	// nothingCloserPattern is a closer that asks for nothing.
	nothingCloserPattern = regexp.MustCompile(`(?i)^nothing\b`)
	// approveCloserPattern is the `approve:` waiting verb.
	approveCloserPattern = regexp.MustCompile(`(?i)^approve\s*:`)
	// pickCloserPattern is the `pick` waiting verb.
	pickCloserPattern = regexp.MustCompile(`(?i)^pick\b`)
	// reviewCloserPattern is the `review:` waiting verb.
	reviewCloserPattern = regexp.MustCompile(`(?i)^review\s*:`)
	// youRunCloserPattern is the `you run:` waiting verb.
	youRunCloserPattern = regexp.MustCompile(`(?i)^you\s+run\s*:`)
	// boardOptInPattern is the explicit `📣 Board:` opt-in.
	boardOptInPattern = regexp.MustCompile(`(?i)📣\s*Board:\s*(.*)`)
	// handoverOptionPattern identifies an option that hands work to a manager.
	handoverOptionPattern = regexp.MustCompile(`(?i)hand\s*over|handover`)
	// pickLabelPattern strips a numbered or bulleted option prefix.
	pickLabelPattern = regexp.MustCompile(`^\s*(?:\d+[.)]|[-*])\s*`)
)

// stop closes a turn: it records the session going idle, and mints a question
// item when the assistant's final message carries a closer the operator can
// answer.
//
// ⚠️ An already-open item blocks the write. A Stop must never supersede a gate
// the session is still waiting on — the closer walker runs after the turn, and
// overwriting a live permission gate with an idle row would erase the question.
func (a *App) stop(stateDir string, event payload) {
	if a.stopSlotBlocks(stateDir, event.SessionID) {
		return
	}
	ask := lastOwnedCloser(event.LastAssistantMessage)
	board, hasBoard := boardOptIn(event.LastAssistantMessage)
	if ask == "" && !hasBoard {
		a.openIdle(stateDir, event, "")
		return
	}
	if ask != "" && (closerExempt(ask) ||
		a.echoesPostedCard(stateDir, event.SessionID, ask) ||
		managerSettleable(ask)) {
		a.suppressCloser(stateDir, event.SessionID, ask)
		a.openIdle(stateDir, event, ask)
		return
	}
	if ask == "" {
		// A board opt-in with no closer still asks the board; the opt-in line
		// itself is the ask.
		a.mintCloserQuestion(stateDir, event, board, nil)
		return
	}
	if !mintableCloser(ask) && !hasBoard {
		a.openIdle(stateDir, event, ask)
		return
	}
	a.mintCloserQuestion(stateDir, event, ask, closerOptions(ask))
}

// lastOwnedCloser returns the closer the turn owns, or "".
func lastOwnedCloser(message string) string {
	if message == "" {
		return ""
	}
	unfenced := make([]string, 0, 2)
	fenced := make([]string, 0, 2)
	block := make([]string, 0, 4)
	inFence := false
	for _, line := range strings.Split(message, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inFence {
				if closer, ok := fencedBlockCloser(block); ok {
					fenced = append(fenced, closer)
				}
			}
			inFence = !inFence
			block = block[:0]
			continue
		}
		if inFence {
			block = append(block, trimmed)
			continue
		}
		if match := closerLinePattern.FindStringSubmatch(trimmed); match != nil {
			unfenced = append(unfenced, match[1])
		}
	}
	asks := unfenced
	if len(asks) == 0 {
		asks = fenced
	}
	if len(asks) == 0 {
		return ""
	}
	return undecorateAsk(asks[len(asks)-1])
}

// fencedBlockCloser returns the last closer in a fenced block when the block is
// a one-line closer or a closer panel, and false otherwise.
func fencedBlockCloser(block []string) (string, bool) {
	lines := make([]string, 0, len(block))
	for _, line := range block {
		if line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return "", false
	}
	panel := false
	closers := make([]string, 0, 1)
	for _, line := range lines {
		if stateGlyphLinePattern.MatchString(line) {
			panel = true
		}
		if match := closerLinePattern.FindStringSubmatch(line); match != nil {
			closers = append(closers, match[1])
		}
	}
	if len(closers) == 0 || (len(lines) != 1 && !panel) {
		return "", false
	}
	return closers[len(closers)-1], true
}

// undecorateAsk strips emphasis and the trailing separator from a closer.
func undecorateAsk(ask string) string {
	cleaned := closerDecorationPattern.ReplaceAllString(ask, "")
	return strings.Trim(cleaned, " :")
}

// normalizeCloserText is the comparison form: punctuation-insensitive and
// whitespace-collapsed. It is applied to BOTH sides of the posted-card echo
// comparison, because the ask has its decoration stripped while `.posted.json`
// stores the closer verbatim.
func normalizeCloserText(value string) string {
	cleaned := closerDecorationPattern.ReplaceAllString(value, "")
	return strings.Join(strings.Fields(cleaned), " ")
}

// boardOptIn returns the text after an explicit `📣 Board:` line, and whether
// the turn carries one.
func boardOptIn(message string) (string, bool) {
	for _, line := range strings.Split(message, "\n") {
		if match := boardOptInPattern.FindStringSubmatch(line); match != nil {
			return strings.TrimSpace(match[1]), true
		}
	}
	return "", false
}

// closerExempt reports whether the closer is a deliberate non-ask: parked,
// explicitly no-reply, or asking for nothing. The hook records an idle row for
// these, and suppresses the unforwarded-closer diagnostic so a deliberate idle
// does not read as a lost card.
func closerExempt(ask string) bool {
	return parkedCloserPattern.MatchString(ask) ||
		noReplyCloserPattern.MatchString(ask) ||
		nothingCloserPattern.MatchString(ask)
}

// mintableCloser reports whether the closer carries a waiting verb.
func mintableCloser(ask string) bool {
	return approveCloserPattern.MatchString(ask) ||
		pickCloserPattern.MatchString(ask) ||
		reviewCloserPattern.MatchString(ask) ||
		youRunCloserPattern.MatchString(ask)
}

// managerSettleable reports whether the closer is a move a manager can settle
// without the operator: a `pick` whose every option is a handover.
func managerSettleable(ask string) bool {
	if !pickCloserPattern.MatchString(ask) {
		return false
	}
	options := pickOptions(ask)
	if len(options) == 0 {
		return false
	}
	for _, entry := range options {
		if !handoverOptionPattern.MatchString(entry.Label) {
			return false
		}
	}
	return true
}

// closerOptions returns the answer surface a waiting verb offers.
func closerOptions(ask string) []option {
	switch {
	case approveCloserPattern.MatchString(ask):
		return approveOptions()
	case pickCloserPattern.MatchString(ask):
		return pickOptions(ask)
	case reviewCloserPattern.MatchString(ask):
		return reviewOptions()
	case youRunCloserPattern.MatchString(ask):
		return handoverOptions()
	default:
		return nil
	}
}

// pickOptions parses a `pick — 1. A · 2. B` panel into its choices.
func pickOptions(ask string) []option {
	body := pickCloserPattern.ReplaceAllString(ask, "")
	body = strings.TrimLeft(body, " —-–:·;|\t")
	parts := strings.FieldsFunc(body, func(r rune) bool {
		return r == '·' || r == ';' || r == '|' || r == '\n'
	})
	out := make([]option, 0, len(parts))
	for _, part := range parts {
		label := strings.TrimSpace(pickLabelPattern.ReplaceAllString(part, ""))
		if label == "" {
			continue
		}
		out = append(out, option{Label: label})
	}
	return out
}

// approveOptions is the answer surface an `approve:` closer offers.
func approveOptions() []option {
	return []option{{Label: "Approve"}, {Label: "Deny"}}
}

// reviewOptions is the answer surface a `review:` closer offers.
func reviewOptions() []option {
	return []option{{Label: "Approve"}, {Label: "Request changes"}}
}

// handoverOptions is the answer surface a `you run:` closer offers.
func handoverOptions() []option {
	return []option{{Label: "Done"}, {Label: "Skip"}}
}

// mintCloserQuestion opens the closer as a question item.
func (a *App) mintCloserQuestion(stateDir string, event payload, detail string, options []option) {
	fullDetail := scrubDetail(detail)
	unit := question{
		Payload:     fullDetail,
		Cardinality: "single",
		Options:     options,
	}
	a.openItem(stateDir, event, "question", fullDetail, gateContent{
		questions: []question{unit},
		clearedBy: &clearedBy{
			Event: eventList{"UserPromptSubmit", "SessionEnd"},
		},
	})
}

// openIdle records the session going idle.
func (a *App) openIdle(stateDir string, event payload, detail string) {
	a.openItem(stateDir, event, "idle", scrubDetail(detail), gateContent{
		clearedBy: &clearedBy{
			Event: eventList{"UserPromptSubmit", "SessionEnd"},
		},
	})
}

// suppressCloser records a deliberate non-ask so the unforwarded-closer
// diagnostic does not read this turn's idle row as a lost card.
func (a *App) suppressCloser(stateDir, sessionID, closer string) {
	record := map[string]any{
		"session_id": sessionID,
		"ts":         a.now(),
	}
	if closer != "" {
		record["closer"] = closer
	}
	appendStateLine(stateDir, suppressedClosersFile, record)
}

// echoesPostedCard reports whether the closer is the one this session already
// posted as a card.
func (a *App) echoesPostedCard(stateDir, sessionID, ask string) bool {
	var record struct {
		Closer string `json:"closer"`
	}
	if !readJSON(stateDir, postedCardPath(sessionID), &record) {
		return false
	}
	if record.Closer == "" {
		return false
	}
	return normalizeCloserText(ask) == normalizeCloserText(record.Closer)
}

// stopSlotBlocks reports whether the session already holds an open item.
func (a *App) stopSlotBlocks(stateDir, sessionID string) bool {
	_, ok := readOpenItem(stateDir, sessionID)
	return ok
}
