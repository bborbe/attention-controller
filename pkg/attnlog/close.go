// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package attnlog

// closeOpen answers the session's open item, when this event may answer it.
//
// The rules, in order:
//   - SessionEnd bypasses the cleared_by.event filter: a dead process clears
//     whatever it held.
//   - Any other event must be named by cleared_by.event (a string or a list);
//     an absent event is answered by any event.
//   - The tool checks fail open: a non-empty cleared_by.tool or opener
//     tool_name that differs from the event's tool blocks, but an empty one
//     does not.
//   - agent_id is compared exactly: an absent opener agent_id means the main
//     thread, so it blocks a present event agent_id.
//
// An answered item appends a close line and removes `.open.json`.
func (a *App) closeOpen(stateDir string, event payload) {
	item, ok := readOpenItem(stateDir, event.SessionID)
	if !ok {
		return
	}
	if !answered(item, event) {
		return
	}

	record := map[string]any{
		"item_id":    item.ItemID,
		"session_id": event.SessionID,
		"ts":         a.now(),
		"closed_by":  event.HookEventName,
		"tool_name":  event.ToolName,
	}
	if session, kind, attributed := a.answerer(stateDir, event.SessionID); attributed {
		record["answerer_session"] = session
		record["answerer_kind"] = kind
	}
	appendEvent(stateDir, event.SessionID, record)
	removeStateFile(stateDir, openPath(event.SessionID))
}

// answered reports whether this event may answer the open item.
func answered(item openItem, event payload) bool {
	if event.HookEventName != "SessionEnd" && !clearedByEvent(item.ClearedBy, event.HookEventName) {
		return false
	}
	clearedTool := ""
	if item.ClearedBy != nil {
		clearedTool = item.ClearedBy.Tool
	}
	if clearedTool != "" && clearedTool != event.ToolName {
		return false
	}
	if item.ToolName != "" && item.ToolName != event.ToolName {
		return false
	}
	// Exact comparison: absent on both sides means the main thread and passes;
	// absent opener against a present event agent blocks.
	return item.AgentID == event.AgentID
}

// clearedByEvent reports whether the event name is in the item's cleared_by
// vocabulary. An absent or empty vocabulary is answered by any event.
func clearedByEvent(cleared *clearedBy, eventName string) bool {
	if cleared == nil || len(cleared.Event) == 0 {
		return true
	}
	for _, allowed := range cleared.Event {
		if allowed == eventName {
			return true
		}
	}
	return false
}

// answerer resolves who answered the item, if the record can say.
//
// A fresh answer marker names the answerer. After the answerer epoch, a close
// with no fresh marker still records `no_answerer` rather than omitting the
// fields. Before the epoch and with no marker, nothing is recorded.
func (a *App) answerer(stateDir, sessionID string) (string, string, bool) {
	now := a.now()
	if session, kind, ok := a.markerAnswerer(stateDir, sessionID, now); ok {
		return session, kind, true
	}
	if now >= answererEpoch {
		return "", "no_answerer", true
	}
	return "", "", false
}

// markerAnswerer returns the answerer a fresh marker names, or false.
func (a *App) markerAnswerer(stateDir, sessionID string, now float64) (string, string, bool) {
	marker, ok := readAnswerMarker(stateDir, sessionID)
	if !ok {
		return "", "", false
	}
	if marker.TS > 0 && now-marker.TS > answerTTL {
		return "", "", false
	}
	session := marker.SessionID
	if session == "" {
		session = marker.Session
	}
	if session == "" && marker.Kind == "" {
		return "", "", false
	}
	return session, marker.Kind, true
}
