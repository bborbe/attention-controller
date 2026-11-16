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
	"time"

	"github.com/bborbe/errors"
	"github.com/bborbe/validation"
	"github.com/golang/glog"
)

// EventIngestInterval is how often the ingest tails the event logs. Two seconds
// matches the Python watcher's poll and is the pace the board's forward lag is
// measured at.
const EventIngestInterval = 2 * time.Second

// eventIngestCursorFile is where this job keeps its own read position.
//
// ⚠️ A DIFFERENT file from the Python watcher's `cursors.json`, deliberately.
// While both processes run, sharing one file would have each rewriting the
// other's offsets — and an offset is a claim about what has already been
// forwarded, so a clobbered one either re-forwards or drops. Separate files
// keep the two readers independent until the Python watcher is retired.
const eventIngestCursorFile = "attention-ingest-cursors.json"

// eventIngestQuarantineFile is where the job records every record it skipped
// past because the sink refused it permanently.
//
// ⚠️ It exists because a skip that leaves only a log line is not a record. The
// log is rotated and its volume is the very symptom the skip relieves, so a
// rejected record's reason has to survive somewhere durable. The skip is also
// made CONDITIONAL on this write: if the quarantine cannot be persisted the
// cursor does not move and the record is retried, because advancing past a
// record with no record of why is the silent loss the task forbids.
const eventIngestQuarantineFile = "attention-ingest-quarantine.json"

// eventIngestQuarantineEntry is one record skipped past permanently.
//
// Reason is the sink's own error text, read at the moment of refusal — never a
// literal the caller supplied — so it names the actual schema violation (e.g.
// "3 options are marked recommended") rather than a generic "invalid record".
type eventIngestQuarantineEntry struct {
	File   string `json:"file"`
	ItemID string `json:"item_id"`
	// Start is the byte offset the record starts at, and After the offset the
	// cursor advanced to. Together they name the exact bytes skipped, so the
	// record can be replayed by hand if the store's schema later accepts it.
	Start  int64  `json:"start"`
	After  int64  `json:"after"`
	Reason string `json:"reason"`
}

// eventIngestKinds is the set of event kinds the board can render. A record of
// any other kind is consumed (its offset advances) but never forwarded: `idle`
// is the session moving, not a gate.
var eventIngestKinds = map[string]bool{
	"permission": true,
	"question":   true,
}

// EventIngestRecord is one decoded line of a session's event log.
type EventIngestRecord struct {
	// ItemID is the log's own id — `sha256(session:kind:detail)`, so a real
	// second ask of the same question hashes to the SAME id. It is the dedup
	// key's input, never a unique row id.
	ItemID string
	// SessionID is the session the event belongs to.
	SessionID string
	// Kind is the event kind; only eventIngestKinds are forwarded.
	Kind string
	// TS is the writer's wall clock in epoch seconds, nil when the line carried
	// no usable value. It is the settled-mark's key, so it is kept as a pointer
	// rather than defaulted to zero — zero would read as "1970" and compare
	// below every mark.
	TS *float64
	// Open is true for an `open` record and false for a close.
	Open bool
	// Event is the harness event name (`Stop`, `SessionEnd`, …).
	Event string
	// Detail is the human-readable detail the class-3 filter reads.
	Detail string
	// Forward is the hook's own declaration. False means "cleared locally,
	// do not post a close" — a closer panel read while the session keeps
	// running proves nothing about an answer.
	Forward bool
	// Raw is the line as written, for the sink to map onto its own schema.
	Raw json.RawMessage
}

// EventIngestSink receives the records the job decides to forward.
//
// ⚠️ It is a seam, not the store itself: the mapping from a hook event to a
// push request is the part that changes most often (schema fields, enrichment),
// while the tailing rules — cursors, torn lines, rewind marks, kind filtering —
// are the part that must not change at all. Splitting them lets each be tested
// for what it is.
type EventIngestSink interface {
	// Open forwards a record whose session is asking something.
	Open(ctx context.Context, record EventIngestRecord) error
	// Close forwards a record whose gate has cleared.
	Close(ctx context.Context, record EventIngestRecord) error
}

