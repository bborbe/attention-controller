// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command attnlog-parity drives the Go writer and a reconstructed Python
// reference writer over the same payloads and deep-equals the parsed store
// records.
//
// ⚠️ The original `~/.claude/hooks/attention-log.py` is not present in the
// build container; `reference.py` beside this file reconstructs the hot path
// from spec 020 and the Go readers. The harness is still a real
// two-implementation differential, but it is not a differential against the
// deleted original — see the prompt's Improvements section.
//
// It exits 0 when every fixture and every randomized payload agree, and
// non-zero with a diff when one does not.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/bborbe/attention-controller/pkg/attnlog"
)

const frozenNow = 1800000000.0

type fixture struct {
	name    string
	payload string
	seed    map[string]string
}

func main() {
	randomCount := flag.Int("random", 100, "number of randomized payloads per event")
	reference := flag.String(
		"reference",
		"hack/attnlog-parity/reference.py",
		"python reference writer",
	)
	flag.Parse()

	checker := &checker{reference: *reference}
	for _, item := range fixtures() {
		if err := checker.compare(item.payload, item.seed); err != nil {
			fmt.Fprintf(os.Stderr, "fixture %s: %v\n", item.name, err)
			os.Exit(1)
		}
	}
	for index, item := range randomized(*randomCount) {
		if err := checker.compare(item.payload, item.seed); err != nil {
			fmt.Fprintf(os.Stderr, "random %d: %v\npayload: %s\n", index, err, item.payload)
			os.Exit(1)
		}
	}
	fmt.Printf("parity ok: %d fixtures, %d randomized payloads\n", len(fixtures()), *randomCount)
}

type checker struct {
	reference string
}

func (c *checker) compare(payload string, seed map[string]string) error {
	goDir, err := os.MkdirTemp("", "attnlog-go-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(goDir)
	pyDir, err := os.MkdirTemp("", "attnlog-py-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(pyDir)

	for file, body := range seed {
		if err := os.WriteFile(filepath.Join(goDir, file), []byte(body), 0o600); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(pyDir, file), []byte(body), 0o600); err != nil {
			return err
		}
	}

	app := &attnlog.App{
		Now: func() float64 { return frozenNow },
		Getenv: func(key string) string {
			switch key {
			case "ATTENTION_STATE_DIR":
				return goDir
			case "WEZTERM_PANE":
				return "77"
			default:
				return ""
			}
		},
	}
	// The sentinel is the one error the binary reports; parity compares the
	// store, not the exit code.
	_ = app.Handle(strings.NewReader(payload))

	//nolint:gosec // the reference path is a harness flag, not untrusted input.
	command := exec.Command("python3", c.reference)
	command.Stdin = strings.NewReader(payload)
	command.Env = append(os.Environ(),
		"ATTENTION_STATE_DIR="+pyDir,
		"ATTENTION_NOW=1800000000",
		"WEZTERM_PANE=77",
	)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("python reference failed: %v: %s", err, output)
	}

	goFiles, err := storeFiles(goDir)
	if err != nil {
		return err
	}
	pyFiles, err := storeFiles(pyDir)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(goFiles, pyFiles) {
		return fmt.Errorf("store differs\n  go: %s\n  py: %s", describe(goFiles), describe(pyFiles))
	}
	return nil
}

// storeFiles parses every store file into comparable values.
func storeFiles(dir string) (map[string]any, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		value, err := parseStoreFile(filepath.Join(dir, entry.Name()), entry.Name())
		if err != nil {
			return nil, err
		}
		out[entry.Name()] = value
	}
	return out, nil
}

// parseStoreFile parses one file, treating an event log as a list of lines.
func parseStoreFile(path, name string) (any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(name, ".events.jsonl") {
		return parseEventLines(raw)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}

// parseEventLines parses one JSON object per line.
func parseEventLines(raw []byte) ([]any, error) {
	lines := make([]any, 0)
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var value any
		if err := json.Unmarshal([]byte(line), &value); err != nil {
			return nil, err
		}
		lines = append(lines, value)
	}
	return lines, nil
}

func describe(files map[string]any) string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		body, _ := json.Marshal(files[name])
		parts = append(parts, name+"="+string(body))
	}
	return strings.Join(parts, " ")
}

func fixtures() []fixture {
	items := hotPathFixtures()
	items = append(items, closeFixtures()...)
	return append(items, gateFixtures()...)
}

