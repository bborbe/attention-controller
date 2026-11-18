// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package attnlog

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
)

const (
	// stateDirEnv names the directory the hook writes its state into.
	stateDirEnv = "ATTENTION_STATE_DIR"
	// defaultStateDirSuffix is appended to the home directory when the env var
	// is unset, matching the Python writer.
	defaultStateDirSuffix = ".claude/state/attention"

	eventsSuffix = ".events.jsonl"
	openSuffix   = ".open.json"
	toolSuffix   = ".tool.json"
	answerSuffix = ".answer.json"
	postedSuffix = ".posted.json"

	// suppressedClosersFile is the global log of closers the Stop branch
	// deliberately did not card. The unforwarded-closer diagnostic reads it so a
	// deliberate idle never reads as a lost gate.
	suppressedClosersFile = "suppressed.jsonl"

	// toolDetailLimit is the maximum detail length in code points.
	toolDetailLimit = 4000
)

// toolMarker is the shape of `<sid>.tool.json`, written by PreToolUse and
// cleared by the finishing tool or SessionEnd.
type toolMarker struct {
	SessionID  string  `json:"session_id"`
	Pane       string  `json:"pane"`
	Cwd        string  `json:"cwd"`
	Kind       string  `json:"kind"`
	Event      string  `json:"event"`
	Transcript string  `json:"transcript"`
	Tool       string  `json:"tool"`
	Detail     string  `json:"detail"`
	TS         float64 `json:"ts"`
	State      string  `json:"state"`
	// ClearedBy is always present and null on a freshly written marker.
	ClearedBy any `json:"cleared_by"`
}

// clearedBy is the answer vocabulary carried by an open item.
type clearedBy struct {
	// Event is a string or a list of strings; see eventList.
	Event eventList `json:"event"`
	Tool  string    `json:"tool"`
}

// eventList accepts either a JSON string or a JSON array of strings.
type eventList []string

// UnmarshalJSON accepts a string or a list.
func (e *eventList) UnmarshalJSON(data []byte) error {
	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		*e = eventList{asString}
		return nil
	}
	var asList []string
	if err := json.Unmarshal(data, &asList); err != nil {
		return err
	}
	*e = asList
	return nil
}

// openItem is the shape of `<sid>.open.json`, as far as the hot path reads it.
type openItem struct {
	ItemID    string     `json:"item_id"`
	SessionID string     `json:"session_id"`
	Kind      string     `json:"kind"`
	Event     string     `json:"event"`
	ToolName  string     `json:"tool_name"`
	AgentID   string     `json:"agent_id"`
	ClearedBy *clearedBy `json:"cleared_by"`
}

// answerMarker is the shape of `<sid>.answer.json`, written by the answering
// agent so a close can attribute itself.
type answerMarker struct {
	SessionID string  `json:"session_id"`
	Session   string  `json:"session"`
	Kind      string  `json:"kind"`
	TS        float64 `json:"ts"`
}

// stateDir resolves the state directory from the injected environment.
func (a *App) stateDir() string {
	if dir := a.getenv(stateDirEnv); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, defaultStateDirSuffix)
}

