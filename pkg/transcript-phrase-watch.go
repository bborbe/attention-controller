// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

const (
	// transcriptPhraseExcerptLimit bounds the text a hit carries, in characters
	// rather than bytes so a multi-byte transcript cannot be cut mid-rune.
	transcriptPhraseExcerptLimit = 400
)

// transcriptPhraseHandoverPattern matches the closer line that makes a later
// refusal meaningful: a `👤 You:` line whose own VALUE starts with an operator
// action verb.
//
// ⚠️ The value must START with the verb. A match anywhere in the turn is not
// enough, because a panel that merely *discusses* `you run:` lines — every
// status panel that quotes one — contains the string.
var transcriptPhraseHandoverPattern = regexp.MustCompile(
	`(?m)^\s*👤 You:[ \t]*(?:you run:|approve:)[ \t]*\S`,
)

// transcriptPhraseBareAffirmatives is the set an operator's answer must be to
// count as the bare affirmative a manager can refuse.
var transcriptPhraseBareAffirmatives = map[string]struct{}{
	"y": {}, "yes": {}, "ok": {}, "okay": {}, "yep": {}, "ja": {},
}

// TranscriptPhraseHit is one confirmed occurrence of a watched phrase.
//
// It is a struct rather than a formatted line because the two consumers want
// different things from it: the job needs the session and timestamp to build a
// card payload, and the hits log wants the one line the Python engine printed.
// String() renders that line, so both read the same value.
type TranscriptPhraseHit struct {
	// File is the transcript the occurrence was read from.
	File string
	// Session is the transcript's session id — its file name without `.jsonl`,
	// which is what the Python engine put in the HIT line.
	Session string
	// Timestamp is the record's own timestamp. It falls back to the injected
	// clock only when a record carries none, so a hit is never stamped with the
	// instant it was scanned while claiming to be the instant it happened.
	Timestamp string
	// Excerpt is the matched assistant text, whitespace-normalised and bounded.
	Excerpt string
}

// String renders the hit in the Python engine's line format —
// `HIT <session> <timestamp> :: <excerpt>` — so the hits log, the card payload
// and the log line stay byte-compatible with the job this replaces.
func (h TranscriptPhraseHit) String() string {
	return "HIT " + h.Session + " " + h.Timestamp + " :: " + h.Excerpt
}

// TranscriptPhraseHits is a collection of TranscriptPhraseHit.
type TranscriptPhraseHits []TranscriptPhraseHit

// TranscriptPhraseTailer scans a root of Claude Code transcripts for phrases,
// resuming from the byte offset each file was left at.
//
// It is an interface so a spec can drive the job without a transcript tree, and
// so the engine can be replaced without touching the job that reports its hits.
type TranscriptPhraseTailer interface {
	// Scan consumes every transcript under the tailer's root that grew since
	// the last call and returns the confirmed occurrences. A quiet scan returns
	// an empty slice and no error.
	Scan(ctx context.Context) (TranscriptPhraseHits, error)
}

// NewTranscriptPhraseTailer builds the tailer.
//
// ⚠️ The root and the state path are parameters rather than package constants
// so a spec can point the engine at a temp directory, and so the layout the
// Python job used — `~/.claude/projects` and a state file beside the hits log —
// is a caller's decision rather than something baked into the engine.
func NewTranscriptPhraseTailer(
	root string,
	statePath string,
	phrases []string,
	now libtime.CurrentDateTimeGetter,
) TranscriptPhraseTailer {
	return &transcriptPhraseTailer{
		root:      root,
		statePath: statePath,
		phrases:   phrases,
		now:       now,
	}
}

type transcriptPhraseTailer struct {
	root      string
	statePath string
	phrases   []string
	now       libtime.CurrentDateTimeGetter
}