func gateFixtures() []fixture {
	return []fixture{
		{
			name: "permission-request",
			payload: `{"session_id":"s1","hook_event_name":"PermissionRequest","tool_name":"Bash",` +
				`"tool_input":{"command":"rm -rf build"},"cwd":"/w","transcript_path":"/t.jsonl","pane":42}`,
		},
		{
			name: "permission-request-read",
			payload: `{"session_id":"s1","hook_event_name":"PermissionRequest","tool_name":"Read",` +
				`"tool_input":{"file_path":"/etc/hosts"}}`,
		},
		{
			name: "permission-request-ask-user-question",
			payload: `{"session_id":"s1","hook_event_name":"PermissionRequest",` +
				`"tool_name":"AskUserQuestion","tool_input":{"questions":[` +
				`{"header":"Pick one","question":"Which database?","multiSelect":false,` +
				`"options":[{"label":"(Recommended) Postgres","description":"managed"},` +
				`{"label":"SQLite"}]}]}}`,
		},
		{
			name: "pre-tool-use-ask-user-question",
			payload: `{"session_id":"s1","hook_event_name":"PreToolUse",` +
				`"tool_name":"AskUserQuestion","tool_input":{"questions":[` +
				`{"header":"A","question":"First?","multiSelect":true,` +
				`"options":[{"label":"(Recommended) One"},{"label":"(Recommended) Two"}]},` +
				`{"header":"B","question":"Second?","options":[{"label":"Yes"}]}]}}`,
		},
		{
			name: "notification-permission",
			payload: `{"session_id":"s1","hook_event_name":"Notification",` +
				`"notification_type":"permission_prompt","message":"Claude needs permission to run Bash"}`,
		},
		{
			name: "notification-question",
			payload: `{"session_id":"s1","hook_event_name":"Notification",` +
				`"notification_type":"elicitation_dialog","message":"Claude is asking a question"}`,
		},
		{
			name: "notification-idle",
			payload: `{"session_id":"s1","hook_event_name":"Notification",` +
				`"notification_type":"idle_prompt","message":"Claude is idle"}`,
		},
		{
			name: "notification-blocks-open-gate",
			payload: `{"session_id":"s1","hook_event_name":"Notification",` +
				`"notification_type":"permission_prompt","message":"Claude needs permission"}`,
			seed: map[string]string{
				"s1.open.json": `{"item_id":"i1","session_id":"s1","kind":"question",` +
					`"cleared_by":{"event":"Stop"}}`,
			},
		},
		{
			name: "supersede",
			payload: `{"session_id":"s1","hook_event_name":"PermissionRequest","tool_name":"Read",` +
				`"tool_input":{"file_path":"/tmp/a"}}`,
			seed: map[string]string{
				"s1.open.json": `{"item_id":"i1","session_id":"s1","kind":"permission",` +
					`"cleared_by":{"event":"Stop"}}`,
			},
		},
		{
			name: "permission-request-closed-by-pre-tool-use",
			payload: `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash",` +
				`"tool_input":{"command":"ls"}}`,
			seed: map[string]string{
				"s1.open.json": `{"item_id":"i1","session_id":"s1","kind":"permission",` +
					`"tool_name":"Bash","cleared_by":{"event":["PreToolUse"],"tool":"Bash"}}`,
			},
		},
	}
}

func hotPathFixtures() []fixture {
	return []fixture{
		{
			name: "pre-tool-use",
			payload: `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash",` +
				`"tool_input":{"command":"ls"},"cwd":"/w","transcript_path":"/t.jsonl","pane":42}`,
		},
		{
			name: "pre-tool-use-credential",
			payload: `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash",` +
				`"tool_input":{"command":"export TOKEN=abc Authorization: Bearer xyz"}}`,
		},
		{
			name: "pre-tool-use-unknown-input",
			payload: `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Mystery",` +
				`"tool_input":{"unknown":"value"}}`,
		},
		{
			name: "pre-tool-use-unicode",
			payload: `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash",` +
				`"tool_input":{"command":"echo ä < b & c > d"}}`,
		},
		{
			name:    "post-tool-use-owner",
			payload: `{"session_id":"s1","hook_event_name":"PostToolUse","tool_name":"Bash"}`,
			seed:    map[string]string{"s1.tool.json": `{"session_id":"s1","tool":"Bash"}`},
		},
		{
			name:    "post-tool-use-other",
			payload: `{"session_id":"s1","hook_event_name":"PostToolUse","tool_name":"Read"}`,
			seed:    map[string]string{"s1.tool.json": `{"session_id":"s1","tool":"Bash"}`},
		},
		{
			name:    "post-tool-use-failure",
			payload: `{"session_id":"s1","hook_event_name":"PostToolUseFailure"}`,
			seed:    map[string]string{"s1.tool.json": `{"session_id":"s1","tool":"Bash"}`},
		},
		{
			name:    "session-end",
			payload: `{"session_id":"s1","hook_event_name":"SessionEnd"}`,
			seed: map[string]string{
				"s1.tool.json": `{"session_id":"s1","tool":"Bash"}`,
				"s1.open.json": `{"item_id":"i1","session_id":"s1","kind":"question","tool_name":"Bash"}`,
			},
		},
	}
}