// validSessionID rejects a session id that would escape the state directory.
//
// ⚠️ The Python writer does not validate this; the Go writer does, deliberately.
// The id is producer-supplied and is used to build file names, so a value
// containing a separator or a parent traversal writes nothing and exits 0.
func validSessionID(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	if strings.ContainsAny(sessionID, `/\`) {
		return false
	}
	return !strings.Contains(sessionID, "..")
}

// eventsPath is the append-only log name.
func eventsPath(sessionID string) string { return sessionID + eventsSuffix }

// openPath is the open-item name.
func openPath(sessionID string) string { return sessionID + openSuffix }

// toolPath is the tool-marker name.
func toolPath(sessionID string) string { return sessionID + toolSuffix }

// answerPath is the answer-marker name.
func answerPath(sessionID string) string { return sessionID + answerSuffix }

// postedCardPath is the posted-card marker name.
func postedCardPath(sessionID string) string { return sessionID + postedSuffix }

// marshalJSON encodes v with HTML escaping disabled so `<`, `>` and `&` survive
// a detail untouched. The returned bytes end with a newline.
func marshalJSON(v any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(v); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// appendEvent appends one JSON object as a line to the session's event log.
func appendEvent(stateDir, sessionID string, record any) {
	appendStateLine(stateDir, eventsPath(sessionID), record)
}

// appendStateLine appends one JSON object as a line to a file in the state dir.
// It is the shared body of appendEvent and the global suppression log.
//
// It is the fail-open wrapper the hook path needs: a hook must exit 0 whatever
// the filesystem does. Callers that need the failure — the store's expiry
// closer, whose whole point is that the line lands — use appendStateLineErr.
func appendStateLine(stateDir, name string, record any) {
	// ⚠️ The hook path carries no context: `Handle(io.Reader)` is a one-shot CLI
	// call that must finish whatever happens, so cancellation has nothing to
	// honour here. This is the one place a background context is correct.
	if err := appendStateLineErr(context.Background(), stateDir, name, record); err != nil {
		glog.V(3).Infof("attention-log: append %s failed: %v", name, err)
	}
}

// appendStateLineErr appends one JSON object as a line to a file in the state
// dir and returns what went wrong instead of logging it.
func appendStateLineErr(ctx context.Context, stateDir, name string, record any) error {
	body, err := marshalJSON(record)
	if err != nil {
		return errors.Wrap(ctx, err, "marshal event failed")
	}
	if err := os.MkdirAll(stateDir, 0o750); err != nil {
		return errors.Wrap(ctx, err, "create state dir failed")
	}
	root, err := os.OpenRoot(stateDir)
	if err != nil {
		return errors.Wrap(ctx, err, "open state dir failed")
	}
	defer root.Close()
	file, err := root.OpenFile(
		name,
		os.O_CREATE|os.O_APPEND|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return errors.Wrap(ctx, err, "open event log failed")
	}
	defer file.Close()
	if _, err := file.Write(body); err != nil {
		return errors.Wrap(ctx, err, "append event failed")
	}
	return nil
}

// writeJSONAtomic writes v through a temp file and a rename, so a reader never
// sees a half-written marker.
func writeJSONAtomic(stateDir, name string, v any) {
	body, err := marshalJSON(v)
	if err != nil {
		glog.V(3).Infof("attention-log: marshal %s failed: %v", name, err)
		return
	}
	if err := os.MkdirAll(stateDir, 0o750); err != nil {
		glog.V(3).Infof("attention-log: create state dir failed: %v", err)
		return
	}
	temp, err := os.CreateTemp(stateDir, name+".tmp-*")
	if err != nil {
		glog.V(3).Infof("attention-log: create temp for %s failed: %v", name, err)
		return
	}
	tempName := temp.Name()
	if _, err := temp.Write(body); err != nil {
		_ = temp.Close()
		_ = os.Remove(tempName)
		glog.V(3).Infof("attention-log: write temp for %s failed: %v", name, err)
		return
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempName)
		glog.V(3).Infof("attention-log: close temp for %s failed: %v", name, err)
		return
	}
	if err := os.Rename(tempName, filepath.Join(stateDir, name)); err != nil {
		_ = os.Remove(tempName)
		glog.V(3).Infof("attention-log: rename %s failed: %v", name, err)
	}
}

// readJSON reads and decodes one state file through the confined root.
func readJSON(stateDir, name string, target any) bool {
	root, err := os.OpenRoot(stateDir)
	if err != nil {
		return false
	}
	defer root.Close()
	file, err := root.Open(name)
	if err != nil {
		return false
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(target); err != nil {
		return false
	}
	return true
}

// removeStateFile removes one state file, ignoring an absent one.
func removeStateFile(stateDir, name string) {
	root, err := os.OpenRoot(stateDir)
	if err != nil {
		return
	}
	defer root.Close()
	_ = root.Remove(name)
}

// readOpenItem returns the open item for a session, or false when absent.
func readOpenItem(stateDir, sessionID string) (openItem, bool) {
	var item openItem
	if !readJSON(stateDir, openPath(sessionID), &item) {
		return openItem{}, false
	}
	return item, true
}

// readToolMarker returns the tool marker for a session, or false when absent.
func readToolMarker(stateDir, sessionID string) (toolMarker, bool) {
	var marker toolMarker
	if !readJSON(stateDir, toolPath(sessionID), &marker) {
		return toolMarker{}, false
	}
	return marker, true
}

// readAnswerMarker returns the answer marker for a session, or false when absent
// or unreadable.
func readAnswerMarker(stateDir, sessionID string) (answerMarker, bool) {
	var marker answerMarker
	if !readJSON(stateDir, answerPath(sessionID), &marker) {
		return answerMarker{}, false
	}
	return marker, true
}

// truncateRunes truncates text to at most limit code points.
func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// stringField renders a JSON scalar as a string: a string verbatim, a number
// without a trailing decimal, and anything else as the empty string.
func stringField(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString
	}
	var asNumber json.Number
	if err := json.Unmarshal(raw, &asNumber); err == nil {
		return asNumber.String()
	}
	return ""
}
