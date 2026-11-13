// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bborbe/errors"
	"github.com/bborbe/run"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

const (
	// heartbeat48hWatchJobName is the `job=` value on every line this job logs.
	// A probe binds the job's effect to a line by grepping it, so it is a stable
	// identifier rather than a label that happens to read well.
	heartbeat48hWatchJobName = "heartbeat-48h-watch"

	// Heartbeat48hWatchDefaultInterval is the cadence the launchd job ran at,
	// under `StartInterval 60` in com.bborbe.heartbeat-48h-watch.
	Heartbeat48hWatchDefaultInterval = 60 * time.Second

	// heartbeat48hWatchProducerID and the two dedup keys are carried over
	// byte-for-byte from the shell job, and must stay that way: the store
	// suppresses a duplicate push on producer id plus dedup key, so a renamed key
	// re-posts a card the operator has already seen and a renamed producer
	// re-opens the whole history.
	heartbeat48hWatchProducerID      ProducerID     = "heartbeat-48h-watch"
	heartbeat48hWindowClosedDedupKey DedupKey       = "heartbeat-48h-window-closed"
	heartbeat48hTerminalDedupKey     DedupKey       = "heartbeat-48h-watch-terminal"
	heartbeat48hWatchInterruptClass  InterruptClass = "pick"

	// heartbeat48hJobNamePrefix is the only job family the window counts. The
	// prod namespace holds other CronJobs, and a wider filter would let one of
	// them keep the window alive.
	heartbeat48hJobNamePrefix = "sentry-heartbeat-emitter-"

	// heartbeat48hWindow is the length of the clean window SC1 needs, and
	// heartbeat48hMinSuccesses is how many 6 h slots must land inside it. Eight
	// successes in 48 h means every slot ran; one fewer means a slot was missed.
	heartbeat48hWindow       = 48 * time.Hour
	heartbeat48hMinSuccesses = 8

	// The two condition reasons that make a job a FAILURE even when its failed
	// count is zero — a backoff-limited or deadline-exceeded pod can leave the
	// counter at zero while the slot itself never completed.
	heartbeat48hReasonBackoffLimitExceeded = "BackoffLimitExceeded"
	heartbeat48hReasonDeadlineExceeded     = "DeadlineExceeded"

	// The state directory layout is the shell's, unchanged: a live watch's
	// ledger, heartbeat and result.json resume instead of being re-derived.
	heartbeat48hResultFileName    = "result.json"
	heartbeat48hLedgerFileName    = "ledger.txt"
	heartbeat48hHeartbeatFileName = "heartbeat"

	// The statuses are the shell's own vocabulary, carried over byte-for-byte so
	// an operator reading ledger.txt across the port does not have to learn a
	// second set of words for the same six outcomes.
	heartbeat48hStatusDone         = "done"
	heartbeat48hStatusVoidInWindow = "void-in-window"
	heartbeat48hStatusWindowClosed = "window-closed"
	heartbeat48hStatusTerminal     = "terminal"
	heartbeat48hStatusProgress     = "progress"
	heartbeat48hStatusError        = "ERROR"

	// The ledger's `notified` values, also carried over from the shell.
	heartbeat48hNotifiedNotSent = "-"
	heartbeat48hNotifiedSent    = "sent"

	// heartbeat48hNotifyOutputLimit is the shell's `cut -c1-200` on a failed
	// post's response, so one verbose failure cannot push the ledger's later
	// columns off the row.
	heartbeat48hNotifyOutputLimit = 200

	// heartbeat48hEmptyReadMessage is the ledger detail the shell wrote when the
	// cluster read came back empty. Kept verbatim: it is the line an operator
	// greps for when the watch goes quiet without a card.
	heartbeat48hEmptyReadMessage = "kubectl returned nothing"

	// The cluster read's own constants. The namespace, the binary and the
	// kubeconfig file are compile-time values: nothing a cluster response says can
	// redirect the read.
	heartbeat48hKubectlBinary = "kubectl"
	heartbeat48hNamespace     = "prod"
	heartbeat48hKubeconfigEnv = "KUBECONFIG"

	// heartbeat48hReadTimeout bounds one cluster read. It sits under the 60 s
	// interval so a stalled far end costs one cycle instead of pushing the next
	// tick into the first.
	heartbeat48hReadTimeout = 45 * time.Second
)

