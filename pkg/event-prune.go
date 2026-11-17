// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
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

// eventPrunePassBudget bounds one prune pass — the second of the two non-arm
// phases the Python watcher's PHASE_BUDGET_S bounded (the other is the
// vault-index walk).
//
// ⚠️ The value is pinned by derivation, not chosen freely. The pass interval is
// EventPruneInterval (1 h) and the budget must be at most 10 % of it, so at most
// 5 min.
//
// ⚠️ It is NOT sized above a whole pass, and that is deliberate. Measured on the
// live state directory 2026-11-17: 7,673 event logs / 165 MB, and one whole pass
// costs 138 s. A budget 5x above that would be ~11 min, i.e. 19 % of the hourly
// interval — so the two rules cannot both hold at this corpus size. The pass
// therefore covers ONE SLICE and rotates (eventPruneSliceDivisor), which keeps
// the 10 % ceiling and gives the ordered scan a guaranteed tail reach.
const eventPrunePassBudget = time.Minute

// eventPruneSliceDivisor is how many passes one full sweep of the state
// directory takes: each pass prunes at most 1/divisor of the logs and resumes
// from a cursor next time.
//
// ⚠️ A divisor rather than a count, because the corpus grows. At the measured
// whole-pass cost of 138 s, one pass over a twelfth is ~11.5 s against a 60 s
// budget — five times inside it, so an ordinary pass never cuts, and full
// coverage arrives every 12 hourly passes (12 h) while retention is 30 days.
const eventPruneSliceDivisor = 12

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
		stateDir:   stateDir,
		retain:     retain,
		interval:   interval,
		now:        now,
		passBudget: eventPrunePassBudget,
	}
}

type eventPruneJob struct {
	stateDir string
	retain   time.Duration
	interval time.Duration
	now      libtime.CurrentDateTimeGetter
	// passBudget bounds one pass. It defaults to eventPrunePassBudget and exists
	// as a field so a spec can drive a cut deterministically instead of waiting
	// out the real minute. A zero value falls back to the constant.
	passBudget time.Duration
	// rotateFrom is the log name the next pass resumes AFTER, so consecutive
	// passes cover disjoint slices rather than rescanning the same prefix — the
	// starvation a budget-cut ordered scan would otherwise produce. Guarded by
	// rotateMu.
	rotateFrom string
	// rotateMu guards rotateFrom: the ticker and a manual PruneOnce can overlap.
	rotateMu sync.Mutex
	// sliceOverride forces the number of logs one pass covers. Zero in
	// production; a spec sets it to observe rotation on a small corpus without
	// writing minSliceSize logs.
	sliceOverride int
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
	_, err := j.prunePassUnderBudget(ctx)
	return err
}

// prunePassUnderBudget runs one pass under the pass budget and reports whether
// it completed.
//
// ⚠️ The budget is issued HERE, at the pass's own call site, and it is the only
// thing that bounds a pass: the job runs on a ticker whose context is the
// long-lived process context, which carries no deadline. A cut pass must not
// claim a completed pass — see prunePass.
//
// It is the seam the budget spec drives: returning completion lets a spec assert
// a cut pass never reached the completed-pass summary, which is the property a
// bare `PruneOnce(ctx) error` cannot expose.
func (j *eventPruneJob) prunePassUnderBudget(ctx context.Context) (bool, error) {
	passCtx, cancel := context.WithTimeout(ctx, j.budget())
	defer cancel()
	completed, err := j.prunePass(passCtx)
	if err != nil {
		return false, err
	}
	if !completed {
		// ⚠️ The cut is logged, not silent: a budget that fires every round would
		// otherwise look like a pass that found nothing to drop.
		glog.V(2).Infof("job=attention-watcher action=prune_cut budget=%s", j.budget())
	}
	return completed, nil
}

