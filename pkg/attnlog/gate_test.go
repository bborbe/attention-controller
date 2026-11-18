// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package attnlog_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg/attnlog"
)

// fakePaneLister returns a fixed listing or error.
type fakePaneLister struct {
	titles map[int]string
	err    error
}

func (f fakePaneLister) ListPanes() (map[int]string, error) {
	return f.titles, f.err
}

func gateApp(stateDir string) *attnlog.App {
	return &attnlog.App{
		Now: func() float64 { return frozenNow },
		Getenv: func(key string) string {
			switch key {
			case "ATTENTION_STATE_DIR":
				return stateDir
			case "WEZTERM_PANE":
				return "77"
			default:
				return ""
			}
		},
		Hostname: func() string { return "test-host" },
	}
}

var _ = Describe("gate path", func() {
	var stateDir string

	BeforeEach(func() {
		stateDir = GinkgoT().TempDir()
	})

	Describe("PermissionRequest", func() {
		It("opens a permission item with its context and cleared_by", func() {
			body := `{"session_id":"s1","hook_event_name":"PermissionRequest","tool_name":"Bash",` +
				`"tool_input":{"command":"ls"},"cwd":"/w","transcript_path":"/t.jsonl",` +
				`"agent_id":"a1","pane":42}`
			Expect(handle(gateApp(stateDir), body)).To(BeNil())

			item := readJSONFile(filepath.Join(stateDir, "s1.open.json"))
			Expect(item["kind"]).To(Equal("permission"))
			Expect(item["detail"]).To(Equal("Bash: ls"))
			Expect(item["tool_name"]).To(Equal("Bash"))
			Expect(item["item_id"]).
				To(Equal(attnlog.EventItemID("s1", "permission", "Bash: ls")))
			Expect(item["context"]).
				To(Equal("Run `ls`. Denying means the command does not run."))
			Expect(item["pane"]).To(Equal("42"))
			Expect(item["cwd"]).To(Equal("/w"))
			Expect(item["transcript"]).To(Equal("/t.jsonl"))
			Expect(item["host"]).To(Equal("test-host"))
			Expect(item["agent_id"]).To(Equal("a1"))
			Expect(item["ts"]).To(Equal(frozenNow))
			Expect(item["event"]).To(Equal("PermissionRequest"))
			cleared, ok := item["cleared_by"].(map[string]any)
			Expect(ok).To(BeTrue())
			Expect(cleared["tool"]).To(Equal("Bash"))
			Expect(cleared["event"]).To(ConsistOf(
				"PreToolUse", "PostToolUse", "PostToolUseFailure", "SessionEnd",
			))

			lines := readEventLines(filepath.Join(stateDir, "s1.events.jsonl"))
			Expect(lines).To(HaveLen(1))
			Expect(lines[0]["type"]).To(Equal("open"))
			Expect(lines[0]["item_id"]).To(Equal(item["item_id"]))
		})

		It("scrubs a credential-shaped detail and hashes the untruncated form", func() {
			body := `{"session_id":"s1","hook_event_name":"PermissionRequest","tool_name":"Bash",` +
				`"tool_input":{"command":"export TOKEN=abc"}}`
			Expect(handle(gateApp(stateDir), body)).To(BeNil())
			item := readJSONFile(filepath.Join(stateDir, "s1.open.json"))
			Expect(item["detail"]).To(Equal("Bash: export TOKEN=<redacted>"))
			Expect(item["item_id"]).To(Equal(attnlog.EventItemID(
				"s1",
				"permission",
				"Bash: export TOKEN=<redacted>",
			)))
		})

		It("opens a question item for AskUserQuestion", func() {
			body := `{"session_id":"s1","hook_event_name":"PermissionRequest",` +
				`"tool_name":"AskUserQuestion","tool_input":{"questions":[` +
				`{"header":"Pick","question":"Which database?","multiSelect":false,` +
				`"options":[{"label":"(Recommended) Postgres","description":"managed"},` +
				`{"label":"SQLite"}]}]}}`
			Expect(handle(gateApp(stateDir), body)).To(BeNil())

			item := readJSONFile(filepath.Join(stateDir, "s1.open.json"))
			Expect(item["kind"]).To(Equal("question"))
			Expect(item["detail"]).To(Equal("Which database?"))
			Expect(item["answer_cardinality"]).To(Equal("single"))
			options, ok := item["options"].([]any)
			Expect(ok).To(BeTrue())
			Expect(options).To(HaveLen(2))
			Expect(options[0]).To(HaveKeyWithValue("label", "Postgres"))
			Expect(options[0]).To(HaveKeyWithValue("recommended", true))
			Expect(options[1]).To(HaveKeyWithValue("label", "SQLite"))
			Expect(options[1]).To(HaveKeyWithValue("recommended", false))
			Expect(item).NotTo(HaveKey("questions"))
		})

		It("keeps every recommendation on a multi-select question", func() {
			body := `{"session_id":"s1","hook_event_name":"PermissionRequest",` +
				`"tool_name":"AskUserQuestion","tool_input":{"questions":[` +
				`{"header":"A","question":"First?","multiSelect":true,"options":[` +
				`{"label":"(Recommended) One"},{"label":"(Recommended) Two"}]},` +
				`{"header":"B","question":"Second?","options":[{"label":"Yes"}]}]}}`
			Expect(handle(gateApp(stateDir), body)).To(BeNil())

			item := readJSONFile(filepath.Join(stateDir, "s1.open.json"))
			Expect(item).NotTo(HaveKey("options"))
			Expect(item).NotTo(HaveKey("answer_cardinality"))
			questions, ok := item["questions"].([]any)
			Expect(ok).To(BeTrue())
			Expect(questions).To(HaveLen(2))
			first, ok := questions[0].(map[string]any)
			Expect(ok).To(BeTrue())
			Expect(first["cardinality"]).To(Equal("multiple"))
			options, ok := first["options"].([]any)
			Expect(ok).To(BeTrue())
			Expect(options[0]).To(HaveKeyWithValue("recommended", true))
			Expect(options[1]).To(HaveKeyWithValue("recommended", true))
		})
	})

	Describe("PreToolUse AskUserQuestion", func() {
		It("writes the tool marker and opens the question item", func() {
			body := `{"session_id":"s1","hook_event_name":"PreToolUse",` +
				`"tool_name":"AskUserQuestion","tool_input":{"questions":[` +
				`{"header":"Pick","question":"Which database?","options":[{"label":"Postgres"}]}]}}`
			Expect(handle(gateApp(stateDir), body)).To(BeNil())

			Expect(filepath.Join(stateDir, "s1.tool.json")).To(BeAnExistingFile())
			item := readJSONFile(filepath.Join(stateDir, "s1.open.json"))
			Expect(item["kind"]).To(Equal("question"))
			Expect(item["detail"]).To(Equal("Which database?"))
			cleared, ok := item["cleared_by"].(map[string]any)
			Expect(ok).To(BeTrue())
			Expect(cleared["tool"]).To(Equal("AskUserQuestion"))
		})

		It("does not rewrite an already-open slot", func() {
			body := `{"session_id":"s1","hook_event_name":"PreToolUse",` +
				`"tool_name":"AskUserQuestion","tool_input":{"questions":[` +
				`{"header":"Pick","question":"Which database?"}]}}`
			Expect(handle(gateApp(stateDir), body)).To(BeNil())
			Expect(handle(gateApp(stateDir), body)).To(BeNil())
			Expect(readEventLines(filepath.Join(stateDir, "s1.events.jsonl"))).To(HaveLen(1))
		})
	})

	Describe("Notification", func() {
		DescribeTable("opens the kind the notification names",
			func(notificationType, expectedKind string) {
				body := `{"session_id":"s1","hook_event_name":"Notification",` +
					`"notification_type":"` + notificationType + `","message":"the message"}`
				Expect(handle(gateApp(stateDir), body)).To(BeNil())
				item := readJSONFile(filepath.Join(stateDir, "s1.open.json"))
				Expect(item["kind"]).To(Equal(expectedKind))
				Expect(item["detail"]).To(Equal("the message"))
			},
			Entry("permission_prompt", "permission_prompt", "permission"),
			Entry("elicitation_dialog", "elicitation_dialog", "question"),
			Entry("elicitation_url_dialog", "elicitation_url_dialog", "question"),
			Entry("agent_needs_input", "agent_needs_input", "question"),
			Entry("idle_prompt", "idle_prompt", "idle"),
		)

		It("ignores an unknown notification type", func() {
			body := `{"session_id":"s1","hook_event_name":"Notification",` +
				`"notification_type":"other","message":"x"}`
			Expect(handle(gateApp(stateDir), body)).To(BeNil())
			entries, err := os.ReadDir(stateDir)
			Expect(err).To(BeNil())
			Expect(entries).To(BeEmpty())
		})

		It("does not rewrite an open gate", func() {
			Expect(os.WriteFile(
				filepath.Join(stateDir, "s1.open.json"),
				[]byte(`{"item_id":"i1","session_id":"s1","kind":"question",`+
					`"cleared_by":{"event":"Stop"}}`),
				0o600,
			)).To(Succeed())
			body := `{"session_id":"s1","hook_event_name":"Notification",` +
				`"notification_type":"permission_prompt","message":"x"}`
			Expect(handle(gateApp(stateDir), body)).To(BeNil())
			Expect(readJSONFile(filepath.Join(stateDir, "s1.open.json"))["item_id"]).
				To(Equal("i1"))
			_, err := os.Stat(filepath.Join(stateDir, "s1.events.jsonl"))
			Expect(os.IsNotExist(err)).To(BeTrue())
		})
	})

	Describe("supersede", func() {
		It("closes a different ask and writes the new open", func() {
			Expect(os.WriteFile(
				filepath.Join(stateDir, "s1.open.json"),
				[]byte(`{"item_id":"i1","session_id":"s1","kind":"permission",`+
					`"cleared_by":{"event":"Stop"}}`),
				0o600,
			)).To(Succeed())
			body := `{"session_id":"s1","hook_event_name":"PermissionRequest",` +
				`"tool_name":"Read","tool_input":{"file_path":"/tmp/a"}}`
			Expect(handle(gateApp(stateDir), body)).To(BeNil())

			lines := readEventLines(filepath.Join(stateDir, "s1.events.jsonl"))
			Expect(lines).To(HaveLen(2))
			Expect(lines[0]["item_id"]).To(Equal("i1"))
			Expect(lines[0]["closed_by"]).To(Equal("SupersededByNewItem"))
			Expect(lines[0]["answerer_session"]).To(Equal(""))
			Expect(lines[0]["answerer_kind"]).To(Equal("no_answerer"))
			Expect(lines[1]["type"]).To(Equal("open"))
			Expect(readJSONFile(filepath.Join(stateDir, "s1.open.json"))["item_id"]).
				To(Equal(lines[1]["item_id"]))
		})

		It("folds a repeated ask instead of superseding it", func() {
			body := `{"session_id":"s1","hook_event_name":"PermissionRequest",` +
				`"tool_name":"Bash","tool_input":{"command":"ls"}}`
			Expect(handle(gateApp(stateDir), body)).To(BeNil())
			Expect(handle(gateApp(stateDir), body)).To(BeNil())
			Expect(readEventLines(filepath.Join(stateDir, "s1.events.jsonl"))).To(HaveLen(1))
		})
	})

	Describe("close a permission gate", func() {
		It("closes it when the tool starts", func() {
			open := `{"session_id":"s1","hook_event_name":"PermissionRequest",` +
				`"tool_name":"Bash","tool_input":{"command":"ls"}}`
			Expect(handle(gateApp(stateDir), open)).To(BeNil())
			Expect(handle(gateApp(stateDir),
				`{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash",`+
					`"tool_input":{"command":"ls"}}`)).To(BeNil())

			_, err := os.Stat(filepath.Join(stateDir, "s1.open.json"))
			Expect(os.IsNotExist(err)).To(BeTrue())
			lines := readEventLines(filepath.Join(stateDir, "s1.events.jsonl"))
			Expect(lines).To(HaveLen(2))
			Expect(lines[1]["closed_by"]).To(Equal("PreToolUse"))
		})
	})

	Describe("owned pane", func() {
		writeRegistry := func(name string) string {
			dir := GinkgoT().TempDir()
			content, err := json.Marshal(map[string]string{
				"sessionId": "s1",
				"name":      name,
			})
			Expect(err).To(BeNil())
			Expect(os.WriteFile(
				filepath.Join(dir, "123.json"),
				content,
				0o600,
			)).To(Succeed())
			return dir
		}

		appWithPanes := func(sessionsDir string, lister attnlog.PaneLister) *attnlog.App {
			app := gateApp(stateDir)
			app.Panes = lister
			app.Getenv = func(key string) string {
				switch key {
				case "ATTENTION_STATE_DIR":
					return stateDir
				case "WEZTERM_PANE":
					return "77"
				case "SESSIONS_DIR":
					return sessionsDir
				default:
					return ""
				}
			}
			return app
		}

		openBody := `{"session_id":"s1","hook_event_name":"PermissionRequest",` +
			`"tool_name":"Bash","tool_input":{"command":"ls"}}`

		It("keeps a pane whose title matches the session name", func() {
			app := appWithPanes(
				writeRegistry("my session"),
				fakePaneLister{titles: map[int]string{77: "◐ my session"}},
			)
			Expect(handle(app, openBody)).To(BeNil())
			Expect(readJSONFile(filepath.Join(stateDir, "s1.open.json"))["pane"]).
				To(Equal("77"))
		})

		It("eliminates a pane whose title belongs to another session", func() {
			app := appWithPanes(
				writeRegistry("my session"),
				fakePaneLister{titles: map[int]string{77: "another session"}},
			)
			Expect(handle(app, openBody)).To(BeNil())
			Expect(readJSONFile(filepath.Join(stateDir, "s1.open.json"))["pane"]).
				To(Equal(""))
		})

		It("eliminates a pane the listing does not contain", func() {
			app := appWithPanes(
				writeRegistry("my session"),
				fakePaneLister{titles: map[int]string{}},
			)
			Expect(handle(app, openBody)).To(BeNil())
			Expect(readJSONFile(filepath.Join(stateDir, "s1.open.json"))["pane"]).
				To(Equal(""))
		})

		It("keeps the pane when the listing cannot be read", func() {
			app := appWithPanes(
				writeRegistry("my session"),
				fakePaneLister{err: errors.New("wezterm down")},
			)
			Expect(handle(app, openBody)).To(BeNil())
			Expect(readJSONFile(filepath.Join(stateDir, "s1.open.json"))["pane"]).
				To(Equal("77"))
		})

		It("keeps the pane when the session has no registered name", func() {
			app := appWithPanes(
				writeRegistry(""),
				fakePaneLister{titles: map[int]string{77: "whatever"}},
			)
			Expect(handle(app, openBody)).To(BeNil())
			Expect(readJSONFile(filepath.Join(stateDir, "s1.open.json"))["pane"]).
				To(Equal("77"))
		})
	})
})