func closeFixtures() []fixture {
	return []fixture{
		{
			name:    "close-by-event-list",
			payload: `{"session_id":"s1","hook_event_name":"PostToolUse","tool_name":"Bash"}`,
			seed: map[string]string{
				"s1.open.json": `{"item_id":"i1","session_id":"s1","kind":"permission",` +
					`"tool_name":"Bash","cleared_by":{"event":["PostToolUse"]}}`,
			},
		},
		{
			name:    "close-blocked-by-event",
			payload: `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{}}`,
			seed: map[string]string{
				"s1.open.json": `{"item_id":"i1","session_id":"s1","kind":"permission",` +
					`"cleared_by":{"event":"Stop"}}`,
			},
		},
		{
			name:    "close-with-answer-marker",
			payload: `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{}}`,
			seed: map[string]string{
				"s1.open.json":   `{"item_id":"i1","session_id":"s1","kind":"question"}`,
				"s1.answer.json": `{"session_id":"manager","kind":"manager","ts":1800000000}`,
			},
		},
		{
			name:    "close-with-stale-marker",
			payload: `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{}}`,
			seed: map[string]string{
				"s1.open.json":   `{"item_id":"i1","session_id":"s1","kind":"question"}`,
				"s1.answer.json": `{"session_id":"old","kind":"manager","ts":1}`,
			},
		},
		{
			name:    "close-agent-mismatch",
			payload: `{"session_id":"s1","hook_event_name":"PreToolUse","agent_id":"a1","tool_input":{}}`,
			seed: map[string]string{
				"s1.open.json": `{"item_id":"i1","session_id":"s1","kind":"question"}`,
			},
		},
		{
			name:    "unknown-event",
			payload: `{"session_id":"s1","hook_event_name":"Whatever"}`,
			seed: map[string]string{
				"s1.open.json": `{"item_id":"i1","session_id":"s1","kind":"question"}`,
			},
		},
		{
			name:    "malformed",
			payload: `not json`,
		},
		{
			name:    "path-session",
			payload: `{"session_id":"../x","hook_event_name":"PreToolUse"}`,
		},
	}
}

func randomized(count int) []fixture {
	//nolint:gosec // a deterministic seed is the point: the differential must
	// replay identically on every run, and this is not security-sensitive.
	source := rand.New(rand.NewSource(20261118))
	tools := []string{"Bash", "Read", "Write", "Mystery", "Bäsh"}
	events := []string{"PreToolUse", "PostToolUse", "PostToolUseFailure", "SessionEnd", "Whatever"}
	out := make([]fixture, 0, count)
	for index := 0; index < count; index++ {
		eventName := events[source.Intn(len(events))]
		tool := tools[source.Intn(len(tools))]
		command := randomText(source)
		toolInput := `{"command":` + quote(command) + `}`
		switch source.Intn(4) {
		case 1:
			toolInput = `{"unknown":` + quote(randomText(source)) + `}`
		case 2:
			toolInput = quote("plain-value")
		case 3:
			toolInput = `{}`
		}
		payload := map[string]any{
			"session_id":      "s1",
			"hook_event_name": eventName,
			"tool_name":       tool,
			"cwd":             randomText(source),
			"transcript_path": "/t.jsonl",
		}
		payload["tool_input"] = json.RawMessage(toolInput)
		if source.Intn(2) == 0 {
			payload["pane"] = source.Intn(1000)
		}
		if source.Intn(5) == 0 {
			payload["agent_id"] = "a1"
		}
		body, err := json.Marshal(payload)
		if err != nil {
			panic(err)
		}
		seed := map[string]string{}
		if source.Intn(2) == 0 {
			seed["s1.tool.json"] = `{"session_id":"s1","tool":` + quote(tool) + `}`
		}
		if source.Intn(2) == 0 {
			seed["s1.open.json"] = randomOpenItem(source)
		}
		if source.Intn(4) == 0 {
			seed["s1.answer.json"] = `{"session_id":"manager","kind":"manager","ts":1800000000}`
		}
		out = append(
			out,
			fixture{name: fmt.Sprintf("random-%d", index), payload: string(body), seed: seed},
		)
	}
	return out
}

func randomOpenItem(source *rand.Rand) string {
	variants := []string{
		`{"item_id":"i1","session_id":"s1","kind":"question"}`,
		`{"item_id":"i1","session_id":"s1","kind":"question","tool_name":"Bash"}`,
		`{"item_id":"i1","session_id":"s1","kind":"question","cleared_by":{"event":["PostToolUse","SessionEnd"]}}`,
		`{"item_id":"i1","session_id":"s1","kind":"question","cleared_by":{"tool":"Bash"}}`,
		`{"item_id":"i1","session_id":"s1","kind":"question","agent_id":"a1"}`,
		`{"item_id":"i1","session_id":"s1","kind":"question","cleared_by":{"event":"Stop"}}`,
	}
	return variants[source.Intn(len(variants))]
}

func randomText(source *rand.Rand) string {
	pieces := []string{
		"ls", "ä", "日本語", "<", ">", "&", "TOKEN=abc", "Authorization: Bearer xyz",
		"PASSWORD=hunter2", "a b c", "\\", `"`, strings.Repeat("x", 4200),
	}
	return pieces[source.Intn(len(pieces))]
}

func quote(value string) string {
	body, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(body)
}
