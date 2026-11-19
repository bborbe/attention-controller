// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
)

const (
	// ContextCrossingTokens is the absolute token count at which a live session
	// is cued to compact.
	//
	// ⚠️ Absolute tokens, never a percentage: a 200k-window session at 90 % is
	// 180k and must raise nothing, while a 1M-window session at 50 % is 500k and
	// must. A percentage threshold would cue exactly the sessions that need it
	// least.
	ContextCrossingTokens = 500000

	// ContextCrossingInterval is how often the crossing scan runs. The
	// statusline publishes on percentage movement, so a minute is comfortably
	// inside the window in which a crossing is still actionable.
	ContextCrossingInterval = 60 * time.Second

	// contextCrossingDedupPrefix is the dedup key prefix. The store collapses a
	// repeat push only while the earlier item is still open, which is what makes
	// "one card per crossing" true without this job keeping a book.
	contextCrossingDedupPrefix = "compact:"
)

// ContextCrossingRecord is one statusline context-state reading.
type ContextCrossingRecord struct {
	// SessionID is the session the reading belongs to.
	SessionID string
	// Name is the session's display name, falling back to the id's first
	// eight characters.
	Name string
	// Tokens is the measured total input tokens.
	Tokens int
}

// ContextCrossingSink raises the crossing card.
type ContextCrossingSink interface {
	Raise(ctx context.Context, record ContextCrossingRecord) error
}

// ContextCrossingJob cues a live session that has crossed the context
// threshold.
//
// It is the in-process replacement for the Python watcher's
// `scan_context_crossings` backstop. The cue is the ORIGINAL `compact:<sid>`
// shape, not the session-raised `compact-approval:<sid>` card: a session that
// judges for itself raises its own card, and this job must not raise a second
// one for the same session.
type ContextCrossingJob interface {
	// Run scans until the context is cancelled.
	Run(ctx context.Context) error

	// ScanOnce runs one pass. It is a seam so a spec can drive a pass
	// deterministically instead of racing a ticker.
	ScanOnce(ctx context.Context) error
}

// NewContextCrossingJob creates the crossing job over the statusline state
// directory.
//
// bookPath is where the crossing book is persisted. It is required for the
// one-card-per-crossing guarantee to survive a process restart: the controller
// restarts on every deploy, and an in-memory book would mint one fresh card per
// live crossing at each restart. An empty path makes the book memory-only,
// which is the test seam.
func NewContextCrossingJob(
	contextDir string,
	sink ContextCrossingSink,
	liveness SessionLivenessChecker,
	threshold int,
	interval time.Duration,
	bookPath string,
) ContextCrossingJob {
	return &contextCrossingJob{
		contextDir: contextDir,
		sink:       sink,
		liveness:   liveness,
		threshold:  threshold,
		interval:   interval,
		book:       newContextCrossingBook(bookPath),
	}
}

type contextCrossingJob struct {
	contextDir string
	sink       ContextCrossingSink
	liveness   SessionLivenessChecker
	threshold  int
	interval   time.Duration
	// book remembers which sessions have been cued for their current crossing.
	// It is the guard the card itself cannot be: an `ack` card is closed by its
	// own acknowledgement, so every card-anchored guard sees "never raised" on
	// the next tick.
	book *contextCrossingBook
}

// Run scans until the context is cancelled.
func (j *contextCrossingJob) Run(ctx context.Context) error {
	if j.interval <= 0 {
		return errors.Errorf(ctx, "context crossing interval must be positive")
	}
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			glog.V(2).Infof("context crossing stopped")
			return nil
		case <-ticker.C:
			if err := j.ScanOnce(ctx); err != nil {
				glog.Warningf("job=attention-watcher action=crossing_error error=%v", err)
			}
		}
	}
}

// ScanOnce cues every live session that has crossed the threshold, once per
// crossing.
//
// ⚠️ The book is what makes "once" true, and the ordering below is the whole
// rule:
//
//   - a sub-threshold reading ENDS the crossing and re-arms the session;
//   - a reading that could not be decoded (a zero, a missing field) is not a
//     measurement and changes nothing — re-arming on it would mint a second
//     card for a crossing that never ended;
//   - a session that is not live is re-armed, because a resumed session keeps
//     its id and a resumed run is a new crossing;
//   - a live session at or above the threshold is raised only when the book
//     does not already hold it.
func (j *contextCrossingJob) ScanOnce(ctx context.Context) error {
	if j.contextDir == "" || j.threshold <= 0 {
		return nil
	}
	if !j.book.loaded {
		if err := j.book.load(ctx); err != nil {
			return err
		}
	}
	names, err := contextStateNames(ctx, j.contextDir)
	if err != nil {
		return errors.Wrap(ctx, err, "list context state failed")
	}
	present := make(map[string]bool, len(names))
	raised := 0
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil
		}
		record, ok := readContextCrossing(ctx, j.contextDir, name)
		if !ok {
			continue
		}
		present[record.SessionID] = true
		didRaise, err := j.scanRecord(ctx, record)
		if err != nil {
			return err
		}
		if didRaise {
			raised++
		}
	}
	if err := j.book.prune(ctx, present, j.liveness); err != nil {
		return errors.Wrap(ctx, err, "prune crossing book failed")
	}
	if raised > 0 {
		glog.Infof(
			"job=attention-watcher action=crossing files=%d raised=%d",
			len(names),
			raised,
		)
	}
	return nil
}

