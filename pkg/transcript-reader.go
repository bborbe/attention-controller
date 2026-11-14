// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// transcriptReaderTailBytes is the tail a delivery check reads when it has no
// byte offset to start from. The submit check normally scans from the offset
// recorded at typing time (see Offset), so this is the fallback for a marker
// written before that offset existed — NOT the working window.
//
// ⚠️ Measured 2026-10-18: the Fleet Manager's transcript was 675 MB, so a fixed
// tail would have read the wrong hour and reported every delivery as missing.
const transcriptReaderTailBytes = 262144

// transcriptReaderScanMaxBytes caps how much one delivery check reads, so a
// marker whose offset went stale (compaction rewrites the transcript) cannot
// turn a retry into a multi-hundred-megabyte read on the one path that must
// stay responsive.
const transcriptReaderScanMaxBytes = 16777216

// transcriptReaderBackupBytes is how far BEFORE a recorded offset the scan
// starts, so a record split across the mark is read whole rather than discarded
// as a partial.
const transcriptReaderBackupBytes = 4096

// transcriptReaderTurnEndTailBytes is how much of the transcript tail the
// turn-end check reads. Transcripts reach hundreds of MB; the turn-end records
// are a few hundred bytes and always last, so the tail is enough.
const transcriptReaderTurnEndTailBytes = 64 * 1024

// transcriptTurnEndSubtypes are the records the harness appends when a turn
// ENDS. A permission gate holds its turn open, so a turn-end as the session's
// last record proves every gate that turn raised is already closed.
var transcriptTurnEndSubtypes = map[string]struct{}{
	"stop_hook_summary": {},
	"turn_duration":     {},
}

// TranscriptReader reads the delivery-side facts out of a session's Claude
// transcript: whether a typed line actually arrived, and whether the session
// has moved past the gate.
//
// ⚠️ It exists because the COMPOSER cannot answer "did the answer land?", and
// asking it was a measured redelivery defect: it reads `empty` both for a turn
// that started and for a text the TUI swallowed without submitting, and it
// reads `busy` for a whole turn whenever the last drawn glyph line is the
// echoed message. The transcript is the only record of what the session
// actually received, so it is the only honest submit proof.
//
// It is an interface so a spec can drive the carrier against a fake transcript
// rather than a live ~/.claude tree.
type TranscriptReader interface {
	// Path returns the session's transcript path, or "" when none exists. It
	// never fails: an absent transcript is a fact, not an error.
	Path(sessionID string) string

	// Offset returns the transcript's current byte length and whether it is
	// known. Taken immediately BEFORE an answer is typed, so everything the
	// delivery writes lands after it — which is what makes the submit check
	// cheap on a long-lived session.
	Offset(sessionID string) (int64, bool)

	// HoldsLine reports whether the transcript already carries line as a
	// delivered message at or after since. A nil since falls back to the tail
	// window. It never fails: an unreadable transcript reports false, which is
	// the conservative direction — the caller then treats the delivery as not
	// yet proven and does not drop the answer.
	HoldsLine(sessionID string, line string, since *int64) bool

	// TurnEnded reports whether the session's last timestamped record is a
	// turn-end. It never fails: a missing or unreadable transcript reports
	// false, keeping the caller's "no live process" refusal.
	TurnEnded(sessionID string) bool
}

// NewTranscriptReader creates the reader the delivery carrier uses.
//
// ⚠️ The root is a parameter rather than a package constant so a spec can point
// the reader at a temp directory, and so the layout the Python watcher used —
// `~/.claude/projects/*/<session>.jsonl` — is a caller's decision rather than
// something baked into the reader.
func NewTranscriptReader(root string) TranscriptReader {
	return &transcriptReader{root: root}
}

type transcriptReader struct {
	root string
}

// Path returns the newest transcript for the session, or "".
//
// ⚠️ The glob and the stat are two syscalls, and a transcript can be pruned or
// rotated between them — an unreadable mtime then sorts last, the same
// conservative direction as a missing transcript.
func (r *transcriptReader) Path(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	pattern := filepath.Join(r.root, "*", sessionID+".jsonl")
	found, err := filepath.Glob(pattern)
	if err != nil || len(found) == 0 {
		return ""
	}
	sort.Slice(found, func(i, j int) bool {
		return transcriptMTime(found[i]) > transcriptMTime(found[j])
	})
	return found[0]
}

