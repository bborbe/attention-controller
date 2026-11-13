// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
)

// UnforwardedClosersJobName is the job's identity in every log line it writes.
const UnforwardedClosersJobName = "attention-unforwarded-closers"

// UnforwardedClosersInterval is how often the check runs. It matches the 900 s
// StartInterval of the launchd job this replaces, so the port changes where the
// tick runs, not how often it runs.
const UnforwardedClosersInterval = 900 * time.Second

const (
	// defaultUnforwardedWindow is the launchd job's `--hours 24`.
	defaultUnforwardedWindow = 24 * time.Hour
	// defaultUnforwardedGrace is the launchd job's `--grace 120`: a question row
	// younger than this has not had time to be forwarded yet.
	defaultUnforwardedGrace = 120 * time.Second
)

// The waiting-verb contract, written INDEPENDENTLY of the Stop hook's own
// predicate in `~/.claude/hooks/attention-log.py`.
//
// ⚠️ That independence is the whole point of the check: if this matcher were
// derived from the hook's predicate, a regression in the hook would blind the
// hook and this diagnostic together, and a lost gate would read as a clean
// scan. Every rule here is therefore stated from the closer contract, not
// imported from the hook.
var (
	// waitVerbPattern is what makes a closer wait on the operator: a pick menu,
	// an approve, a review, or a command the operator runs.
	waitVerbPattern = regexp.MustCompile(`(?i)^(pick\b|approve\s*:|review\s*:|you\s+run\s*:)`)
	// parkedPattern is a closer that names the event it waits for. It is not a
	// question the operator can answer now, so it is not a loss.
	parkedPattern = regexp.MustCompile(`(?i)^later\s*\(on\b`)
	// noReplyPattern is a closer that explicitly asks for no reply. The trailing
	// clause may be preceded by an em/en dash and may carry one trailing word.
	noReplyPattern = regexp.MustCompile(
		`(?i)(^|[—–]\s*)(no reply( needed)?|nothing needed|no action needed)(\s+\w{1,12})?[.!]?\s*$`,
	)
	// stateGlyphPattern is a closer panel's state line. A fenced block carrying
	// one is a panel, not a quote of another session's closer.
	stateGlyphPattern = regexp.MustCompile(`^[*_\s]*(🟢|🟡|🔴|🔵|⚪|⏰)`)
	// closerPattern is the `👤 You:` line, tolerant of emphasis and of the colon
	// written detached (`👤 You : pick — …`) or the name padded (`👤  You:`).
	closerPattern = regexp.MustCompile(`^[*_\s]*👤\s*You\s*[*_]*\s*:\s*[*_]*\s*:?(.*)$`)
	// closerDecoration is the emphasis a session wraps a closer line in.
	closerDecoration = regexp.MustCompile("[`*_]")
	// postedCardPattern is a closer that names an attention card it already
	// posted. That card IS the delivered item, so counting the closer would
	// report a loss that is not one.
	postedCardPattern = regexp.MustCompile(`(?i)\bcard\s+[0-9a-f]{32}\b`)
	// forwardLinePattern is the watcher's own forward line for a question or
	// permission item: `open <kind> <item8> -> <store8>`.
	forwardLinePattern = regexp.MustCompile(` open\s+\S+\s+([0-9a-f]{8}) ->`)
)

// UnforwardedOutcome is the three-valued result of one scan.
//
// ⚠️ It is the in-process replacement for the launchd job's exit code: 0 none
// found, 1 found, 2 read error. A read error is a DISTINCT outcome and never a
// zero count, so a crash can never read as "nothing lost".
type UnforwardedOutcome string

const (
	// UnforwardedOutcomeNone means the scan completed and found nothing.
	UnforwardedOutcomeNone UnforwardedOutcome = "none"
	// UnforwardedOutcomeFound means the scan completed and found at least one.
	UnforwardedOutcomeFound UnforwardedOutcome = "found"
	// UnforwardedOutcomeReadError means the scan could not read its inputs.
	UnforwardedOutcomeReadError UnforwardedOutcome = "read-error"
)

// UnforwardedFinding is one attention item that should have reached the store
// but did not.
type UnforwardedFinding struct {
	// Item is the item id's first eight characters.
	Item string
	// Session is the session that recorded the row, when it carries one.
	Session string
	// Kind is the recorded row kind: `idle`, `question` or `permission`.
	Kind string
	// Why says which detector found it.
	Why string
	// Ask is the waiting closer's text, for an idle row.
	Ask string
}

