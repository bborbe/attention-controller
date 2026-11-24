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
	"strconv"
	"strings"
	"sync"
	"time"
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

// transcriptNegativeCacheMaxLines bounds the absent-line proofs kept for ONE
// transcript path. The hot path carries one card per pane, so the working set
// is a handful; the cap only stops a long-lived path accumulating every line it
// was ever asked about.
const transcriptNegativeCacheMaxLines = 64

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

	// Paths returns the newest transcript path for every session under the root,
	// keyed by session id, built from ONE pass over the project directories.
	//
	// ⚠️ Callers that need a path for MANY sessions must use this rather than
	// Path per session. Path globs the whole project tree, so calling it once per
	// registry session is O(sessions × files): the who-needs-me feed did exactly
	// that and measured 60 % of the process's CPU in filepath.Glob / os.readdir,
	// stretching a single read to 8–19 s and saturating the process (2026-11-19).
	// This builds the same answer from a single scan. It never fails: an
	// unreadable tree yields an empty map.
	Paths() map[string]string

	// Offset returns the transcript's current byte length and whether it is
	// known. Taken immediately BEFORE an answer is typed, so everything the
	// delivery writes lands after it — which is what makes the submit check
	// cheap on a long-lived session.
	Offset(sessionID string) (int64, bool)

	// HoldsLine reports whether the transcript already carries line as a
	// delivered message at or after since. A nil since falls back to the narrow
	// tail window; a non-nil zero asks for the WIDE tail (the last
	// transcriptReaderScanMaxBytes), which a fresh-entry delivery guard uses to
	// find a copy that landed before a restart. It never fails: an unreadable
	// transcript reports false, which is the conservative direction — the caller
	// then treats the delivery as not yet proven and does not drop the answer.
	HoldsLine(sessionID string, line string, since *int64) bool

	// TurnEnded reports whether the session's last timestamped record is a
	// turn-end. It never fails: a missing or unreadable transcript reports
	// false, keeping the caller's "no live process" refusal.
	TurnEnded(sessionID string) bool

	// ModifiedAt returns when the transcript was last written, and whether it
	// could be read. It never fails: an absent or unreadable transcript reports
	// false.
	//
	// ⚠️ It is the compact trio's only honest "is a turn running?" probe. The
	// registry cannot answer it — it reads `busy` for a running turn AND for a
	// session merely holding a background shell, so a shell-holder could never be
	// compacted. A running turn writes the transcript continuously while a
	// background shell writes nothing at all (measured 2026-10-18: three
	// shell-holding sessions kept their mtime frozen over 60 s while every
	// genuinely mid-turn session advanced).
	ModifiedAt(sessionID string) (time.Time, bool)

	// CompactionBoundaries returns how many `compact_boundary` records the
	// transcript holds, and whether it could be read. It never fails: an
	// unreadable transcript reports false, and the caller then withholds rather
	// than guessing.
	//
	// ⚠️ Every real compaction appends one, so a COUNT is a signal rather than a
	// timer: the composer clears the instant `/compact` submits while the
	// compaction is still running, so an emptiness-only gate would fire
	// `post-compact` into the middle of it and lose exactly what the sequence
	// exists to preserve.
	CompactionBoundaries(sessionID string) (int, bool)

	// LastAssistantText returns the text of the LAST assistant text block in the
	// transcript tail, and whether one was found. ok is false when the tail holds
	// no assistant text at all — which is NOT the same as "the last block carries
	// no closer": a failed read must not be representable as an empty result.
	LastAssistantText(sessionID string) (string, bool)

	// PendingToolCall returns the name of the last tool_use that has no matching
	// tool_result, and whether one is pending. It reports the CALL NAME only; it
	// does not claim the session is parked — the caller supplies that from the
	// transcript's mtime.
	PendingToolCall(sessionID string) (string, bool)
}

// NewTranscriptReader creates the reader the delivery carrier uses.
//
// ⚠️ The root is a parameter rather than a package constant so a spec can point
// the reader at a temp directory, and so the layout the Python watcher used —
// `~/.claude/projects/*/<session>.jsonl` — is a caller's decision rather than
// something baked into the reader.
func NewTranscriptReader(root string) TranscriptReader {
	return &transcriptReader{root: root, readWindow: readTranscriptWindow}
}

