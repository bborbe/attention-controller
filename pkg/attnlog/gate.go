// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package attnlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/golang/glog"
)

// PaneLister lists the live WezTerm panes as pane id -> title.
//
// It is an injected dependency rather than a call in this package because the
// per-tool-call path must not spawn a subprocess: `owned_pane` is paid only on
// an item-opening event, and even there the listing is the caller's business.
// A nil lister leaves ownership unprovable, and an unprovable pane is kept
// rather than stripped (see ownedPane).
type PaneLister interface {
	ListPanes() (map[int]string, error)
}

// supersededByNewItem is the close reason a prior open item carries when a
// different ask replaces it.
const supersededByNewItem = "SupersededByNewItem"

// recommendedPattern matches the producer's `(Recommended)` option marker.
var recommendedPattern = regexp.MustCompile(`(?i)\s*\(recommended\)\s*`)

// option is one choice a question item offers.
type option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Recommended bool   `json:"recommended"`
}

// question is one question unit of an AskUserQuestion gate.
type question struct {
	Tab         string   `json:"tab"`
	Payload     string   `json:"payload"`
	Cardinality string   `json:"cardinality,omitempty"`
	Options     []option `json:"options,omitempty"`
}

// openItemRecord is the shape written to `.open.json` and, with `type:"open"`
// added, appended to the event log.
type openItemRecord struct {
	ItemID            string     `json:"item_id"`
	SessionID         string     `json:"session_id"`
	Kind              string     `json:"kind"`
	Detail            string     `json:"detail"`
	ToolName          string     `json:"tool_name"`
	Context           string     `json:"context,omitempty"`
	Options           []option   `json:"options,omitempty"`
	AnswerCardinality string     `json:"answer_cardinality,omitempty"`
	Questions         []question `json:"questions,omitempty"`
	Pane              string     `json:"pane"`
	Cwd               string     `json:"cwd"`
	Transcript        string     `json:"transcript"`
	Host              string     `json:"host"`
	AgentID           string     `json:"agent_id"`
	TS                float64    `json:"ts"`
	Event             string     `json:"event"`
	ClearedBy         any        `json:"cleared_by"`
}

// openEvent is the open record plus its type discriminator.
type openEvent struct {
	openItemRecord
	Type string `json:"type"`
}

// gateContent carries the kind-specific fields an open item may hold.
type gateContent struct {
	context   string
	questions []question
	clearedBy *clearedBy
}

// permissionRequest opens the gate a permission prompt describes.
//
// ⚠️ The AskUserQuestion branch is a question, not a permission: its choices
// are the answer surface, and the item must carry them. It is guarded by
// slotBlocksNewGate; the permission branch goes straight to openItem, whose
// supersede rule handles a repeated or replaced ask.
func (a *App) permissionRequest(stateDir string, event payload) {
	if event.ToolName == "AskUserQuestion" {
		a.openQuestionGate(stateDir, event)
		return
	}
	fullDetail := scrubDetail(event.ToolName + ": " + toolDetail(event.ToolInput))
	a.openItem(stateDir, event, "permission", fullDetail, gateContent{
		context: permissionContext(event.ToolName, event.ToolInput),
		clearedBy: &clearedBy{
			Event: eventList{"PreToolUse", "PostToolUse", "PostToolUseFailure", "SessionEnd"},
			Tool:  event.ToolName,
		},
	})
}

// openQuestionGate opens an AskUserQuestion item unless its slot is already
// open.
func (a *App) openQuestionGate(stateDir string, event payload) {
	questions := questionsFrom(event.ToolInput)
	fullDetail := questionDetail(event, questions)
	itemID := EventItemID(event.SessionID, "question", fullDetail)
	if a.slotBlocksNewGate(stateDir, event.SessionID, itemID) {
		return
	}
	a.openItem(stateDir, event, "question", fullDetail, gateContent{
		questions: questions,
		clearedBy: &clearedBy{
			Event: eventList{"PostToolUse", "PostToolUseFailure", "SessionEnd"},
			Tool:  "AskUserQuestion",
		},
	})
}

// notification opens the gate a notification describes.
//
// ⚠️ Every notification kind is guarded by gateIsOpen: the harness emits a
// Notification for a gate it already announced with PermissionRequest, and a
// second item for the same ask is a duplicate the operator has to dismiss.
func (a *App) notification(stateDir string, event payload) {
	kind, ok := notificationKind(event.NotificationType)
	if !ok {
		return
	}
	if a.gateIsOpen(stateDir, event.SessionID) {
		return
	}
	detail := scrubDetail(event.Message)
	if detail == "" {
		detail = event.NotificationType
	}
	a.openItem(stateDir, event, kind, detail, notificationContent(kind))
}