// heartbeat48hDeadlineInstant is the watch's own expiry, carried over from the
// shell's `DEADLINE_ISO` byte-for-byte. Past it the watch posts one terminal
// card, so silence is never mistaken for "still running".
var heartbeat48hDeadlineInstant = time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)

// heartbeat48hWatchInfof and heartbeat48hWatchErrorf are package variables so a
// spec can capture the per-cycle job lines.
//
// ⚠️ The line is not decoration: the job's effects are a file in the state
// directory and a card in the store, and `job=heartbeat-48h-watch action=scan
// status=<status>` / `action=create card=<id>` is what binds them to the job. A
// spec that had to read glog's file output would be testing glog rather than the
// job.
var (
	heartbeat48hWatchInfof = func(format string, args ...any) {
		glog.Infof(format, args...)
	}
	heartbeat48hWatchErrorf = func(format string, args ...any) {
		glog.Errorf(format, args...)
	}
)

// HeartbeatJobCondition is one entry of a job's `status.conditions`.
type HeartbeatJobCondition struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// HeartbeatJob is one sentry-heartbeat-emitter job as the window reads it: when
// its slot started, whether it succeeded, whether it failed, and why.
type HeartbeatJob struct {
	// Name is the job's metadata.name.
	Name string
	// Start is status.startTime, falling back to metadata.creationTimestamp. The
	// shell's `startTime // creationTimestamp` exists because a job that never
	// started still has a creation time, and dropping it would hide the slot.
	Start time.Time
	// Succeeded is the count of successful completions.
	Succeeded int
	// Failed is the count of failed attempts.
	Failed int
	// Conditions is the job's condition list, read for the two failure reasons a
	// zero counter can hide.
	Conditions []HeartbeatJobCondition
}

// HeartbeatJobLister returns the heartbeat jobs the window counts.
//
// It is an interface so the job can be driven without a cluster: the lister's
// whole subject is a live prod namespace, which a spec must not depend on.
type HeartbeatJobLister interface {
	List(ctx context.Context) ([]HeartbeatJob, error)
}

// NewHeartbeat48hWatchJob tracks the Sentry heartbeat 48 h window every
// interval, until ctx is cancelled.
//
// SC1 needs a >= 48 h window with >= 8 successful emitter completions and zero
// BackoffLimitExceeded / DeadlineExceeded inside it. That is clock-bound and
// cannot be observed from a session, so the window is tracked durably here:
//
//	quiet          -> ledger line only, no notification
//	window closes  -> one card, write result.json, stop reporting
//	any failure    -> the window restarts at the first successful slot after it
//	past deadline  -> one TERMINAL card, so silence is never mistaken for
//	                  "still running"
//
// ⚠️ Two architectural choices are deliberate and must not be reversed without
// re-deriving them:
//
//  1. The TICK MOVES IN-PROCESS; THE CLUSTER READ STAYS A SUBPROCESS. This
//     service holds no cluster client and must not gain one here: a client would
//     mean a kubeconfig, a credential and a connection pool inside a daemon whose
//     only other cluster relationship is none, widening this process's blast
//     radius for one read a minute. `kubectl -n prod get jobs -o json` with an
//     explicit KUBECONFIG is the same read the shell made, and it keeps the
//     credential where it already lives. The tick moved because two lifecycles
//     for one observable is the cost the port exists to remove.
//
//  2. Cards go to THIS process's attention store through its own Push, not to the
//     prod core. The store this service serves is the one the operator's board
//     renders, so a push is the whole delivery — there is no second hop to make.
//
// ⚠️ Every failure is fail-soft: a missed cycle costs one interval of watching
// and loses nothing, so the loop logs it and continues. Taking the process down
// would drop the board over something the board survives.
//
// The interval is a parameter rather than the constant read directly, so a spec
// can drive the tick without waiting a minute for it.
func NewHeartbeat48hWatchJob(
	jobLister HeartbeatJobLister,
	store AttentionStore,
	stateDir string,
	interval time.Duration,
	now libtime.CurrentDateTimeGetter,
) run.Func {
	job := &heartbeat48hWatchJob{
		jobLister: jobLister,
		store:     store,
		stateDir:  stateDir,
		interval:  interval,
		now:       now,
	}
	return job.run
}