// Offset returns the transcript's current byte length.
func (r *transcriptReader) Offset(sessionID string) (int64, bool) {
	path := r.Path(sessionID)
	if path == "" {
		return 0, false
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	return info.Size(), true
}

// HoldsLine reports whether the transcript carries line as a delivered message.
//
// ⚠️ It matches `type: user` AND `api-request-blob` records. A message that
// arrives while the session is MID-TURN is never written as a `type: user`
// record at all — the harness surfaces it as a text block inside the next
// `api-request-blob`. Filtering on `type: user` therefore made the reader blind
// to every mid-turn delivery and the retry re-typed forever (measured
// 2026-10-19: six typings in four minutes, every delivery present only as an
// api-request-blob block).
//
// It matches by CONTAINMENT rather than equality because the harness sometimes
// wraps an arriving message ("The user sent a new message while you were
// working: …"), and a wrapped delivery is still a delivery. A tool result that
// happens to quote the delivered line is filtered out by the record-type test.
func (r *transcriptReader) HoldsLine(sessionID string, line string, since *int64) bool {
	path := r.Path(sessionID)
	if path == "" {
		return false
	}
	want := strings.TrimSpace(line)
	if want == "" {
		return false
	}
	raw, ok := readTranscriptWindow(path, since)
	if !ok {
		return false
	}
	for _, chunk := range bytes.Split(raw, []byte("\n")) {
		if transcriptRecordQuotes(string(chunk), want) {
			return true
		}
	}
	return false
}

// TurnEnded reports whether the session's last timestamped record is a
// turn-end.
//
// ⚠️ Conservative in every unsure direction: no transcript, an unreadable
// tail, or a last line that does not parse all read false, which keeps the
// caller's "no live process" refusal. Records without a `timestamp` are
// skipped, because the harness appends untimed bookkeeping lines after a turn
// ends.
func (r *transcriptReader) TurnEnded(sessionID string) bool {
	path := r.Path(sessionID)
	if path == "" {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return false
	}
	size := info.Size()
	start := size - transcriptReaderTurnEndTailBytes
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, 0); err != nil {
		return false
	}
	buf := make([]byte, size-start)
	n, err := file.Read(buf)
	if err != nil && n == 0 {
		return false
	}
	return transcriptTailEndsTurn(buf[:n], start > 0)
}

// transcriptTailEndsTurn walks the tail backwards to the last timestamped,
// parseable record and reports whether it is a turn-end.
func transcriptTailEndsTurn(tail []byte, midFile bool) bool {
	lines := bytes.Split(tail, []byte("\n"))
	if midFile && len(lines) > 0 {
		lines = lines[1:] // the first line of a mid-file read is partial
	}
	for i := len(lines) - 1; i >= 0; i-- {
		raw := bytes.TrimSpace(lines[i])
		if len(raw) == 0 {
			continue
		}
		var record struct {
			Type      string `json:"type"`
			Subtype   string `json:"subtype"`
			Timestamp string `json:"timestamp"`
		}
		if err := json.Unmarshal(raw, &record); err != nil {
			return false // a torn last record is no proof of anything
		}
		if record.Timestamp == "" {
			continue
		}
		if record.Type != "system" {
			return false
		}
		_, turnEnd := transcriptTurnEndSubtypes[record.Subtype]
		return turnEnd
	}
	return false
}

// readTranscriptWindow reads the portion of the transcript a delivery check
// looks at: from just before `since` when it is set, else the tail, capped at
// transcriptReaderScanMaxBytes.
//
// ⚠️ The cap bounds how much is READ, never where the read STARTS. Clamping the
// start forward (an earlier shape) skipped past the delivery and reported every
// delivered answer as missing — the false negative this whole check exists to
// remove.
func readTranscriptWindow(path string, since *int64) ([]byte, bool) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, false
	}
	size := info.Size()
	start := size - transcriptReaderTailBytes
	if since != nil && *since > 0 {
		start = *since - transcriptReaderBackupBytes
	}
	if start < 0 {
		start = 0
	}
	if start > size {
		start = size
	}
	if _, err := file.Seek(start, 0); err != nil {
		return nil, false
	}
	buf := make([]byte, transcriptReaderScanMaxBytes)
	n, err := file.Read(buf)
	if err != nil && n == 0 {
		return nil, false
	}
	return buf[:n], true
}

// transcriptRecordQuotes reports whether one transcript line is a user or
// api-request-blob record whose text contains want.
func transcriptRecordQuotes(chunk string, want string) bool {
	chunk = strings.TrimSpace(chunk)
	if chunk == "" {
		return false
	}
	var record struct {
		Type    string `json:"type"`
		Message *struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(chunk), &record); err != nil {
		return false // the first line of a tail read is usually a partial record
	}
	if record.Type != "user" && record.Type != "api-request-blob" {
		return false
	}
	if record.Message == nil {
		return false
	}
	for _, text := range transcriptMessageTexts(record.Message.Content) {
		if strings.Contains(text, want) {
			return true
		}
	}
	return false
}

// transcriptMessageTexts returns a message content's text whether it is a bare
// string or a block list.
func transcriptMessageTexts(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return []string{text}
	}
	var blocks []transcriptBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	texts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			texts = append(texts, block.Text)
		}
	}
	return texts
}

// transcriptMTime returns a transcript's mtime, or 0 when it cannot be read.
func transcriptMTime(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.ModTime().UnixNano()
}

// Compile-time guard: the reader must satisfy its own interface.
var _ TranscriptReader = (*transcriptReader)(nil)
