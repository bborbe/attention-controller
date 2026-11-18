// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package attnlog_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg/attnlog"
)

const frozenNow = 1800000000.0

func newApp(stateDir string) *attnlog.App {
	return &attnlog.App{
		Now: func() float64 { return frozenNow },
		Getenv: func(key string) string {
			if key == "ATTENTION_STATE_DIR" {
				return stateDir
			}
			if key == "WEZTERM_PANE" {
				return "77"
			}
			return ""
		},
	}
}

func handle(app *attnlog.App, body string) error {
	return app.Handle(strings.NewReader(body))
}

func readJSONFile(path string) map[string]any {
	raw, err := os.ReadFile(path)
	Expect(err).To(BeNil())
	var value map[string]any
	Expect(json.Unmarshal(raw, &value)).To(Succeed())
	return value
}

func readEventLines(path string) []map[string]any {
	raw, err := os.ReadFile(path)
	Expect(err).To(BeNil())
	lines := make([]map[string]any, 0)
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var value map[string]any
		Expect(json.Unmarshal([]byte(line), &value)).To(Succeed())
		lines = append(lines, value)
	}
	return lines
}

var _ = Describe("Handle", func() {
	var stateDir string

	BeforeEach(func() {
		stateDir = GinkgoT().TempDir()
	})

	Describe("malformed input", func() {
		DescribeTable(
			"exits 0 and writes nothing",
			func(body string) {
				Expect(handle(newApp(stateDir), body)).To(BeNil())
				entries, err := os.ReadDir(stateDir)
				Expect(err).To(BeNil())
				Expect(entries).To(BeEmpty())
			},
			Entry("empty stdin", ""),
			Entry("not json", "not json"),
			Entry("json but no session", `{"hook_event_name":"PreToolUse"}`),
			Entry("empty session", `{"session_id":"","hook_event_name":"PreToolUse"}`),
			Entry("session with a slash", `{"session_id":"../x","hook_event_name":"PreToolUse"}`),
			Entry(
				"session with a backslash",
				`{"session_id":"a\\b","hook_event_name":"PreToolUse"}`,
			),
			Entry("session with a parent", `{"session_id":"a..b","hook_event_name":"PreToolUse"}`),
		)
	})

	Describe("PreToolUse", func() {
		It("writes the tool marker with the specified shape", func() {
			body := `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash",
"tool_input":{"command":"ls"},"cwd":"/w","transcript_path":"/t.jsonl","pane":42}`
			Expect(handle(newApp(stateDir), body)).To(BeNil())

			marker := readJSONFile(filepath.Join(stateDir, "s1.tool.json"))
			Expect(marker["session_id"]).To(Equal("s1"))
			Expect(marker["pane"]).To(Equal("42"))
			Expect(marker["cwd"]).To(Equal("/w"))
			Expect(marker["kind"]).To(Equal("tool"))
			Expect(marker["event"]).To(Equal("PreToolUse"))
			Expect(marker["transcript"]).To(Equal("/t.jsonl"))
			Expect(marker["tool"]).To(Equal("Bash"))
			Expect(marker["detail"]).To(Equal("Bash: ls"))
			Expect(marker["ts"]).To(Equal(frozenNow))
			Expect(marker["state"]).To(Equal("open"))
			Expect(marker).To(HaveKey("cleared_by"))
			Expect(marker["cleared_by"]).To(BeNil())
		})

		It("falls back to WEZTERM_PANE when the payload carries none", func() {
			body := `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{}}`
			Expect(handle(newApp(stateDir), body)).To(BeNil())
			Expect(readJSONFile(filepath.Join(stateDir, "s1.tool.json"))["pane"]).To(Equal("77"))
		})

		It("scrubs a credential-shaped detail", func() {
			body := `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash",
"tool_input":{"command":"export TOKEN=abc"}}`
			Expect(handle(newApp(stateDir), body)).To(BeNil())
			Expect(readJSONFile(filepath.Join(stateDir, "s1.tool.json"))["detail"]).
				To(Equal("Bash: export TOKEN=<redacted>"))
		})

		It("truncates the detail to 4000 code points", func() {
			command := strings.Repeat("ä", 5000)
			body := `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"` +
				command + `"}}`
			Expect(handle(newApp(stateDir), body)).To(BeNil())
			detail, ok := readJSONFile(filepath.Join(stateDir, "s1.tool.json"))["detail"].(string)
			Expect(ok).To(BeTrue())
			Expect([]rune(detail)).To(HaveLen(4000))
		})

		It("renders a non-object tool_input verbatim", func() {
			body := `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":"plain"}`
			Expect(handle(newApp(stateDir), body)).To(BeNil())
			Expect(readJSONFile(filepath.Join(stateDir, "s1.tool.json"))["detail"]).
				To(Equal(`Bash: "plain"`))
		})

		It("falls back to the whole tool_input for an unrecognised object", func() {
			body := `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Mystery",
"tool_input":{"unknown":"value"}}`
			Expect(handle(newApp(stateDir), body)).To(BeNil())
			Expect(readJSONFile(filepath.Join(stateDir, "s1.tool.json"))["detail"]).
				To(ContainSubstring(`"unknown":"value"`))
		})

		It("keeps HTML metacharacters unescaped", func() {
			body := `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash",
"tool_input":{"command":"a < b && c > d"}}`
			Expect(handle(newApp(stateDir), body)).To(BeNil())
			raw, err := os.ReadFile(filepath.Join(stateDir, "s1.tool.json"))
			Expect(err).To(BeNil())
			Expect(string(raw)).To(ContainSubstring("a < b && c > d"))
		})
	})

	Describe("PostToolUse", func() {
		BeforeEach(func() {
			body := `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`
			Expect(handle(newApp(stateDir), body)).To(BeNil())
		})

		It("clears the marker for the owning tool", func() {
			body := `{"session_id":"s1","hook_event_name":"PostToolUse","tool_name":"Bash"}`
			Expect(handle(newApp(stateDir), body)).To(BeNil())
			_, err := os.Stat(filepath.Join(stateDir, "s1.tool.json"))
			Expect(os.IsNotExist(err)).To(BeTrue())
		})

		It("keeps the marker for a different tool", func() {
			body := `{"session_id":"s1","hook_event_name":"PostToolUse","tool_name":"Read"}`
			Expect(handle(newApp(stateDir), body)).To(BeNil())
			_, err := os.Stat(filepath.Join(stateDir, "s1.tool.json"))
			Expect(err).To(BeNil())
		})

		It("clears unconditionally when the event names no tool", func() {
			body := `{"session_id":"s1","hook_event_name":"PostToolUseFailure"}`
			Expect(handle(newApp(stateDir), body)).To(BeNil())
			_, err := os.Stat(filepath.Join(stateDir, "s1.tool.json"))
			Expect(os.IsNotExist(err)).To(BeTrue())
		})
	})

	Describe("SessionEnd", func() {
		It("clears the tool marker and the open item", func() {
			Expect(os.WriteFile(
				filepath.Join(stateDir, "s1.tool.json"),
				[]byte(`{"session_id":"s1","tool":"Bash"}`),
				0o600,
			)).To(Succeed())
			Expect(os.WriteFile(
				filepath.Join(stateDir, "s1.open.json"),
				[]byte(`{"item_id":"i1","session_id":"s1","kind":"question"}`),
				0o600,
			)).To(Succeed())

			body := `{"session_id":"s1","hook_event_name":"SessionEnd"}`
			Expect(handle(newApp(stateDir), body)).To(BeNil())

			_, err := os.Stat(filepath.Join(stateDir, "s1.tool.json"))
			Expect(os.IsNotExist(err)).To(BeTrue())
			_, err = os.Stat(filepath.Join(stateDir, "s1.open.json"))
			Expect(os.IsNotExist(err)).To(BeTrue())
			lines := readEventLines(filepath.Join(stateDir, "s1.events.jsonl"))
			Expect(lines).To(HaveLen(1))
			Expect(lines[0]["closed_by"]).To(Equal("SessionEnd"))
			Expect(lines[0]["item_id"]).To(Equal("i1"))
		})
	})

	Describe("closeOpen", func() {
		writeOpen := func(clearedBy string, toolName, agentID string) {
			Expect(os.WriteFile(
				filepath.Join(stateDir, "s1.open.json"),
				[]byte(`{"item_id":"i1","session_id":"s1","kind":"permission",`+
					`"tool_name":`+jsonString(toolName)+`,"agent_id":`+jsonString(agentID)+
					`,"cleared_by":`+clearedBy+`}`),
				0o600,
			)).To(Succeed())
		}

		It("honours a cleared_by event list", func() {
			writeOpen(`{"event":["PostToolUse"]}`, "", "")
			Expect(handle(newApp(stateDir), `{"session_id":"s1","hook_event_name":"PreToolUse"}`)).
				To(BeNil())
			_, err := os.Stat(filepath.Join(stateDir, "s1.open.json"))
			Expect(err).To(BeNil(), "a non-listed event must not answer the item")

			Expect(handle(newApp(stateDir), `{"session_id":"s1","hook_event_name":"PostToolUse"}`)).
				To(BeNil())
			_, err = os.Stat(filepath.Join(stateDir, "s1.open.json"))
			Expect(os.IsNotExist(err)).To(BeTrue())
			Expect(readEventLines(filepath.Join(stateDir, "s1.events.jsonl"))[0]["closed_by"]).
				To(Equal("PostToolUse"))
		})

		It("honours a cleared_by event string", func() {
			writeOpen(`{"event":"Stop"}`, "", "")
			Expect(handle(newApp(stateDir), `{"session_id":"s1","hook_event_name":"PreToolUse"}`)).
				To(BeNil())
			_, err := os.Stat(filepath.Join(stateDir, "s1.open.json"))
			Expect(err).To(BeNil())
		})

		It("answers any event when cleared_by names none", func() {
			writeOpen(`null`, "", "")
			Expect(handle(newApp(stateDir), `{"session_id":"s1","hook_event_name":"Whatever"}`)).
				To(BeNil())
			Expect(readEventLines(filepath.Join(stateDir, "s1.events.jsonl"))[0]["closed_by"]).
				To(Equal("Whatever"))
		})

		It("lets SessionEnd bypass the event filter", func() {
			writeOpen(`{"event":["Stop"]}`, "", "")
			Expect(handle(newApp(stateDir), `{"session_id":"s1","hook_event_name":"SessionEnd"}`)).
				To(BeNil())
			_, err := os.Stat(filepath.Join(stateDir, "s1.open.json"))
			Expect(os.IsNotExist(err)).To(BeTrue())
		})

		It("fails open when the tool identity is empty", func() {
			writeOpen(`{"tool":"Bash"}`, "", "")
			Expect(handle(newApp(stateDir), `{"session_id":"s1","hook_event_name":"PreToolUse"}`)).
				To(BeNil())
			// An empty event tool differs from the non-empty cleared_by tool, so it blocks.
			_, err := os.Stat(filepath.Join(stateDir, "s1.open.json"))
			Expect(err).To(BeNil())

			Expect(handle(newApp(stateDir),
				`{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash"}`)).
				To(BeNil())
			_, err = os.Stat(filepath.Join(stateDir, "s1.open.json"))
			Expect(os.IsNotExist(err)).To(BeTrue())
		})

		It("blocks a mismatched opener tool_name", func() {
			writeOpen(`null`, "Bash", "")
			Expect(handle(newApp(stateDir),
				`{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Read"}`)).
				To(BeNil())
			_, err := os.Stat(filepath.Join(stateDir, "s1.open.json"))
			Expect(err).To(BeNil())
		})

		It("compares agent_id exactly", func() {
			writeOpen(`null`, "", "")
			Expect(handle(newApp(stateDir),
				`{"session_id":"s1","hook_event_name":"PreToolUse","agent_id":"a1"}`)).
				To(BeNil())
			_, err := os.Stat(filepath.Join(stateDir, "s1.open.json"))
			Expect(err).To(BeNil(), "an absent opener agent must block a present event agent")

			writeOpen(`null`, "", "a1")
			Expect(handle(newApp(stateDir),
				`{"session_id":"s1","hook_event_name":"PreToolUse","agent_id":"a1"}`)).
				To(BeNil())
			_, err = os.Stat(filepath.Join(stateDir, "s1.open.json"))
			Expect(os.IsNotExist(err)).To(BeTrue())
		})

		It("records the answerer when a fresh marker names one", func() {
			writeOpen(`null`, "", "")
			Expect(os.WriteFile(
				filepath.Join(stateDir, "s1.answer.json"),
				[]byte(`{"session_id":"manager","kind":"manager","ts":1800000000}`),
				0o600,
			)).To(Succeed())
			Expect(handle(newApp(stateDir), `{"session_id":"s1","hook_event_name":"PreToolUse"}`)).
				To(BeNil())
			line := readEventLines(filepath.Join(stateDir, "s1.events.jsonl"))[0]
			Expect(line["answerer_session"]).To(Equal("manager"))
			Expect(line["answerer_kind"]).To(Equal("manager"))
		})

		It("ignores a stale marker and records no_answerer after the epoch", func() {
			writeOpen(`null`, "", "")
			Expect(os.WriteFile(
				filepath.Join(stateDir, "s1.answer.json"),
				[]byte(`{"session_id":"old","kind":"manager","ts":1}`),
				0o600,
			)).To(Succeed())
			Expect(handle(newApp(stateDir), `{"session_id":"s1","hook_event_name":"PreToolUse"}`)).
				To(BeNil())
			line := readEventLines(filepath.Join(stateDir, "s1.events.jsonl"))[0]
			Expect(line["answerer_session"]).To(Equal(""))
			Expect(line["answerer_kind"]).To(Equal("no_answerer"))
		})

		It("records no_answerer after the epoch when no marker exists", func() {
			writeOpen(`null`, "", "")
			Expect(handle(newApp(stateDir), `{"session_id":"s1","hook_event_name":"PreToolUse"}`)).
				To(BeNil())
			line := readEventLines(filepath.Join(stateDir, "s1.events.jsonl"))[0]
			Expect(line["answerer_session"]).To(Equal(""))
			Expect(line["answerer_kind"]).To(Equal("no_answerer"))
		})
	})

	Describe("unsupported events", func() {
		DescribeTable("return the sentinel but still run closeOpen",
			func(eventName string) {
				Expect(os.WriteFile(
					filepath.Join(stateDir, "s1.open.json"),
					[]byte(`{"item_id":"i1","session_id":"s1","kind":"question"}`),
					0o600,
				)).To(Succeed())
				body := `{"session_id":"s1","hook_event_name":"` + eventName + `"}`
				err := handle(newApp(stateDir), body)
				Expect(errors.Is(err, attnlog.ErrUnsupportedEvent)).To(BeTrue())
				lines := readEventLines(filepath.Join(stateDir, "s1.events.jsonl"))
				Expect(lines).To(HaveLen(1))
				Expect(lines[0]["closed_by"]).To(Equal(eventName))
			},
			Entry("Stop", "Stop"),
			Entry("UserPromptSubmit", "UserPromptSubmit"),
		)

		It("exits 0 for an unknown event and writes nothing in a fresh dir", func() {
			Expect(handle(newApp(stateDir), `{"session_id":"s1","hook_event_name":"Nope"}`)).
				To(BeNil())
			entries, err := os.ReadDir(stateDir)
			Expect(err).To(BeNil())
			Expect(entries).To(BeEmpty())
		})
	})
})

func jsonString(value string) string {
	raw, err := json.Marshal(value)
	Expect(err).To(BeNil())
	return string(raw)
}