// scanRecord applies the crossing rule to one decoded reading, reporting whether
// it raised a card.
//
// ⚠️ The two re-arm branches look alike and are not: one is the crossing
// ending, the other a session that cannot be cued. Both must leave the book
// empty, and a zero reading reaches neither — readContextCrossing rejects it
// before this method is called, which is what keeps "nothing measured yet" from
// minting a second card.
func (j *contextCrossingJob) scanRecord(
	ctx context.Context,
	record ContextCrossingRecord,
) (bool, error) {
	// The crossing ended: the next one raises a fresh card.
	if record.Tokens < j.threshold {
		return false, j.rearm(ctx, record.SessionID)
	}
	// ⚠️ The file proves nothing about liveness. An idle session stops writing
	// and its last value stays on disk indefinitely — measured 2026-09-25, 83
	// files sat at or above the threshold while only 2 of their sessions were
	// alive. Pushing on file presence alone would raise 81 cards for sessions
	// that cannot be compacted at all.
	if !j.liveness.IsLive(ctx, record.SessionID) {
		return false, j.rearm(ctx, record.SessionID)
	}
	if j.book.raised(record.SessionID) {
		return false, nil
	}
	if err := j.sink.Raise(ctx, record); err != nil {
		return false, errors.Wrapf(ctx, err, "raise %s failed", record.SessionID)
	}
	// ⚠️ Marked only AFTER the raise succeeded: a failed push must be retried on
	// the next tick, not suppressed by a book that says the session was already
	// cued.
	if err := j.book.mark(ctx, record); err != nil {
		return false, errors.Wrapf(ctx, err, "record crossing for %s failed", record.SessionID)
	}
	glog.V(2).Infof(
		"job=attention-watcher action=crossing session=%s tokens=%d",
		record.SessionID,
		record.Tokens,
	)
	return true, nil
}

// rearm clears the session's book entry so its next crossing raises a card.
func (j *contextCrossingJob) rearm(ctx context.Context, sessionID string) error {
	if err := j.book.clear(ctx, sessionID); err != nil {
		return errors.Wrapf(ctx, err, "re-arm %s failed", sessionID)
	}
	return nil
}

// readContextCrossing decodes one statusline state file.
//
// A ZERO IS NOT A MEASUREMENT: the statusline publishes the harness's own
// `total_input_tokens`, so a zero means "nothing measured yet", not "a context
// of no tokens". Every zero observed in the fleet was followed by a high value,
// never by a low one, so reading a zero as a drop would close a crossing that
// never changed and the next push would land as a second row.
func readContextCrossing(
	ctx context.Context,
	dir string,
	name string,
) (ContextCrossingRecord, bool) {
	content, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ContextCrossingRecord{}, false
	}
	var raw struct {
		SessionID        string   `json:"session_id"`
		SessionName      string   `json:"session_name"`
		TotalInputTokens *float64 `json:"total_input_tokens"`
	}
	if err := json.Unmarshal(content, &raw); err != nil {
		return ContextCrossingRecord{}, false
	}
	if raw.SessionID == "" || raw.TotalInputTokens == nil {
		return ContextCrossingRecord{}, false
	}
	if *raw.TotalInputTokens <= 0 {
		return ContextCrossingRecord{}, false
	}
	name2 := raw.SessionName
	if name2 == "" {
		name2 = shortSessionID(raw.SessionID)
	}
	_ = ctx
	return ContextCrossingRecord{
		SessionID: raw.SessionID,
		Name:      name2,
		Tokens:    int(*raw.TotalInputTokens),
	}, true
}

// contextStateNames lists the statusline state files beneath dir.
func contextStateNames(ctx context.Context, dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		names = append(names, entry.Name())
	}
	return names, nil
}

// shortSessionID returns the first eight characters of a session id.
func shortSessionID(sessionID string) string {
	if len(sessionID) <= 8 {
		return sessionID
	}
	return sessionID[:8]
}

// Compile-time guard: the job must satisfy its own interface.
var _ ContextCrossingJob = (*contextCrossingJob)(nil)
