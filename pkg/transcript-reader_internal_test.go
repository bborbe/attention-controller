// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// writeTranscript writes a transcript for sessionID under root/project/.
func writeTranscript(root string, sessionID string, content string) string {
	dir := filepath.Join(root, "project-"+sessionID[:8])
	Expect(os.MkdirAll(dir, 0o750)).To(Succeed())
	path := filepath.Join(dir, sessionID+".jsonl")
	Expect(os.WriteFile(path, []byte(content), 0o600)).To(Succeed())
	return path
}

var _ = Describe("transcript reader", func() {
	const sessionID = "11111111-2222-3333-4444-555555555555"

	var root string
	var reader TranscriptReader

	BeforeEach(func() {
		root = GinkgoT().TempDir()
		reader = NewTranscriptReader(root)
	})

	Describe("Path and Offset", func() {
		It("returns the transcript path and its byte length", func() {
			path := writeTranscript(root, sessionID, "{\"type\":\"user\"}\n")
			Expect(reader.Path(sessionID)).To(Equal(path))
			offset, ok := reader.Offset(sessionID)
			Expect(ok).To(BeTrue())
			Expect(offset).To(Equal(int64(len("{\"type\":\"user\"}\n"))))
		})

		It("returns empty and false for an unknown session", func() {
			Expect(reader.Path("99999999-9999-9999-9999-999999999999")).To(BeEmpty())
			_, ok := reader.Offset("99999999-9999-9999-9999-999999999999")
			Expect(ok).To(BeFalse())
		})
	})

	Describe("HoldsLine", func() {
		It("finds a delivered line in a user record", func() {
			writeTranscript(
				root,
				sessionID,
				`{"type":"user","message":{"content":"Operator answer, via attention board: allow [abc]"}}`+"\n",
			)
			Expect(
				reader.HoldsLine(
					sessionID,
					"Operator answer, via attention board: allow [abc]",
					nil,
				),
			).
				To(BeTrue())
		})

		It("finds a delivered line carried inside an api-request-blob text block", func() {
			writeTranscript(root, sessionID,
				`{"type":"api-request-blob","message":{"content":[{"type":"text",`+
					`"text":"The user sent a new message while you were working: Operator answer, via attention board: deny [xyz]"}]}}`+"\n")
			Expect(
				reader.HoldsLine(
					sessionID,
					"Operator answer, via attention board: deny [xyz]",
					nil,
				),
			).
				To(BeTrue())
		})

		It("ignores an assistant record that merely quotes the line", func() {
			writeTranscript(root, sessionID,
				`{"type":"assistant","message":{"content":[{"type":"text",`+
					`"text":"I will deliver Operator answer, via attention board: allow [abc]"}]}}`+"\n")
			Expect(
				reader.HoldsLine(
					sessionID,
					"Operator answer, via attention board: allow [abc]",
					nil,
				),
			).
				To(BeFalse())
		})

		It("reports false for an empty line rather than matching everything", func() {
			writeTranscript(
				root,
				sessionID,
				`{"type":"user","message":{"content":"anything"}}`+"\n",
			)
			Expect(reader.HoldsLine(sessionID, "   ", nil)).To(BeFalse())
		})

		It("reports false when the transcript is absent", func() {
			Expect(reader.HoldsLine("99999999-9999-9999-9999-999999999999", "anything", nil)).
				To(BeFalse())
		})
	})

	Describe("negative cache", func() {
		// countingWindow wraps the real read and records how many scans the
		// reader actually performed, so a spec can prove an unchanged
		// transcript is not re-read on every call.
		countScans := func(concrete *transcriptReader, scans *int) {
			real := concrete.readWindow
			concrete.readWindow = func(path string, since *int64) ([]byte, bool) {
				*scans++
				return real(path, since)
			}
		}

		It("scans an unchanged transcript once across repeated misses", func() {
			writeTranscript(root, sessionID,
				`{"type":"user","message":{"content":"something else"}}`+"\n")
			concrete, ok := reader.(*transcriptReader)
			Expect(ok).To(BeTrue())
			scans := 0
			countScans(concrete, &scans)

			for i := 0; i < 5; i++ {
				Expect(
					reader.HoldsLine(
						sessionID,
						"Operator answer, via attention board: allow [abc]",
						nil,
					),
				).
					To(BeFalse())
			}
			Expect(scans).To(Equal(1))
		})

		It("keeps one line's proof without evicting a sibling line's", func() {
			writeTranscript(root, sessionID,
				`{"type":"user","message":{"content":"something else"}}`+"\n")
			concrete, ok := reader.(*transcriptReader)
			Expect(ok).To(BeTrue())
			scans := 0
			countScans(concrete, &scans)

			Expect(reader.HoldsLine(sessionID, "allow [aaa]", nil)).To(BeFalse())
			Expect(reader.HoldsLine(sessionID, "deny [bbb]", nil)).To(BeFalse())
			Expect(scans).To(Equal(2))

			// Both proofs survive: two cards on one session must not thrash.
			Expect(reader.HoldsLine(sessionID, "allow [aaa]", nil)).To(BeFalse())
			Expect(reader.HoldsLine(sessionID, "deny [bbb]", nil)).To(BeFalse())
			Expect(scans).To(Equal(2))
		})

		It("does not mask a line appended after the miss", func() {
			path := writeTranscript(root, sessionID,
				`{"type":"user","message":{"content":"something else"}}`+"\n")
			Expect(
				reader.HoldsLine(
					sessionID,
					"Operator answer, via attention board: allow [abc]",
					nil,
				),
			).
				To(BeFalse())

			// Append the delivered line: the size changes, so the earlier miss
			// must not answer for the new file state.
			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
			Expect(err).NotTo(HaveOccurred())
			_, err = file.WriteString(
				`{"type":"user","message":{"content":"Operator answer, via attention board: allow [abc]"}}` + "\n",
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(file.Close()).To(Succeed())

			Expect(
				reader.HoldsLine(
					sessionID,
					"Operator answer, via attention board: allow [abc]",
					nil,
				),
			).
				To(BeTrue())
		})

		It("drops a proof when the transcript disappears", func() {
			path := writeTranscript(root, sessionID,
				`{"type":"user","message":{"content":"something else"}}`+"\n")
			Expect(reader.HoldsLine(sessionID, "allow [abc]", nil)).To(BeFalse())
			concrete, ok := reader.(*transcriptReader)
			Expect(ok).To(BeTrue())
			Expect(concrete.cache).To(HaveLen(1))
			Expect(os.Remove(path)).To(Succeed())
			Expect(reader.HoldsLine(sessionID, "allow [abc]", nil)).To(BeFalse())
			Expect(concrete.cache).To(BeEmpty())
		})
	})

	Describe("readTranscriptWindow", func() {
		It("reads the tail window, not the 16 MiB scan cap", func() {
			padding := strings.Repeat(`{"type":"user","message":{"content":"pad"}}`+"\n", 20000)
			path := writeTranscript(root, sessionID, padding)
			raw, ok := readTranscriptWindow(path, nil)
			Expect(ok).To(BeTrue())
			Expect(len(raw)).To(Equal(transcriptReaderTailBytes))
		})

		It("still finds a line that sits inside the tail", func() {
			padding := strings.Repeat(`{"type":"user","message":{"content":"pad"}}`+"\n", 20000)
			path := writeTranscript(root, sessionID,
				padding+`{"type":"user","message":{"content":"allow [abc]"}}`+"\n")
			Expect(reader.HoldsLine(sessionID, "allow [abc]", nil)).To(BeTrue())
			raw, ok := readTranscriptWindow(path, nil)
			Expect(ok).To(BeTrue())
			Expect(len(raw)).To(Equal(transcriptReaderTailBytes))
		})
	})

	Describe("TurnEnded", func() {
		It("is true when the last timestamped record is a turn-end", func() {
			writeTranscript(root, sessionID,
				`{"type":"assistant","timestamp":"2026-11-13T10:00:00Z"}`+"\n"+
					`{"type":"system","subtype":"stop_hook_summary","timestamp":"2026-11-13T10:00:01Z"}`+"\n")
			Expect(reader.TurnEnded(sessionID)).To(BeTrue())
		})

		It("is false when the last timestamped record is an assistant turn", func() {
			writeTranscript(
				root,
				sessionID,
				`{"type":"system","subtype":"turn_duration","timestamp":"2026-11-13T10:00:00Z"}`+"\n"+
					`{"type":"assistant","timestamp":"2026-11-13T10:00:01Z"}`+"\n",
			)
			Expect(reader.TurnEnded(sessionID)).To(BeFalse())
		})

		It("skips untimed bookkeeping lines after the turn-end", func() {
			writeTranscript(
				root,
				sessionID,
				`{"type":"system","subtype":"turn_duration","timestamp":"2026-11-13T10:00:00Z"}`+"\n"+
					`{"type":"file-history-snapshot"}`+"\n",
			)
			Expect(reader.TurnEnded(sessionID)).To(BeTrue())
		})

		It("is false when the transcript is absent", func() {
			Expect(reader.TurnEnded("99999999-9999-9999-9999-999999999999")).To(BeFalse())
		})
	})

	Describe("CompactionBoundaries", func() {
		It("counts only records that DECLARE the subtype", func() {
			// ⚠️ The marker string also occurs inside tool results and message text
			// that merely quote a compaction. Counting those fabricates the boundary
			// `post-compact` waits for, so only a TOP-LEVEL subtype counts.
			writeTranscript(
				root,
				sessionID,
				`{"type":"system","subtype":"compact_boundary","compactMetadata":{"preTokens":100}}`+"\n"+
					`{"type":"user","message":{"content":"we saw compact_boundary earlier"}}`+"\n"+
					`{"type":"user","toolUseResult":{"note":"compact_boundary"}}`+"\n"+
					`{"type":"system","subtype":"compact_boundary","compactMetadata":{"preTokens":200}}`+"\n",
			)
			count, ok := reader.CompactionBoundaries(sessionID)
			Expect(ok).To(BeTrue())
			Expect(count).To(Equal(2))
		})

		It("re-reads only when the file changed", func() {
			path := writeTranscript(
				root,
				sessionID,
				`{"type":"system","subtype":"compact_boundary"}`+"\n",
			)
			first, ok := reader.CompactionBoundaries(sessionID)
			Expect(ok).To(BeTrue())
			Expect(first).To(Equal(1))

			// An append moves size and mtime, so the cached count is invalidated.
			Expect(os.WriteFile(
				path,
				[]byte(`{"type":"system","subtype":"compact_boundary"}`+"\n"+
					`{"type":"system","subtype":"compact_boundary"}`+"\n"),
				0o600,
			)).To(Succeed())
			second, ok := reader.CompactionBoundaries(sessionID)
			Expect(ok).To(BeTrue())
			Expect(second).To(Equal(2))
		})

		It("is false when the transcript is absent", func() {
			_, ok := reader.CompactionBoundaries("99999999-9999-9999-9999-999999999999")
			Expect(ok).To(BeFalse())
		})
	})

	Describe("LastAssistantText", func() {
		It("TestTranscriptReaderLastAssistantText", func() {
			// ⚠️ The last assistant record is frequently NOT the last record in the
			// file: a `user` tool-result record follows it.
			writeTranscript(
				root,
				sessionID,
				`{"type":"assistant","message":{"content":[{"type":"text","text":"closing: pick"}]}}`+"\n"+
					`{"type":"user","message":{"content":[{"type":"tool_result",`+
					`"tool_use_id":"tu-9","content":"ok"}]}}`+"\n",
			)
			text, ok := reader.LastAssistantText(sessionID)
			Expect(ok).To(BeTrue())
			Expect(text).To(Equal("closing: pick"))

			// Two assistant text blocks: the LAST one wins, not the first.
			writeTranscript(
				root,
				sessionID,
				`{"type":"assistant","message":{"content":[{"type":"text","text":"first"}]}}`+"\n"+
					`{"type":"assistant","message":{"content":[{"type":"text","text":"second"}]}}`+"\n",
			)
			text, ok = reader.LastAssistantText(sessionID)
			Expect(ok).To(BeTrue())
			Expect(text).To(Equal("second"))

			// No assistant text at all is ("", false), never ("", true): an empty
			// result must not be readable as "the last block carries no closer".
			writeTranscript(
				root,
				sessionID,
				`{"type":"user","message":{"content":"anything"}}`+"\n",
			)
			text, ok = reader.LastAssistantText(sessionID)
			Expect(ok).To(BeFalse())
			Expect(text).To(BeEmpty())

			text, ok = reader.LastAssistantText("99999999-9999-9999-9999-999999999999")
			Expect(ok).To(BeFalse())
			Expect(text).To(BeEmpty())
		})
	})

	Describe("PendingToolCall", func() {
		It("TestTranscriptReaderPendingToolCall", func() {
			writeTranscript(
				root,
				sessionID,
				`{"type":"assistant","message":{"content":[{"type":"tool_use",`+
					`"id":"tu-1","name":"AskUserQuestion"}]}}`+"\n",
			)
			name, ok := reader.PendingToolCall(sessionID)
			Expect(ok).To(BeTrue())
			Expect(name).To(Equal("AskUserQuestion"))

			// A tool_result for the same call closes it.
			writeTranscript(
				root,
				sessionID,
				`{"type":"assistant","message":{"content":[{"type":"tool_use",`+
					`"id":"tu-1","name":"AskUserQuestion"}]}}`+"\n"+
					`{"type":"user","message":{"content":[{"type":"tool_result",`+
					`"tool_use_id":"tu-1"}]}}`+"\n",
			)
			name, ok = reader.PendingToolCall(sessionID)
			Expect(ok).To(BeFalse())
			Expect(name).To(BeEmpty())

			// ⚠️ An OLDER call's result must not clear a newer pending call.
			writeTranscript(
				root,
				sessionID,
				`{"type":"assistant","message":{"content":[{"type":"tool_use",`+
					`"id":"tu-1","name":"Bash"}]}}`+"\n"+
					`{"type":"assistant","message":{"content":[{"type":"tool_use",`+
					`"id":"tu-2","name":"AskUserQuestion"}]}}`+"\n"+
					`{"type":"user","message":{"content":[{"type":"tool_result",`+
					`"tool_use_id":"tu-1"}]}}`+"\n",
			)
			name, ok = reader.PendingToolCall(sessionID)
			Expect(ok).To(BeTrue())
			Expect(name).To(Equal("AskUserQuestion"))

			name, ok = reader.PendingToolCall("99999999-9999-9999-9999-999999999999")
			Expect(ok).To(BeFalse())
			Expect(name).To(BeEmpty())
		})
	})
})
