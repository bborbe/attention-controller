// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// managerStuckTestBase is the clock every fixture's record stamps hang off, so
// the windowed cause is reproducible rather than dependent on the test's own
// wall clock.
var managerStuckTestBase = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

const (
	managerStuckTestClosedBody = "Tool permission request failed: AbortError: Stream closed"
	managerStuckTestDeniedBody = "Tool permission request failed: denied"
)

// managerStuckStamp renders one record timestamp at an offset from the base.
func managerStuckStamp(offset time.Duration) string {
	return managerStuckTestBase.Add(offset).Format(time.RFC3339Nano)
}

// managerStuckJSON renders one transcript record as a line.
func managerStuckJSON(record map[string]any) string {
	raw, err := json.Marshal(record)
	Expect(err).NotTo(HaveOccurred())
	return string(raw) + "\n"
}

// managerStuckAssistant renders one assistant record carrying a text block.
func managerStuckAssistant(stamp string, text string) string {
	return managerStuckJSON(map[string]any{
		"type":      "assistant",
		"timestamp": stamp,
		"message": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": text}},
		},
	})
}

// managerStuckToolResult renders one user record carrying a tool_result block.
func managerStuckToolResult(stamp string, content string, isError bool) string {
	block := map[string]any{"type": "tool_result", "content": content}
	if isError {
		block["is_error"] = true
	}
	return managerStuckJSON(map[string]any{
		"type":      "user",
		"timestamp": stamp,
		"message":   map[string]any{"content": []any{block}},
	})
}

// expectStuckCausePerFixture derives each of the four causes from transcript
// bytes, with no fixture supplying a cause string.
func expectStuckCausePerFixture() {
	By("deriving stream-closed from a trailing closed channel")
	streamClosed := []byte(
		managerStuckAssistant(managerStuckStamp(0), "working") +
			managerStuckToolResult(
				managerStuckStamp(time.Minute),
				managerStuckTestClosedBody,
				true,
			),
	)
	Expect(managerStuckReason("", streamClosed, streamClosed)).
		To(Equal(managerStuckCauseStreamClosed))

	By("deriving permission-channel from three aborts inside the window")
	channel := []byte(
		managerStuckToolResult(managerStuckStamp(0), managerStuckTestClosedBody, true) +
			managerStuckToolResult(
				managerStuckStamp(time.Minute),
				managerStuckTestClosedBody,
				true,
			) +
			managerStuckToolResult(
				managerStuckStamp(2*time.Minute),
				managerStuckTestClosedBody,
				true,
			) +
			managerStuckToolResult(managerStuckStamp(3*time.Minute), "ok", false),
	)
	Expect(managerStuckReason("", channel, channel)).
		To(Equal(managerStuckCausePermissionChannel))

	By("deriving blocked-closer from a blocked panel nobody can answer")
	Expect(managerStuckReason("🔴 BLOCKED — cannot proceed\n👤 You: nothing", nil, nil)).
		To(Equal(managerStuckCauseBlockedCloser))

	By("deriving permission-failures from a trailing run without a closed channel")
	failures := []byte(
		managerStuckToolResult(managerStuckStamp(0), managerStuckTestDeniedBody, true) +
			managerStuckToolResult(
				managerStuckStamp(time.Minute),
				managerStuckTestDeniedBody,
				true,
			) +
			managerStuckToolResult(
				managerStuckStamp(2*time.Minute),
				managerStuckTestDeniedBody,
				true,
			),
	)
	Expect(managerStuckReason("", failures, failures)).
		To(Equal(managerStuckCausePermissionFailures))
}

// expectStuckCauseOrder proves the most specific cause wins.
func expectStuckCauseOrder() {
	By("naming the cause rather than the symptom when both fire")
	both := []byte(
		managerStuckToolResult(managerStuckStamp(0), managerStuckTestClosedBody, true) +
			managerStuckToolResult(
				managerStuckStamp(time.Minute),
				managerStuckTestClosedBody,
				true,
			) +
			managerStuckToolResult(
				managerStuckStamp(2*time.Minute),
				managerStuckTestClosedBody,
				true,
			),
	)
	Expect(managerStuckReason("", both, both)).To(Equal(managerStuckCauseStreamClosed))
}

// expectStuckWiderRead proves permission-channel fires only from the wider read.
func expectStuckWiderRead() {
	By("seeing the abort only through readRecentTail")
	path := filepath.Join(GinkgoT().TempDir(), "session.jsonl")
	var fixture strings.Builder
	for index := 0; index < 3; index++ {
		fixture.WriteString(managerStuckToolResult(
			managerStuckStamp(time.Duration(index)*time.Minute), managerStuckTestClosedBody, true,
		))
	}
	padding := strings.Repeat("x", 1000)
	for index := 0; index < 600; index++ {
		fixture.WriteString(managerStuckAssistant(
			managerStuckStamp(3*time.Minute+time.Duration(index)*time.Second), padding,
		))
	}
	content := fixture.String()
	Expect(os.WriteFile(path, []byte(content), 0o600)).To(Succeed())

	narrowStart := len(content) - 400*1024
	if narrowStart < 0 {
		narrowStart = 0
	}
	narrow := []byte(content[narrowStart:])
	Expect(string(narrow)).NotTo(ContainSubstring(managerStuckStreamClosedMark))

	wide, ok := readRecentTail(path, managerStuckPermChannelWindow, managerStuckReadMaxBytes)
	Expect(ok).To(BeTrue())
	Expect(string(wide)).To(ContainSubstring(managerStuckStreamClosedMark))

	Expect(managerStuckReason("", narrow, wide)).To(Equal(managerStuckCausePermissionChannel))
	Expect(managerStuckReason("", narrow, narrow)).To(BeEmpty())
}