// prunePass runs one pass and reports whether it COMPLETED.
//
// ⚠️ A pass cut by the budget returns false and never reaches the
// `action=prune files=… dropped=…` summary, so the log never says "done" about a
// pass that stopped. The summary is the completed-pass claim, and it is emitted
// only on the one path that reached the end of the file list.
func (j *eventPruneJob) prunePass(ctx context.Context) (bool, error) {
	if j.stateDir == "" {
		// No state directory is the ordinary case for a deployment with no
		// Claude Code beside it, not an error worth reporting.
		return true, nil
	}
	if j.retain <= 0 {
		return false, errors.Errorf(ctx, "event prune retention must be positive")
	}
	paths, err := eventLogNames(ctx, j.stateDir)
	if err != nil {
		return false, errors.Wrap(ctx, err, "list event logs failed")
	}
	if len(paths) == 0 {
		// An absent state directory, or one with no event logs, is the ordinary
		// case for a deployment with no Claude Code beside it. Nothing to prune
		// and nothing to open.
		glog.V(3).Infof("job=attention-watcher action=prune files=0 dropped=0")
		return true, nil
	}
	// Confined through an os.Root, the same way every other event-log read in
	// this package is: the names come from the directory listing, but a
	// confinement is what makes that a guarantee rather than an assumption, and
	// it is what keeps the lint from having to trust the listing.
	root, err := os.OpenRoot(j.stateDir)
	if err != nil {
		return false, errors.Wrap(ctx, err, "open attention state dir failed")
	}
	defer root.Close()
	cutoff := j.now.Now().Time().Add(-j.retain)

	// ⚠️ This pass covers ONE SLICE, starting where the last one stopped. A pass
	// over the whole list is cut by the budget at this corpus size (7,673 logs
	// cost 138 s against a 60 s budget), and a cut that always stops at the same
	// point never reaches the tail — the starvation the Python `rotate_from`
	// helper existed to prevent. The cursor is what makes a cut safe: the work
	// already done is not redone, and the tail is reached on a later pass.
	sort.Strings(paths)
	ordered := rotateFrom(paths, j.cursor())
	// ⚠️ The slice is sized from the WHOLE list, never from what is left after
	// the cursor: sizing it from the remainder shrinks the last passes to one
	// file each and the cycle never completes on schedule.
	slice := sliceSize(len(paths), eventPruneSliceDivisor, minSliceSize)
	if j.sliceOverride > 0 {
		slice = j.sliceOverride
	}

	scanned, dropped := 0, 0
	last := j.cursor()
	for _, name := range ordered {
		// ⚠️ The budget is checked BEFORE the slice bound: a file whose prune
		// blocked until the deadline must end the pass, not let the slice bound
		// end it as if the work had finished.
		if err := ctx.Err(); err != nil {
			j.setCursor(last)
			return false, nil
		}
		if scanned >= slice {
			break
		}
		scanned++
		last = name
		count, err := pruneOneFile(j, ctx, root, name, cutoff)
		if err != nil {
			// One unreadable file must not stop the pass: the remaining files
			// are independent, and the failure is retried on a later pass.
			glog.Warningf(
				"job=attention-watcher action=prune_file_error file=%s error=%v",
				name,
				err,
			)
			continue
		}
		dropped += count
	}
	// A pass whose slice reached the LAST log has finished the sweep, so the next
	// one starts from the top; otherwise the cursor moves to where this one
	// stopped. ⚠️ The test is the last name, not "the list was exhausted":
	// rotateFrom wraps, so `ordered` is always the whole list and never runs out —
	// the wrap point is what marks the end of a sweep.
	if len(paths) == 0 || last == paths[len(paths)-1] {
		j.setCursor("")
	} else {
		j.setCursor(last)
	}
	// Logged whether or not anything was dropped: a pass that fired only on a
	// drop left no trace at all, which made "the prune ran" unprovable. The
	// cursor and total make the slice visible, so "one pass" is never mistaken
	// for "the whole directory".
	glog.Infof(
		"job=attention-watcher action=prune files=%d dropped=%d cursor=%q total=%d",
		scanned,
		dropped,
		j.cursor(),
		len(paths),
	)
	return true, nil
}

// cursor returns the log the next pass resumes after, empty meaning the top.
func (j *eventPruneJob) cursor() string {
	j.rotateMu.Lock()
	defer j.rotateMu.Unlock()
	return j.rotateFrom
}

// setCursor records where the next pass resumes from.
func (j *eventPruneJob) setCursor(name string) {
	j.rotateMu.Lock()
	defer j.rotateMu.Unlock()
	j.rotateFrom = name
}

// pruneOneFile prunes one event log. It is a package variable rather than a
// direct method call so a spec can make one file's prune block until the pass
// budget fires, which is how the in-loop cut is exercised deterministically
// instead of raced against a real file read. Same seam pattern as taskFileReader.
var pruneOneFile = func(
	j *eventPruneJob,
	ctx context.Context,
	root *os.Root,
	name string,
	cutoff time.Time,
) (int, error) {
	return j.pruneFile(ctx, root, name, cutoff)
}

// budget returns the pass budget, defaulting to the constant when the field is
// unset so every construction path is bounded.
func (j *eventPruneJob) budget() time.Duration {
	if j.passBudget > 0 {
		return j.passBudget
	}
	return eventPrunePassBudget
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
	keep, dropped, err := j.agedLines(ctx, lines, cutoff)
	if err != nil {
		// Cancelled mid-file: nothing has been written, so the next pass
		// simply re-reads it.
		return 0, nil
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

// agedLines splits one file's lines into the ones to keep and how many to drop.
//
// Cancellation is checked inside the loop as well as between files: an event
// log can hold a great many lines, and nothing has been written when this
// returns, so abandoning the file costs only the work already done.
func (j *eventPruneJob) agedLines(
	ctx context.Context,
	lines []string,
	cutoff time.Time,
) ([]string, int, error) {
	keep := make([]string, 0, len(lines))
	dropped := 0
	for index, line := range lines {
		if index%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, 0, err
			}
		}
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
	return keep, dropped, nil
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
func eventLogNames(ctx context.Context, stateDir string) ([]string, error) {
	entries, err := os.ReadDir(stateDir)
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
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".events.jsonl") {
			continue
		}
		names = append(names, entry.Name())
	}
	return names, nil
}

// Compile-time guard: the job must satisfy its own interface.
var _ EventPruneJob = (*eventPruneJob)(nil)