type heartbeat48hWatchJob struct {
	jobLister HeartbeatJobLister
	store     AttentionStore
	stateDir  string
	interval  time.Duration
	now       libtime.CurrentDateTimeGetter
}

// run is the loop shape every in-process job in this repo shares: a ticker and
// ctx.Done, with a failed cycle logged and continued rather than returned.
//
// ⚠️ It checks once before the first tick, matching launchd, which ran the tick
// immediately on load. A restart is exactly when the window may have closed
// unwatched, so waiting a whole interval to look would keep the first minute
// after every deploy blind to the condition this job exists to report.
func (j *heartbeat48hWatchJob) run(ctx context.Context) error {
	if ctx.Err() == nil {
		j.cycle(ctx)
	}
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			j.cycle(ctx)
		}
	}
}

// cycle runs one pass: touch the heartbeat, read the cluster, classify, report,
// ledger.
func (j *heartbeat48hWatchJob) cycle(ctx context.Context) {
	now := j.now.Now().Time().UTC()

	// ⚠️ The heartbeat is written BEFORE any post. The store's `heartbeat:<path>`
	// liveness model reads a stale or missing file as a dead producer and would
	// prune the card on its next read, so a push that precedes its own heartbeat
	// leaves a window in which the card is already prunable. A failure to write it
	// is logged and the cycle CONTINUES — unlike the class-D watch, no offset
	// advances here, so there is nothing a later write would move past.
	if err := j.touchHeartbeat(ctx, now); err != nil {
		heartbeat48hWatchErrorf(
			"job=%s action=heartbeat_error error=%v",
			heartbeat48hWatchJobName,
			err,
		)
	}

	jobs, err := j.jobLister.List(ctx)
	if err != nil {
		j.reportReadError(ctx, now, err)
		return
	}

	reading := heartbeat48hReadWindow(jobs, now)
	status := j.status(ctx, reading, now)
	// ⚠️ The cycle line is logged even when nothing changed. It is the evidence
	// the job ran, and a quiet cycle is a fact an absent log line would hide — a
	// probe could not tell "no window yet" from "the loop is dead".
	heartbeat48hWatchInfof(
		"job=%s action=scan status=%s",
		heartbeat48hWatchJobName,
		status,
	)

	notified := heartbeat48hNotifiedNotSent
	switch status {
	case heartbeat48hStatusWindowClosed:
		notified = j.closeWindow(ctx, reading)
	case heartbeat48hStatusTerminal:
		notified = j.terminate(ctx, now, reading)
	default:
		// done, void-in-window and progress are quiet: the ledger line below is
		// the whole record of a cycle that changed nothing.
	}

	j.appendLedger(ctx, now, reading, status, notified)
}

// status classifies one cycle, in the shell's own order.
//
// ⚠️ The order is load-bearing, not incidental:
//
//   - result.json first, so a window already reported never reports twice.
//   - A trailing-48 h failure with no success after it is a VOID window, not a
//     closed one: once a success exists the window restarts AFTER that failure by
//     construction, so a failure inside the last 48 h is history rather than a
//     broken window.
//   - A closed window outranks the deadline, so a window that closed in time is
//     reported as closed even on the tick after the deadline passed.
func (j *heartbeat48hWatchJob) status(
	ctx context.Context,
	reading heartbeat48hWindowReading,
	now time.Time,
) string {
	if j.resultExists(ctx) {
		return heartbeat48hStatusDone
	}
	if reading.Failed48h > 0 && reading.WindowStart.IsZero() {
		return heartbeat48hStatusVoidInWindow
	}
	if reading.SuccessesAfter >= heartbeat48hMinSuccesses && !now.Before(reading.WindowClose()) {
		return heartbeat48hStatusWindowClosed
	}
	if now.After(heartbeat48hDeadlineInstant) {
		return heartbeat48hStatusTerminal
	}
	return heartbeat48hStatusProgress
}

