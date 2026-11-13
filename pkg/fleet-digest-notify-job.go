// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bytes"
	"context"
	"encoding/json"
	stderrors "errors"
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
	// fleetDigestNotifyJobName is the `job=` value on every line this job logs.
	// A probe binds the job's effect to a line by grepping it, so it is a stable
	// identifier rather than a label that happens to read well.
	fleetDigestNotifyJobName = "fleet-digest-notify"

	// FleetDigestNotifyDefaultInterval is the cadence the launchd job ran at,
	// under `StartInterval 60` in com.bborbe.fleet-digest-notify.
	FleetDigestNotifyDefaultInterval = 60 * time.Second

	// The two exit codes fleet-digest.py --check uses to report a RESULT rather
	// than a failure: 0 is a healthy round, 10 is a lapsed one. Every other code
	// means the check itself did not complete.
	fleetDigestCheckHealthyCode = 0
	fleetDigestCheckLapsedCode  = 10

	// fleetDigestExecFailureCode is the code recorded when the check could not be
	// launched at all. ⚠️ It is 127 because that is what bash reported for the
	// same failure — a `python3` that never started — so a streak that began
	// under the shell job continues under this one instead of looking like a new
	// kind of failure.
	fleetDigestExecFailureCode = 127

	// The payload and ledger bounds mirror the shell's `cut -c` calls, and they
	// are applied per field rather than to the finished line: a long check output
	// must not push the ledger's rc and notified columns off the line.
	fleetDigestLapseMessageLimit   = 400
	fleetDigestFailureMessageLimit = 300
	fleetDigestLedgerOutputLimit   = 300
	fleetDigestAlertOutputLimit    = 300
	fleetDigestNotifyOutputLimit   = 200

	// The three names inside the state directory, unchanged from the shell job.
	// ⚠️ The layout is kept rather than redesigned: `alert` is the dead-man's
	// switch fleet-digest.py --stale reads, and a renamed file would silently
	// disable it.
	fleetDigestLedgerFileName = "ledger.txt"
	fleetDigestStreakFileName = "failed-streak"
	fleetDigestAlertFileName  = "alert"

	// The ledger's `notified` values, carried over byte-for-byte from the shell:
	// an operator reading ledger.txt across the port must not have to learn a
	// second vocabulary for the same three outcomes.
	fleetDigestNotifyNotSent    = "-"
	fleetDigestNotifySent       = "sent"
	fleetDigestNotifySuppressed = "suppressed: FAILED streak continues (alerted once)"

	// The prod notification surface the shell published to. The path and the
	// environment are compile-time constants: neither is derived from a check's
	// output, so nothing a stale digest says can redirect a notification.
	fleetDigestPublishEnvironment = "prod"
	fleetDigestPublishPath        = "/api/1.0/command/core-notification-v1/notification-publish?sync=true"

	// fleetDigestPython is the interpreter the shell job used, named explicitly
	// rather than resolved from PATH so a login shell's pyenv cannot change which
	// python runs the engine.
	fleetDigestPython = "/usr/bin/python3"

	// fleetDigestCheckTimeout bounds one engine run and fleetDigestPublishTimeout
	// one publish. Both sit under the 60 s interval so a stalled far end costs one
	// cycle instead of pushing the next tick into the first.
	fleetDigestCheckTimeout   = 45 * time.Second
	fleetDigestPublishTimeout = 60 * time.Second
)

// fleetDigestNotifyInfof and fleetDigestNotifyErrorf are package variables so a
// spec can capture the per-cycle job lines.
//
// ⚠️ The line is not decoration: the job's effects are a file in the state
// directory and a notification posted to a remote service that no in-process
// assertion can read back, so `job=fleet-digest-notify action=check rc=<n>` and
// `action=publish rc=<n>` are what bind those effects to the job. A spec that
// had to read glog's file output would be testing glog rather than the job.
var (
	fleetDigestNotifyInfof = func(format string, args ...any) {
		glog.Infof(format, args...)
	}
	fleetDigestNotifyErrorf = func(format string, args ...any) {
		glog.Errorf(format, args...)
	}
)

