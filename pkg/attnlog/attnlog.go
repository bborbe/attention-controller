// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package attnlog is the compiled replacement for the attention-log Claude Code
// hook. It reads one hook payload from stdin and writes the attention event
// store: `<sid>.events.jsonl`, `<sid>.open.json` and `<sid>.tool.json`.
//
// ⚠️ Behaviour parity with `~/.claude/hooks/attention-log.py` is the contract.
// This package implements the store layer, redaction, the dedup key, the
// per-tool-call hot path, the gate path (`PermissionRequest`, `Notification`,
// and `AskUserQuestion`), and the `Stop` closer walker plus the
// `UserPromptSubmit` system-envelope branch.
package attnlog

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"

	"github.com/golang/glog"
)

const (
	// answerTTL is how long an answer marker stays attributable.
	answerTTL = 300.0
	// answererEpoch is the instant after which every close carries an answerer
	// attribution, falling back to `no_answerer` when no marker names one.
	answererEpoch = 1764000000.0
)

// App is the hook. Its dependencies are injectable so a spec can freeze the
// clock, point the writer at a temp state directory and stub the pane listing.
type App struct {
	// Now returns the wall clock in epoch seconds.
	Now func() float64
	// Getenv reads the process environment.
	Getenv func(string) string
	// Panes lists the live WezTerm panes for the owned-pane check. It is
	// consulted only on an item-opening event; nil leaves ownership unprovable
	// and keeps the recorded pane.
	Panes PaneLister
	// Hostname returns the machine name the open item records. It defaults to
	// os.Hostname.
	Hostname func() string
}

// NewApp creates the hook with the process clock and environment.
func NewApp() *App {
	return &App{Now: nowSeconds, Getenv: os.Getenv}
}

// nowSeconds is the default clock.
func nowSeconds() float64 {
	return float64(time.Now().UnixNano()) / float64(time.Second)
}

// now returns the injected clock, or the process clock when none was set.
func (a *App) now() float64 {
	if a.Now != nil {
		return a.Now()
	}
	return nowSeconds()
}

// getenv returns the injected environment, or the process environment.
func (a *App) getenv(key string) string {
	if a.Getenv != nil {
		return a.Getenv(key)
	}
	return os.Getenv(key)
}

// payload is the subset of a Claude Code hook payload the hot path reads.
type payload struct {
	SessionID        string          `json:"session_id"`
	HookEventName    string          `json:"hook_event_name"`
	TranscriptPath   string          `json:"transcript_path"`
	Cwd              string          `json:"cwd"`
	ToolName         string          `json:"tool_name"`
	ToolInput        json.RawMessage `json:"tool_input"`
	AgentID          string          `json:"agent_id"`
	Pane             json.RawMessage `json:"pane"`
	NotificationType string          `json:"notification_type"`
	Message          string          `json:"message"`
	// LastAssistantMessage is the final assistant text a Stop payload carries.
	LastAssistantMessage string `json:"last_assistant_message"`
	// Prompt is the user turn a UserPromptSubmit payload carries.
	Prompt string `json:"prompt"`
}

// Handle reads one payload and writes the store.
//
// ⚠️ It never returns an error for malformed or absent input: the hook always
// exits 0 so a bad payload can never block a tool call. Every event is handled;
// there is no unsupported-event sentinel.
func (a *App) Handle(reader io.Reader) error {
	raw, err := io.ReadAll(reader)
	if err != nil {
		glog.V(3).Infof("attention-log: read stdin failed: %v", err)
		return nil
	}
	var event payload
	if err := json.Unmarshal(raw, &event); err != nil {
		glog.V(3).Infof("attention-log: decode payload failed: %v", err)
		return nil
	}
	if !validSessionID(event.SessionID) {
		// A missing or path-bearing session id writes nothing.
		return nil
	}
	stateDir := a.stateDir()
	if stateDir == "" {
		return nil
	}

	// ⚠️ closeOpen runs FIRST, before dispatch and before any unknown-event
	// check, so an open item is answered even by an event this build does not
	// know yet.
	a.closeOpen(stateDir, event)

	switch event.HookEventName {
	case "PreToolUse":
		a.preToolUse(stateDir, event)
		return nil
	case "PostToolUse", "PostToolUseFailure":
		a.clearToolMarker(stateDir, event)
		return nil
	case "SessionEnd":
		a.clearToolMarker(stateDir, event)
		return nil
	case "PermissionRequest":
		a.permissionRequest(stateDir, event)
		return nil
	case "Notification":
		a.notification(stateDir, event)
		return nil
	case "Stop":
		a.stop(stateDir, event)
		return nil
	case "UserPromptSubmit":
		// closeOpen already ran; the envelope branch is part of it, and a
		// non-envelope turn has no other work to do.
		return nil
	default:
		// Unknown or absent hook_event_name: closeOpen already ran, nothing
		// else to do.
		return nil
	}
}

// preToolUse writes the tool marker for the running tool, then opens the
// question item an AskUserQuestion call represents.
func (a *App) preToolUse(stateDir string, event payload) {
	detail := truncateRunes(
		scrubDetail(event.ToolName+": "+toolDetail(event.ToolInput)),
		toolDetailLimit,
	)
	marker := toolMarker{
		SessionID:  event.SessionID,
		Pane:       a.paneOf(event),
		Cwd:        event.Cwd,
		Kind:       "tool",
		Event:      "PreToolUse",
		Transcript: event.TranscriptPath,
		Tool:       event.ToolName,
		Detail:     detail,
		TS:         a.now(),
		State:      "open",
		ClearedBy:  nil,
	}
	writeJSONAtomic(stateDir, toolPath(event.SessionID), marker)
	if event.ToolName == "AskUserQuestion" {
		a.openQuestionGate(stateDir, event)
	}
}

// clearToolMarker removes the tool marker when it belongs to the finishing tool.
// An empty event tool name (the SessionEnd case) clears unconditionally.
func (a *App) clearToolMarker(stateDir string, event payload) {
	marker, ok := readToolMarker(stateDir, event.SessionID)
	if !ok {
		return
	}
	if event.ToolName != "" && marker.Tool != event.ToolName {
		// Another tool is still running; keep its marker.
		return
	}
	removeStateFile(stateDir, toolPath(event.SessionID))
}

// paneOf returns the pane the payload carries, falling back to the mux's own
// environment variable.
func (a *App) paneOf(event payload) string {
	if pane := stringField(event.Pane); pane != "" {
		return pane
	}
	return a.paneFromEnv()
}

// paneFromEnv reads WEZTERM_PANE.
func (a *App) paneFromEnv() string {
	return a.getenv("WEZTERM_PANE")
}

// toolDetail renders the human-readable part of tool_input.
func toolDetail(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var value any
	if err := json.Unmarshal(input, &value); err != nil {
		return ""
	}
	object, ok := value.(map[string]any)
	if !ok {
		// A non-object tool_input is rendered as compact JSON rather than
		// dropped.
		return compactJSON(value)
	}
	for _, key := range []string{
		"command", "file_path", "path", "url", "pattern", "query", "description", "prompt",
	} {
		if text, ok := object[key].(string); ok && text != "" {
			return text
		}
	}
	return compactJSON(object)
}

// compactJSON renders a value as compact JSON without HTML escaping.
func compactJSON(value any) string {
	body, err := marshalJSON(value)
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(body), "\n")
}
