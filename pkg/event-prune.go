// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

// EventPruneRetain is how long one event-log line is kept, measured by the
// line's own `ts`.
//
// ⚠️ 30 days is not a tuning knob. The gate must exceed the widest window any
// reader pins, or an instrument reading that window reports a population being
// deleted underneath it — the defect the line-age gate replaced the closed-book
// gate to remove. `recurring-gates.py` defaults to a 7-day window, so 30 leaves
// a wide margin and still bounds growth.
const EventPruneRetain = 30 * 24 * time.Hour

// EventPruneInterval is how often the prune pass runs. An hourly pass is
// generous: the retention horizon is measured in days, so the only cost of a
// later pass is that the log carries a few more aged lines for a while.
const EventPruneInterval = time.Hour

// EventPruneJob keeps the attention event logs bounded by dropping lines older
// than the retention horizon.
//
// ⚠️ The gate is the LINE'S AGE, never membership in any closed book. An
// earlier design pruned on "this item is closed", which bounded growth but made
// the surviving log a biased sample: only an answered-and-synced item was ever
// eligible, so the lines it deleted were disproportionately the gates the
// operator actually engaged with, and every baseline a reader recorded came
// from a population that shrank fastest where the operator was most active.
// Age treats every line the same way, answered or not.
//
// It is the in-process replacement for the Python watcher's `prune` phase. It
// runs here rather than beside the ingest job because the two have different
// failure shapes: a missed prune costs disk, a missed ingest costs a card, so
// the prune must never be able to hold the ingest's offset.
type EventPruneJob interface {
	// Run prunes until the context is cancelled. A failed pass is logged and
	// the next tick retries: the log being briefly over budget is not a reason
	// to take the process down.
	Run(ctx context.Context) error

	// PruneOnce runs one pass. It is a seam so a spec can drive a pass
	// deterministically instead of racing a ticker.
	PruneOnce(ctx context.Context) error
}

// NewEventPruneJob creates the prune job over the attention state directory.
func NewEventPruneJob(
	stateDir string,
	retain time.Duration,
	interval time.Duration,
	now libtime.CurrentDateTimeGetter,
) EventPruneJob {
	return &eventPruneJob{
		stateDir: stateDir,
		retain:   retain,
		interval: interval,
		now:      now,
	}
}

type eventPruneJob struct {
	stateDir string
	retain   time.Duration
	interval time.Duration
	now      libtime.CurrentDateTimeGetter
}

// Run prunes until the context is cancelled.
func (j *eventPruneJob) Run(ctx context.Context) error {
	if j.interval <= 0 {
		return errors.Errorf(ctx, "event prune interval must be positive")
	}
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			glog.V(2).Infof("event prune stopped")
			return nil
		case <-ticker.C:
			if err := j.PruneOnce(ctx); err != nil {
				glog.Warningf("job=attention-watcher action=prune_error error=%v", err)
			}
		}
	}
}

// PruneOnce drops aged lines from every event log beneath the state directory.
//
// ⚠️ A pass that drops nothing writes nothing. Rewriting an unchanged file
// would move its mtime for no reason, and a reader watching for "the log moved"
// would see activity that is not there.
func (j *eventPruneJob) PruneOnce(ctx context.Context) error {
	if j.stateDir == "" {
		// No state directory is the ordinary case for a deployment with no
		// Claude Code beside it, not an error worth reporting.
		return nil
	}
	if j.retain <= 0 {
		return errors.Errorf(ctx, "event prune retention must be positive")
	}
	paths, err := eventLogNames(j.stateDir)
	if err != nil {
		return errors.Wrap(ctx, err, "list event logs failed")
	}
	if len(paths) == 0 {
		// An absent state directory, or one with no event logs, is the ordinary
		// case for a deployment with no Claude Code beside it. Nothing to prune
		// and nothing to open.
		glog.V(3).Infof("job=attention-watcher action=prune files=0 dropped=0")
		return nil
	}
	// Confined through an os.Root, the same way every other event-log read in
	// this package is: the names come from the directory listing, but a
	// confinement is what makes that a guarantee rather than an assumption, and
	// it is what keeps the lint from having to trust the listing.
	root, err := os.OpenRoot(j.stateDir)
	if err != nil {
		return errors.Wrap(ctx, err, "open attention state dir failed")
	}
	defer root.Close()
	cutoff := j.now.Now().Time().Add(-j.retain)
	scanned, dropped := 0, 0
	for _, name := range paths {
		if err := ctx.Err(); err != nil {
			return nil
		}
		scanned++
		count, err := j.pruneFile(ctx, root, name, cutoff)
		if err != nil {
			// One unreadable file must not stop the pass: the remaining files
			// are independent, and the failure is retried on the next tick.
			glog.Warningf(
				"job=attention-watcher action=prune_file_error file=%s error=%v",
				name,
				err,
			)
			continue
		}
		dropped += count
	}
	// Logged whether or not anything was dropped: a pass that fired only on a
	// drop left no trace at all, which made "the prune ran" unprovable.
	glog.Infof(
		"job=attention-watcher action=prune files=%d dropped=%d",
		scanned,
		dropped,
	)
	return nil
}