// closeWindow writes result.json and posts the one closed-window card.
//
// ⚠️ result.json is written BEFORE the post, exactly as the shell did. It is the
// marker that silences every later cycle; writing it after a successful post
// would let a post that landed but whose write did not repeat the card forever.
// The converse — a write that lands and a post that does not — is accepted
// deliberately: the shell made the same trade, and a missing card beside a
// recorded result is visible in the ledger's `notified` column.
func (j *heartbeat48hWatchJob) closeWindow(
	ctx context.Context,
	reading heartbeat48hWindowReading,
) string {
	windowStart := heartbeat48hFormatInstant(reading.WindowStart)
	windowClose := heartbeat48hFormatInstant(reading.WindowClose())
	j.writeResult(ctx, heartbeat48hWindowClosedResult{
		WindowStart: windowStart,
		WindowClose: windowClose,
		Successes:   reading.SuccessesAfter,
		FailedTotal: reading.FailedTotal,
	})
	return j.post(
		ctx,
		heartbeat48hWindowClosedDedupKey,
		"Sentry heartbeat 48 h window CLOSED CLEAN — "+
			strconv.Itoa(reading.SuccessesAfter)+
			" successful emitter completions, "+
			strconv.Itoa(reading.FailedTotal)+
			" failures total, window "+windowStart+" -> "+windowClose+
			". SC1's Prometheus read is the remaining check; reopen the task.",
	)
}

// terminate writes result.json and posts the one terminal card.
//
// The result is written first for the reason closeWindow documents.
func (j *heartbeat48hWatchJob) terminate(
	ctx context.Context,
	now time.Time,
	reading heartbeat48hWindowReading,
) string {
	timestamp := heartbeat48hFormatInstant(now)
	j.writeResult(ctx, heartbeat48hTerminalResult{
		Terminal:    timestamp,
		Successes:   reading.SuccessesAfter,
		FailedTotal: reading.FailedTotal,
	})
	return j.post(
		ctx,
		heartbeat48hTerminalDedupKey,
		"Sentry heartbeat 48 h watch TERMINAL at "+timestamp+
			": no clean 48 h window before "+
			heartbeat48hFormatInstant(heartbeat48hDeadlineInstant)+
			". "+strconv.Itoa(reading.SuccessesAfter)+
			" successes since the newest failure, "+
			strconv.Itoa(reading.FailedTotal)+
			" failures total. The emitter is not holding; re-derive from the "+
			"emitter task rather than re-arming this watch.",
	)
}

// post pushes one card and reports the ledger's `notified` value.
func (j *heartbeat48hWatchJob) post(ctx context.Context, dedupKey DedupKey, payload string) string {
	item, err := j.store.Push(ctx, PushRequest{
		ProducerID:      heartbeat48hWatchProducerID,
		ProducerKind:    SessionProducerKind,
		LivenessRef:     LivenessRef("heartbeat:" + j.heartbeatPath()),
		DedupKey:        dedupKey,
		InterruptClass:  heartbeat48hWatchInterruptClass,
		Payload:         Payload(payload),
		AnswerMechanism: MessageAnswerMechanism,
	})
	if err != nil {
		heartbeat48hWatchErrorf(
			"job=%s action=create_error dedup_key=%s error=%v",
			heartbeat48hWatchJobName,
			dedupKey,
			err,
		)
		return "FAILED: " + heartbeat48hOneLine(err.Error(), heartbeat48hNotifyOutputLimit)
	}
	if item == nil {
		heartbeat48hWatchErrorf(
			"job=%s action=create_error dedup_key=%s error=store returned no item",
			heartbeat48hWatchJobName,
			dedupKey,
		)
		return "FAILED: store returned no item"
	}
	heartbeat48hWatchInfof(
		"job=%s action=create card=%s dedup_key=%s",
		heartbeat48hWatchJobName,
		item.ItemID,
		dedupKey,
	)
	return heartbeat48hNotifiedSent
}

