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
			Expect(os.Remove(path)).To(Succeed())
			Expect(reader.HoldsLine(sessionID, "allow [abc]", nil)).To(BeFalse())
			Expect(reader.(*transcriptReader).cache).To(BeEmpty())
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
})
