// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"time"
)

const (
	managerStuckPermissionFailures = 3
	managerStuckPermChannelMin     = 3
	managerStuckPermChannelWindow  = 15 * time.Minute
	managerPermFailureMark         = "permission request failed"
	managerBlockedPrefix           = "🔴 BLOCKED"
	managerStuckReadMaxBytes       = int64(8 << 20)
)

const (
	managerStuckCauseStreamClosed       = "stream-closed"
	managerStuckCausePermissionChannel  = "permission-channel"
	managerStuckCauseBlockedCloser      = "blocked-closer"
	managerStuckCausePermissionFailures = "permission-failures"
)

// managerStuckStreamClosedMark is the abort wording inside a permission
// failure that proves the permission channel itself is gone.
const managerStuckStreamClosedMark = "Stream closed"

// managerStuckReadChunkBytes is how much readRecentTail reads per backward step.
// Each chunk boundary splits one record in half, so a larger chunk loses fewer
// records and a smaller one costs more reads; 400 KB matches the tail window the
// closer reader has always used.
const managerStuckReadChunkBytes = int64(400 * 1024)

// managerPermissionOutcome is one `tool_result` reduced to the two facts the
// stuck causes read: was it a permission failure, and did it name a closed
// channel.
type managerPermissionOutcome struct {
	PermissionFailure bool
	StreamClosed      bool
}

// managerPermissionOutcomes walks a decoded tail and returns one outcome per
// `tool_result` block, in transcript order.
//
// ⚠️ Only TOOL RESULTS count, never assistant prose. A worker discussing a closed
// channel — quoting the error, or writing about this very detector — carries the
// words in its text blocks, and a substring match over the whole tail would read
// that worker as stuck and resume a healthy session.
//
// ⚠️ record.Message is nil on every record that carries no message (a `summary`,
// a `system` hook result, a `file-history-snapshot`), and those lines are
// ordinary in a real transcript. The nil check comes BEFORE `.Content` is read:
// one such line would otherwise panic inside the per-session pass and crash the
// whole wake job on its first poll instead of costing that session its gate.
func managerPermissionOutcomes(tail []byte) []managerPermissionOutcome {
	outcomes := make([]managerPermissionOutcome, 0)
	for _, line := range bytes.Split(tail, []byte("\n")) {
		record, ok := decodeTranscriptRecord(line)
		if !ok || record.Message == nil {
			continue
		}
		for _, block := range transcriptContentBlocks(record.Message.Content) {
			if block.Type != "tool_result" {
				continue
			}
			text := managerToolResultText(block.Content)
			permissionFailure := block.IsError && strings.Contains(text, managerPermFailureMark)
			outcomes = append(outcomes, managerPermissionOutcome{
				PermissionFailure: permissionFailure,
				StreamClosed: permissionFailure &&
					strings.Contains(text, managerStuckStreamClosedMark),
			})
		}
	}
	return outcomes
}

// managerToolResultText renders a tool_result payload as searchable text. The
// harness emits the body either as a bare string or as a block list, and an
// unrecognised shape is kept raw rather than dropped: a dropped abort reads as a
// success.
func managerToolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var blocks []transcriptBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		texts := make([]string, 0, len(blocks))
		for _, block := range blocks {
			if block.Text != "" {
				texts = append(texts, block.Text)
			}
		}
		return strings.Join(texts, " ")
	}
	return string(raw)
}

// managerTrailingFailures returns the run of permission failures since the last
// non-permission `tool_result`.
//
// ⚠️ "Consecutive" counts among PERMISSION OUTCOMES, not consecutive records:
// assistant text and attachments between failures do not break the run. Any
// `tool_result` that is not a permission failure means the tool executed, so the
// channel works again and the run resets there — a longest-run reading kept a
// recovered worker stuck until its evidence scrolled out of the window.
func managerTrailingFailures(outcomes []managerPermissionOutcome) int {
	run := 0
	for _, outcome := range outcomes {
		if !outcome.PermissionFailure {
			run = 0
			continue
		}
		run++
	}
	return run
}

// managerChannelAborts counts the `Stream closed` aborts inside the last
// `window` of the tail's own record time.
//
// ⚠️ The anchor is the MAXIMUM stamp in the tail, never the tail's last stamp and
// never the last abort: records can be out of order, and anchoring on the aborts
// themselves would make the window self-referential — three aborts an hour ago
// would always sit inside "the last 15 minutes of abort time" and the cause would
// never clear. Recency is measured against the transcript's own now.
//
// It returns 0 when no stamp parses: an undated tail cannot establish recency,
// and firing on one would mark every transcript whose clock field failed to
// parse.
func managerChannelAborts(tail []byte, window time.Duration) int {
	aborts := make([]time.Time, 0)
	anchor, found := time.Time{}, false
	for _, line := range bytes.Split(tail, []byte("\n")) {
		record, ok := decodeTranscriptRecord(line)
		if !ok {
			continue
		}
		stamp, parsed := managerRecordStamp(record.Timestamp)
		if !parsed {
			continue
		}
		if !found || stamp.After(anchor) {
			anchor, found = stamp, true
		}
		if record.Message == nil {
			continue
		}
		if managerRecordHasChannelAbort(transcriptContentBlocks(record.Message.Content)) {
			aborts = append(aborts, stamp)
		}
	}
	if !found {
		return 0
	}
	earliest := anchor.Add(-window)
	count := 0
	for _, abort := range aborts {
		if !abort.Before(earliest) {
			count++
		}
	}
	return count
}

// managerRecordStamp parses a transcript record's timestamp.
func managerRecordStamp(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	stamp, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, false
	}
	return stamp, true
}

