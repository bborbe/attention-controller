// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// transcriptHandoverRecord is the assistant turn that makes a later refusal
// meaningful: its own `👤 You:` value starts with an operator-action verb.
const transcriptHandoverRecord = `{"type":"assistant","timestamp":"2026-10-05T10:00:00Z",` +
	`"message":{"content":[{"type":"text","text":"👤 You: you run: reply ` + "`done`" + `"}]}}` + "\n"

// transcriptAffirmativeRecord is the operator's bare affirmative, which is the
// answer the refusal refuses.
const transcriptAffirmativeRecord = `{"type":"user","message":{"content":"y"}}` + "\n"

// transcriptRefusalRecord builds the assistant turn carrying a watched phrase.
func transcriptRefusalRecord(timestamp string, text string) string {
	return `{"type":"assistant","timestamp":"` + timestamp + `",` +
		`"message":{"content":[{"type":"text","text":"` + text + `"}]}}` + "\n"
}

// transcriptTrigger is the three-record shape a hit needs: handover, bare
// affirmative, refusal.
func transcriptTrigger(timestamp string, phrase string) string {
	return transcriptHandoverRecord +
		transcriptAffirmativeRecord +
		transcriptRefusalRecord(timestamp, "Refused: "+phrase+".")
}

var _ = Describe("transcript phrase tailer", func() {
	var ctx context.Context
	var root string
	var statePath string
	var tailer TranscriptPhraseTailer

	BeforeEach(func() {
		ctx = context.Background()
		root = GinkgoT().TempDir()
		statePath = filepath.Join(GinkgoT().TempDir(), "offsets.json")
		tailer = NewTranscriptPhraseTailer(
			root,
			statePath,
			[]string{
				classDRefusalWatchPhraseRefusal,
				classDRefusalWatchPhraseKeystroke,
			},
			libtime.NewCurrentDateTime(),
		)
	})

	// writeTranscript creates <root>/<session>.jsonl with content, appending
	// when the file already exists.
	writeTranscript := func(session string, content string) string {
		dir := filepath.Join(root, session)
		Expect(os.MkdirAll(dir, 0o750)).To(Succeed())
		path := filepath.Join(dir, session+".jsonl")
		file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		Expect(err).To(BeNil())
		defer file.Close()
		_, err = file.WriteString(content)
		Expect(err).To(BeNil())
		return path
	}

	It("reports both wordings across two transcripts", func() {
		writeTranscript("session-a", transcriptHandoverRecord)
		writeTranscript("session-b", transcriptHandoverRecord)

		// The first scan arms both files at their current size, so nothing that
		// predates the watch is reported.
		armed, err := tailer.Scan(ctx)
		Expect(err).To(BeNil())
		Expect(armed).To(BeEmpty())

		writeTranscript("session-a", transcriptTrigger("2026-10-05T10:00:05Z",
			classDRefusalWatchPhraseRefusal))
		writeTranscript("session-b", transcriptTrigger("2026-10-05T10:01:05Z",
			classDRefusalWatchPhraseKeystroke))

		hits, err := tailer.Scan(ctx)
		Expect(err).To(BeNil())
		Expect(hits).To(HaveLen(2))
		Expect(hits[0].Session).To(Equal("session-a"))
		Expect(hits[0].Timestamp).To(Equal("2026-10-05T10:00:05Z"))
		Expect(hits[0].Excerpt).To(ContainSubstring(classDRefusalWatchPhraseRefusal))
		Expect(hits[1].Session).To(Equal("session-b"))
		Expect(hits[1].Excerpt).To(ContainSubstring(classDRefusalWatchPhraseKeystroke))
	})

	It("resumes from the persisted offsets without re-reporting a hit", func() {
		writeTranscript("session-a", transcriptHandoverRecord)
		_, err := tailer.Scan(ctx)
		Expect(err).To(BeNil())

		writeTranscript("session-a", transcriptTrigger("2026-10-05T10:00:05Z",
			classDRefusalWatchPhraseRefusal))
		first, err := tailer.Scan(ctx)
		Expect(err).To(BeNil())
		Expect(first).To(HaveLen(1))

		// A second run over unchanged bytes reports nothing.
		second, err := tailer.Scan(ctx)
		Expect(err).To(BeNil())
		Expect(second).To(BeEmpty())

		// A resumed tailer built from the same state file agrees: the offsets
		// persisted, so the earlier hit is not re-reported by a fresh process.
		resumed := NewTranscriptPhraseTailer(
			root,
			statePath,
			[]string{classDRefusalWatchPhraseRefusal, classDRefusalWatchPhraseKeystroke},
			libtime.NewCurrentDateTime(),
		)
		third, err := resumed.Scan(ctx)
		Expect(err).To(BeNil())
		Expect(third).To(BeEmpty())
	})

	It("arms a transcript it has never seen at its current size", func() {
		writeTranscript("session-a", transcriptTrigger("2026-10-05T10:00:05Z",
			classDRefusalWatchPhraseRefusal))

		// Everything present at arming predates the watch.
		armed, err := tailer.Scan(ctx)
		Expect(err).To(BeNil())
		Expect(armed).To(BeEmpty())

		writeTranscript("session-a", transcriptTrigger("2026-10-05T10:05:05Z",
			classDRefusalWatchPhraseRefusal))

		hits, err := tailer.Scan(ctx)
		Expect(err).To(BeNil())
		Expect(hits).To(HaveLen(1))
		Expect(hits[0].Timestamp).To(Equal("2026-10-05T10:05:05Z"))
	})

	It("ignores a phrase quoted in a tool result rather than spoken", func() {
		writeTranscript("session-a", transcriptHandoverRecord)
		_, err := tailer.Scan(ctx)
		Expect(err).To(BeNil())

		// An assistant turn that merely READS the phrase is not a refusal: the
		// first discriminator is that the phrase sits in a text block of a turn
		// that follows a handover answered with a bare affirmative. Here the
		// operator's answer is not a bare affirmative, so nothing fires.
		writeTranscript("session-a",
			`{"type":"user","message":{"content":"please check the file"}}`+"\n"+
				transcriptRefusalRecord("2026-10-05T10:00:05Z", "not an answer"))

		hits, err := tailer.Scan(ctx)
		Expect(err).To(BeNil())
		Expect(hits).To(BeEmpty())
	})

	It("renders the Python engine's HIT line", func() {
		hit := TranscriptPhraseHit{
			File:      "/tmp/session-a.jsonl",
			Session:   "session-a",
			Timestamp: "2026-10-05T10:00:05Z",
			Excerpt:   "That is not an answer",
		}
		Expect(hit.String()).To(
			Equal("HIT session-a 2026-10-05T10:00:05Z :: That is not an answer"),
		)
	})
})

var _ = Describe("newestClassDRefusalVersionDir", func() {
	It("ignores a non-version directory that sorts last", func() {
		Expect(newestClassDRefusalVersionDir([]string{
			"/cache/supervisor/0.9.0",
			"/cache/supervisor/0.10.0",
			"/cache/supervisor/builtin",
		})).To(Equal("/cache/supervisor/0.10.0"))
	})

	It("returns empty when no directory is version-like", func() {
		Expect(newestClassDRefusalVersionDir([]string{"/cache/supervisor/builtin"})).To(BeEmpty())
	})
})