// UnforwardedScan is one pass over the attention state directory.
type UnforwardedScan struct {
	// Scanned is how many `open` rows fell inside the window.
	Scanned int
	// Findings is what the two detectors counted.
	Findings []UnforwardedFinding
	// Err is the read error, when the scan could not complete.
	Err error
}

// Outcome maps the scan onto the three-outcome contract.
func (s UnforwardedScan) Outcome() UnforwardedOutcome {
	if s.Err != nil {
		return UnforwardedOutcomeReadError
	}
	if len(s.Findings) > 0 {
		return UnforwardedOutcomeFound
	}
	return UnforwardedOutcomeNone
}

// UnforwardedClosersConfig is the scanner's inputs.
type UnforwardedClosersConfig struct {
	// StateDir holds the producers' `*.events.jsonl` files, `suppressed.jsonl`
	// and the per-session `<sid>.posted.json` files.
	StateDir string
	// WatcherLog is the watcher's log, whose forward lines say which question
	// and permission rows reached the store.
	WatcherLog string
	// TranscriptRoot bounds which transcripts may be read: a path outside it is
	// ignored, so a poisoned state file cannot aim this reader at any local file.
	TranscriptRoot string
	// Window is how far back the scan looks. Zero means 24 h.
	Window time.Duration
	// Grace is how long a question row must exist before it is due. Zero means
	// 120 s.
	Grace time.Duration
	// Now is the clock. Nil means the real clock.
	Now libtime.CurrentDateTimeGetter
}

// UnforwardedClosersScanner counts attention items that should have reached the
// store but did not.
type UnforwardedClosersScanner struct {
	stateDir       string
	watcherLog     string
	transcriptRoot string
	window         time.Duration
	grace          time.Duration
	now            libtime.CurrentDateTimeGetter
}

// NewUnforwardedClosersScanner builds a scanner over the given inputs.
func NewUnforwardedClosersScanner(config UnforwardedClosersConfig) *UnforwardedClosersScanner {
	window := config.Window
	if window <= 0 {
		window = defaultUnforwardedWindow
	}
	grace := config.Grace
	if grace <= 0 {
		grace = defaultUnforwardedGrace
	}
	now := config.Now
	if now == nil {
		now = libtime.NewCurrentDateTime()
	}
	return &UnforwardedClosersScanner{
		stateDir:       config.StateDir,
		watcherLog:     config.WatcherLog,
		transcriptRoot: config.TranscriptRoot,
		window:         window,
		grace:          grace,
		now:            now,
	}
}

// Scan performs one pass and returns what it counted.
func (s *UnforwardedClosersScanner) Scan(ctx context.Context) UnforwardedScan {
	if err := s.checkStateDir(ctx); err != nil {
		return UnforwardedScan{Err: err}
	}
	forwarded, err := s.readForwarded(ctx)
	if err != nil {
		return UnforwardedScan{Err: err}
	}
	suppressed, err := s.readSuppressed(ctx)
	if err != nil {
		return UnforwardedScan{Err: err}
	}
	now := unixSeconds(s.now.Now())
	window := unforwardedWindow{
		since:       now - s.window.Seconds(),
		until:       now,
		now:         now,
		forwarded:   forwarded,
		suppressed:  suppressed,
		transcripts: make(map[string][]transcriptMessage),
	}
	findings, scanned, err := s.scanEventFiles(ctx, window)
	if err != nil {
		return UnforwardedScan{Err: err}
	}
	return UnforwardedScan{Scanned: scanned, Findings: findings}
}

// checkStateDir refuses a missing or non-directory state dir. It is the read
// that turns a bad `--state-dir` into a loud error instead of an empty scan.
func (s *UnforwardedClosersScanner) checkStateDir(ctx context.Context) error {
	info, err := os.Stat(s.stateDir)
	if err != nil {
		return errors.Wrapf(ctx, err, "state dir %s", s.stateDir)
	}
	if !info.IsDir() {
		return errors.Errorf(ctx, "state dir %s is not a directory", s.stateDir)
	}
	return nil
}

// unforwardedWindow carries one scan's bounds and read inputs.
type unforwardedWindow struct {
	since       float64
	until       float64
	now         float64
	forwarded   map[string]struct{}
	suppressed  map[string][]float64
	transcripts map[string][]transcriptMessage
}

// transcriptMessage is one assistant text message from a transcript.
type transcriptMessage struct {
	At   float64
	Text string
}

