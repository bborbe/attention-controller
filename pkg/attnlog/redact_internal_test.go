// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package attnlog

import (
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("stateDir", func() {
	It("prefers the injected ATTENTION_STATE_DIR", func() {
		app := &App{Getenv: func(key string) string {
			if key == stateDirEnv {
				return "/tmp/custom"
			}
			return ""
		}}
		Expect(app.stateDir()).To(Equal("/tmp/custom"))
	})

	It("falls back to the home directory when the env var is unset", func() {
		app := &App{Getenv: func(string) string { return "" }}
		Expect(app.stateDir()).To(HaveSuffix(filepath.Join(".claude", "state", "attention")))
	})
})

var _ = Describe("redact", func() {
	DescribeTable(
		"replaces the header and bearer forms only",
		func(input, expected string) {
			Expect(redact(input)).To(Equal(expected))
		},
		Entry(
			"authorization bearer header",
			"Authorization: Bearer abc123",
			"Authorization: [REDACTED]",
		),
		Entry("authorization bare header", "authorization: abc123", "authorization: [REDACTED]"),
		Entry("bare bearer", "curl -H Bearer abc123", "curl -H Bearer [REDACTED]"),
		Entry("non-credential is untouched", "Bash: ls -la", "Bash: ls -la"),
		Entry("a bare assignment is NOT redacted by redact", "TOKEN=x", "TOKEN=x"),
	)

	DescribeTable("scrubDetail adds the bare assignment form",
		func(input, expected string) {
			Expect(scrubDetail(input)).To(Equal(expected))
		},
		Entry("exported token", "export TOKEN=x", "export TOKEN=<redacted>"),
		Entry("api key", "API_KEY=abc", "API_KEY=<redacted>"),
		Entry("password", "PASSWORD=hunter2", "PASSWORD=<redacted>"),
		Entry("header still redacted", "Authorization: Bearer abc", "Authorization: [REDACTED]"),
		Entry("ordinary prose untouched", "Bash: go test ./...", "Bash: go test ./..."),
	)
})

var _ = Describe("EventItemID", func() {
	It("hashes session, kind and redacted detail over the whole untruncated value", func() {
		Expect(EventItemID("s1", "question", "What?")).
			To(Equal("38f7e91ea678e5c0b22603fe3ec921f6eb012134edde98590a7cdcea57221898"))
	})

	It("collapses two events of one gate to the same id", func() {
		first := EventItemID("s1", "question", "Same question")
		second := EventItemID("s1", "question", "Same question")
		Expect(first).To(Equal(second))
	})
})