// reportReadError records a cluster read that produced nothing.
//
// ⚠️ One ledger line and NO card, matching the shell: a card per failed read
// would fire every minute for a cluster the operator cannot fix from a card, and
// the ledger is where a read outage is legible after the fact.
func (j *heartbeat48hWatchJob) reportReadError(ctx context.Context, now time.Time, err error) {
	heartbeat48hWatchInfof(
		"job=%s action=scan status=%s",
		heartbeat48hWatchJobName,
		heartbeat48hStatusError,
	)
	heartbeat48hWatchErrorf(
		"job=%s action=scan_error error=%v",
		heartbeat48hWatchJobName,
		err,
	)
	j.appendLine(
		ctx,
		heartbeat48hFormatInstant(now)+"\t"+heartbeat48hStatusError+"\t"+
			heartbeat48hOneLine(err.Error(), heartbeat48hNotifyOutputLimit)+"\n",
	)
}

// appendLedger appends one line per cycle.
//
// ⚠️ A failure here is logged and the cycle still ends: the card is the signal
// and the ledger is the evidence, so losing the second must not cost the
// operator the first.
func (j *heartbeat48hWatchJob) appendLedger(
	ctx context.Context,
	now time.Time,
	reading heartbeat48hWindowReading,
	status string,
	notified string,
) {
	line := heartbeat48hFormatInstant(now) + "\t" + status +
		"\tfailed_total=" + strconv.Itoa(reading.FailedTotal) +
		"\tfailed_48h=" + strconv.Itoa(reading.Failed48h) +
		"\tsuccesses_after=" + strconv.Itoa(reading.SuccessesAfter) +
		"\twindow_start=" + heartbeat48hFormatInstant(reading.WindowStart) +
		"\tclose=" + heartbeat48hFormatInstant(reading.WindowClose()) +
		"\tlast_success=" + heartbeat48hFormatInstant(reading.LastSuccess) +
		"\t" + notified + "\n"
	j.appendLine(ctx, line)
}

// appendLine appends one already-rendered line to the ledger.
func (j *heartbeat48hWatchJob) appendLine(ctx context.Context, line string) {
	if err := os.MkdirAll(j.stateDir, 0o750); err != nil {
		heartbeat48hWatchErrorf(
			"job=%s action=ledger_error error=%v",
			heartbeat48hWatchJobName,
			errors.Wrapf(ctx, err, "create state dir '%s' failed", j.stateDir),
		)
		return
	}
	path := j.ledgerPath()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		heartbeat48hWatchErrorf(
			"job=%s action=ledger_error error=%v",
			heartbeat48hWatchJobName,
			errors.Wrapf(ctx, err, "open ledger '%s' failed", path),
		)
		return
	}
	defer file.Close()
	if _, err := file.WriteString(line); err != nil {
		heartbeat48hWatchErrorf(
			"job=%s action=ledger_error error=%v",
			heartbeat48hWatchJobName,
			errors.Wrapf(ctx, err, "append to ledger '%s' failed", path),
		)
	}
}

// touchHeartbeat writes the heartbeat file the card's liveness ref names,
// creating the state directory if needed.
//
// The content mirrors the shell's `<epoch> <rfc3339>` line, but the file's MTIME
// is what the store reads — the content is for a human looking at the file.
func (j *heartbeat48hWatchJob) touchHeartbeat(ctx context.Context, now time.Time) error {
	if err := os.MkdirAll(j.stateDir, 0o750); err != nil {
		return errors.Wrapf(ctx, err, "create state dir '%s' failed", j.stateDir)
	}
	content := strconv.FormatInt(now.Unix(), 10) + " " + now.Format(time.RFC3339) + "\n"
	path := j.heartbeatPath()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return errors.Wrapf(ctx, err, "write heartbeat '%s' failed", path)
	}
	return nil
}

// writeResult records the verdict that silences every later cycle.
//
// ⚠️ A failure here is logged and the card still goes out. The marker is what
// stops a repeat, not what makes the report — and a report the operator never
// sees is the worse of the two failures.
func (j *heartbeat48hWatchJob) writeResult(ctx context.Context, result any) {
	content, err := json.Marshal(result)
	if err != nil {
		heartbeat48hWatchErrorf(
			"job=%s action=result_error error=%v",
			heartbeat48hWatchJobName,
			errors.Wrapf(ctx, err, "marshal result failed"),
		)
		return
	}
	if err := os.MkdirAll(j.stateDir, 0o750); err != nil {
		heartbeat48hWatchErrorf(
			"job=%s action=result_error error=%v",
			heartbeat48hWatchJobName,
			errors.Wrapf(ctx, err, "create state dir '%s' failed", j.stateDir),
		)
		return
	}
	path := j.resultPath()
	if err := os.WriteFile(path, append(content, '\n'), 0o600); err != nil {
		heartbeat48hWatchErrorf(
			"job=%s action=result_error error=%v",
			heartbeat48hWatchJobName,
			errors.Wrapf(ctx, err, "write result '%s' failed", path),
		)
	}
}