// expectStuckBlockedCloserDiscriminator proves an answerable verb is a park.
func expectStuckBlockedCloserDiscriminator() {
	By("not firing blocked-closer on a verb-carrying closer")
	Expect(managerStuckReason("🔴 BLOCKED — cannot proceed\n👤 You: approve: run it", nil, nil)).
		To(BeEmpty())
	By("firing blocked-closer on a message with no panel at all")
	Expect(managerStuckReason("🔴 BLOCKED — cannot proceed", nil, nil)).
		To(Equal(managerStuckCauseBlockedCloser))
}

// expectStuckProseIsNotAFailure proves assistant prose quoting the wording is
// not a tool result.
func expectStuckProseIsNotAFailure() {
	By("ignoring prose that quotes the failure wording")
	prose := []byte(
		managerStuckAssistant(managerStuckStamp(0), "permission request failed: Stream closed") +
			managerStuckAssistant(
				managerStuckStamp(time.Minute),
				"permission request failed: Stream closed",
			) +
			managerStuckAssistant(
				managerStuckStamp(2*time.Minute),
				"permission request failed: Stream closed",
			),
	)
	Expect(managerPermissionOutcomes(prose)).To(BeEmpty())
	Expect(managerStuckReason("", prose, prose)).To(BeEmpty())
}

// expectStuckTrailingRunResets proves a tool that ran clears the run.
func expectStuckTrailingRunResets() {
	By("resetting the trailing run on a tool that ran")
	tail := []byte(
		managerStuckToolResult(managerStuckStamp(0), managerStuckTestDeniedBody, true) +
			managerStuckToolResult(
				managerStuckStamp(time.Minute),
				managerStuckTestDeniedBody,
				true,
			) +
			managerStuckToolResult(
				managerStuckStamp(2*time.Minute),
				managerStuckTestDeniedBody,
				true,
			) +
			managerStuckToolResult(managerStuckStamp(3*time.Minute), "ok", false),
	)
	Expect(managerTrailingFailures(managerPermissionOutcomes(tail))).To(Equal(0))
	Expect(managerStuckReason("", tail, tail)).To(BeEmpty())
}

// expectStuckNilMessageSkipped proves a record with no message is skipped rather
// than dereferenced.
func expectStuckNilMessageSkipped() {
	By("skipping a record with no message instead of panicking")
	Expect(managerPermissionOutcomes([]byte(
		`{"type":"summary","summary":"x"}` + "\n" +
			`{"type":"system","subtype":"hook"}` + "\n" +
			`{"type":"file-history-snapshot"}` + "\n",
	))).To(BeEmpty())

	By("not letting a nil-message line break the trailing run")
	tail := []byte(
		managerStuckToolResult(managerStuckStamp(0), managerStuckTestDeniedBody, true) +
			`{"type":"summary","summary":"x","timestamp":"` + managerStuckStamp(time.Second) + `"}` + "\n" +
			managerStuckToolResult(
				managerStuckStamp(time.Minute),
				managerStuckTestDeniedBody,
				true,
			),
	)
	Expect(managerTrailingFailures(managerPermissionOutcomes(tail))).To(Equal(2))
}

// expectStuckGatePrefixes proves both prefixes are formed for every cause.
func expectStuckGatePrefixes() {
	By("forming both prefixes for every cause")
	Expect(managerStuckGateReason("", false)).To(BeEmpty())
	Expect(managerStuckGateReason("", true)).To(BeEmpty())
	for _, cause := range []string{
		managerStuckCauseStreamClosed,
		managerStuckCausePermissionChannel,
		managerStuckCauseBlockedCloser,
		managerStuckCausePermissionFailures,
	} {
		Expect(managerStuckGateReason(cause, false)).To(Equal("stuck:" + cause))
		Expect(managerStuckGateReason(cause, true)).To(Equal("stuck-tab:" + cause))
	}
}

var _ = Describe("manager gate stuck", func() {
	It("StuckReasonCauses", func() {
		expectStuckCausePerFixture()
		expectStuckCauseOrder()
		expectStuckWiderRead()
		expectStuckBlockedCloserDiscriminator()
		expectStuckProseIsNotAFailure()
		expectStuckTrailingRunResets()
		expectStuckNilMessageSkipped()
		expectStuckGatePrefixes()
	})
})
