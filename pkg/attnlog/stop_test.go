// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package attnlog_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// stopBody builds a Stop payload whose final assistant message is message.
func stopBody(message string) string {
	return `{"session_id":"s1","hook_event_name":"Stop","cwd":"/w",` +
		`"transcript_path":"/t.jsonl","pane":42,"last_assistant_message":` +
		jsonString(message) + `}`
}

// promptBody builds a UserPromptSubmit payload.
func promptBody(prompt string) string {
	return `{"session_id":"s1","hook_event_name":"UserPromptSubmit","prompt":` +
		jsonString(prompt) + `}`
}

func seedOpen(stateDir, body string) {
	Expect(os.WriteFile(filepath.Join(stateDir, "s1.open.json"), []byte(body), 0o600)).
		To(Succeed())
}

var _ = Describe("Stop closer walker", func() {
	var stateDir string

	BeforeEach(func() {
		stateDir = GinkgoT().TempDir()
	})

	DescribeTable("mints a question item for a waiting verb",
		func(message, detail string, wantOptions []string) {
			Expect(handle(newApp(stateDir), stopBody(message))).To(BeNil())

			item := readJSONFile(filepath.Join(stateDir, "s1.open.json"))
			Expect(item["kind"]).To(Equal("question"))
			Expect(item["event"]).To(Equal("Stop"))
			Expect(item["detail"]).To(Equal(detail))
			Expect(item["answer_cardinality"]).To(Equal("single"))
			Expect(item["cleared_by"]).To(Equal(map[string]any{
				"event": []any{"UserPromptSubmit", "SessionEnd"},
				"tool":  "",
			}))
			options, ok := item["options"].([]any)
			Expect(ok).To(BeTrue())
			labels := make([]string, 0, len(options))
			for _, raw := range options {
				entry, isObject := raw.(map[string]any)
				Expect(isObject).To(BeTrue())
				label, isString := entry["label"].(string)
				Expect(isString).To(BeTrue())
				labels = append(labels, label)
			}
			Expect(labels).To(Equal(wantOptions))
		},
		Entry("approve", "Done.\n🔵 READY · you decide\n👤 You: `approve: repair` — run it",
			"approve: repair — run it", []string{"Approve", "Deny"}),
		Entry("pick", "👤 You: pick — 1. Postgres · 2. SQLite",
			"pick — 1. Postgres · 2. SQLite", []string{"Postgres", "SQLite"}),
		Entry("review", "👤 You: review: the patch",
			"review: the patch", []string{"Approve", "Request changes"}),
		Entry("you run", "👤 You: you run: make deploy",
			"you run: make deploy", []string{"Done", "Skip"}),
	)

	It("records an idle item when the turn carries no closer", func() {
		Expect(handle(newApp(stateDir), stopBody("Done, all green."))).To(BeNil())

		item := readJSONFile(filepath.Join(stateDir, "s1.open.json"))
		Expect(item["kind"]).To(Equal("idle"))
		Expect(item["event"]).To(Equal("Stop"))
		Expect(item["detail"]).To(Equal(""))
		Expect(item).NotTo(HaveKey("options"))
	})

	It("ignores a quoted closer inside a multi-line fence", func() {
		message := "Here is the panel I was shown:\n```\nsome quoted output\n👤 You: approve: repair\nmore output\n```"
		Expect(handle(newApp(stateDir), stopBody(message))).To(BeNil())

		item := readJSONFile(filepath.Join(stateDir, "s1.open.json"))
		Expect(item["kind"]).To(Equal("idle"))
	})

	It("emits a fenced closer panel with a state glyph", func() {
		message := "```\n🟢 READY · nothing running\n👤 You: approve: repair\n```"
		Expect(handle(newApp(stateDir), stopBody(message))).To(BeNil())

		item := readJSONFile(filepath.Join(stateDir, "s1.open.json"))
		Expect(item["kind"]).To(Equal("question"))
		Expect(item["detail"]).To(Equal("approve: repair"))
	})

	It("emits a one-line fence", func() {
		message := "```\n👤 You: review: the patch\n```"
		Expect(handle(newApp(stateDir), stopBody(message))).To(BeNil())

		item := readJSONFile(filepath.Join(stateDir, "s1.open.json"))
		Expect(item["kind"]).To(Equal("question"))
	})

	DescribeTable("suppresses a deliberate non-ask and records idle",
		func(message string) {
			Expect(handle(newApp(stateDir), stopBody(message))).To(BeNil())

			item := readJSONFile(filepath.Join(stateDir, "s1.open.json"))
			Expect(item["kind"]).To(Equal("idle"))

			lines := readEventLines(filepath.Join(stateDir, "suppressed.jsonl"))
			Expect(lines).To(HaveLen(1))
			Expect(lines[0]["session_id"]).To(Equal("s1"))
		},
		Entry("parked", "👤 You: later (on the deploy): approve it"),
		Entry("no reply", "👤 You: you run: make deploy — no reply needed"),
		Entry("nothing", "👤 You: nothing"),
	)

	It("suppresses a closer that echoes an already-posted card", func() {
		Expect(os.WriteFile(
			filepath.Join(stateDir, "s1.posted.json"),
			[]byte(`{"closer":"approve:  repair — run it"}`),
			0o600,
		)).To(Succeed())

		Expect(handle(newApp(stateDir), stopBody("👤 You: approve: repair — run it"))).To(BeNil())

		Expect(readJSONFile(filepath.Join(stateDir, "s1.open.json"))["kind"]).To(Equal("idle"))
		Expect(readEventLines(filepath.Join(stateDir, "suppressed.jsonl"))).To(HaveLen(1))
	})

	It("records idle for a manager-settleable handover pick", func() {
		message := "👤 You: pick — 1. Handover to the manager · 2. Hand over to ops"
		Expect(handle(newApp(stateDir), stopBody(message))).To(BeNil())

		Expect(readJSONFile(filepath.Join(stateDir, "s1.open.json"))["kind"]).To(Equal("idle"))
		Expect(readEventLines(filepath.Join(stateDir, "suppressed.jsonl"))).To(HaveLen(1))
	})

	It("mints a board opt-in even without a waiting verb", func() {
		message := "Done.\n📣 Board: review the migration plan"
		Expect(handle(newApp(stateDir), stopBody(message))).To(BeNil())

		item := readJSONFile(filepath.Join(stateDir, "s1.open.json"))
		Expect(item["kind"]).To(Equal("question"))
		Expect(item["detail"]).To(Equal("review the migration plan"))
		Expect(item).NotTo(HaveKey("options"))
	})

	It("does not write when an item is already open", func() {
		seedOpen(
			stateDir,
			`{"item_id":"i1","session_id":"s1","kind":"permission","cleared_by":{"event":["PreToolUse"]}}`,
		)

		Expect(handle(newApp(stateDir), stopBody("👤 You: approve: repair"))).To(BeNil())

		Expect(readJSONFile(filepath.Join(stateDir, "s1.open.json"))["item_id"]).To(Equal("i1"))
		_, err := os.Stat(filepath.Join(stateDir, "s1.events.jsonl"))
		Expect(os.IsNotExist(err)).To(BeTrue())
	})

	It("scrubs a credential-shaped closer detail", func() {
		Expect(handle(newApp(stateDir), stopBody("👤 You: approve: TOKEN=abc"))).To(BeNil())

		item := readJSONFile(filepath.Join(stateDir, "s1.open.json"))
		Expect(item["detail"]).To(Equal("approve: TOKEN=<redacted>"))
	})
})