// FleetDigestCheckResult is one run of the staleness engine: its exit code and
// its combined output.
//
// ⚠️ ExitCode is part of the RESULT, not an error. `--check` answers 0 / 10 as
// verdicts, and folding 10 into an error would erase the difference between "the
// fleet round lapsed" and "the check could not run".
type FleetDigestCheckResult struct {
	// ExitCode is the engine's exit status: 0 healthy, 10 lapsed, anything else
	// a check that did not complete.
	ExitCode int
	// Output is stdout and stderr interleaved, as the shell's `2>&1` captured it.
	Output string
}

// FleetDigestChecker runs the staleness engine once.
//
// It is an interface so the job can be driven without an interpreter on disk:
// the engine's subject is another repository's script and a fleet snapshot, and
// a spec must depend on neither.
type FleetDigestChecker interface {
	Check(ctx context.Context) (FleetDigestCheckResult, error)
}

// FleetDigestNotifier publishes one agent-escalation notification.
//
// It is an interface for the same reason, and one more: the real notifier holds
// the prod write path, so a spec that exercised it would post to a live service.
type FleetDigestNotifier interface {
	Publish(ctx context.Context, message string) error
}

// NewFleetDigestNotifyJob runs the fleet-digest notify tick every interval,
// until ctx is cancelled.
//
// ⚠️ Two architectural choices are deliberate and must not be reversed without
// re-deriving them:
//
//  1. The STALENESS ENGINE STAYS AN EXTERNAL COMMAND. `fleet-digest.py --check`
//     carries baseline carry-forward and lapse-interval semantics — the rules
//     that decide when a fleet round counts as lapsed — and reimplementing them
//     here would be a second copy of a decision that already has one owner. Two
//     copies drift, and the drift is silent: the job would keep reporting a
//     verdict computed by rules nobody updated. The tick moves in-process; the
//     rule does not.
//
//  2. The PUBLISH SHELLS OUT, so NO CREDENTIAL ENTERS THIS SERVICE. The
//     notification is posted through `trading-api-write.sh`, which resolves its
//     own token; this service holds none, logs none and cannot leak one. That
//     keeps a prod credential out of a long-running daemon's environment, its
//     argv and its error paths, which is a strictly smaller blast radius than
//     teaching this binary to authenticate against the core.
//
// ⚠️ Every failure is fail-soft: a missed cycle costs one interval of fleet
// watching and loses nothing, so the loop logs it and continues. Taking the
// process down would drop the board over something the board survives.
//
// The interval is a parameter rather than the constant read directly, so a spec
// can drive the tick without waiting a minute for it.
func NewFleetDigestNotifyJob(
	check FleetDigestChecker,
	notifier FleetDigestNotifier,
	stateDir string,
	interval time.Duration,
	now libtime.CurrentDateTimeGetter,
) run.Func {
	job := &fleetDigestNotifyJob{
		check:    check,
		notifier: notifier,
		stateDir: stateDir,
		interval: interval,
		now:      now,
	}
	return job.run
}

type fleetDigestNotifyJob struct {
	check    FleetDigestChecker
	notifier FleetDigestNotifier
	stateDir string
	interval time.Duration
	now      libtime.CurrentDateTimeGetter
}