// managerRecordHasChannelAbort reports whether any block is a permission failure
// naming a closed channel.
func managerRecordHasChannelAbort(blocks []transcriptContentBlock) bool {
	for _, block := range blocks {
		if block.Type != "tool_result" || !block.IsError {
			continue
		}
		text := managerToolResultText(block.Content)
		if strings.Contains(text, managerPermFailureMark) &&
			strings.Contains(text, managerStuckStreamClosedMark) {
			return true
		}
	}
	return false
}

// managerStuckReason returns why a worker reads stuck, or "" when it does not.
// Four causes, in a load-bearing order — the most specific wins, so a lost
// permission channel is named as such rather than as a generic retry run.
func managerStuckReason(closerText string, tail []byte, channelTail []byte) string {
	if channelTail == nil {
		// The source's default: a caller with no wider read keeps the exact
		// reading the narrow tail gives.
		channelTail = tail
	}
	outcomes := managerPermissionOutcomes(tail)
	run := managerTrailingFailures(outcomes)
	if run > 0 && managerRunHasStreamClosed(outcomes, run) {
		return managerStuckCauseStreamClosed
	}
	if managerChannelAborts(
		channelTail,
		managerStuckPermChannelWindow,
	) >= managerStuckPermChannelMin {
		return managerStuckCausePermissionChannel
	}
	if managerBlockedCloser(closerText) {
		return managerStuckCauseBlockedCloser
	}
	if run >= managerStuckPermissionFailures {
		return managerStuckCausePermissionFailures
	}
	return ""
}

// managerRunHasStreamClosed reports whether the trailing run of outcomes carries
// a closed channel.
func managerRunHasStreamClosed(outcomes []managerPermissionOutcome, run int) bool {
	for _, outcome := range outcomes[len(outcomes)-run:] {
		if outcome.StreamClosed {
			return true
		}
	}
	return false
}

// managerBlockedCloser reports a `🔴 BLOCKED` state line whose closer is not an
// answerable ask.
//
// ⚠️ That is the verb discriminator: a `🔴 BLOCKED` line accompanied by an
// answerable `👤 You:` verb is a worker that reached its own closer and told the
// operator where to answer — parked, not stuck. Only a panel that reads
// `nothing` (or carries no panel at all) means nobody was told.
func managerBlockedCloser(closerText string) bool {
	if !managerHasBlockedLine(closerText) {
		return false
	}
	// The body tested is the one managerCloserBody returns: the whole message
	// contains the panel's heading and prose, so the ask test must not run on the
	// raw text — that reads every panel as an ask. managerIsAsk normalizes
	// internally, so the body is passed through unwrapped.
	body, _ := managerCloserBody(closerText)
	return !managerIsAsk(body)
}

// managerHasBlockedLine reports a line starting with the blocked prefix. It is a
// line PREFIX, not a substring: a worker discussing the phrase also quotes it.
func managerHasBlockedLine(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), managerBlockedPrefix) {
			return true
		}
	}
	return false
}

// managerStuckGateReason forms the gate reason for a stuck cause, or "" for an
// empty cause. It is the only place the two prefixes are formed: `stuck:` is a
// headless worker, `stuck-tab:` a registered one, and the discriminator is
// registry presence.
func managerStuckGateReason(cause string, registered bool) string {
	if cause == "" {
		return ""
	}
	if registered {
		return "stuck-tab:" + cause
	}
	return "stuck:" + cause
}

// readRecentTail returns the transcript tail spanning at least `window` of its
// OWN record time, walking backward in chunks.
//
// ⚠️ A fixed byte window is the wrong unit for the channel read: on a long, busy
// transcript the aborts sit near the middle of the file while a 400 KB tail
// reaches only the last few tool results, so the count is zero and a session
// whose writes are aborting reads clean. Recency is measured in the unit the
// question is actually about, bounded by maxBytes and the file start.
//
// It takes a path rather than a session id and is a package function, not a
// TranscriptReader method: the reader's interface must not grow a third read for
// this.
func readRecentTail(path string, window time.Duration, maxBytes int64) ([]byte, bool) {
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
	chunks := make([][]byte, 0)
	end := size
	for end > 0 && size-end < maxBytes {
		start := end - managerStuckReadChunkBytes
		if start < 0 {
			start = 0
		}
		if _, err := file.Seek(start, 0); err != nil {
			return nil, false
		}
		buffer := make([]byte, end-start)
		read, err := file.Read(buffer)
		if err != nil && read == 0 {
			return nil, false
		}
		chunks = append(chunks, buffer[:read])
		end = start
		if managerTailSpansWindow(managerJoinChunks(chunks), window) {
			break
		}
	}
	return managerJoinChunks(chunks), true
}

// managerJoinChunks joins backward-read chunks in file order with one newline
// between them, so both halves of a record split at a chunk boundary fail to
// parse rather than one being miscounted.
func managerJoinChunks(chunks [][]byte) []byte {
	joined := make([]byte, 0)
	for index := len(chunks) - 1; index >= 0; index-- {
		if len(joined) > 0 {
			joined = append(joined, '\n')
		}
		joined = append(joined, chunks[index]...)
	}
	return joined
}

// managerTailSpansWindow reports whether the text's record timestamps span at
// least window.
func managerTailSpansWindow(text []byte, window time.Duration) bool {
	earliest, latest := time.Time{}, time.Time{}
	found := false
	for _, line := range bytes.Split(text, []byte("\n")) {
		record, ok := decodeTranscriptRecord(line)
		if !ok {
			continue
		}
		stamp, parsed := managerRecordStamp(record.Timestamp)
		if !parsed {
			continue
		}
		if !found || stamp.Before(earliest) {
			earliest = stamp
		}
		if !found || stamp.After(latest) {
			latest = stamp
		}
		found = true
	}
	return found && latest.Sub(earliest) >= window
}