// EventIngestJob tails the attention event logs and forwards what the board
// should render.
//
// The tailing contract, inherited from the Python watcher and preserved here
// because each clause is a loss the old design had already paid for:
//
//   - The cursor advances ONLY after a record's effect landed, and only per
//     record. Advancing a file up front would drop the backlog on a store
//     outage — the exact loss the down-store criterion forbids.
//   - A torn final line is left where it is and the cursor does not move past
//     it: the hook appends while this reads, so a partial line is expected.
//   - A file shorter than its cursor has been truncated or replaced; the read
//     restarts at 0 and the settled-`ts` mark suppresses the lines already
//     consumed. The mark is applied ONLY on a rewind, never to ordinary
//     tailing: `ts` is the writer's clock, so a backwards step would put a live
//     gate below the mark and silently drop it.
type EventIngestJob interface {
	// Run ingests until the context is cancelled.
	Run(ctx context.Context) error

	// IngestOnce runs one pass. It is a seam so a spec can drive a pass
	// deterministically instead of racing a ticker.
	IngestOnce(ctx context.Context) error
}

// NewEventIngestJob creates the ingest job over the attention state directory.
func NewEventIngestJob(
	stateDir string,
	sink EventIngestSink,
	filter EventIngestFilter,
	interval time.Duration,
) EventIngestJob {
	return &eventIngestJob{
		stateDir: stateDir,
		sink:     sink,
		filter:   filter,
		interval: interval,
	}
}

type eventIngestJob struct {
	stateDir string
	sink     EventIngestSink
	filter   EventIngestFilter
	interval time.Duration
}

// eventIngestCursors is the job's own read position per file: how far it has
// read, and the newest record `ts` it has settled in that file.
//
// ⚠️ The two are not the same guarantee. The offset says how FAR; the mark says
// how RECENT. A rewind re-reads lines already settled, and only the mark can
// tell them from a genuine re-ask.
type eventIngestCursors struct {
	Offsets map[string]int64   `json:"offsets"`
	Marks   map[string]float64 `json:"marks"`
}

// Run ingests until the context is cancelled.
func (j *eventIngestJob) Run(ctx context.Context) error {
	if j.interval <= 0 {
		return errors.Errorf(ctx, "event ingest interval must be positive")
	}
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			glog.V(2).Infof("event ingest stopped")
			return nil
		case <-ticker.C:
			if err := j.IngestOnce(ctx); err != nil {
				glog.Warningf("job=attention-watcher action=ingest_error error=%v", err)
			}
		}
	}
}

// IngestOnce tails every event log once, forwarding what it has not yet read.
func (j *eventIngestJob) IngestOnce(ctx context.Context) error {
	if j.stateDir == "" {
		return nil
	}
	names, err := eventLogNames(ctx, j.stateDir)
	if err != nil {
		return errors.Wrap(ctx, err, "list event logs failed")
	}
	if len(names) == 0 {
		return nil
	}
	root, err := os.OpenRoot(j.stateDir)
	if err != nil {
		return errors.Wrap(ctx, err, "open attention state dir failed")
	}
	defer root.Close()

	cursors := j.loadCursors(ctx, root)
	opened, closed, skipped := 0, 0, 0
	advanced := false
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil
		}
		counts, err := j.ingestFile(ctx, root, name, cursors)
		// ⚠️ Accumulated BEFORE the error check, not after it. A file whose pass
		// failed on one record has still advanced its cursor past the records
		// before it — those effects landed — and dropping that advance would
		// re-read and re-forward them on the next pass. The old unconditional
		// save persisted it; this keeps that property while still skipping the
		// write when nothing moved.
		advanced = advanced || counts.advanced
		if err != nil {
			// The cursor for this file was not advanced past the failed
			// record, so the next pass retries it rather than skipping it.
			glog.Warningf(
				"job=attention-watcher action=ingest_file_error file=%s error=%v",
				name,
				err,
			)
			continue
		}
		opened += counts.opened
		closed += counts.closed
		skipped += counts.skipped
	}
	// ⚠️ Written only when a cursor actually moved. The cursor file is the whole
	// population — 6676 entries on 2026-11-16 — so rewriting it on every pass
	// rewrote ~400 KB every two seconds to record that nothing had changed. A
	// skipped write is safe because an unchanged cursor and an unwritten one are
	// the same claim; the next pass that does advance writes the full set.
	if advanced {
		j.saveCursors(ctx, root, cursors)
	}
	glog.V(2).Infof(
		"job=attention-watcher action=ingest files=%d opened=%d closed=%d skipped=%d",
		len(names),
		opened,
		closed,
		skipped,
	)
	return nil
}