// run is the loop shape every in-process job in this repo shares: a ticker and
// ctx.Done, with a failed cycle logged and continued rather than returned.
//
// ⚠️ It checks once before the first tick, unlike the metrics push. A lapsed
// fleet round is the state a restart leaves behind — nothing about a restart
// makes the digest current — so waiting a whole interval to look would keep the
// first minute after every deploy blind to the exact condition this job exists
// to report.
func (j *fleetDigestNotifyJob) run(ctx context.Context) error {
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

// cycle runs one pass: check, notify, ledger.
func (j *fleetDigestNotifyJob) cycle(ctx context.Context) {
	timestamp := j.now.Now().Time().UTC().Format(time.RFC3339)

	result, err := j.check.Check(ctx)
	if err != nil {
		// ⚠️ A check that could not be launched is a FAILED check, not a quiet
		// one, and it is folded into the failure branch with the code bash
		// reported: a job that stayed silent here would be indistinguishable from
		// a healthy round at the exact moment nothing is being watched.
		fleetDigestNotifyErrorf(
			"job=%s action=check_error error=%v",
			fleetDigestNotifyJobName,
			err,
		)
		result = FleetDigestCheckResult{
			ExitCode: fleetDigestExecFailureCode,
			Output:   err.Error(),
		}
	}
	// ⚠️ The cycle line is logged even when the round is healthy. It is the
	// evidence the job ran, and a quiet cycle is a fact an absent log line would
	// hide — a probe could not tell "nothing lapsed" from "the loop is dead".
	fleetDigestNotifyInfof(
		"job=%s action=check rc=%d",
		fleetDigestNotifyJobName,
		result.ExitCode,
	)

	notified := fleetDigestNotifyNotSent
	switch result.ExitCode {
	case fleetDigestCheckHealthyCode:
		j.clearStreak(ctx)
	case fleetDigestCheckLapsedCode:
		// Dedup for a lapse is the engine's own (notified.json): one lapse yields
		// one exit 10, so there is nothing to suppress here — the streak is
		// cleared and the lapse reported.
		j.clearStreak(ctx)
		notified = j.publish(ctx, timestamp, "Fleet round lapsed — "+fleetDigestOneLine(
			result.Output,
			" ",
			fleetDigestLapseMessageLimit,
		))
	default:
		notified = j.publishFailureStreak(ctx, timestamp, result)
	}

	j.appendLedger(ctx, timestamp, result, notified)
}

// publishFailureStreak publishes one alert per failure streak, never one per
// tick.
//
// ⚠️ The streak file is created even when the publish failed, matching the
// shell: the streak records that this failure has been attempted, and the
// `alert` file — not the streak — is what reports a publish that did not land.
func (j *fleetDigestNotifyJob) publishFailureStreak(
	ctx context.Context,
	timestamp string,
	result FleetDigestCheckResult,
) string {
	if j.streakExists(ctx) {
		fleetDigestNotifyInfof(
			"job=%s action=publish_suppressed rc=%d",
			fleetDigestNotifyJobName,
			result.ExitCode,
		)
		return fleetDigestNotifySuppressed
	}
	notified := j.publish(ctx, timestamp, "Fleet digest watcher FAILED (rc="+
		strconv.Itoa(result.ExitCode)+
		") — the check did not complete: "+fleetDigestOneLine(
		result.Output,
		" ",
		fleetDigestFailureMessageLimit,
	))
	j.createStreak(ctx)
	return notified
}

// publish sends one agent-escalation and reports the ledger's `notified` value.
//
// On success it clears the dead-man's switch; on failure it writes it, so a
// failed publish is reported by `--stale` instead of vanishing.
func (j *fleetDigestNotifyJob) publish(
	ctx context.Context,
	timestamp string,
	message string,
) string {
	if err := j.notifier.Publish(ctx, message); err != nil {
		code, output := fleetDigestPublishFailure(err)
		j.writeAlert(ctx, timestamp, code, output)
		fleetDigestNotifyErrorf(
			"job=%s action=publish rc=%d error=%v",
			fleetDigestNotifyJobName,
			code,
			err,
		)
		return "FAILED(rc=" + strconv.Itoa(code) + "): " + fleetDigestOneLine(
			output,
			" ",
			fleetDigestNotifyOutputLimit,
		)
	}
	j.clearAlert(ctx)
	// ⚠️ Logged only for a publish that actually happened, and with the exit code
	// rather than the message: the message names a fleet state, and the line's
	// job is to prove the effect occurred.
	fleetDigestNotifyInfof(
		"job=%s action=publish rc=%d",
		fleetDigestNotifyJobName,
		fleetDigestCheckHealthyCode,
	)
	return fleetDigestNotifySent
}

// appendLedger appends one line per cycle.
//
// ⚠️ A failure here is logged and the cycle still ends: the notification is the
// signal and the ledger is the evidence, so losing the second must not cost the
// operator the first.
func (j *fleetDigestNotifyJob) appendLedger(
	ctx context.Context,
	timestamp string,
	result FleetDigestCheckResult,
	notified string,
) {
	if err := os.MkdirAll(j.stateDir, 0o750); err != nil {
		fleetDigestNotifyErrorf(
			"job=%s action=ledger_error error=%v",
			fleetDigestNotifyJobName,
			err,
		)
		return
	}
	// The shell used `;` here and ` ` in the messages: the ledger is a
	// tab-separated record, so a newline inside the output must not split a row,
	// while a message is prose a human reads.
	line := timestamp + "\t" + strconv.Itoa(result.ExitCode) + "\t" +
		fleetDigestOneLine(result.Output, ";", fleetDigestLedgerOutputLimit) + "\t" +
		notified + "\n"
	path := j.ledgerPath()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fleetDigestNotifyErrorf(
			"job=%s action=ledger_error error=%v",
			fleetDigestNotifyJobName,
			errors.Wrapf(ctx, err, "open ledger '%s' failed", path),
		)
		return
	}
	defer file.Close()
	if _, err := file.WriteString(line); err != nil {
		fleetDigestNotifyErrorf(
			"job=%s action=ledger_error error=%v",
			fleetDigestNotifyJobName,
			errors.Wrapf(ctx, err, "append to ledger '%s' failed", path),
		)
	}
}