// notificationKind maps a `notification_type` to an item kind.
func notificationKind(notificationType string) (string, bool) {
	switch notificationType {
	case "permission_prompt":
		return "permission", true
	case "elicitation_dialog", "elicitation_url_dialog", "agent_needs_input":
		return "question", true
	case "idle_prompt":
		return "idle", true
	default:
		return "", false
	}
}

// notificationContent returns the cleared_by vocabulary for a notification
// kind.
func notificationContent(kind string) gateContent {
	switch kind {
	case "permission":
		return gateContent{
			context: "Claude Code is waiting for permission to continue.",
			clearedBy: &clearedBy{
				Event: eventList{"PreToolUse", "PostToolUse", "PostToolUseFailure", "SessionEnd"},
			},
		}
	case "question":
		return gateContent{
			clearedBy: &clearedBy{
				Event: eventList{"PostToolUse", "PostToolUseFailure", "SessionEnd"},
			},
		}
	default:
		return gateContent{
			clearedBy: &clearedBy{Event: eventList{"UserPromptSubmit", "SessionEnd"}},
		}
	}
}

// openItem writes the open item and appends its open line.
//
// A prior open item with a DIFFERENT id is superseded first: a close line with
// `closed_by:"SupersededByNewItem"`, then the new open. A prior item with the
// SAME id is the same ask repeated, and folding it keeps one card.
func (a *App) openItem(
	stateDir string,
	event payload,
	kind, fullDetail string,
	content gateContent,
) {
	itemID := EventItemID(event.SessionID, kind, fullDetail)
	if prior, ok := readOpenItem(stateDir, event.SessionID); ok {
		if foldRepeatedPermission(prior.ItemID, itemID) {
			return
		}
		a.supersede(stateDir, event.SessionID, prior)
	}
	record := openItemRecord{
		ItemID:     itemID,
		SessionID:  event.SessionID,
		Kind:       kind,
		Detail:     truncateRunes(fullDetail, toolDetailLimit),
		ToolName:   event.ToolName,
		Context:    content.context,
		Pane:       a.ownedPane(event),
		Cwd:        event.Cwd,
		Transcript: event.TranscriptPath,
		Host:       a.hostname(),
		AgentID:    event.AgentID,
		TS:         a.now(),
		Event:      event.HookEventName,
		ClearedBy:  content.clearedBy,
	}
	if kind == "question" {
		if len(content.questions) == 1 {
			record.Options = content.questions[0].Options
			record.AnswerCardinality = content.questions[0].Cardinality
		} else if len(content.questions) > 1 {
			record.Questions = content.questions
		}
	}
	writeJSONAtomic(stateDir, openPath(event.SessionID), record)
	appendEvent(stateDir, event.SessionID, openEvent{openItemRecord: record, Type: "open"})
}

// supersede closes a prior open item that a new ask replaces.
func (a *App) supersede(stateDir, sessionID string, prior openItem) {
	record := map[string]any{
		"item_id":    prior.ItemID,
		"session_id": sessionID,
		"ts":         a.now(),
		"closed_by":  supersededByNewItem,
	}
	if session, kind, attributed := a.answerer(stateDir, sessionID); attributed {
		record["answerer_session"] = session
		record["answerer_kind"] = kind
	}
	appendEvent(stateDir, sessionID, record)
	removeStateFile(stateDir, openPath(sessionID))
}

// foldRepeatedPermission reports whether the new ask is the same item as the
// one already open. The id is the dedup key, so equality is the whole test.
func foldRepeatedPermission(priorItemID, itemID string) bool {
	return priorItemID != "" && priorItemID == itemID
}

// slotBlocksNewGate reports whether the open item already holds this slot.
func (a *App) slotBlocksNewGate(stateDir, sessionID, itemID string) bool {
	prior, ok := readOpenItem(stateDir, sessionID)
	return ok && prior.ItemID == itemID
}

// gateIsOpen reports whether the session already has an open item.
func (a *App) gateIsOpen(stateDir, sessionID string) bool {
	_, ok := readOpenItem(stateDir, sessionID)
	return ok
}

// ownedPane returns the pane id when the pane's title proves it belongs to this
// session, and "" when it does not.
//
// The rule is the reader's (pkg.OwnsPane): a pane id is a lease WezTerm
// renumbers and reuses, so existence proves nothing and the title is what
// proves ownership. A mismatch is required to eliminate — when either side has
// no name, ownership is unprovable and the pane is kept rather than stripped.
// The listing is consulted only here, on an item-opening event.
func (a *App) ownedPane(event payload) string {
	pane := a.paneOf(event)
	if pane == "" {
		return ""
	}
	id, err := strconv.Atoi(pane)
	if err != nil {
		return ""
	}
	if a.Panes == nil {
		return pane
	}
	titles, err := a.Panes.ListPanes()
	if err != nil {
		glog.V(3).Infof("attention-log: list panes failed: %v", err)
		return pane
	}
	title, ok := titles[id]
	if !ok {
		return ""
	}
	name := a.sessionName(event.SessionID)
	if name == "" || title == "" {
		return pane
	}
	if stripStatusGlyph(title) == stripStatusGlyph(name) {
		return pane
	}
	return ""
}