// Scan is one pass over every transcript under the root.
//
// ⚠️ A file the state has never seen is ARMED AT ITS CURRENT SIZE, not read
// from zero. Transcripts are append-only, so "new occurrence" means bytes
// appended after arming; reading a pre-existing transcript in full would report
// every occurrence that predates the watch as if it had just happened. The
// Python job drew the same line, and it is what makes a first run quiet.
func (t *transcriptPhraseTailer) Scan(ctx context.Context) (TranscriptPhraseHits, error) {
	state, err := loadTranscriptPhraseWatchState(ctx, t.statePath)
	if err != nil {
		return nil, errors.Wrap(ctx, err, "load transcript watch state failed")
	}
	targets, err := t.targets(ctx)
	if err != nil {
		return nil, err
	}
	for _, path := range targets {
		if _, found := state.Offsets[path]; found {
			continue
		}
		state.Offsets[path] = transcriptSize(path)
	}

	hits := TranscriptPhraseHits{}
	for _, path := range targets {
		fileHits, err := t.scanFile(ctx, path, state)
		if err != nil {
			// Logged and skipped: one unreadable transcript costs that file's
			// window and nothing else, and taking the whole sweep down for it
			// would let one permission problem hide every other file's hits.
			glog.Warningf("scan transcript '%s' failed: %v", path, err)
			continue
		}
		hits = append(hits, fileHits...)
	}

	if err := saveTranscriptPhraseWatchState(ctx, t.statePath, state); err != nil {
		return nil, errors.Wrap(ctx, err, "save transcript watch state failed")
	}
	return hits, nil
}

// targets returns the transcripts to watch: one level of session directory, one
// level of file, sorted so a run's order is reproducible.
//
// ⚠️ `*/*.jsonl` deliberately excludes `<session>/subagents/*.jsonl`, whose
// records echo their parent's reads and would report every occurrence twice.
func (t *transcriptPhraseTailer) targets(ctx context.Context) ([]string, error) {
	pattern := filepath.Join(t.root, "*", "*.jsonl")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "glob transcripts '%s' failed", pattern)
	}
	sort.Strings(paths)
	return paths, nil
}

// scanFile consumes one transcript from its stored offset and returns the
// occurrences confirmed in the appended bytes.
func (t *transcriptPhraseTailer) scanFile(
	ctx context.Context,
	path string,
	state *transcriptPhraseWatchState,
) (TranscriptPhraseHits, error) {
	start := state.Offsets[path]
	if transcriptSize(path) <= start {
		return nil, nil
	}
	raw, effectiveStart, err := readTranscriptFrom(ctx, path, start)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "read transcript '%s' failed", path)
	}
	// A trailing partial line is dropped and picked up complete next cycle:
	// consuming half a JSON record would decode as malformed and lose the line.
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		if cut := bytes.LastIndexByte(raw, '\n'); cut >= 0 {
			raw = raw[:cut+1]
		} else {
			raw = nil
		}
	}
	state.Offsets[path] = effectiveStart + int64(len(raw))
	if !transcriptContainsPhrase(raw, t.phrases) {
		return nil, nil
	}
	return t.confirm(ctx, path, effectiveStart, state)
}

// confirm re-reads the whole transcript and returns the occurrences that carry
// all four discriminators, newest last.
//
// ⚠️ `minOffset` is load-bearing. The scan above only decides whether to look;
// this function then walks the WHOLE file, so without the bound it would also
// re-report every occurrence that predates this run — the false positive the
// Python engine measured on its first armed run, from a session that merely
// quoted the phrase.
//
// The four discriminators, all required:
//
//  1. the phrase sits in an assistant `text` block, never in a `tool_result` —
//     a session that READS a file quoting the phrase would otherwise hit;
//  2. the turn's PRECEDING assistant turn emitted a handover line;
//  3. the operator's turn between them is a bare affirmative;
//  4. that operator turn is the operator's own words, never a tool payload.
func (t *transcriptPhraseTailer) confirm(
	ctx context.Context,
	path string,
	minOffset int64,
	state *transcriptPhraseWatchState,
) (TranscriptPhraseHits, error) {
	session := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	progress := &transcriptConfirmProgress{}
	hits := TranscriptPhraseHits{}
	err := forEachTranscriptLine(ctx, path, func(start int64, raw []byte) {
		record, ok := decodeTranscriptRecord(raw)
		if !ok {
			return
		}
		hits = append(
			hits,
			t.confirmRecord(path, session, start, minOffset, record, progress, state)...,
		)
	})
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "confirm transcript '%s' failed", path)
	}
	return hits, nil
}