// writeAlert records a publish that did not land.
//
// ⚠️ This file IS the dead-man's switch: fleet-digest.py --stale reads it and
// reports it, which is how a notification that never left this host becomes
// visible instead of silent.
func (j *fleetDigestNotifyJob) writeAlert(
	ctx context.Context,
	timestamp string,
	code int,
	output string,
) {
	if err := os.MkdirAll(j.stateDir, 0o750); err != nil {
		fleetDigestNotifyErrorf(
			"job=%s action=alert_error error=%v",
			fleetDigestNotifyJobName,
			errors.Wrapf(ctx, err, "create state dir '%s' failed", j.stateDir),
		)
		return
	}
	content := timestamp + "\trc=" + strconv.Itoa(code) + "\t" +
		fleetDigestOneLine(output, " ", fleetDigestAlertOutputLimit) + "\n"
	path := j.alertPath()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		fleetDigestNotifyErrorf(
			"job=%s action=alert_error error=%v",
			fleetDigestNotifyJobName,
			errors.Wrapf(ctx, err, "write alert '%s' failed", path),
		)
	}
}

// clearAlert removes the dead-man's switch after a publish that landed.
func (j *fleetDigestNotifyJob) clearAlert(ctx context.Context) {
	j.removeStateFile(ctx, j.alertPath())
}

// clearStreak removes the failure-streak marker.
func (j *fleetDigestNotifyJob) clearStreak(ctx context.Context) {
	j.removeStateFile(ctx, j.streakPath())
}

// removeStateFile removes one state file, treating an absent file as done.
func (j *fleetDigestNotifyJob) removeStateFile(ctx context.Context, path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		fleetDigestNotifyErrorf(
			"job=%s action=state_error error=%v",
			fleetDigestNotifyJobName,
			errors.Wrapf(ctx, err, "remove state file '%s' failed", path),
		)
	}
}

// createStreak writes the empty failure-streak marker, mirroring the shell's
// `: > "$STREAK"`.
func (j *fleetDigestNotifyJob) createStreak(ctx context.Context) {
	if err := os.MkdirAll(j.stateDir, 0o750); err != nil {
		fleetDigestNotifyErrorf(
			"job=%s action=streak_error error=%v",
			fleetDigestNotifyJobName,
			errors.Wrapf(ctx, err, "create state dir '%s' failed", j.stateDir),
		)
		return
	}
	path := j.streakPath()
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		fleetDigestNotifyErrorf(
			"job=%s action=streak_error error=%v",
			fleetDigestNotifyJobName,
			errors.Wrapf(ctx, err, "write streak '%s' failed", path),
		)
	}
}