// ingestCounts is what one file's pass did, for the cycle's log line.
type ingestCounts struct {
	opened   int
	closed   int
	skipped  int
	advanced bool
}

// eventRecordAt is one decoded record together with the byte offset AFTER it,
// so the caller advances the cursor per record and only for records whose
// effect landed. Advancing a file up front would drop the backlog on a store
// outage — precisely the loss the down-store criterion forbids.
type eventRecordAt struct {
	record EventIngestRecord
	// start is the byte offset the record begins at; after is the offset of the
	// next record. start is carried so a permanent skip can name the exact bytes
	// it quarantined.
	start int64
	after int64
}

// ingestFile tails one log from its cursor and forwards what it reads.
func (j *eventIngestJob) ingestFile(
	ctx context.Context,
	root *os.Root,
	name string,
	cursors *eventIngestCursors,
) (ingestCounts, error) {
	var counts ingestCounts
	content, start, err := readRootFileFrom(root, name, cursors.Offsets[name])
	if err != nil {
		return counts, err
	}
	// A file SHORTER than its cursor has been truncated or replaced, and
	// readRootFileFrom has already re-read it from the start. The settled-ts
	// mark below is what tells a line already consumed from a genuine re-ask.
	rewound := start < cursors.Offsets[name]
	records := readEventRecords(content, start)
	for _, entry := range records {
		if err := ctx.Err(); err != nil {
			return counts, nil
		}
		if rewound && entry.record.TS != nil && *entry.record.TS <= cursors.Marks[name] {
			// Settled already — a re-read, not a new event.
			cursors.Offsets[name] = entry.after
			counts.advanced = true
			continue
		}
		if err := j.ingestRecord(ctx, root, name, entry, cursors, &counts); err != nil {
			return counts, err
		}
	}
	return counts, nil
}

// ingestRecord forwards one record and records what happened to it.
//
// It is split out of ingestFile so the permanent-skip decision and the
// quarantine write do not nest four deep inside the file loop — the shape that
// made ingestFile's cognitive complexity climb past the linter's limit.
func (j *eventIngestJob) ingestRecord(
	ctx context.Context,
	root *os.Root,
	name string,
	entry eventRecordAt,
	cursors *eventIngestCursors,
	counts *ingestCounts,
) error {
	forwarded, err := j.forward(ctx, entry.record)
	if err != nil {
		// ⚠️ A PERMANENT failure must not pin the cursor. A record the sink can
		// never accept — a push request the schema refuses — would otherwise be
		// re-read and re-pushed every two seconds for the life of the file.
		// Measured 2026-11-16 on a8719677: one record whose options carried three
		// `recommended` flags made the ingest the process's largest CPU consumer
		// while forwarding nothing.
		//
		// A transient failure still retries, and that asymmetry is the point: a
		// store outage must not advance a cursor past work that has not landed, so
		// only an error the sink itself declares permanent is skipped past.
		if !isPermanentIngestError(err) {
			// The cursor stays where it was, so the next pass re-reads this
			// record instead of skipping it.
			return err
		}
		// ⚠️ The quarantine write is LOAD-BEARING, not best-effort. If the record
		// cannot be persisted, the cursor does not move and this record is retried
		// next pass — advancing past a record with no durable record of why is
		// exactly the silent loss the task forbids, and a log line is not a record
		// (it rotates, and its volume is the symptom the skip relieves).
		if qErr := j.recordQuarantine(ctx, root, eventIngestQuarantineEntry{
			File:   name,
			ItemID: entry.record.ItemID,
			Start:  entry.start,
			After:  entry.after,
			// The reason is the sink's own error text, read HERE at runtime —
			// never a literal the caller typed — so it names the actual rejection
			// the store gave.
			Reason: err.Error(),
		}); qErr != nil {
			return qErr
		}
		cursors.Offsets[name] = entry.after
		counts.advanced = true
		counts.skipped++
		glog.Warningf(
			"job=attention-watcher action=ingest_skip_invalid file=%s item=%s error=%v",
			name,
			entry.record.ItemID,
			err,
		)
		return nil
	}
	if !forwarded {
		counts.skipped++
	} else if entry.record.Open {
		counts.opened++
	} else {
		counts.closed++
	}
	cursors.Offsets[name] = entry.after
	counts.advanced = true
	if entry.record.TS != nil {
		cursors.Marks[name] = maxFloat(cursors.Marks[name], *entry.record.TS)
	}
	return nil
}