// resultExists reports whether a window has already been reported.
//
// ⚠️ An unreadable result file reads as absent, matching the shell's `[ -f ]`,
// and the error is logged rather than swallowed: reporting a second time is the
// safe direction, but a permissions problem on the state directory is worth
// seeing.
func (j *heartbeat48hWatchJob) resultExists(ctx context.Context) bool {
	_, err := os.Stat(j.resultPath())
	if err == nil {
		return true
	}
	if !os.IsNotExist(err) {
		heartbeat48hWatchErrorf(
			"job=%s action=result_error error=%v",
			heartbeat48hWatchJobName,
			errors.Wrapf(ctx, err, "stat result '%s' failed", j.resultPath()),
		)
	}
	return false
}

// heartbeat48hWindowClosedResult is the result.json a closed window writes.
//
// ⚠️ The field order and names are the shell's own JSON, so a reader of the file
// cannot tell the port happened.
type heartbeat48hWindowClosedResult struct {
	WindowStart string `json:"window_start"`
	WindowClose string `json:"window_close"`
	Successes   int    `json:"successes"`
	FailedTotal int    `json:"failed_total"`
}

// heartbeat48hTerminalResult is the result.json a terminal watch writes.
type heartbeat48hTerminalResult struct {
	Terminal    string `json:"terminal"`
	Successes   int    `json:"successes"`
	FailedTotal int    `json:"failed_total"`
}

// heartbeat48hWindowReading is what one read of the job list says about the
// window.
type heartbeat48hWindowReading struct {
	// FailedTotal counts every failed emitter job, however old.
	FailedTotal int
	// Failed48h counts the failed emitter jobs inside the trailing 48 h. A
	// non-zero value with no running window is the void case.
	Failed48h int
	// SuccessesAfter counts the successful slots that follow the newest failure.
	// The window restarts after a failure by construction, so this is the count
	// that decides whether 48 h ran clean.
	SuccessesAfter int
	// WindowStart is the earliest success after the newest failure — the instant
	// the current window opened. Zero when no window is running.
	WindowStart time.Time
	// LastSuccess is the newest successful slot of any age, for the ledger.
	LastSuccess time.Time
}

// WindowClose is the instant the current window closes, or the zero time when no
// window is running.
//
// ⚠️ The zero case is not a degenerate close of `windowStart + 48h`: the shell
// printed `-` for it, and a reader must not see a 1970 close where no window
// exists.
func (r heartbeat48hWindowReading) WindowClose() time.Time {
	if r.WindowStart.IsZero() {
		return time.Time{}
	}
	return r.WindowStart.Add(heartbeat48hWindow)
}

// heartbeat48hReadWindow computes the reading from the job list.
//
// ⚠️ The newest failure is a MAX over the failed jobs and the window start a MIN
// over the successes after it; the shell sorted the list first and then took
// `last` / `min`, which is the same pair of answers without an ordering step
// whose tie-breaking nothing depends on.
func heartbeat48hReadWindow(jobs []HeartbeatJob, now time.Time) heartbeat48hWindowReading {
	reading := heartbeat48hWindowReading{}
	newestFailure := time.Time{}
	for _, job := range jobs {
		if !heartbeat48hJobFailed(job) {
			continue
		}
		reading.FailedTotal++
		if job.Start.After(now.Add(-heartbeat48hWindow)) {
			reading.Failed48h++
		}
		if job.Start.After(newestFailure) {
			newestFailure = job.Start
		}
	}
	for _, job := range jobs {
		if job.Succeeded <= 0 {
			continue
		}
		if job.Start.After(reading.LastSuccess) {
			reading.LastSuccess = job.Start
		}
		if !job.Start.After(newestFailure) {
			continue
		}
		reading.SuccessesAfter++
		if reading.WindowStart.IsZero() || job.Start.Before(reading.WindowStart) {
			reading.WindowStart = job.Start
		}
	}
	return reading
}