// pruneFile drops the aged lines of one event log and returns how many went.
func (j *eventPruneJob) pruneFile(
	ctx context.Context,
	root *os.Root,
	name string,
	cutoff time.Time,
) (int, error) {
	handle, err := root.Open(name)
	if err != nil {
		return 0, errors.Wrapf(ctx, err, "open %s failed", name)
	}
	content, err := io.ReadAll(handle)
	closeErr := handle.Close()
	if err != nil {
		return 0, errors.Wrapf(ctx, err, "read %s failed", name)
	}
	if closeErr != nil {
		return 0, errors.Wrapf(ctx, closeErr, "close %s failed", name)
	}
	lines := strings.Split(string(content), "\n")
	// Split leaves an empty final element for a file ending in a newline. It is
	// not a line and must not be re-emitted as one.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	keep := make([]string, 0, len(lines))
	dropped := 0
	for _, line := range lines {
		stripped := strings.TrimSpace(line)
		if stripped == "" {
			// Blank lines carry nothing; the Python pass dropped them too.
			continue
		}
		if eventLineAged(stripped, cutoff) {
			dropped++
			continue
		}
		keep = append(keep, line)
	}
	if dropped == 0 {
		return 0, nil
	}
	// Written to a sibling and renamed, so a reader never sees a half file.
	tmpName := name + ".tmp"
	body := ""
	if len(keep) > 0 {
		body = strings.Join(keep, "\n") + "\n"
	}
	tmp, err := root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, errors.Wrapf(ctx, err, "create %s failed", tmpName)
	}
	if _, err := tmp.WriteString(body); err != nil {
		_ = tmp.Close()
		_ = root.Remove(tmpName)
		return 0, errors.Wrapf(ctx, err, "write %s failed", tmpName)
	}
	if err := tmp.Close(); err != nil {
		_ = root.Remove(tmpName)
		return 0, errors.Wrapf(ctx, err, "close %s failed", tmpName)
	}
	if err := root.Rename(tmpName, name); err != nil {
		// Leave nothing behind on a failed rename; the original is intact.
		_ = root.Remove(tmpName)
		return 0, errors.Wrapf(ctx, err, "rename %s failed", name)
	}
	glog.V(2).Infof(
		"job=attention-watcher action=prune_file file=%s dropped=%d",
		name,
		dropped,
	)
	return dropped, nil
}

// eventLineAged reports whether one line's own `ts` is older than cutoff.
//
// A line with no usable timestamp is KEPT, never guessed at: a torn final line,
// an unknown record shape and a record written without a clock are all cases
// where the age is unknown rather than old, and dropping them would delete
// evidence the reader cannot recover.
func eventLineAged(line string, cutoff time.Time) bool {
	var record struct {
		TS *float64 `json:"ts"`
	}
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		return false
	}
	if record.TS == nil {
		return false
	}
	return time.Unix(int64(*record.TS), 0).Before(cutoff)
}

// eventLogNames lists the attention event logs directly beneath stateDir.
func eventLogNames(stateDir string) ([]string, error) {
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".events.jsonl") {
			continue
		}
		names = append(names, entry.Name())
	}
	return names, nil
}

// Compile-time guard: the job must satisfy its own interface.
var _ EventPruneJob = (*eventPruneJob)(nil)