// readForwarded collects the item ids the watcher logged a forward line for.
func (s *UnforwardedClosersScanner) readForwarded(
	ctx context.Context,
) (map[string]struct{}, error) {
	fh, err := os.Open(s.watcherLog)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "read watcher log %s", s.watcherLog)
	}
	defer fh.Close()
	forwarded := make(map[string]struct{})
	scanner := newLineScanner(fh)
	for scanner.Scan() {
		for _, match := range forwardLinePattern.FindAllStringSubmatch(scanner.Text(), -1) {
			if len(match) > 1 {
				forwarded[match[1]] = struct{}{}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, errors.Wrapf(ctx, err, "read watcher log %s", s.watcherLog)
	}
	return forwarded, nil
}

// readSuppressed collects the hook's own suppression records, keyed by session.
//
// A malformed line is skipped: the record is best-effort evidence, and a single
// unparseable line must not turn the whole scan into a read error.
func (s *UnforwardedClosersScanner) readSuppressed(
	ctx context.Context,
) (map[string][]float64, error) {
	path := filepath.Join(s.stateDir, "suppressed.jsonl")
	fh, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string][]float64{}, nil
		}
		return nil, errors.Wrapf(ctx, err, "read suppressed %s", path)
	}
	defer fh.Close()
	suppressed := make(map[string][]float64)
	scanner := newLineScanner(fh)
	for scanner.Scan() {
		var record map[string]any
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			continue
		}
		session, ok := record["session_id"].(string)
		if !ok {
			continue
		}
		at, ok := asFloat(record["ts"])
		if !ok {
			continue
		}
		suppressed[session] = append(suppressed[session], at)
	}
	if err := scanner.Err(); err != nil {
		return nil, errors.Wrapf(ctx, err, "read suppressed %s", path)
	}
	return suppressed, nil
}

// scanEventFiles walks every producer's event log.
func (s *UnforwardedClosersScanner) scanEventFiles(
	ctx context.Context,
	window unforwardedWindow,
) ([]UnforwardedFinding, int, error) {
	paths, err := filepath.Glob(filepath.Join(s.stateDir, "*.events.jsonl"))
	if err != nil {
		return nil, 0, errors.Wrapf(ctx, err, "glob attention state %s", s.stateDir)
	}
	findings := make([]UnforwardedFinding, 0, 4)
	scanned := 0
	for _, path := range paths {
		fileFindings, fileScanned, err := s.scanEventFile(ctx, path, window)
		if err != nil {
			return nil, 0, err
		}
		findings = append(findings, fileFindings...)
		scanned += fileScanned
	}
	return findings, scanned, nil
}

// scanEventFile reads one producer's event log.
func (s *UnforwardedClosersScanner) scanEventFile(
	ctx context.Context,
	path string,
	window unforwardedWindow,
) ([]UnforwardedFinding, int, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, 0, errors.Wrapf(ctx, err, "read attention events %s", path)
	}
	defer fh.Close()
	findings := make([]UnforwardedFinding, 0, 4)
	scanned := 0
	scanner := newLineScanner(fh)
	for scanner.Scan() {
		finding, counted := s.scanEvent(scanner.Bytes(), window)
		if !counted {
			continue
		}
		scanned++
		if finding != nil {
			findings = append(findings, *finding)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, errors.Wrapf(ctx, err, "read attention events %s", path)
	}
	return findings, scanned, nil
}

// scanEvent classifies one event record. It returns the finding (nil when the
// row is not a loss) and whether the row counted toward `scanned`.
func (s *UnforwardedClosersScanner) scanEvent(
	line []byte,
	window unforwardedWindow,
) (*UnforwardedFinding, bool) {
	var record map[string]any
	if json.Unmarshal(line, &record) != nil {
		return nil, false
	}
	if asString(record["type"]) != "open" {
		return nil, false
	}
	at, ok := asFloat(record["ts"])
	if !ok || at < window.since || at > window.until {
		return nil, false
	}
	item := firstEight(asString(record["item_id"]))
	kind := asString(record["kind"])
	switch kind {
	case "question", "permission":
		return s.questionFinding(at, item, kind, record, window), true
	case "idle":
		if asString(record["event"]) != "Stop" {
			return nil, true
		}
		return s.idleFinding(at, item, record, window), true
	default:
		return nil, true
	}
}

// questionFinding reports a question or permission row with no forward line,
// once it is older than the grace window.
func (s *UnforwardedClosersScanner) questionFinding(
	at float64,
	item string,
	kind string,
	record map[string]any,
	window unforwardedWindow,
) *UnforwardedFinding {
	if window.now-at < s.grace.Seconds() {
		return nil
	}
	if _, ok := window.forwarded[item]; ok {
		return nil
	}
	return &UnforwardedFinding{
		Item:    item,
		Session: asString(record["session_id"]),
		Kind:    kind,
		Why:     "no forward line",
	}
}

