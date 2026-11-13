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

// routineQuestion builds a wrapper question envelope for a tool call.
func routineQuestion(tool string, call map[string]any) json.RawMessage {
	raw, err := json.Marshal(map[string]any{
		"input": map[string]any{
			"arguments": map[string]any{
				"tool_name": tool,
				"input":     call,
			},
		},
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
