// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"strings"
	"time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A card that reopens N times used to carry N identical `reopened: …` lines —
// the operator's "some cards in the subtitle multiple times the same line".
// These specs pin the one counted line that replaces them.
var _ = Describe("appendEvidenceNote", func() {
	// at builds a fixed instant so the rendered HH:MM is deterministic.
	at := func(hour, minute int) libtime.DateTime {
		return libtime.DateTime(time.Date(2026, 11, 19, hour, minute, 0, 0, time.Local))
	}

	countLines := func(note string) []string {
		lines := make([]string, 0)
		for _, line := range strings.Split(note, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), evidenceNotePrefix) {
				lines = append(lines, line)
			}
		}
		return lines
	}

	It("writes one counted line on the first reopen", func() {
		Expect(appendEvidenceNote("", at(17, 5))).To(Equal("reopened ×1, last 17:05"))
	})

	It("replaces the line instead of appending a second one", func() {
		note := appendEvidenceNote("", at(17, 5))
		note = appendEvidenceNote(note, at(17, 35))
		note = appendEvidenceNote(note, at(18, 5))

		Expect(countLines(note)).To(HaveLen(1), "N reopens must leave one line, not N")
		Expect(note).To(Equal("reopened ×3, last 18:05"))
	})

	It("counts the legacy bare note as one reopen", func() {
		Expect(appendEvidenceNote(EvidenceNote, at(9, 12))).
			To(Equal("reopened ×2, last 09:12"))
	})

	It("keeps the surrounding context lines", func() {
		note := "asked in pane 229\ntype anything and press Enter"
		got := appendEvidenceNote(note, at(17, 5))
		Expect(got).To(Equal(note + "\nreopened ×1, last 17:05"))
	})

	It("keeps unrelated lines when it collapses an existing reopen line", func() {
		note := "first line\n" + EvidenceNote + "\nlast line"
		got := appendEvidenceNote(note, at(17, 5))
		Expect(got).To(Equal("first line\nlast line\nreopened ×2, last 17:05"))
	})
})
