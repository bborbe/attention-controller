// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// routineQuestion builds the wrapper's question object for a tool call.
//
// ⚠️ The shape is FLAT — `{"tool_name": ..., "input": {...}}` — because that is
// what the wrapper publishes on `/api/1.0/sessions/<id>`. A fixture that nests
// it under `input.arguments` tests a payload no producer sends, which is how
// the arm shipped reading every gate as `no-routine-rule`.
func routineQuestion(tool string, call map[string]any) json.RawMessage {
	raw, err := json.Marshal(map[string]any{
		"tool_name": tool,
		"input":     call,
	})
	Expect(err).NotTo(HaveOccurred())
	return raw
}

var _ = Describe("routine policy", func() {
	var (
		workspace string
		policy    RoutinePolicy
	)

	BeforeEach(func() {
		workspace = GinkgoT().TempDir()
		policy = NewRoutinePolicy(nil)
	})

	It("allows a Read inside the session workspace", func() {
		rule, allowed := policy.RoutineRule(
			routineQuestion(
				"Read",
				map[string]any{"file_path": filepath.Join(workspace, "main.go")},
			),
			workspace,
		)
		Expect(allowed).To(BeTrue())
		Expect(rule).To(Equal("routine-tool:Read"))
	})

	It("resolves a relative path against the session cwd", func() {
		rule, allowed := policy.RoutineRule(
			routineQuestion("Read", map[string]any{"file_path": "main.go"}),
			workspace,
		)
		Expect(allowed).To(BeTrue())
		Expect(rule).To(Equal("routine-tool:Read"))
	})

	It("refuses a Read outside the workspace", func() {
		rule, allowed := policy.RoutineRule(
			routineQuestion("Read", map[string]any{"file_path": "/etc/passwd"}),
			workspace,
		)
		Expect(allowed).To(BeFalse())
		Expect(rule).To(Equal("path-outside-workspace:Read"))
	})

	It("refuses a Read of a credential file inside the workspace", func() {
		env := filepath.Join(workspace, ".env")
		Expect(os.WriteFile(env, []byte("SECRET=1"), 0o600)).To(Succeed())
		rule, allowed := policy.RoutineRule(
			routineQuestion("Read", map[string]any{"file_path": env}),
			workspace,
		)
		Expect(allowed).To(BeFalse())
		Expect(rule).To(Equal("path-outside-workspace:Read"))
	})

	It("refuses a Grep, whose reads cannot be enumerated", func() {
		rule, allowed := policy.RoutineRule(
			routineQuestion("Grep", map[string]any{"path": workspace}),
			workspace,
		)
		Expect(allowed).To(BeFalse())
		Expect(rule).To(Equal("no-routine-rule"))
	})

	It("refuses a gate with no path argument", func() {
		rule, allowed := policy.RoutineRule(
			routineQuestion("Read", map[string]any{"query": "x"}),
			workspace,
		)
		Expect(allowed).To(BeFalse())
		Expect(rule).To(Equal("no-routine-rule"))
	})

	// The wrapper publishes the FLAT question and the nested one is a legacy
	// shape this arm still accepts. Both must reach the same decision: a policy
	// that parses only the nesting refuses every live gate, which is exactly
	// the defect that made the item-aware Bash rule unreachable.
	It("parses the wrapper's flat question", func() {
		rule, allowed := policy.RoutineRule(
			routineQuestion(
				"Read",
				map[string]any{"file_path": filepath.Join(workspace, "main.go")},
			),
			workspace,
		)
		Expect(allowed).To(BeTrue())
		Expect(rule).To(Equal("routine-tool:Read"))
	})

	It("still parses the legacy nested question", func() {
		raw, err := json.Marshal(map[string]any{
			"input": map[string]any{
				"arguments": map[string]any{
					"tool_name": "Read",
					"input": map[string]any{
						"file_path": filepath.Join(workspace, "main.go"),
					},
				},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		rule, allowed := policy.RoutineRule(raw, workspace)
		Expect(allowed).To(BeTrue())
		Expect(rule).To(Equal("routine-tool:Read"))
	})

	It("parses a legacy nested Bash gate", func() {
		raw, err := json.Marshal(map[string]any{
			"input": map[string]any{
				"arguments": map[string]any{
					"tool_name": "Bash",
					"input":     map[string]any{"command": "ls -la"},
				},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		command, sandboxDisabled, ok := routineBashCommand(raw)
		Expect(ok).To(BeTrue())
		Expect(sandboxDisabled).To(BeFalse())
		Expect(command).To(Equal("ls -la"))
	})

	It("fails closed on a flat question with no input object", func() {
		rule, allowed := policy.RoutineRule(
			json.RawMessage(`{"tool_name":"Read","input":null}`),
			workspace,
		)
		Expect(allowed).To(BeFalse())
		Expect(rule).To(Equal("no-routine-rule"))
	})

	It("fails closed on a flat question whose input is not an object", func() {
		rule, allowed := policy.RoutineRule(
			json.RawMessage(`{"tool_name":"Read","input":"main.go"}`),
			workspace,
		)
		Expect(allowed).To(BeFalse())
		Expect(rule).To(Equal("no-routine-rule"))
	})

	// `input` is model-controlled tool input, so an empty `tool_name` must not
	// let a tool input's own `arguments` key choose the tool name the policy
	// sees. Flat is selected by the key's presence, and an empty value is
	// malformed — never a fall-through to the legacy branch.
	It("fails closed when tool_name is present but empty", func() {
		raw, err := json.Marshal(map[string]any{
			"tool_name": "",
			"input": map[string]any{
				"arguments": map[string]any{
					"tool_name": "Read",
					"input": map[string]any{
						"file_path": filepath.Join(workspace, "main.go"),
					},
				},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		rule, allowed := policy.RoutineRule(raw, workspace)
		Expect(allowed).To(BeFalse())
		Expect(rule).To(Equal("no-routine-rule"))
	})

	It("refuses a Write when no routine root is declared", func() {
		rule, allowed := policy.RoutineRule(
			routineQuestion(
				"Write",
				map[string]any{"file_path": filepath.Join(workspace, "out.txt")},
			),
			workspace,
		)
		Expect(allowed).To(BeFalse())
		Expect(rule).To(Equal("write-outside-declared-root:Write"))
	})

	It("allows a Write inside a declared routine root", func() {
		declared := GinkgoT().TempDir()
		policy = NewRoutinePolicy([]string{declared})
		rule, allowed := policy.RoutineRule(
			routineQuestion(
				"Write",
				map[string]any{"file_path": filepath.Join(declared, "out.txt")},
			),
			workspace,
		)
		Expect(allowed).To(BeTrue())
		Expect(rule).To(Equal("routine-write:Write"))
	})

	It("drops a declared root that is not a boundary", func() {
		// `/` would make every absolute path routine; it must be refused.
		policy = NewRoutinePolicy([]string{"/"})
		rule, allowed := policy.RoutineRule(
			routineQuestion("Write", map[string]any{"file_path": "/etc/hosts"}),
			workspace,
		)
		Expect(allowed).To(BeFalse())
		Expect(rule).To(Equal("write-outside-declared-root:Write"))
	})

	It("refuses when the cwd is not absolute", func() {
		rule, allowed := policy.RoutineRule(
			routineQuestion("Read", map[string]any{"file_path": "main.go"}),
			"relative/path",
		)
		Expect(allowed).To(BeFalse())
		Expect(rule).To(Equal("path-outside-workspace:Read"))
	})
})