// streakExists reports whether this failure streak has already been alerted.
//
// ⚠️ An unreadable streak file reads as absent, matching the shell's `[ -e ]`,
// and the error is logged rather than swallowed: publishing a second alert is
// the safe direction, but a permissions problem on the state directory is worth
// seeing.
func (j *fleetDigestNotifyJob) streakExists(ctx context.Context) bool {
	_, err := os.Stat(j.streakPath())
	if err == nil {
		return true
	}
	if !os.IsNotExist(err) {
		fleetDigestNotifyErrorf(
			"job=%s action=streak_error error=%v",
			fleetDigestNotifyJobName,
			errors.Wrapf(ctx, err, "stat streak '%s' failed", j.streakPath()),
		)
	}
	return false
}

// ledgerPath is the per-cycle evidence trail.
func (j *fleetDigestNotifyJob) ledgerPath() string {
	return filepath.Join(j.stateDir, fleetDigestLedgerFileName)
}

// streakPath is the marker that suppresses a second alert for one streak.
func (j *fleetDigestNotifyJob) streakPath() string {
	return filepath.Join(j.stateDir, fleetDigestStreakFileName)
}

// alertPath is the dead-man's switch a failed publish leaves behind.
func (j *fleetDigestNotifyJob) alertPath() string {
	return filepath.Join(j.stateDir, fleetDigestAlertFileName)
}

// fleetDigestOneLine collapses value onto one line, separated by separator, and
// bounds it to limit characters.
//
// ⚠️ Trailing newlines are trimmed FIRST, mirroring the shell's `$(...)` command
// substitution: without it every ledger row would end in a stray separator. The
// bound counts characters rather than bytes so a multi-byte digest cannot be cut
// mid-rune.
func fleetDigestOneLine(value string, separator string, limit int) string {
	oneLine := strings.ReplaceAll(strings.TrimRight(value, "\r\n"), "\r\n", "\n")
	oneLine = strings.ReplaceAll(oneLine, "\n", separator)
	runes := []rune(oneLine)
	if len(runes) <= limit {
		return oneLine
	}
	return string(runes[:limit])
}

// fleetDigestPublishError carries the publisher's own exit code and output, which
// the dead-man's switch records.
//
// ⚠️ Neither field is a credential: the script's output is its own response
// text, and the token it resolves never reaches this process. The message is
// bounded on format so a verbose failure cannot flood the log.
type fleetDigestPublishError struct {
	exitCode int
	output   string
}

func (e *fleetDigestPublishError) Error() string {
	return "publish failed rc=" + strconv.Itoa(e.exitCode) + ": " +
		fleetDigestOneLine(e.output, " ", fleetDigestAlertOutputLimit)
}

// fleetDigestPublishFailure extracts the exit code and output a failed publish
// should record, falling back to the exec-failure code for an error that never
// reached the script.
func fleetDigestPublishFailure(err error) (int, string) {
	var publishErr *fleetDigestPublishError
	if stderrors.As(err, &publishErr) {
		return publishErr.exitCode, publishErr.output
	}
	return fleetDigestExecFailureCode, err.Error()
}

// NewFleetDigestCommandChecker builds the checker over the engine script.
func NewFleetDigestCommandChecker(python, script string) FleetDigestChecker {
	return &fleetDigestCommandChecker{python: python, script: script}
}

type fleetDigestCommandChecker struct {
	python string
	script string
}