// heartbeat48hJobFailed reports whether one job is a failure by the shell's
// rule: a non-zero failed count, or a condition that says the slot was
// backoff-limited or deadline-exceeded.
//
// ⚠️ The condition half is not redundant. A pod that never got to run leaves
// `failed` at zero while its condition names exactly why, so a watch keyed on
// the counter alone would call that slot clean.
func heartbeat48hJobFailed(job HeartbeatJob) bool {
	if job.Failed > 0 {
		return true
	}
	for _, condition := range job.Conditions {
		if condition.Status != "True" {
			continue
		}
		if condition.Reason == heartbeat48hReasonBackoffLimitExceeded ||
			condition.Reason == heartbeat48hReasonDeadlineExceeded {
			return true
		}
	}
	return false
}

// heartbeat48hFormatInstant renders an instant the way the shell's `iso` did:
// UTC to the second, and `-` for the zero time.
func heartbeat48hFormatInstant(instant time.Time) string {
	if instant.IsZero() {
		return "-"
	}
	return instant.UTC().Format("2006-01-02T15:04:05Z")
}

// heartbeat48hOneLine collapses value onto one line and bounds it to limit
// characters.
//
// ⚠️ Trailing newlines are trimmed FIRST, mirroring the shell's `$(...)` command
// substitution: without it every ledger row would end in a stray space. The
// bound counts characters rather than bytes so a multi-byte response cannot be
// cut mid-rune.
func heartbeat48hOneLine(value string, limit int) string {
	oneLine := strings.ReplaceAll(strings.TrimRight(value, "\r\n"), "\r\n", "\n")
	oneLine = strings.ReplaceAll(oneLine, "\n", " ")
	runes := []rune(oneLine)
	if len(runes) <= limit {
		return oneLine
	}
	return string(runes[:limit])
}

// resultPath is the marker that silences every later cycle.
func (j *heartbeat48hWatchJob) resultPath() string {
	return filepath.Join(j.stateDir, heartbeat48hResultFileName)
}

// ledgerPath is the per-cycle evidence trail.
func (j *heartbeat48hWatchJob) ledgerPath() string {
	return filepath.Join(j.stateDir, heartbeat48hLedgerFileName)
}

// heartbeatPath is the file the card's liveness ref names.
func (j *heartbeat48hWatchJob) heartbeatPath() string {
	return filepath.Join(j.stateDir, heartbeat48hHeartbeatFileName)
}

// NewKubectlHeartbeatJobLister builds the lister over the prod namespace.
//
// ⚠️ The cluster read STAYS A SUBPROCESS, and this is the decision the job's own
// doc comment explains at length: this service has no cluster client and must not
// gain one for a single read a minute. The kubeconfig path is resolved once, by
// the caller, and passed in — so nothing a cluster response says can redirect the
// read.
func NewKubectlHeartbeatJobLister(kubeconfigPath string) HeartbeatJobLister {
	return &kubectlHeartbeatJobLister{kubeconfigPath: kubeconfigPath}
}

type kubectlHeartbeatJobLister struct {
	kubeconfigPath string
}

// List runs `kubectl -n prod get jobs -o json` once and parses it.
//
// ⚠️ An empty read is reported as the shell's own sentence rather than as a
// wrapped exec error. The ledger line it produces is the one an operator greps
// for when the watch goes quiet, and the shell's `2>/dev/null` discarded the exit
// status the same way: a kubectl that failed and a namespace that answered
// nothing are the same fact to this watch — the read did not happen.
func (l *kubectlHeartbeatJobLister) List(ctx context.Context) ([]HeartbeatJob, error) {
	commandCtx, cancel := context.WithTimeout(ctx, heartbeat48hReadTimeout)
	defer cancel()
	// #nosec G204 -- the binary, the namespace and the kubeconfig path are
	// compile-time values resolved by the default constructor; no caller and no
	// cluster response reaches the command line.
	cmd := exec.CommandContext(
		commandCtx,
		heartbeat48hKubectlBinary,
		"-n",
		heartbeat48hNamespace,
		"get",
		"jobs",
		"-o",
		"json",
	)
	cmd.Env = append(os.Environ(), heartbeat48hKubeconfigEnv+"="+l.kubeconfigPath)
	var output bytes.Buffer
	cmd.Stdout = &output
	// Stderr stays nil, so it is discarded: the shell sent it to /dev/null, and
	// an unparseable read is reported by the empty-output branch below.
	err := cmd.Run()
	if strings.TrimSpace(output.String()) == "" {
		return nil, errors.Errorf(ctx, heartbeat48hEmptyReadMessage)
	}
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "read prod jobs failed")
	}
	return parseHeartbeatJobs(ctx, output.Bytes())
}