type transcriptReader struct {
	root string

	// readWindow is a seam for the specs: they swap in a counting reader to
	// prove an unchanged transcript is scanned once across N calls. Production
	// always uses readTranscriptWindow.
	readWindow func(path string, since *int64) ([]byte, bool)

	mu    sync.Mutex
	cache map[string]*transcriptAbsence
	// boundaries caches one count per transcript path, keyed on (size, mtime).
	// ⚠️ The trio polls its gate every 2 s for up to 600 s while `/compact`
	// runs, and a transcript measured at 675 MB would otherwise be re-read
	// hundreds of times. An append changes both size and mtime, so a stale count
	// can never mask the boundary the gate is waiting for.
	boundaries map[string]transcriptBoundaryCount
}

// transcriptBoundaryCount is one cached count and the file state it describes.
type transcriptBoundaryCount struct {
	size  int64
	mtime time.Time
	count int
}

// transcriptAbsence is the proof, for ONE transcript path, that a set of lines
// was ABSENT from the file as it stood at (size, mtime). Any change to either
// invalidates the whole entry, so a line appended after the scan is never
// masked by the earlier miss.
type transcriptAbsence struct {
	size  int64
	mtime time.Time
	lines map[string]struct{}
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

// Paths returns the newest transcript path for every session under the root,
// keyed by session id, from a SINGLE pass over the project directories.
//
// ⚠️ It exists because Path per session is O(sessions × files): Go's
// filepath.Glob opens and sorts EVERY project directory on each call, so one
// glob per registry session (the who-needs-me host snapshot did this for ~58
// sessions against 131 project dirs) burned 60 % of the process's CPU in
// readdir and stretched a read to 8–19 s (2026-11-19). One glob here answers
// the whole snapshot. A session with a single transcript is taken without a
// stat; mtime is consulted only to break a tie between two files for the same
// session, matching Path's "newest wins".
func (r *transcriptReader) Paths() map[string]string {
	out := make(map[string]string)
	if r.root == "" {
		// An empty root would make the glob relative to the process cwd.
		return out
	}
	found, err := filepath.Glob(filepath.Join(r.root, "*", "*.jsonl"))
	if err != nil {
		return out
	}
	bySession := make(map[string][]string, len(found))
	for _, path := range found {
		sessionID := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		if sessionID == "" {
			continue
		}
		bySession[sessionID] = append(bySession[sessionID], path)
	}
	for sessionID, paths := range bySession {
		if len(paths) == 1 {
			out[sessionID] = paths[0]
			continue
		}
		sort.Slice(paths, func(i, j int) bool {
			return transcriptMTime(paths[i]) > transcriptMTime(paths[j])
		})
		out[sessionID] = paths[0]
	}
	return out
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

// ModifiedAt returns when the transcript was last written.
//
// ⚠️ The stat is a THIRD syscall on the trio's hot path (glob, stat, read), and
// it is deliberate rather than folded into Offset: the two answer different
// questions. Offset bounds a submit check's scan; the mtime answers "is a turn
// running", which no offset can — an unchanged offset over one tick is one
// sample, while the mtime carries the quiet period that already elapsed.
func (r *transcriptReader) ModifiedAt(sessionID string) (time.Time, bool) {
	path := r.Path(sessionID)
	if path == "" {
		return time.Time{}, false
	}
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, false
	}
	return info.ModTime(), true
}

// compactionBoundaryMarker is the substring every compaction record carries.
//
// ⚠️ It is only a PRE-FILTER. The record is a `system` entry with
// `subtype: compact_boundary`; the string also appears inside ordinary user and
// tool-result records that merely mention it (measured on this machine: a
// `toolUseResult` quoting a compaction, and a `user` record carrying
// `compactMetadata` in nested text). Counting raw occurrences therefore reports
// a boundary that never happened — and a false boundary is the worst kind here,
// because it is what licenses `post-compact` to fire into a running compaction.
const compactionBoundaryMarker = "compact_boundary"

// compactionBoundarySubtype is the record's own declaration that it IS a
// boundary, matched at the TOP LEVEL of the line's JSON.
const compactionBoundarySubtype = "compact_boundary"

// compactionBoundaryCacheMax bounds the per-path count cache. One entry per
// session ever polled is tiny, but the map must not grow without limit in a
// process that lives for weeks.
const compactionBoundaryCacheMax = 64

// CompactionBoundaries counts the transcript's compaction records.
//
// ⚠️ The whole file is read, not the tail. A compaction boundary older than the
// tail window is still part of the COUNT, and a count that silently restarted
// from a partial file would read "a new boundary appeared" on the first poll
// after any window slide. The file is read once per gate poll, and the gate
// stops polling the moment the count moves.
//
// ⚠️ The count is CACHED against (size, mtime). The trio asks this every 2 s for
// up to ten minutes; on a 675 MB transcript an uncached read would be a
// multi-hundred-megabyte scan per poll. An append moves both fields, so the
// cache can never hide the boundary being waited for.
func (r *transcriptReader) CompactionBoundaries(sessionID string) (int, bool) {
	path := r.Path(sessionID)
	if path == "" {
		return 0, false
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	if cached, ok := r.cachedBoundaries(path, info); ok {
		return cached, true
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	count := countCompactionBoundaries(content)
	r.rememberBoundaries(path, info, count)
	return count, true
}

// countCompactionBoundaries counts the transcript's real compaction records.
//
// ⚠️ A line counts only when its TOP-LEVEL JSON declares
// `subtype: compact_boundary`. The marker string also occurs inside tool
// results and message text that merely quote a compaction, and those are nested
// rather than top-level — counting them would fabricate the very boundary the
// gate waits for.
func countCompactionBoundaries(content []byte) int {
	count := 0
	for _, line := range bytes.Split(content, []byte("\n")) {
		if !bytes.Contains(line, []byte(compactionBoundaryMarker)) {
			continue
		}
		var record struct {
			Subtype string `json:"subtype"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			continue
		}
		if record.Subtype == compactionBoundarySubtype {
			count++
		}
	}
	return count
}

// cachedBoundaries returns the cached count for exactly this file state.
func (r *transcriptReader) cachedBoundaries(path string, info os.FileInfo) (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cached, ok := r.boundaries[path]
	if !ok || cached.size != info.Size() || !cached.mtime.Equal(info.ModTime()) {
		return 0, false
	}
	return cached.count, true
}

// rememberBoundaries stores one count against the file state it was taken at.
func (r *transcriptReader) rememberBoundaries(path string, info os.FileInfo, count int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.boundaries == nil {
		r.boundaries = map[string]transcriptBoundaryCount{}
	}
	if len(r.boundaries) >= compactionBoundaryCacheMax {
		// Forget one arbitrary entry rather than grow without bound. A dropped
		// entry costs one re-read on its next poll; it can never hide a boundary,
		// because the count is always recomputed from the file's current state.
		for stale := range r.boundaries {
			delete(r.boundaries, stale)
			break
		}
	}
	r.boundaries[path] = transcriptBoundaryCount{
		size:  info.Size(),
		mtime: info.ModTime(),
		count: count,
	}
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
//
// ⚠️ A MISS is cached; a hit is not. The delivery carrier asks this on every
// ~2.6 s tick while a composer is busy, and an uncached miss re-read the
// transcript tail each time — measured 2026-11-16 at v0.92.0: 1113 retries in
// 17 min, 17.8 GB of 34.5 GB allocated from this one path. The proof is keyed
// on the file's (size, mtime), so an append invalidates it and a line that
// arrived after the scan is never masked by the earlier miss.
//
// ⚠️ The scan window is chosen by `since`: nil is the narrow tail (256 KiB),
// a non-nil zero is the WIDE tail (the last transcriptReaderScanMaxBytes), and
// a positive value scans from that byte mark. The wide tail exists for the
// fresh-entry delivery guard, which has no marker after a restart and must still
// find a copy the previous process typed.
func (r *transcriptReader) HoldsLine(sessionID string, line string, since *int64) bool {
	path := r.Path(sessionID)
	if path == "" {
		// No transcript under this session: any proof held for a file that was
		// pruned or rotated away is now about a different file.
		r.forgetMissing()
		return false
	}
	want := strings.TrimSpace(line)
	if want == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		// A transcript that vanished between the glob and the stat proves
		// nothing, and any cached proof about it is now about another file.
		r.forgetAbsence(path)
		return false
	}
	key := transcriptAbsenceKey(want, since)
	if r.cachedAbsence(path, info, key) {
		return false
	}
	readWindow := r.readWindow
	if readWindow == nil {
		// Only NewTranscriptReader sets this; the default keeps a directly
		// constructed reader from panicking on the hot path.
		readWindow = readTranscriptWindow
	}
	raw, ok := readWindow(path, since)
	if !ok {
		r.forgetAbsence(path)
		return false
	}
	// The scan below walks an already-read in-memory buffer: no I/O, so there is
	// nothing to cancel mid-loop and the interface carries no ctx.
	for _, chunk := range bytes.Split(raw, []byte("\n")) {
		if transcriptRecordQuotes(string(chunk), want) {
			return true
		}
	}
	r.rememberAbsence(path, info, key)
	return false
}

// transcriptAbsenceKey identifies one (line, since) question. Three questions
// are distinct: no offset (the narrow tail), a zero offset (the WIDE tail — see
// readTranscriptWindow), and a positive offset (from that byte mark). They must
// not share a key, or a wide-tail miss would mask a narrow-tail hit.
func transcriptAbsenceKey(line string, since *int64) string {
	if since == nil {
		return line + "\x00tail"
	}
	return line + "\x00" + strconv.FormatInt(*since, 10)
}

// cachedAbsence reports whether (path, key) was already proven absent against
// exactly this file state. A size or mtime change drops the entry first, so the
// answer can only ever be about the file as it stands now.
func (r *transcriptReader) cachedAbsence(path string, info os.FileInfo, key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.cache[path]
	if !ok {
		return false
	}
	if entry.size != info.Size() || !entry.mtime.Equal(info.ModTime()) {
		delete(r.cache, path)
		return false
	}
	_, hit := entry.lines[key]
	return hit
}

// rememberAbsence records one absent line against the file state it was proven
// against, replacing the entry whenever that state moved.
func (r *transcriptReader) rememberAbsence(path string, info os.FileInfo, key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cache == nil {
		r.cache = make(map[string]*transcriptAbsence, 1)
	}
	entry, ok := r.cache[path]
	if !ok || entry.size != info.Size() || !entry.mtime.Equal(info.ModTime()) {
		entry = &transcriptAbsence{
			size:  info.Size(),
			mtime: info.ModTime(),
			lines: make(map[string]struct{}, 1),
		}
		r.cache[path] = entry
	}
	if len(entry.lines) >= transcriptNegativeCacheMaxLines {
		// The working set is one card per pane; a path that outgrew the cap is
		// asked about lines that can no longer be in flight. Start over rather
		// than grow without bound.
		entry.lines = make(map[string]struct{}, 1)
	}
	entry.lines[key] = struct{}{}
}

// forgetAbsence drops every proof held for one path.
func (r *transcriptReader) forgetAbsence(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.cache, path)
}

// forgetMissing drops proofs whose transcript no longer exists. Path() returns
// "" when the glob finds nothing, so the vanished path is not known at the call
// site; sweeping the few in-flight entries is cheaper than tracking it.
func (r *transcriptReader) forgetMissing() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for path := range r.cache {
		if _, err := os.Stat(path); err != nil {
			delete(r.cache, path)
		}
	}
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

// LastAssistantText returns the text of the last assistant text block in the
// transcript tail.
//
// ⚠️ Fail-closed in every unsure direction: an absent transcript, an unreadable
// tail or a tail with no assistant text all report ("", false). A read failure
// must never be representable as an empty closer, because the caller reads the
// empty string as "this session said nothing".
func (r *transcriptReader) LastAssistantText(sessionID string) (string, bool) {
	path := r.Path(sessionID)
	if path == "" {
		return "", false
	}
	tail, ok := readTranscriptWindow(path, nil)
	if !ok {
		return "", false
	}
	return lastAssistantTextFromTail(tail)
}

// lastAssistantTextFromTail walks the tail in ORDER and keeps the last non-empty
// text block of any assistant record.
//
// ⚠️ The last assistant record is frequently NOT the last record in the file — a
// `user` tool-result record follows it. Stopping at the last line, or returning
// the first assistant text, both report the wrong turn.
func lastAssistantTextFromTail(tail []byte) (string, bool) {
	text := ""
	found := false
	for _, line := range bytes.Split(tail, []byte("\n")) {
		record, ok := decodeTranscriptRecord(line)
		if !ok || record.Type != "assistant" || record.Message == nil {
			continue
		}
		for _, block := range transcriptContentBlocks(record.Message.Content) {
			if block.Type == "text" && block.Text != "" {
				text = block.Text
				found = true
			}
		}
	}
	return text, found
}

// PendingToolCall returns the name of the last tool_use with no matching
// tool_result.
//
// ⚠️ It reports the CALL NAME only and claims nothing about the session being
// parked: the park decision needs the transcript's mtime, which the caller reads
// through ModifiedAt rather than this method stat-ing the file a second time.
func (r *transcriptReader) PendingToolCall(sessionID string) (string, bool) {
	path := r.Path(sessionID)
	if path == "" {
		return "", false
	}
	tail, ok := readTranscriptWindow(path, nil)
	if !ok {
		return "", false
	}
	return pendingToolCallFromTail(tail)
}

// pendingToolCallFromTail walks the tail in ORDER holding ONE pending slot.
//
// ⚠️ A tool_result clears the slot only when it answers the call the slot holds.
// A result for an OLDER call must not clear a newer pending one — measured shape:
// `tool_use tu-1`, `tool_use tu-2`, `tool_result tu-1`, where the session is
// still parked on tu-2. One slot, last-writer-wins; a set would report a call
// that is already answered.
func pendingToolCallFromTail(tail []byte) (string, bool) {
	pending := false
	pendingID := ""
	pendingName := ""
	for _, line := range bytes.Split(tail, []byte("\n")) {
		record, ok := decodeTranscriptRecord(line)
		if !ok || record.Message == nil {
			continue
		}
		for _, block := range transcriptContentBlocks(record.Message.Content) {
			switch block.Type {
			case "tool_use":
				pendingID = block.ID
				pendingName = block.Name
				pending = true
			case "tool_result":
				if pending && block.ToolUseID == pendingID {
					pending = false
					pendingID = ""
					pendingName = ""
				}
			}
		}
	}
	return pendingName, pending
}

// transcriptContentBlock is one element of a record's message.content. It is a
// superset of transcriptBlock: the tool fields are read by PendingToolCall and
// by the stuck detection, and text by LastAssistantText.
type transcriptContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

// transcriptContentBlocks decodes a message content block list. It returns nil
// for a bare string content or a malformed body, which is the fail-closed
// direction every reader on this file takes.
func transcriptContentBlocks(raw json.RawMessage) []transcriptContentBlock {
	if len(raw) == 0 {
		return nil
	}
	var blocks []transcriptContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	return blocks
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
	switch {
	case since != nil && *since > 0:
		start = *since - transcriptReaderBackupBytes
	case since != nil:
		// ⚠️ A non-nil zero offset asks for the WIDE tail, not the narrow one. The
		// delivery carrier's fresh-entry guard uses it: after a controller restart
		// the in-process pending marker is gone, and a copy the previous process
		// typed may sit further back than the narrow tail on a busy transcript.
		// Without this a restart re-typed a delivery that had already landed
		// (measured 2026-11-24: one answer arrived at 07:14:52 and again at
		// 07:19:55, ~1.75 MB apart, the second on a fresh process).
		start = size - transcriptReaderScanMaxBytes
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
	// ⚠️ Size the buffer to what will be read, never to the cap. A fixed
	// transcriptReaderScanMaxBytes allocation made every call cost 16 MiB
	// whatever the window held — measured 2026-11-16 at v0.92.0: 1113 calls,
	// 17.8 GB. The cap bounds the read; it is not the working size.
	length := size - start
	if length > transcriptReaderScanMaxBytes {
		length = transcriptReaderScanMaxBytes
	}
	buf := make([]byte, length)
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