// forward hands one record to the sink, reporting whether it was forwarded.
func (j *eventIngestJob) forward(
	ctx context.Context,
	record EventIngestRecord,
) (bool, error) {
	if !record.Open {
		return true, j.sink.Close(ctx, record)
	}
	if !eventIngestKinds[record.Kind] {
		// Consumed but deliberately not forwarded: `idle` is the session
		// moving, not a gate.
		return false, nil
	}
	if j.filter != nil && j.filter.Skip(ctx, record) {
		// Class 3: the record names a task that was already finished when it
		// was written. Consumed, never forwarded.
		glog.V(2).Infof(
			"job=attention-watcher action=skip item=%s kind=%s (named task already finished)",
			record.ItemID,
			record.Kind,
		)
		return false, nil
	}
	return true, j.sink.Open(ctx, record)
}

// readEventRecords decodes the records appended since offset.
//
// Each record carries the offset AFTER it. It also reports whether the file had
// shrunk below the cursor — a re-read rather than new traffic, which is the only
// case where the settled-`ts` guard applies. A torn final line is left: the hook
// appends while this reads, so a partial line is expected rather than
// exceptional.
// readEventRecords decodes the records in content, which begins at offset in the
// file.
//
// ⚠️ offset is the base the returned `after` positions are measured from, not a
// slice point: the caller has already read only the tail it needs (see
// readRootFileFrom), so content starts at offset. The rewind decision lives in
// the reader, which is the only place the file's size is known — this function
// is handed a tail and has no way to tell a rewind from ordinary appending.
func readEventRecords(content []byte, offset int64) []eventRecordAt {
	if len(content) == 0 {
		return nil
	}
	reader := bufio.NewReader(bytes.NewReader(content))
	records := make([]eventRecordAt, 0)
	consumed := offset
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			// EOF with no newline is a torn line: leave it for the next pass.
			break
		}
		consumed += int64(len(line))
		record, ok := decodeEventRecord(line)
		if !ok {
			continue
		}
		records = append(records, eventRecordAt{
			record: record,
			start:  consumed - int64(len(line)),
			after:  consumed,
		})
	}
	return records
}

// decodeEventRecord decodes one line, reporting false for a line that carries
// no item id or cannot be parsed. Neither is an error: the hook may append a
// shape this reader does not know, and skipping it costs nothing while failing
// the pass would stall every later record in the file.
func decodeEventRecord(line []byte) (EventIngestRecord, bool) {
	stripped := bytes.TrimSpace(line)
	if len(stripped) == 0 {
		return EventIngestRecord{}, false
	}
	var raw struct {
		ItemID    string   `json:"item_id"`
		SessionID string   `json:"session_id"`
		Kind      string   `json:"kind"`
		TS        *float64 `json:"ts"`
		Type      string   `json:"type"`
		Event     string   `json:"event"`
		Detail    string   `json:"detail"`
		Forward   *bool    `json:"forward"`
	}
	if err := json.Unmarshal(stripped, &raw); err != nil {
		return EventIngestRecord{}, false
	}
	if raw.ItemID == "" {
		return EventIngestRecord{}, false
	}
	forward := true
	if raw.Forward != nil {
		forward = *raw.Forward
	}
	return EventIngestRecord{
		ItemID:    raw.ItemID,
		SessionID: raw.SessionID,
		Kind:      raw.Kind,
		TS:        raw.TS,
		Open:      raw.Type == "open",
		Event:     raw.Event,
		Detail:    raw.Detail,
		Forward:   forward,
		Raw:       append(json.RawMessage(nil), stripped...),
	}, true
}