// transcriptConfirmProgress is the two-turn window the discriminators need: did
// the preceding assistant turn emit a handover, and was that handover answered
// with a bare affirmative.
type transcriptConfirmProgress struct {
	previousHandover bool
	pending          bool
}

// confirmRecord advances the window by one record and returns the occurrences
// that record confirms.
//
// The operator branch returns without touching previousHandover, exactly as the
// Python engine's `continue` does: the window is "the assistant turn before the
// operator's answer", and a user turn is the middle of it, not the end.
func (t *transcriptPhraseTailer) confirmRecord(
	path string,
	session string,
	start int64,
	minOffset int64,
	record transcriptRecord,
	progress *transcriptConfirmProgress,
	state *transcriptPhraseWatchState,
) TranscriptPhraseHits {
	if said, isOperator := transcriptOperatorText(record); isOperator {
		progress.pending = progress.previousHandover && transcriptIsBareAffirmative(said)
		return nil
	}
	blocks := transcriptAssistantTexts(record)
	if len(blocks) == 0 {
		return nil
	}
	hits := TranscriptPhraseHits{}
	if progress.pending {
		hits = t.matchingHits(
			path,
			session,
			transcriptHitTimestamp(record, t.now),
			start,
			minOffset,
			blocks,
			state,
		)
	}
	progress.previousHandover = transcriptEmitsHandover(blocks)
	return hits
}

// matchingHits returns the hits among one turn's text blocks, skipping the ones
// that predate the resume point or were already reported.
func (t *transcriptPhraseTailer) matchingHits(
	path string,
	session string,
	timestamp string,
	start int64,
	minOffset int64,
	blocks []string,
	state *transcriptPhraseWatchState,
) TranscriptPhraseHits {
	if start < minOffset {
		return nil
	}
	key := [2]string{path, timestamp}
	hits := TranscriptPhraseHits{}
	for _, text := range blocks {
		if !transcriptTextContainsPhrase(text, t.phrases) {
			continue
		}
		// One hit per turn: two matching blocks in one turn are one occurrence,
		// which is the Python engine's behaviour — it keys the emitted set on the
		// turn's timestamp, so the second block is dropped there too.
		if _, emitted := state.Emitted[key]; emitted {
			continue
		}
		state.Emitted[key] = struct{}{}
		hits = append(hits, TranscriptPhraseHit{
			File:      path,
			Session:   session,
			Timestamp: timestamp,
			Excerpt:   transcriptExcerpt(text),
		})
	}
	return hits
}

// forEachTranscriptLine visits every line of a transcript with its byte offset.
func forEachTranscriptLine(
	ctx context.Context,
	path string,
	visit func(start int64, raw []byte),
) error {
	file, err := os.Open(path)
	if err != nil {
		return errors.Wrapf(ctx, err, "open transcript '%s' failed", path)
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64*1024)
	var position int64
	for {
		raw, readErr := reader.ReadBytes('\n')
		if len(raw) > 0 {
			visit(position, raw)
			position += int64(len(raw))
		}
		if readErr == nil {
			continue
		}
		if readErr == io.EOF {
			return nil
		}
		return errors.Wrapf(ctx, readErr, "read transcript '%s' failed", path)
	}
}

// transcriptEmitsHandover reports whether any text block carries a handover
// line.
func transcriptEmitsHandover(blocks []string) bool {
	for _, text := range blocks {
		if transcriptPhraseHandoverPattern.MatchString(text) {
			return true
		}
	}
	return false
}