// heartbeat48hJobList is the shape of `kubectl get jobs -o json` this watch
// reads.
type heartbeat48hJobList struct {
	Items []heartbeat48hJobItem `json:"items"`
}

type heartbeat48hJobItem struct {
	Metadata heartbeat48hJobMetadata `json:"metadata"`
	Status   heartbeat48hJobStatus   `json:"status"`
}

type heartbeat48hJobMetadata struct {
	Name              string `json:"name"`
	CreationTimestamp string `json:"creationTimestamp"`
}

type heartbeat48hJobStatus struct {
	StartTime  string                  `json:"startTime"`
	Succeeded  int                     `json:"succeeded"`
	Failed     int                     `json:"failed"`
	Conditions []HeartbeatJobCondition `json:"conditions"`
}

// parseHeartbeatJobs keeps the emitter jobs and drops everything else.
//
// ⚠️ A job whose slot time cannot be read is SKIPPED rather than failing the
// read. Every emitter job carries a start time, so the case only arises for a job
// that is mid-creation, and turning that into a read error would make one
// half-born job blank the whole window — the opposite of the fail-soft rule this
// job runs under.
func parseHeartbeatJobs(ctx context.Context, data []byte) ([]HeartbeatJob, error) {
	var list heartbeat48hJobList
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, errors.Wrapf(ctx, err, "decode kubectl jobs failed")
	}
	jobs := make([]HeartbeatJob, 0, len(list.Items))
	for _, item := range list.Items {
		if !strings.HasPrefix(item.Metadata.Name, heartbeat48hJobNamePrefix) {
			continue
		}
		start, ok := heartbeat48hJobStart(item)
		if !ok {
			continue
		}
		jobs = append(jobs, HeartbeatJob{
			Name:       item.Metadata.Name,
			Start:      start,
			Succeeded:  item.Status.Succeeded,
			Failed:     item.Status.Failed,
			Conditions: item.Status.Conditions,
		})
	}
	return jobs, nil
}

// heartbeat48hJobStart resolves a job's slot time, preferring the status's start
// time and falling back to the creation timestamp.
func heartbeat48hJobStart(item heartbeat48hJobItem) (time.Time, bool) {
	for _, value := range []string{item.Status.StartTime, item.Metadata.CreationTimestamp} {
		if value == "" {
			continue
		}
		start, err := time.Parse(time.RFC3339, value)
		if err != nil {
			continue
		}
		return start, true
	}
	return time.Time{}, false
}

// NewDefaultHeartbeat48hWatchJob builds the job against the paths the launchd
// job used: the prod kubeconfig and the state directory.
//
// ⚠️ Resolving them here rather than in main keeps the job's paths beside the
// job's semantics, and returns an error instead of a job with an unresolvable
// home directory — a watch armed on a guessed path would read nothing and look
// exactly like a quiet one.
func NewDefaultHeartbeat48hWatchJob(ctx context.Context, store AttentionStore) (run.Func, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, errors.Wrap(ctx, err, "resolve home dir failed")
	}
	return NewHeartbeat48hWatchJob(
		NewKubectlHeartbeatJobLister(filepath.Join(home, ".kube", "nuke-prod")),
		store,
		filepath.Join(home, ".claude", "state", "heartbeat-48h-watch"),
		Heartbeat48hWatchDefaultInterval,
		libtime.NewCurrentDateTime(),
	), nil
}