// idleFinding reports an idle row whose turn's final assistant message carries
// a waiting closer, after subtracting the four hook-contract exemptions.
func (s *UnforwardedClosersScanner) idleFinding(
	at float64,
	item string,
	record map[string]any,
	window unforwardedWindow,
) *UnforwardedFinding {
	session := asString(record["session_id"])
	if suppressedWithin(window.suppressed[session], at) {
		return nil
	}
	transcript := s.safeTranscript(asString(record["transcript"]))
	message := finalAssistantMessage(transcript, at, window.transcripts)
	ask := waitingAsk(message)
	if ask == "" {
		return nil
	}
	if normalizeCloser(ask) == s.postedCloser(session) {
		return nil
	}
	return &UnforwardedFinding{
		Item:    item,
		Session: session,
		Kind:    "idle",
		Why:     "waiting closer recorded idle",
		Ask:     truncate(ask, 100),
	}
}

// waitingAsk returns the ask a message's final closer would own if it waits.
//
// Decoration-tolerant, and deliberately written from the closer contract rather
// than from the hook's predicate. It mirrors the hook's three ownership rules,
// because reporting a loss the hook never owed is as wrong as missing one:
//   - an UNFENCED `👤 You:` line wins; fenced closers are considered only when
//     there is no unfenced one;
//   - a fenced block counts only when it is a one-line closer, or a closer panel
//     (a state glyph beside the closer);
//   - blank lines are not content, and an unterminated fence is dropped.
func waitingAsk(message string) string {
	if message == "" {
		return ""
	}
	unfenced := make([]string, 0, 2)
	fenced := make([]string, 0, 2)
	block := make([]string, 0, 4)
	inFence := false
	for _, line := range strings.Split(message, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inFence {
				if closer, ok := blockCloser(block); ok {
					fenced = append(fenced, closer)
				}
			}
			inFence = !inFence
			block = block[:0]
			continue
		}
		if inFence {
			block = append(block, trimmed)
			continue
		}
		if match := closerPattern.FindStringSubmatch(trimmed); match != nil {
			unfenced = append(unfenced, match[1])
		}
	}
	asks := unfenced
	if len(asks) == 0 {
		asks = fenced
	}
	if len(asks) == 0 {
		return ""
	}
	ask := strings.Trim(closerDecoration.ReplaceAllString(asks[len(asks)-1], ""), " :")
	if !waitVerbPattern.MatchString(ask) ||
		parkedPattern.MatchString(ask) ||
		noReplyPattern.MatchString(ask) ||
		postedCardPattern.MatchString(ask) {
		return ""
	}
	return ask
}

// blockCloser returns the last closer in a fenced block when the block is a
// one-line closer or a closer panel, and false otherwise.
func blockCloser(block []string) (string, bool) {
	lines := make([]string, 0, len(block))
	for _, line := range block {
		if line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return "", false
	}
	panel := false
	closers := make([]string, 0, 1)
	for _, line := range lines {
		if stateGlyphPattern.MatchString(line) {
			panel = true
		}
		if match := closerPattern.FindStringSubmatch(line); match != nil {
			closers = append(closers, match[1])
		}
	}
	if len(closers) == 0 || (len(lines) != 1 && !panel) {
		return "", false
	}
	return closers[len(closers)-1], true
}

// postedCloser returns the closer this session already posted a card for.
//
// The hook's `echoes_posted_card` exemption, mirrored: `<sid>.posted.json` is
// written when a closer is deliberately posted, and counting it here would
// report a loss that is not one. An unreadable or malformed file returns "" —
// i.e. the closer is counted, the safe direction for a check whose job is to
// make losses visible.
func (s *UnforwardedClosersScanner) postedCloser(session string) string {
	if !safeSessionID(session) {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(s.stateDir, session+".posted.json"))
	if err != nil {
		return ""
	}
	var record map[string]any
	if json.Unmarshal(data, &record) != nil {
		return ""
	}
	closer, ok := record["closer"].(string)
	if !ok {
		return ""
	}
	return normalizeCloser(closer)
}

// safeSessionID refuses a session id that would aim a read outside the state
// dir. The id is read verbatim from an events record.
func safeSessionID(session string) bool {
	return session != "" &&
		!strings.HasPrefix(session, ".") &&
		!strings.ContainsAny(session, "/\\")
}

// normalizeCloser is the comparison form: punctuation-insensitive and
// whitespace-collapsed. Applied to BOTH sides of the posted-closer comparison,
// because the ask has its decoration stripped while `<sid>.posted.json` stores
// the closer verbatim.
func normalizeCloser(value string) string {
	cleaned := closerDecoration.ReplaceAllString(value, "")
	return strings.Join(strings.Fields(cleaned), " ")
}