// transcriptRecord is the part of a transcript line this engine reads.
type transcriptRecord struct {
	Type      string             `json:"type"`
	Timestamp string             `json:"timestamp"`
	Message   *transcriptMessage `json:"message"`
}

type transcriptMessage struct {
	Content json.RawMessage `json:"content"`
}

type transcriptBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// decodeTranscriptRecord parses one transcript line, reporting false for a line
// that is blank or not a JSON object.
func decodeTranscriptRecord(raw []byte) (transcriptRecord, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return transcriptRecord{}, false
	}
	var record transcriptRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return transcriptRecord{}, false
	}
	return record, true
}

// transcriptAssistantTexts returns the text of each assistant `text` block.
// A `tool_use` block and every non-assistant record yield nothing, which is the
// first discriminator: a `tool_result` payload is not the manager speaking.
func transcriptAssistantTexts(record transcriptRecord) []string {
	if record.Type != "assistant" || record.Message == nil {
		return nil
	}
	blocks, ok := transcriptBlocks(record.Message.Content)
	if !ok {
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

// transcriptOperatorText returns the operator's own words in a user turn, and
// reports false for a turn whose content is a tool payload rather than text.
func transcriptOperatorText(record transcriptRecord) (string, bool) {
	if record.Type != "user" || record.Message == nil {
		return "", false
	}
	var text string
	if err := json.Unmarshal(record.Message.Content, &text); err == nil {
		return text, true
	}
	blocks, ok := transcriptBlocks(record.Message.Content)
	if !ok {
		return "", false
	}
	texts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			texts = append(texts, block.Text)
		}
	}
	if len(texts) == 0 {
		return "", false
	}
	return strings.Join(texts, " "), true
}

// transcriptBlocks decodes a message's content list, reporting false when the
// content is a bare string rather than blocks.
func transcriptBlocks(raw json.RawMessage) ([]transcriptBlock, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var blocks []transcriptBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, false
	}
	return blocks, true
}

// transcriptIsBareAffirmative reports whether an operator turn is the bare
// affirmative a manager refuses.
func transcriptIsBareAffirmative(text string) bool {
	if text == "" {
		return false
	}
	normalised := strings.TrimRight(strings.ToLower(strings.TrimSpace(text)), ".!")
	_, found := transcriptPhraseBareAffirmatives[normalised]
	return found
}

// transcriptContainsPhrase reports whether any line of raw carries a phrase.
func transcriptContainsPhrase(raw []byte, phrases []string) bool {
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if transcriptTextContainsPhrase(string(line), phrases) {
			return true
		}
	}
	return false
}