// Check runs `--check` once and returns its verdict.
//
// ⚠️ A non-zero exit is a RESULT, not an error: `--check` answers 10 for a
// lapsed round, and that is the whole point of calling it. Only a launch failure
// — no interpreter, no script — is an error, and the job folds it back into a
// 127 result.
func (c *fleetDigestCommandChecker) Check(
	ctx context.Context,
) (FleetDigestCheckResult, error) {
	commandCtx, cancel := context.WithTimeout(ctx, fleetDigestCheckTimeout)
	defer cancel()
	// #nosec G204 -- the interpreter and the script are compile-time constants
	// resolved by the default constructor; no caller or check output reaches the
	// command line.
	cmd := exec.CommandContext(commandCtx, c.python, c.script, "--check")
	// ⚠️ An allowlisted environment, not the inherited one: this service holds
	// ATTENTION_STORE_TOKEN and ATTENTION_UPSTREAM_TOKEN, and the staleness engine
	// needs neither.
	cmd.Env = childEnv()
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	if err == nil {
		return FleetDigestCheckResult{
			ExitCode: fleetDigestCheckHealthyCode,
			Output:   output.String(),
		}, nil
	}
	var exitErr *exec.ExitError
	if stderrors.As(err, &exitErr) {
		return FleetDigestCheckResult{ExitCode: exitErr.ExitCode(), Output: output.String()}, nil
	}
	return FleetDigestCheckResult{}, errors.Wrapf(
		ctx,
		err,
		"run fleet digest check '%s' failed",
		c.script,
	)
}

// NewFleetDigestCommandNotifier builds the notifier over the prod write script.
func NewFleetDigestCommandNotifier(script string) FleetDigestNotifier {
	return &fleetDigestCommandNotifier{script: script}
}

type fleetDigestCommandNotifier struct {
	script string
}

// Publish posts one agent-escalation through the write script.
//
// ⚠️ The script's path, its environment and the notification path are all
// constants; only the message is variable. That is what keeps a digest's output
// from steering a write, and it is why the command line is never logged: the
// script resolves its own credential, and a logged argv would be the one place a
// future edit could put one.
func (n *fleetDigestCommandNotifier) Publish(ctx context.Context, message string) error {
	body, err := json.Marshal(struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}{
		Type:    "agent-escalation",
		Message: message,
	})
	if err != nil {
		return errors.Wrapf(ctx, err, "marshal notification payload failed")
	}
	commandCtx, cancel := context.WithTimeout(ctx, fleetDigestPublishTimeout)
	defer cancel()
	// #nosec G204 -- the script is a compile-time constant resolved by the default
	// constructor, the environment and path are constants, and the message is a
	// JSON argument rather than a shell word.
	cmd := exec.CommandContext(
		commandCtx,
		n.script,
		fleetDigestPublishEnvironment,
		fleetDigestPublishPath,
		string(body),
	)
	// ⚠️ The write script resolves its own token; it is handed no part of this
	// service's environment, so neither of this service's credentials can reach
	// it.
	cmd.Env = childEnv()
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if stderrors.As(err, &exitErr) {
			return &fleetDigestPublishError{exitCode: exitErr.ExitCode(), output: output.String()}
		}
		return &fleetDigestPublishError{
			exitCode: fleetDigestExecFailureCode,
			output:   err.Error(),
		}
	}
	return nil
}

// NewDefaultFleetDigestNotifyJob builds the job against the paths the launchd
// job used: the engine script in the supervisor marketplace clone, the write
// script in the vault, and the state directory.
//
// ⚠️ Resolving them here rather than in main keeps the job's paths beside the
// job's semantics, and returns an error instead of a job with an unresolvable
// home directory — a job armed on a guessed path would check nothing and look
// exactly like a healthy fleet.
func NewDefaultFleetDigestNotifyJob(ctx context.Context) (run.Func, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, errors.Wrap(ctx, err, "resolve home dir failed")
	}
	return NewFleetDigestNotifyJob(
		NewFleetDigestCommandChecker(
			fleetDigestPython,
			filepath.Join(
				home,
				".claude",
				"plugins",
				"marketplaces",
				"claude-supervisor",
				"scripts",
				"fleet-digest.py",
			),
		),
		NewFleetDigestCommandNotifier(
			filepath.Join(
				home,
				"Documents",
				"Obsidian",
				"private-personal",
				".claude",
				"scripts",
				"trading-api-write.sh",
			),
		),
		filepath.Join(home, ".claude", "state", "fleet-digest"),
		FleetDigestNotifyDefaultInterval,
		libtime.NewCurrentDateTime(),
	), nil
}