// loadCursors reads the job's read position, treating an absent or unreadable
// file as "nothing read yet". A cursor file is a cache of what was forwarded,
// never the record itself, so starting over re-forwards into a store that
// dedups on the dedup key.
func (j *eventIngestJob) loadCursors(
	ctx context.Context,
	root *os.Root,
) *eventIngestCursors {
	cursors := &eventIngestCursors{
		Offsets: map[string]int64{},
		Marks:   map[string]float64{},
	}
	content, err := readRootFile(root, eventIngestCursorFile)
	if err != nil {
		return cursors
	}
	var loaded eventIngestCursors
	if err := json.Unmarshal(content, &loaded); err != nil {
		glog.Warningf("job=attention-watcher action=ingest_cursor_parse error=%v", err)
		return cursors
	}
	if loaded.Offsets != nil {
		cursors.Offsets = loaded.Offsets
	}
	if loaded.Marks != nil {
		cursors.Marks = loaded.Marks
	}
	_ = ctx
	return cursors
}

// saveCursors persists the read position. A failure is logged rather than
// returned: the cycle's forwarding has already happened, and losing the cursor
// costs a re-read that the store dedups, not a lost card.
func (j *eventIngestJob) saveCursors(
	ctx context.Context,
	root *os.Root,
	cursors *eventIngestCursors,
) {
	body, err := json.Marshal(cursors)
	if err != nil {
		glog.Warningf("job=attention-watcher action=ingest_cursor_marshal error=%v", err)
		return
	}
	tmpName := eventIngestCursorFile + ".tmp"
	handle, err := root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		glog.Warningf("job=attention-watcher action=ingest_cursor_write error=%v", err)
		return
	}
	if _, err := handle.Write(append(body, '\n')); err != nil {
		_ = handle.Close()
		_ = root.Remove(tmpName)
		glog.Warningf("job=attention-watcher action=ingest_cursor_write error=%v", err)
		return
	}
	if err := handle.Close(); err != nil {
		_ = root.Remove(tmpName)
		glog.Warningf("job=attention-watcher action=ingest_cursor_close error=%v", err)
		return
	}
	if err := root.Rename(tmpName, eventIngestCursorFile); err != nil {
		_ = root.Remove(tmpName)
		glog.Warningf("job=attention-watcher action=ingest_cursor_rename error=%v", err)
	}
	_ = ctx
}

// loadQuarantine reads the durable quarantine list, treating an absent or
// unreadable file as "nothing quarantined yet".
//
// ⚠️ A parse failure returns nil rather than an error: the list is rewritten on
// the next quarantine write, and refusing to ingest because the list is corrupt
// would stop the whole arm over records it is not even reading.
func (j *eventIngestJob) loadQuarantine(root *os.Root) []eventIngestQuarantineEntry {
	content, err := readRootFile(root, eventIngestQuarantineFile)
	if err != nil {
		return nil
	}
	var entries []eventIngestQuarantineEntry
	if err := json.Unmarshal(content, &entries); err != nil {
		glog.Warningf("job=attention-watcher action=ingest_quarantine_parse error=%v", err)
		return nil
	}
	return entries
}