// transcriptTextContainsPhrase reports whether text carries any phrase.
func transcriptTextContainsPhrase(text string, phrases []string) bool {
	for _, phrase := range phrases {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

// transcriptHitTimestamp returns the record's own timestamp, or the injected
// clock's instant when the record carries none.
func transcriptHitTimestamp(
	record transcriptRecord,
	now libtime.CurrentDateTimeGetter,
) string {
	if record.Timestamp != "" {
		return record.Timestamp
	}
	return now.Now().Time().UTC().Format(time.RFC3339)
}

// transcriptExcerpt normalises the matched text to one line and bounds it.
func transcriptExcerpt(text string) string {
	normalised := strings.Join(strings.Fields(text), " ")
	runes := []rune(normalised)
	if len(runes) <= transcriptPhraseExcerptLimit {
		return normalised
	}
	return string(runes[:transcriptPhraseExcerptLimit])
}

// transcriptSize returns a transcript's size, or 0 when it cannot be read —
// which arms it at the start rather than skipping it for good.
func transcriptSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// readTranscriptFrom reads a transcript from offset to EOF and returns the
// effective start it read from.
//
// ⚠️ A file SHORTER than the stored offset was rotated or truncated. Seeking to
// a stale offset would read nothing and leave the offset above the size, so the
// `size <= offset` guard would skip that file forever — a silently frozen
// watch. Restarting at zero costs one re-read and the emitted set keeps the
// replay from re-reporting.
func readTranscriptFrom(
	ctx context.Context,
	path string,
	offset int64,
) ([]byte, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, offset, errors.Wrapf(ctx, err, "open transcript '%s' failed", path)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, offset, errors.Wrapf(ctx, err, "stat transcript '%s' failed", path)
	}
	if info.Size() < offset {
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, errors.Wrapf(ctx, err, "seek transcript '%s' failed", path)
	}
	raw, err := io.ReadAll(file)
	if err != nil {
		return nil, offset, errors.Wrapf(ctx, err, "read transcript '%s' failed", path)
	}
	return raw, offset, nil
}

// transcriptPhraseWatchState is the resume state: the byte offset each
// transcript was left at, and the occurrences already reported.
//
// ⚠️ The emitted set must persist across runs, not merely within one: confirm
// re-reads the whole file whenever a candidate appears, so without it a later
// candidate would re-report every earlier occurrence alongside it.
type transcriptPhraseWatchState struct {
	Offsets map[string]int64
	Emitted map[[2]string]struct{}
}

// transcriptPhraseWatchStateWire is the on-disk shape, byte-compatible with the
// file the Python engine wrote so an existing state file resumes rather than
// re-baselines.
type transcriptPhraseWatchStateWire struct {
	Offsets map[string]int64 `json:"offsets"`
	Emitted [][2]string      `json:"emitted"`
}

// loadTranscriptPhraseWatchState reads the state file, treating a missing or
// unparseable one as empty.
func loadTranscriptPhraseWatchState(
	ctx context.Context,
	path string,
) (*transcriptPhraseWatchState, error) {
	state := &transcriptPhraseWatchState{
		Offsets: map[string]int64{},
		Emitted: map[[2]string]struct{}{},
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return nil, errors.Wrapf(ctx, err, "read state '%s' failed", path)
	}
	var wire transcriptPhraseWatchStateWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		// A torn state file re-baselines rather than stopping the watch: the
		// offsets are a resume aid, and losing them costs the window since the
		// last good save rather than reporting old history.
		glog.Warningf("reset unparseable transcript watch state '%s': %v", path, err)
		return state, nil
	}
	for path, offset := range wire.Offsets {
		state.Offsets[path] = offset
	}
	for _, key := range wire.Emitted {
		state.Emitted[key] = struct{}{}
	}
	return state, nil
}

// saveTranscriptPhraseWatchState writes the state atomically — a torn file would
// re-baseline and lose a window.
func saveTranscriptPhraseWatchState(
	ctx context.Context,
	path string,
	state *transcriptPhraseWatchState,
) error {
	wire := transcriptPhraseWatchStateWire{
		Offsets: make(map[string]int64, len(state.Offsets)),
		Emitted: make([][2]string, 0, len(state.Emitted)),
	}
	for file, offset := range state.Offsets {
		wire.Offsets[file] = offset
	}
	for key := range state.Emitted {
		wire.Emitted = append(wire.Emitted, key)
	}
	sort.Slice(wire.Emitted, func(i, j int) bool {
		if wire.Emitted[i][0] != wire.Emitted[j][0] {
			return wire.Emitted[i][0] < wire.Emitted[j][0]
		}
		return wire.Emitted[i][1] < wire.Emitted[j][1]
	})
	encoded, err := json.MarshalIndent(wire, "", " ")
	if err != nil {
		return errors.Wrapf(ctx, err, "encode state failed")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return errors.Wrapf(ctx, err, "create state dir for '%s' failed", path)
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return errors.Wrapf(ctx, err, "write state '%s' failed", temporary)
	}
	if err := os.Rename(temporary, path); err != nil {
		return errors.Wrapf(ctx, err, "replace state '%s' failed", path)
	}
	return nil
}