// safeTranscript returns `path` when it resolves under the transcript root, and
// "" otherwise. The path is read verbatim from an events record, and a
// corrupted or poisoned state file would otherwise aim this reader at any local
// file.
func (s *UnforwardedClosersScanner) safeTranscript(path string) string {
	if path == "" {
		return ""
	}
	root := s.transcriptRoot
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	if !strings.HasPrefix(resolved, root+string(os.PathSeparator)) {
		return ""
	}
	return resolved
}

// finalAssistantMessage returns the last assistant text at or before `at`
// (+1 s slack), or "" when the transcript cannot be read.
func finalAssistantMessage(
	path string,
	at float64,
	cache map[string][]transcriptMessage,
) string {
	if path == "" {
		return ""
	}
	messages, ok := cache[path]
	if !ok {
		messages = readAssistantMessages(path)
		cache[path] = messages
	}
	index := sort.Search(len(messages), func(i int) bool {
		return messages[i].At > at+1
	})
	if index == 0 {
		return ""
	}
	return messages[index-1].Text
}

// readAssistantMessages parses a transcript's assistant text messages, sorted by
// timestamp. A transcript is JSONL and grows after the fact, so it is read once
// per scan rather than cached across cycles.
func readAssistantMessages(path string) []transcriptMessage {
	fh, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer fh.Close()
	messages := make([]transcriptMessage, 0, 8)
	scanner := newLineScanner(fh)
	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.Contains(line, []byte(`"assistant"`)) || !bytes.Contains(line, []byte(`"text"`)) {
			continue
		}
		var record map[string]any
		if json.Unmarshal(line, &record) != nil {
			continue
		}
		if asString(record["type"]) != "assistant" {
			continue
		}
		at, ok := transcriptEpoch(record["timestamp"])
		if !ok {
			continue
		}
		text := messageText(record["message"])
		if strings.TrimSpace(text) == "" {
			continue
		}
		messages = append(messages, transcriptMessage{At: at, Text: text})
	}
	sort.SliceStable(messages, func(i int, j int) bool {
		return messages[i].At < messages[j].At
	})
	return messages
}

// messageText joins a transcript record's assistant text content blocks.
func messageText(message any) string {
	record, ok := message.(map[string]any)
	if !ok {
		return ""
	}
	content, ok := record["content"].([]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(content))
	for _, entry := range content {
		block, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if asString(block["type"]) != "text" {
			continue
		}
		if text, ok := block["text"].(string); ok {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

// transcriptEpoch parses a transcript timestamp into epoch seconds.
func transcriptEpoch(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case string:
		return parseISOEpoch(typed)
	default:
		return 0, false
	}
}

// parseISOEpoch parses an ISO-8601 timestamp into epoch seconds. A timestamp
// with no zone is read in local time, matching the launchd job's behaviour.
func parseISOEpoch(value string) (float64, bool) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return unixSecondsFromTime(parsed), true
		}
	}
	if parsed, err := time.ParseInLocation("2006-01-02T15:04:05.999999999", value, time.Local); err == nil {
		return unixSecondsFromTime(parsed), true
	}
	return 0, false
}

// unixSeconds converts a libtime reading to epoch seconds.
func unixSeconds(value libtime.DateTime) float64 {
	return unixSecondsFromTime(value.Time())
}

// unixSecondsFromTime converts a time instant to epoch seconds.
func unixSecondsFromTime(value time.Time) float64 {
	return float64(value.UnixNano()) / float64(time.Second)
}

// suppressedWithin reports whether a suppression record falls in the one second
// before the open.
func suppressedWithin(timestamps []float64, at float64) bool {
	for _, candidate := range timestamps {
		if at-1 <= candidate && candidate <= at {
			return true
		}
	}
	return false
}

// newLineScanner builds a scanner whose buffer holds the long lines a
// transcript can carry.
func newLineScanner(fh *os.File) *bufio.Scanner {
	scanner := bufio.NewScanner(fh)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	return scanner
}

// asString renders a decoded JSON value as a string. A non-string value renders
// the way Python's `str()` did, so a malformed record cannot crash the scan.
func asString(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

// asFloat decodes a JSON number or numeric string.
func asFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// firstEight returns the first eight characters of an item id.
func firstEight(value string) string {
	runes := []rune(value)
	if len(runes) > 8 {
		runes = runes[:8]
	}
	return string(runes)
}

// truncate shortens a string to at most `limit` characters.
func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