// sessionName reads the registry's current name for a session, or "".
func (a *App) sessionName(sessionID string) string {
	dir := a.getenv("SESSIONS_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".claude", "sessions")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var record struct {
			SessionID string `json:"sessionId"`
			Name      string `json:"name"`
		}
		if err := json.Unmarshal(content, &record); err != nil {
			continue
		}
		if record.SessionID == sessionID {
			return record.Name
		}
	}
	return ""
}

// stripStatusGlyph removes Claude Code's leading status glyph from a name,
// mirroring pkg.StripStatusGlyph (and the Python reader it mirrors). It is
// duplicated rather than imported so this leaf package does not depend on the
// store package.
func stripStatusGlyph(text string) string {
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

// hostname returns the machine name the record carries.
func (a *App) hostname() string {
	if a.Hostname != nil {
		return a.Hostname()
	}
	host, err := os.Hostname()
	if err != nil {
		return ""
	}
	return host
}

// questionDetail renders the question payloads as the item's detail, falling
// back to the tool input when the payload carries no question text.
func questionDetail(event payload, questions []question) string {
	parts := make([]string, 0, len(questions))
	for _, q := range questions {
		if q.Payload != "" {
			parts = append(parts, q.Payload)
		}
	}
	text := strings.Join(parts, "\n")
	if text == "" {
		text = event.ToolName + ": " + toolDetail(event.ToolInput)
	}
	return scrubDetail(text)
}

// questionsFrom parses an AskUserQuestion tool input into question units.
func questionsFrom(input json.RawMessage) []question {
	object := toolInputObject(input)
	if object == nil {
		return nil
	}
	raw, ok := object["questions"].([]any)
	if !ok {
		return nil
	}
	out := make([]question, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		cardinality := "single"
		if multi, _ := entry["multiSelect"].(bool); multi {
			cardinality = "multiple"
		}
		out = append(out, question{
			Tab:         stringValue(entry["header"]),
			Payload:     stringValue(entry["question"]),
			Cardinality: cardinality,
			Options:     answerOptions(entry["options"], cardinality),
		})
	}
	return out
}

// answerOptions parses an option list, enforcing the first-only recommendation
// rule on a single-pick question and leaving a multi-pick one unconstrained.
func answerOptions(raw any, cardinality string) []option {
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]option, 0, len(list))
	seenRecommended := false
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		parsed := answerOption(entry)
		if parsed.Recommended {
			if cardinality != "multiple" && seenRecommended {
				parsed.Recommended = false
			} else {
				seenRecommended = true
			}
		}
		out = append(out, parsed)
	}
	return out
}

// answerOption parses one option, stripping a `(Recommended)` marker from the
// label and recording it as the flag.
func answerOption(entry map[string]any) option {
	label := stringValue(entry["label"])
	recommended := recommendedPattern.MatchString(label)
	if recommended {
		label = strings.TrimSpace(recommendedPattern.ReplaceAllString(label, " "))
	}
	return option{
		Label:       label,
		Description: stringValue(entry["description"]),
		Recommended: recommended,
	}
}

// permissionContext describes what the tool wants and what denying it means.
func permissionContext(tool string, input json.RawMessage) string {
	return permissionWhat(tool, input) + " Denying " + permissionDenyMeaning(tool)
}

// permissionWhat renders the tool's action in one sentence.
func permissionWhat(tool string, input json.RawMessage) string {
	object := toolInputObject(input)
	switch tool {
	case "Bash":
		if command := stringValue(object["command"]); command != "" {
			return "Run `" + command + "`."
		}
	case "Read", "Write", "Edit", "NotebookEdit":
		if path := stringValue(object["file_path"]); path != "" {
			return "Access `" + path + "`."
		}
	case "WebFetch":
		if url := stringValue(object["url"]); url != "" {
			return "Fetch `" + url + "`."
		}
	}
	return "Use the " + tool + " tool."
}

// permissionDenyMeaning describes the consequence of denying the tool.
func permissionDenyMeaning(tool string) string {
	switch tool {
	case "Bash":
		return "means the command does not run."
	case "Read":
		return "means the file is not read."
	case "Write", "Edit", "NotebookEdit":
		return "means the file is left unchanged."
	case "WebFetch", "WebSearch":
		return "means the request is not made."
	default:
		return "means the tool call is blocked."
	}
}

// toolInputObject decodes a tool_input into its object form, or nil.
func toolInputObject(input json.RawMessage) map[string]any {
	if len(input) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(input, &value); err != nil {
		return nil
	}
	object, _ := value.(map[string]any)
	return object
}

// stringValue renders a decoded JSON scalar as a string, or "".
func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}