// recordQuarantine appends one entry to the durable quarantine list.
//
// ⚠️ Unlike saveCursors, a failure here is RETURNED, not logged: the caller must
// not advance the cursor past a record it could not record. The write is atomic
// (tmp + rename) so a torn file cannot lose every earlier entry.
func (j *eventIngestJob) recordQuarantine(
	ctx context.Context,
	root *os.Root,
	entry eventIngestQuarantineEntry,
) error {
	entries := j.loadQuarantine(root)
	replaced := false
	for i := range entries {
		if entries[i].File == entry.File &&
			entries[i].ItemID == entry.ItemID &&
			entries[i].Start == entry.Start {
			entries[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		entries = append(entries, entry)
	}
	body, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return errors.Wrap(ctx, err, "marshal ingest quarantine failed")
	}
	tmpName := eventIngestQuarantineFile + ".tmp"
	handle, err := root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return errors.Wrap(ctx, err, "open ingest quarantine failed")
	}
	if _, err := handle.Write(append(body, '\n')); err != nil {
		_ = handle.Close()
		_ = root.Remove(tmpName)
		return errors.Wrap(ctx, err, "write ingest quarantine failed")
	}
	if err := handle.Close(); err != nil {
		_ = root.Remove(tmpName)
		return errors.Wrap(ctx, err, "close ingest quarantine failed")
	}
	if err := root.Rename(tmpName, eventIngestQuarantineFile); err != nil {
		_ = root.Remove(tmpName)
		return errors.Wrap(ctx, err, "rename ingest quarantine failed")
	}
	return nil
}

// readRootFile reads a file through the confined root, returning an error when
// it is absent or unreadable.
func readRootFile(root *os.Root, name string) ([]byte, error) {
	handle, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	content, readErr := io.ReadAll(handle)
	closeErr := handle.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return content, nil
}

// readRootFileFrom reads name from offset to the end of the file, and reports
// where the returned content begins.
//
// ⚠️ This is what keeps a pass over an unchanged log cheap, and it is the whole
// reason the ingest is affordable. The job tails EVERY event log every two
// seconds; on 2026-11-16 that was 6676 files and 162 MB, and reading each one in
// full to discover that nothing had been appended cost 55% of the process's
// allocation and 17% of its CPU. A file already at its cursor now costs one
// open, one stat and one close.
//
// The returned start is not always the offset asked for: a file SHORTER than
// offset has been truncated or replaced, and is re-read from 0 with start 0. The
// caller detects that rewind by comparing start against its own cursor, and
// applies the settled-`ts` mark to the lines it re-reads. Empty content with
// start == offset means nothing was appended.
func readRootFileFrom(root *os.Root, name string, offset int64) ([]byte, int64, error) {
	// ⚠️ Stat BEFORE opening. The common case is a log that has not grown since
	// the last pass, and it must cost one syscall rather than three: the job
	// stats every log in the directory every two seconds (6681 of them on
	// 2026-11-16), so an open+stat+close per file stayed the largest single CPU
	// consumer even after the read itself was made incremental.
	info, err := root.Stat(name)
	if err != nil {
		return nil, offset, err
	}
	size := info.Size()
	if size < offset {
		offset = 0
	}
	if size == offset {
		return nil, offset, nil
	}
	handle, err := root.Open(name)
	if err != nil {
		return nil, offset, err
	}
	if offset > 0 {
		if _, seekErr := handle.Seek(offset, io.SeekStart); seekErr != nil {
			_ = handle.Close()
			return nil, offset, seekErr
		}
	}
	// Sized from the stat so the common case — a handful of appended lines —
	// allocates once instead of growing a buffer by doubling. A file that grows
	// between the stat and the read simply appends past the capacity, which
	// bytes.Buffer grows into.
	buffer := bytes.NewBuffer(make([]byte, 0, size-offset))
	if _, readErr := buffer.ReadFrom(handle); readErr != nil {
		_ = handle.Close()
		return nil, offset, readErr
	}
	if closeErr := handle.Close(); closeErr != nil {
		return nil, offset, closeErr
	}
	return buffer.Bytes(), offset, nil
}

// isPermanentIngestError reports whether a forward failure can never succeed on
// a retry.
//
// ⚠️ Only the sink's own validation is treated as permanent. Everything else —
// an unreadable store, a full disk, a cancelled context — is assumed transient,
// because moving the cursor past a record that could still land is the loss the
// tailing contract forbids.
func isPermanentIngestError(err error) bool {
	return errors.Is(err, validation.Error)
}

// maxFloat returns the larger of two values, used to keep a file's settled mark
// monotonic. A mark that moved backwards would let a rewind re-forward lines it
// had already settled.
func maxFloat(current float64, candidate float64) float64 {
	if candidate > current {
		return candidate
	}
	return current
}

// Compile-time guard: the job must satisfy its own interface.
var _ EventIngestJob = (*eventIngestJob)(nil)