var _ = Describe("UserPromptSubmit system envelope", func() {
	var stateDir string

	BeforeEach(func() {
		stateDir = GinkgoT().TempDir()
	})

	It("closes a question item locally with forward:false", func() {
		seedOpen(stateDir, `{"item_id":"i1","session_id":"s1","kind":"question"}`)

		Expect(handle(newApp(stateDir), promptBody("<system-reminder>hello</system-reminder>"))).
			To(BeNil())

		lines := readEventLines(filepath.Join(stateDir, "s1.events.jsonl"))
		Expect(lines).To(HaveLen(1))
		Expect(lines[0]["closed_by"]).To(Equal("UserPromptSubmit"))
		Expect(lines[0]["forward"]).To(Equal(false))
		Expect(lines[0]["item_id"]).To(Equal("i1"))
		_, err := os.Stat(filepath.Join(stateDir, "s1.open.json"))
		Expect(os.IsNotExist(err)).To(BeTrue())
	})

	It("falls through to no close for a real prompt", func() {
		seedOpen(
			stateDir,
			`{"item_id":"i1","session_id":"s1","kind":"question","cleared_by":{"event":["PostToolUse"]}}`,
		)

		Expect(handle(newApp(stateDir), promptBody("please continue"))).To(BeNil())

		_, err := os.Stat(filepath.Join(stateDir, "s1.events.jsonl"))
		Expect(os.IsNotExist(err)).To(BeTrue())
		Expect(readJSONFile(filepath.Join(stateDir, "s1.open.json"))["item_id"]).To(Equal("i1"))
	})

	It("does not close a permission item locally", func() {
		seedOpen(
			stateDir,
			`{"item_id":"i1","session_id":"s1","kind":"permission","cleared_by":{"event":["PreToolUse"]}}`,
		)

		Expect(handle(newApp(stateDir), promptBody("<system-reminder>x</system-reminder>"))).
			To(BeNil())

		Expect(readJSONFile(filepath.Join(stateDir, "s1.open.json"))["item_id"]).To(Equal("i1"))
	})

	It("does not close a question whose cleared_by names a tool", func() {
		seedOpen(
			stateDir,
			`{"item_id":"i1","session_id":"s1","kind":"question","cleared_by":{"tool":"AskUserQuestion"}}`,
		)

		Expect(handle(newApp(stateDir), promptBody("<system-reminder>x</system-reminder>"))).
			To(BeNil())

		Expect(readJSONFile(filepath.Join(stateDir, "s1.open.json"))["item_id"]).To(Equal("i1"))
	})

	It("attributes the answerer after the epoch", func() {
		seedOpen(stateDir, `{"item_id":"i1","session_id":"s1","kind":"question"}`)

		Expect(handle(newApp(stateDir), promptBody("<system-reminder>x</system-reminder>"))).
			To(BeNil())

		line := readEventLines(filepath.Join(stateDir, "s1.events.jsonl"))[0]
		Expect(line["answerer_session"]).To(Equal(""))
		Expect(line["answerer_kind"]).To(Equal("no_answerer"))
	})
})
