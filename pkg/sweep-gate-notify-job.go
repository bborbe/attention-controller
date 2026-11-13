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
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bborbe/errors"
	"github.com/bborbe/run"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

const (
	// sweepGateNotifyJobName is the `job=` value on every line this job logs.
	// A probe binds the job's effect to a line by grepping it, so it is a stable
	// identifier rather than a label that happens to read well.
	sweepGateNotifyJobName = "sweep-gate-notify"

	// SweepGateNotifyDefaultInterval is the cadence the launchd job ran at,
	// under `StartInterval 3600` in com.bborbe.sweep-gate-notify.
	SweepGateNotifyDefaultInterval = time.Hour

	// The two exit codes sweep-gate.py --tick uses to report a VERDICT rather
	// than a failure: 0 is EMPTY (nothing changed), 10 is CHANGE-WAKE (the digest
	// moved, or the gate failed open). Every other code means the gate itself did
	// not complete.
	sweepGateTickEmptyCode      = 0
	sweepGateTickChangeWakeCode = 10

	// sweepGateClassifyQuietCode is the classifier's "nothing here needs the
	// operator" answer: the change is progress-only and stays in the ledger.
	sweepGateClassifyQuietCode = 0

	// sweepGateExecFailureCode is the code recorded when a subprocess could not be
	// launched at all. ⚠️ It is 127 because that is what bash reported for the
	// same failure — a `python3` that never started — so a failure streak that
	// began under the shell job continues under this one instead of looking like a
	// new kind of failure.
	sweepGateExecFailureCode = 127

	// The five per-subject state files, unchanged from the shell job. ⚠️ The
	// layout is kept rather than redesigned: `alert` is the dead-man's switch
	// sweep-gate-notify-stale.sh reads and `failed-streak` is the marker that
	// suppresses a second alert, so a renamed file would silently disable one of
	// them.
	sweepGateLedgerSuffix    = ".ledger.txt"
	sweepGateHeartbeatSuffix = ".heartbeat"
	sweepGateTickFileSuffix  = ".tick.txt"
	sweepGateCardSuffix      = ".card.json"
	sweepGateAlertSuffix     = ".alert"
	sweepGateStreakSuffix    = ".failed-streak"

	// sweepGateLastPassFileName is the pass record the stale checker reads to
	// report a pass that outlived its interval.
	sweepGateLastPassFileName = "last-pass.json"

	// The ad-hoc park pass's own state, unchanged from the shell. ⚠️ It lives in
	// its own `adhoc/` dir rather than under a vault: the pass is per-host, not
	// per-vault, and the stale checker's `<BASE>/*/*.heartbeat` glob already
	// covers it, so a renamed dir would silently stop being watched.
	sweepGateAdhocDirName       = "adhoc"
	sweepGateAdhocStem          = "adhoc"
	sweepGateAdhocAlertFileName = sweepGateAdhocStem + ".alert"
	sweepGateAdhocStreakName    = sweepGateAdhocStem + ".failed-streak"
	sweepGateAdhocLedgerName    = sweepGateAdhocStem + ".ledger.txt"
	sweepGateAdhocStateFileName = "adhoc-parks.json"

	// sweepGateAdhocParkMessage is the shell's alert text for a pass that found
	// an unanchored park. The detail follows on its own line.
	sweepGateAdhocParkMessage = "Sweep gate: unanchored park needs you:\n"

	// sweepGateAdhocSkippedCode is recorded when the ad-hoc pass did not run at
	// all because the process was shutting down. ⚠️ It is deliberately outside
	// every real exit code: recording 0 would claim a quiet pass that never
	// happened, and recording a failure would page the operator on every
	// shutdown.
	sweepGateAdhocSkippedCode = -1

	// The ledger's `notified` values, carried over byte-for-byte from the shell:
	// an operator reading ledger.txt across the port must not have to learn a
	// second vocabulary for the same four outcomes.
	sweepGateNotifiedNotSent     = "-"
	sweepGateNotifiedSent        = "sent"
	sweepGateNotifiedProgress    = "progress-only"
	sweepGateNotifiedSuppressed  = "suppressed: FAILED streak continues (alerted once)"
	sweepGateNotifiedInvariant   = "sent: invariant "
	sweepGateInvariantFailedPref = "FAILED invariant: "

	// The payload bounds mirror the shell's `cut -c` calls. ⚠️ They are applied
	// per field rather than to the finished ledger line: a verbose classifier
	// event must not push the ledger's later columns off the row.
	sweepGateLedgerDetailLimit = 300
	sweepGateAlertDetailLimit  = 300
	sweepGateNotifyOutputLimit = 200
	sweepGateCardOutputLimit   = 200

	// ⚠️ The prod notification surface itself is not redeclared here: the
	// notifier implementation is the fleet-digest job's, so the environment and
	// the notification path stay one set of constants in one place.

	// The card poster's producer identity, carried over from the shell: `cron` is
	// the store's own kind for a scheduled job, and `sweep-gate` is a stable id
	// where a session id would be empty.
	sweepGateCardProducerID   = "sweep-gate"
	sweepGateCardProducerKind = "cron"

	// The two environment variables the subprocesses read. ⚠️ The gate writes its
	// tick file and card into SWEEP_GATE_STATE_DIR, and the classifier keeps its
	// once-per-error marker there, so the value must be the per-vault state dir —
	// a subject whose card landed in a shared dir would be posted from another
	// subject's verdict.
	sweepGateStateDirEnv  = "SWEEP_GATE_STATE_DIR"
	sweepGateManagersEnv  = "SWEEP_GATE_MANAGERS"
	sweepGatePythonBinary = "/usr/bin/python3"

	// sweepGateAdhocStateEnv is the ad-hoc pass's dedup ledger. ⚠️ It must sit
	// under the tick's state dir like every other piece of tick state, or a
	// SWEEP_GATE_BASE-isolated run (the induced-failure E2E, or any probe) would
	// silently consume the live parks it was only meant to observe.
	sweepGateAdhocStateEnv = "SWEEP_GATE_ADHOC_STATE"

	// The timeouts bound one subprocess each. The tick's sits far above the
	// measured ~17 s per-subject cost while still bounding a hung interpreter; the
	// three others sit under the interval so a stalled far end costs one subject
	// instead of wedging the pass.
	sweepGateTickTimeout     = 10 * time.Minute
	sweepGateClassifyTimeout = 60 * time.Second
	sweepGatePublishTimeout  = 60 * time.Second
	sweepGateCardTimeout     = 60 * time.Second

	// sweepGateAdhocTimeout bounds the ad-hoc pass. Its scan reads the frontmatter
	// of ~8.7k vault files, so it is given more room than a classifier call while
	// still bounding a hung interpreter.
	sweepGateAdhocTimeout = 2 * time.Minute
)

// sweepGateNotifyInfof and sweepGateNotifyErrorf are package variables so a spec
// can capture the per-pass and per-subject job lines.
//
// ⚠️ The lines are not decoration: the job's effects are files in a state
// directory and a notification posted to a remote service that no in-process
// assertion can read back, so `action=pass`, `action=publish` and
// `action=create` are what bind those effects to the job. A spec that had to read
// glog's file output would be testing glog rather than the job.
var (
	sweepGateNotifyInfof = func(format string, args ...any) {
		glog.Infof(format, args...)
	}
	sweepGateNotifyErrorf = func(format string, args ...any) {
		glog.Errorf(format, args...)
	}
)

// sweepGateSlugPattern is the subject-to-filename collapse the shell applied with
// `tr` + `sed`, and the gate applies to its own state files. ⚠️ It must stay
// identical to sweep-gate.py's `slug()`, or this job would delete one file and
// read another.
var sweepGateSlugPattern = regexp.MustCompile(`[^a-z0-9]+`)

// SweepGateSubject is one thing the pass ticks: a topic or goal page, its vault,
// and the branch that resolved it.
type SweepGateSubject struct {
	// Name is the subject as the gate resolves it.
	Name string
	// Vault is the vault path the subject's page lives in.
	Vault string
	// Branch is the page folder the subject resolved under, as the enumerator
	// reported it. It is carried for diagnostics only: the gate re-resolves the
	// subject from its own name and vault.
	Branch string
}

// SweepGateTickResult is one run of the gate: its exit code and its combined
// output.
//
// ⚠️ ExitCode is part of the RESULT, not an error. `--tick` answers 0 / 10 as
// verdicts, and folding 10 into an error would erase the difference between "the
// digest moved" and "the gate could not run".
type SweepGateTickResult struct {
	// ExitCode is the gate's exit status: 0 EMPTY, 10 CHANGE-WAKE, anything else
	// a tick that did not complete.
	ExitCode int
	// Output is stdout and stderr interleaved, as the shell's `2>&1` captured it.
	Output string
}

// SweepGateClassifyResult is one run of the change classifier.
type SweepGateClassifyResult struct {
	// ExitCode is 0 for progress-only and non-zero for anything the operator
	// should see.
	ExitCode int
	// Output is the classifier's event list, which becomes the notification's
	// detail.
	Output string
}

// SweepGateAdhocResult is one run of the ad-hoc park pass.
//
// ⚠️ ExitCode is a RESULT, not an error, for the same reason the gate's is: the
// pass answers 0 / 10 as verdicts, and folding 10 into an error would erase the
// difference between "an unanchored park is waiting" and "the pass could not run".
type SweepGateAdhocResult struct {
	// ExitCode is 0 quiet, 10 an unanchored park needs the operator, anything
	// else a pass that did not complete.
	ExitCode int
	// Output is the pass's own report, which becomes the notification's detail.
	Output string
}

// SweepGateCard is the card payload sweep-gate.py --tick leaves beside its tick
// file for this job to post.
//
// ⚠️ The JSON tags are the gate's own field names. The gate writes this file and
// the poster reads it through attention-ask.py's flags, so a renamed field here
// would post an empty card rather than fail.
type SweepGateCard struct {
	DedupKey  string   `json:"dedup_key"`
	Payload   string   `json:"payload"`
	Context   string   `json:"context"`
	Options   []string `json:"options"`
	Recommend string   `json:"recommend"`
}

// SweepGateSubjectLister enumerates the subjects one pass ticks.
//
// It is an interface so the job can be driven without the vault tree or the
// manager state dir on disk: the enumerator's whole subject is another
// repository's script and a live state directory, and a spec must depend on
// neither.
type SweepGateSubjectLister interface {
	List(ctx context.Context) ([]SweepGateSubject, error)
}

// SweepGateGate runs the gate's `--tick` for one subject.
//
// It is an interface for the same reason, and one more: the gate owns the digest,
// the fail-open rule and the card payload, so a spec that exercised it would be
// testing another repository's script through this job.
type SweepGateGate interface {
	Tick(
		ctx context.Context,
		subject string,
		vault string,
		stateDir string,
	) (SweepGateTickResult, error)
}

// SweepGateClassifier decides whether a CHANGE-WAKE needs the operator.
type SweepGateClassifier interface {
	Classify(
		ctx context.Context,
		subject string,
		vault string,
		gateVerdict string,
		stateDir string,
	) (SweepGateClassifyResult, error)
}

// SweepGateAdhocPass runs the ad-hoc park pass: the one that reaches a worker
// anchored on no task.
//
// ⚠️ It is a seam for the same reason the gate and the classifier are. The pass
// reads the session registry and every vault's task frontmatter to decide what an
// unanchored park is, and that rule lives in `sweep-gate-adhoc.py` where it
// already has an owner; reimplementing it here would be a second copy of the
// decision, drifting silently.
type SweepGateAdhocPass interface {
	Run(ctx context.Context, stateDir string) (SweepGateAdhocResult, error)
}

// SweepGateNotifier publishes one agent-escalation notification.
//
// It is an interface because the real notifier holds the prod write path: a spec
// that exercised it would post to a live service.
type SweepGateNotifier interface {
	Publish(ctx context.Context, message string) error
}

// SweepGateCardPoster posts one card through the attention store's CLI.
//
// It returns the poster's own output so the ledger can record what a post said,
// and an error for a post that did not land.
type SweepGateCardPoster interface {
	Post(ctx context.Context, card SweepGateCard) (string, error)
}

// NewSweepGateNotifyJob walks every sweep-gate subject every interval, until ctx
// is cancelled.
//
// Per subject, three classes that are never folded together:
//
//	gate exit 0   -> EMPTY: clear the streak, ledger + heartbeat only
//	gate exit 10  -> CHANGE-WAKE: clear the streak, ask the classifier whether the
//	                 change needs the operator, publish ONE alert when it does
//	any other rc  -> FAILED: the gate did not complete; publish once per streak
//
// After the subject loop, EVERY pass runs the ad-hoc park pass as well: the loop
// reaches a park only through a tracked task's liveness, so a worker anchored on
// no task is invisible to it. That pass answers the same three classes on its own
// `adhoc/` state, and the pass record and the `action=pass` line report both
// halves — dropping it would be a behaviour regression the moment the launchd
// plist is removed.
//
// ⚠️ Four architectural choices are deliberate and must not be reversed without
// re-deriving them:
//
//  1. THE TICK MOVES IN-PROCESS; THE GATE, THE CLASSIFIER, THE AD-HOC PASS AND
//     THE SUBJECT ENUMERATOR STAY SUBPROCESSES. Those four carry the rules that
//     decide what a change means — the digest, the fail-open path, the
//     actionable-vs-progress split, the unanchored-park rule, the vault and
//     manager resolution — and they live in the Personal vault and ~/.claude,
//     where they already have an owner. Reimplementing them here would be a
//     second copy of a decision, and the drift would be silent: the job would
//     keep reporting a verdict computed by rules nobody updated. What moves is
//     the 3600 s lifecycle, which is the cost the port exists to remove.
//
//  2. THE PROD PUBLISH STILL SHELLS OUT, so NO CREDENTIAL ENTERS THIS SERVICE. The
//     notification is posted through `trading-api-write.sh`, which resolves its
//     own token; this service holds none, logs none and cannot leak one. The
//     notifier implementation is the one the fleet-digest job already uses — the
//     write path, the environment and the notification path are the same
//     constants, and a second copy would be a second place to update when the
//     write script changes.
//
//  3. THE SINGLE-FLIGHT LOCK IS GONE, AND ITS ABSENCE IS STRUCTURAL, NOT AN
//     OVERSIGHT. The shell's `$BASE/.pass.lock` existed only because launchd
//     could start a second pass on top of one still walking the subject list —
//     measured 2026-11-12T17:25Z: two passes alive at once, one 29 s old and one
//     2 h 27 m old, with per-subject ledger gaps of 1 h 32 m, 1 h 44 m, then
//     6 h 54 m. The in-process loop is one goroutine that walks the subject list
//     sequentially, so overlap is structurally impossible: there is no second
//     invocation to refuse. Porting the mkdir lock would add a state file, a
//     stale-holder rule and a takeover race to guard against a caller that no
//     longer exists.
//
//  4. SWEEP_GATE_NO_SEND IS NOT PORTED. The shell's dry-run flag existed to
//     exercise the publish DECISION without pushing to the operator's phone; here
//     the notifier and the card poster are injected seams, so a spec injects a
//     fake instead of setting an environment variable the production path would
//     also have to read.
//
// ⚠️ Every failure is fail-soft: a missed subject costs one interval of watching
// and loses nothing, so the pass logs it and continues. Taking the process down
// would drop the board over something the board survives.
//
// The interval is a parameter rather than the constant read directly, so a spec
// can drive the pass without waiting an hour for it.
func NewSweepGateNotifyJob(
	subjects SweepGateSubjectLister,
	gate SweepGateGate,
	classifier SweepGateClassifier,
	adhoc SweepGateAdhocPass,
	notifier SweepGateNotifier,
	poster SweepGateCardPoster,
	stateDir string,
	interval time.Duration,
	now libtime.CurrentDateTimeGetter,
) run.Func {
	job := &sweepGateNotifyJob{
		subjects:   subjects,
		gate:       gate,
		classifier: classifier,
		adhoc:      adhoc,
		notifier:   notifier,
		poster:     poster,
		stateDir:   stateDir,
		interval:   interval,
		now:        now,
	}
	return job.run
}

type sweepGateNotifyJob struct {
	subjects   SweepGateSubjectLister
	gate       SweepGateGate
	classifier SweepGateClassifier
	adhoc      SweepGateAdhocPass
	notifier   SweepGateNotifier
	poster     SweepGateCardPoster
	stateDir   string
	interval   time.Duration
	now        libtime.CurrentDateTimeGetter
}

// run is the loop shape every in-process job in this repo shares: a ticker and
// ctx.Done, with a failed pass logged and continued rather than returned.
//
// ⚠️ It walks once before the first tick, matching launchd, which ran the tick
// immediately on load. A restart is exactly when a digest may have moved
// unwatched, so waiting a whole interval to look would keep the first hour after
// every deploy blind to the condition this job exists to report.
func (j *sweepGateNotifyJob) run(ctx context.Context) error {
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

// cycle runs one pass: enumerate, tick every subject, record the pass.
func (j *sweepGateNotifyJob) cycle(ctx context.Context) {
	started := j.now.Now().Time()

	subjects, err := j.subjects.List(ctx)
	if err != nil {
		// ⚠️ An enumeration that failed is logged and the pass still records
		// itself: a pass with zero subjects is a fact an absent record would hide,
		// and the record's subject count is how a reader tells "nothing is
		// declared" from "the enumerator broke".
		sweepGateNotifyErrorf(
			"job=%s action=subjects_error error=%v",
			sweepGateNotifyJobName,
			err,
		)
		subjects = nil
	}

	walked := 0
	for _, subject := range subjects {
		// ⚠️ Cancellation is checked BETWEEN subjects, so a shutdown does not wait
		// out a gate run per remaining subject.
		if ctx.Err() != nil {
			break
		}
		j.tickSubject(ctx, subject)
		walked++
	}

	// ⚠️ The ad-hoc pass runs AFTER the subject loop, every cycle. The loop reaches
	// a park only through a TRACKED task's liveness, so a worker anchored on no
	// task sits in no subject's tracked set and its park reaches nothing — measured
	// 2026-09-24: 4 of 12 headless workers over two fleet days carried no anchor at
	// all, and 9 of the 12 raised a park. Dropping this half would be a behaviour
	// regression the moment the launchd plist is removed.
	adhocRC := j.runAdhocPass(ctx)

	j.writeLastPass(ctx, started, walked, adhocRC)
}

// tickSubject runs one subject: clear the card, tick, classify, report, ledger,
// heartbeat.
//
// ⚠️ The order inside this function is the shell's, and each step's position is
// load-bearing — the comments below say which.
func (j *sweepGateNotifyJob) tickSubject(ctx context.Context, subject SweepGateSubject) {
	state := j.subjectStateDir(subject.Vault)
	if err := os.MkdirAll(state, 0o750); err != nil {
		sweepGateNotifyErrorf(
			"job=%s action=state_error error=%v",
			sweepGateNotifyJobName,
			errors.Wrapf(ctx, err, "create state dir '%s' failed", state),
		)
		return
	}
	slug := sweepGateSlug(subject.Name)
	cardPath := filepath.Join(state, slug+sweepGateCardSuffix)
	timestamp := sweepGateTimestamp(j.now.Now().Time())

	// ⚠️ The card is cleared BEFORE the gate runs, so a tick that fails cannot
	// re-post the card an earlier tick wrote — the file's absence is the honest
	// "no verdict this tick", never the previous tick's verdict.
	j.removeStateFile(ctx, cardPath)

	result, err := j.gate.Tick(ctx, subject.Name, subject.Vault, state)
	if err != nil {
		// ⚠️ A gate that could not be launched is a FAILED tick, not a quiet one,
		// and it is folded into the failure branch with the code bash reported: a
		// job that stayed silent here would be indistinguishable from a healthy
		// subject at the exact moment nothing is being watched.
		sweepGateNotifyErrorf(
			"job=%s action=tick_error subject=%s error=%v",
			sweepGateNotifyJobName,
			subject.Name,
			err,
		)
		result = SweepGateTickResult{ExitCode: sweepGateExecFailureCode, Output: err.Error()}
	}

	verdict := sweepGateLastLine(result.Output)
	notified := sweepGateNotifiedNotSent
	switch result.ExitCode {
	case sweepGateTickEmptyCode:
		j.clearStreak(ctx, state, slug)
	case sweepGateTickChangeWakeCode:
		j.clearStreak(ctx, state, slug)
		notified = j.reportChangeWake(ctx, subject, state, slug, verdict)
	default:
		verdict = "FAILED(rc=" + strconv.Itoa(result.ExitCode) + "): " + verdict
		notified = j.reportFailureStreak(ctx, subject, state, slug, result)
	}

	invariant := j.postCard(ctx, subject, cardPath, result.ExitCode)
	j.appendLedger(ctx, state, slug, timestamp, result.ExitCode, verdict, notified, invariant)
	j.touchHeartbeat(ctx, state, slug, timestamp, result.ExitCode)
}

// reportChangeWake asks the classifier whether a changed digest needs the
// operator, and publishes one alert when it does.
func (j *sweepGateNotifyJob) reportChangeWake(
	ctx context.Context,
	subject SweepGateSubject,
	state string,
	slug string,
	verdict string,
) string {
	classified, err := j.classifier.Classify(
		ctx,
		subject.Name,
		subject.Vault,
		verdict,
		state,
	)
	if err != nil {
		// ⚠️ A classifier that could not run fails OPEN, matching the gate's own
		// rule: the change is published rather than swallowed, because an
		// unreported change is the failure the operator cannot see.
		sweepGateNotifyErrorf(
			"job=%s action=classify_error subject=%s error=%v",
			sweepGateNotifyJobName,
			subject.Name,
			err,
		)
		classified = SweepGateClassifyResult{
			ExitCode: sweepGateExecFailureCode,
			Output:   err.Error(),
		}
	}
	if classified.ExitCode == sweepGateClassifyQuietCode {
		return sweepGateNotifiedProgress
	}
	return j.publish(
		ctx,
		"Sweep gate: "+j.tickMarker(ctx, state, slug)+" — needs you:\n"+classified.Output,
		classified.Output,
		filepath.Join(state, slug+sweepGateAlertSuffix),
	)
}

// reportFailureStreak publishes one alert per failure streak, never one per tick.
//
// ⚠️ The streak file is created even when the publish failed, matching the shell:
// the streak records that this failure has been attempted, and the `alert` file —
// not the streak — is what reports a publish that did not land.
func (j *sweepGateNotifyJob) reportFailureStreak(
	ctx context.Context,
	subject SweepGateSubject,
	state string,
	slug string,
	result SweepGateTickResult,
) string {
	streakPath := filepath.Join(state, slug+sweepGateStreakSuffix)
	if j.stateFileExists(ctx, streakPath) {
		sweepGateNotifyInfof(
			"job=%s action=publish_suppressed subject=%s rc=%d",
			sweepGateNotifyJobName,
			subject.Name,
			result.ExitCode,
		)
		return sweepGateNotifiedSuppressed
	}
	notified := j.publish(
		ctx,
		"Sweep gate FAILED: "+subject.Name+" — the gate did not complete (rc="+
			strconv.Itoa(result.ExitCode)+"):\n"+result.Output,
		"",
		filepath.Join(state, slug+sweepGateAlertSuffix),
	)
	j.createStateFile(ctx, streakPath)
	return notified
}

// runAdhocPass runs the unanchored-park pass and records its own ledger line and
// heartbeat, returning the pass's exit code for the pass record.
//
// ⚠️ The pass is NOT a fourth publisher: it reuses `publish`, so the phone path
// stays the ones that already exist.
//
// ⚠️ The state dir is created even when the pass then fails, matching the shell's
// `mkdir -p`: the ledger and the heartbeat live in it, and a pass whose own
// bookkeeping could not be written would be indistinguishable from one that never
// ran.
func (j *sweepGateNotifyJob) runAdhocPass(ctx context.Context) int {
	// ⚠️ A shutdown must not page the operator: the pass would fail to launch
	// against a cancelled context and be recorded as a FAILED streak. Skipping it
	// with a code outside every real rc keeps the pass record honest.
	if ctx.Err() != nil {
		return sweepGateAdhocSkippedCode
	}
	dir := filepath.Join(j.stateDir, sweepGateAdhocDirName)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		sweepGateNotifyErrorf(
			"job=%s action=adhoc_state_error error=%v",
			sweepGateNotifyJobName,
			errors.Wrapf(ctx, err, "create ad-hoc state dir '%s' failed", dir),
		)
	}
	// ⚠️ The timestamp is taken BEFORE the pass runs, as the shell did: the ledger
	// row names when the pass was started, so a slow pass is visible as the gap
	// between its row and the next.
	timestamp := sweepGateTimestamp(j.now.Now().Time())
	result, err := j.adhoc.Run(ctx, dir)
	if err != nil {
		// ⚠️ A pass that could not be launched is FAILED, not quiet — the same rule
		// the gate keeps: silence here would read as "no unanchored park" at the
		// exact moment nothing is being watched.
		sweepGateNotifyErrorf(
			"job=%s action=adhoc_error error=%v",
			sweepGateNotifyJobName,
			err,
		)
		result = SweepGateAdhocResult{ExitCode: sweepGateExecFailureCode, Output: err.Error()}
	}

	notified := j.adhocNotified(ctx, dir, result)
	j.appendLines(ctx, filepath.Join(dir, sweepGateAdhocLedgerName), []string{
		timestamp + "\t" + strconv.Itoa(result.ExitCode) + "\t" + notified + "\t" +
			sweepGateLedgerParent() + "\n",
	})
	j.touchHeartbeat(ctx, dir, sweepGateAdhocStem, timestamp, result.ExitCode)
	return result.ExitCode
}

// adhocNotified publishes (or suppresses) the alert for one ad-hoc pass and
// returns the ledger's `notified` value.
func (j *sweepGateNotifyJob) adhocNotified(
	ctx context.Context,
	dir string,
	result SweepGateAdhocResult,
) string {
	streakPath := filepath.Join(dir, sweepGateAdhocStreakName)
	alertPath := filepath.Join(dir, sweepGateAdhocAlertFileName)
	switch result.ExitCode {
	case sweepGateTickEmptyCode:
		j.removeStateFile(ctx, streakPath)
		return sweepGateNotifiedNotSent
	case sweepGateTickChangeWakeCode:
		// ⚠️ The streak is cleared on an actionable pass too: a park that resolved
		// and later recurs must notify again rather than be suppressed by the
		// failure that happened in between.
		j.removeStateFile(ctx, streakPath)
		return j.publish(
			ctx,
			sweepGateAdhocParkMessage+result.Output,
			result.Output,
			alertPath,
		)
	default:
		return j.adhocFailureStreak(ctx, alertPath, streakPath, result)
	}
}

// adhocFailureStreak publishes once per ad-hoc failure streak, never once per
// tick.
func (j *sweepGateNotifyJob) adhocFailureStreak(
	ctx context.Context,
	alertPath string,
	streakPath string,
	result SweepGateAdhocResult,
) string {
	if j.stateFileExists(ctx, streakPath) {
		sweepGateNotifyInfof(
			"job=%s action=publish_suppressed pass=%s rc=%d",
			sweepGateNotifyJobName,
			sweepGateAdhocStem,
			result.ExitCode,
		)
		return sweepGateNotifiedSuppressed
	}
	notified := j.publish(
		ctx,
		"Sweep gate ad-hoc pass FAILED (rc="+strconv.Itoa(result.ExitCode)+
			") — the pass did not complete:\n"+result.Output,
		"",
		alertPath,
	)
	j.createStateFile(ctx, streakPath)
	return notified
}

// publish sends one agent-escalation and reports the ledger's `notified` value.
//
// On success it clears the dead-man's switch; on failure it writes it, so a failed
// publish is reported by sweep-gate-notify-stale.sh instead of vanishing.
func (j *sweepGateNotifyJob) publish(
	ctx context.Context,
	message string,
	detail string,
	alertPath string,
) string {
	if err := j.notifier.Publish(ctx, message); err != nil {
		code, output := fleetDigestPublishFailure(err)
		j.writeAlert(ctx, alertPath, code, output)
		sweepGateNotifyErrorf(
			"job=%s action=publish_failed rc=%d error=%v",
			sweepGateNotifyJobName,
			code,
			err,
		)
		return "FAILED(rc=" + strconv.Itoa(code) + "): " +
			sweepGateCollapse(output, " ", sweepGateNotifyOutputLimit)
	}
	j.removeStateFile(ctx, alertPath)
	// ⚠️ Logged only for a publish that actually happened: the line's job is to
	// prove the effect occurred, and a failed one has its own line above.
	sweepGateNotifyInfof(
		"job=%s action=publish",
		sweepGateNotifyJobName,
	)
	if detail == "" {
		return sweepGateNotifiedSent
	}
	return sweepGateNotifiedSent + ": " +
		sweepGateCollapse(detail, ";", sweepGateLedgerDetailLimit)
}

// postCard posts the card the tick just wrote, and returns the extra ledger
// value the shell recorded for it ("" when there was no card).
//
// ⚠️ The card is READ, never re-derived: running the gate a second time in
// `--invariant` mode resolved the whole tracked tree again — measured 2026-11-13
// at 9.3 s of a 16.9 s per-subject total, more than half the pass — to produce a
// payload the tick already had in hand. The file was cleared before the gate ran,
// so an absent file means "no verdict", never the previous tick's card.
func (j *sweepGateNotifyJob) postCard(
	ctx context.Context,
	subject SweepGateSubject,
	cardPath string,
	exitCode int,
) string {
	content, err := os.ReadFile(cardPath)
	if err != nil {
		if !os.IsNotExist(err) {
			sweepGateNotifyErrorf(
				"job=%s action=card_read_error subject=%s error=%v",
				sweepGateNotifyJobName,
				subject.Name,
				errors.Wrapf(ctx, err, "read card '%s' failed", cardPath),
			)
		}
		// ⚠️ The verdict is named on EVERY tick, not only when a card fires. A
		// check whose only trace is a row it writes when it fires is
		// indistinguishable from a check that never ran — and with the invariant
		// firing for 0 of 59 subjects today, "no card" is the EXPECTED state, so an
		// absence-only trace would read as success whether the wiring worked or not.
		sweepGateNotifyInfof(
			"job=%s action=invariant subject=%s result=%s",
			sweepGateNotifyJobName,
			subject.Name,
			sweepGateInvariantAbsent(exitCode),
		)
		return ""
	}

	var card SweepGateCard
	if err := json.Unmarshal(content, &card); err != nil {
		sweepGateNotifyErrorf(
			"job=%s action=card_decode_error subject=%s error=%v",
			sweepGateNotifyJobName,
			subject.Name,
			errors.Wrapf(ctx, err, "decode card '%s' failed", cardPath),
		)
		return ""
	}
	sweepGateNotifyInfof(
		"job=%s action=invariant subject=%s result=card",
		sweepGateNotifyJobName,
		subject.Name,
	)

	output, err := j.poster.Post(ctx, card)
	if err != nil {
		sweepGateNotifyErrorf(
			"job=%s action=create_failed card=%s error=%v",
			sweepGateNotifyJobName,
			card.DedupKey,
			err,
		)
		return sweepGateInvariantFailedPref +
			sweepGateCollapse(err.Error(), ";", sweepGateCardOutputLimit)
	}
	// ⚠️ Logged with the card's dedup key rather than an item id: the poster
	// prints its own text, and parsing an id out of it would bind this job to
	// another script's output format.
	sweepGateNotifyInfof(
		"job=%s action=create card=%s",
		sweepGateNotifyJobName,
		card.DedupKey,
	)
	if output == "" {
		output = "(no output)"
	}
	return sweepGateNotifiedInvariant +
		sweepGateCollapse(output, ";", sweepGateCardOutputLimit)
}

// appendLedger appends the per-subject ledger lines.
//
// ⚠️ Every row carries the same five columns — timestamp, gate rc, verdict,
// notified, and the writing process — because a reader's grep across the file
// must not have to know which line is which. The shell's fifth column named the
// spawning parent, which is what distinguished overlapping passes; overlap is
// structurally impossible here, so the column names this process instead.
//
// ⚠️ A failure here is logged and the pass still continues: the notification is
// the signal and the ledger is the evidence, so losing the second must not cost
// the operator the first.
func (j *sweepGateNotifyJob) appendLedger(
	ctx context.Context,
	state string,
	slug string,
	timestamp string,
	exitCode int,
	verdict string,
	notified string,
	invariant string,
) {
	parent := sweepGateLedgerParent()
	lines := make([]string, 0, 2)
	if invariant != "" {
		lines = append(lines, timestamp+"\t"+strconv.Itoa(exitCode)+"\tINVARIANT\t"+
			invariant+"\t"+parent+"\n")
	}
	lines = append(lines, timestamp+"\t"+strconv.Itoa(exitCode)+"\t"+verdict+"\t"+
		notified+"\t"+parent+"\n")
	j.appendLines(ctx, filepath.Join(state, slug+sweepGateLedgerSuffix), lines)
}

// appendLines appends already-rendered lines to one file.
func (j *sweepGateNotifyJob) appendLines(ctx context.Context, path string, lines []string) {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		sweepGateNotifyErrorf(
			"job=%s action=ledger_error error=%v",
			sweepGateNotifyJobName,
			errors.Wrapf(ctx, err, "open ledger '%s' failed", path),
		)
		return
	}
	defer file.Close()
	for _, line := range lines {
		if _, err := file.WriteString(line); err != nil {
			sweepGateNotifyErrorf(
				"job=%s action=ledger_error error=%v",
				sweepGateNotifyJobName,
				errors.Wrapf(ctx, err, "append to ledger '%s' failed", path),
			)
			return
		}
	}
}

// touchHeartbeat rewrites the subject's heartbeat, which is how
// sweep-gate-notify-stale.sh tells a watched subject from an abandoned one.
//
// The content mirrors the shell's `<epoch> <rfc3339> rc=<n>` line, but the file's
// MTIME is what the stale checker reads — the content is for a human looking at
// the file.
func (j *sweepGateNotifyJob) touchHeartbeat(
	ctx context.Context,
	state string,
	slug string,
	timestamp string,
	exitCode int,
) {
	instant := j.now.Now().Time().UTC()
	content := strconv.FormatInt(instant.Unix(), 10) + " " + timestamp +
		" rc=" + strconv.Itoa(exitCode) + "\n"
	path := filepath.Join(state, slug+sweepGateHeartbeatSuffix)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		sweepGateNotifyErrorf(
			"job=%s action=heartbeat_error error=%v",
			sweepGateNotifyJobName,
			errors.Wrapf(ctx, err, "write heartbeat '%s' failed", path),
		)
	}
}

// writeAlert records a publish that did not land.
//
// ⚠️ This file IS the dead-man's switch: sweep-gate-notify-stale.sh reads it and
// reports it, which is how a notification that never left this host becomes
// visible instead of silent.
func (j *sweepGateNotifyJob) writeAlert(
	ctx context.Context,
	alertPath string,
	code int,
	output string,
) {
	content := sweepGateTimestamp(j.now.Now().Time()) + "\trc=" + strconv.Itoa(code) +
		"\t" + sweepGateCollapse(output, " ", sweepGateAlertDetailLimit) + "\n"
	if err := os.WriteFile(alertPath, []byte(content), 0o600); err != nil {
		sweepGateNotifyErrorf(
			"job=%s action=alert_error error=%v",
			sweepGateNotifyJobName,
			errors.Wrapf(ctx, err, "write alert '%s' failed", alertPath),
		)
	}
}

// writeLastPass records the pass itself, so a pass that outlives its interval is
// readable from disk instead of from `ps -eo etime`.
//
// ⚠️ duration_s is the MEASURED delta between the pass's two clock reads, never a
// constant: a constant beside the per-subject heartbeats would assert nothing. rc
// is 0 because an in-process pass has no exit status — a per-subject failure is
// recorded in the ledger, and the pass completing is the only thing this file
// claims. `adhoc_rc` carries the second half's verdict, so a reader can tell a
// pass that walked every subject AND ran the ad-hoc pass from one that did only
// the first.
//
// ⚠️ It is written AFTER both halves, so its duration covers the ad-hoc pass too.
func (j *sweepGateNotifyJob) writeLastPass(
	ctx context.Context,
	started time.Time,
	subjects int,
	adhocRC int,
) {
	finished := j.now.Now().Time()
	record := sweepGateLastPass{
		StartedAt:    started.Unix(),
		StartedAtISO: sweepGateTimestamp(started),
		EndEpoch:     finished.Unix(),
		DurationS:    int64(finished.Sub(started) / time.Second),
		PID:          os.Getpid(),
		Subjects:     subjects,
		AdhocRC:      adhocRC,
		RC:           sweepGateTickEmptyCode,
	}
	content, err := json.Marshal(record)
	if err != nil {
		sweepGateNotifyErrorf(
			"job=%s action=last_pass_error error=%v",
			sweepGateNotifyJobName,
			errors.Wrapf(ctx, err, "marshal last pass failed"),
		)
		return
	}
	if err := os.MkdirAll(j.stateDir, 0o750); err != nil {
		sweepGateNotifyErrorf(
			"job=%s action=last_pass_error error=%v",
			sweepGateNotifyJobName,
			errors.Wrapf(ctx, err, "create state dir '%s' failed", j.stateDir),
		)
		return
	}
	path := filepath.Join(j.stateDir, sweepGateLastPassFileName)
	if err := os.WriteFile(path, append(content, '\n'), 0o600); err != nil {
		sweepGateNotifyErrorf(
			"job=%s action=last_pass_error error=%v",
			sweepGateNotifyJobName,
			errors.Wrapf(ctx, err, "write last pass '%s' failed", path),
		)
	}
	// ⚠️ The pass line is logged even when every subject was quiet. It is the
	// evidence the job ran, and a quiet pass is a fact an absent log line would
	// hide — a probe could not tell "nothing changed" from "the loop is dead". It
	// names BOTH halves, because the ad-hoc pass is part of the same tick and a
	// line reporting only the subject count would read as a complete pass while
	// half of it was missing.
	sweepGateNotifyInfof(
		"job=%s action=pass subjects=%d adhoc_rc=%d duration=%d",
		sweepGateNotifyJobName,
		subjects,
		adhocRC,
		record.DurationS,
	)
}

// sweepGateLastPass is the pass record's on-disk shape.
//
// ⚠️ Every field the shell wrote keeps its name and type, because
// sweep-gate-notify-stale.sh reads them by name. `adhoc_rc` is additive: the
// checker ignores unknown keys, and without it the record could not say whether
// the ad-hoc half ran.
type sweepGateLastPass struct {
	StartedAt    int64  `json:"started_at"`
	StartedAtISO string `json:"started_at_iso"`
	EndEpoch     int64  `json:"end_epoch"`
	DurationS    int64  `json:"duration_s"`
	PID          int    `json:"pid"`
	Subjects     int    `json:"subjects"`
	AdhocRC      int    `json:"adhoc_rc"`
	RC           int    `json:"rc"`
}

// clearStreak removes the failure-streak marker, which is what lets the next
// streak alert.
func (j *sweepGateNotifyJob) clearStreak(ctx context.Context, state string, slug string) {
	j.removeStateFile(ctx, filepath.Join(state, slug+sweepGateStreakSuffix))
}

// createStateFile writes an empty marker file, mirroring the shell's `: > "$f"`.
func (j *sweepGateNotifyJob) createStateFile(ctx context.Context, path string) {
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		sweepGateNotifyErrorf(
			"job=%s action=state_error error=%v",
			sweepGateNotifyJobName,
			errors.Wrapf(ctx, err, "write state file '%s' failed", path),
		)
	}
}

// removeStateFile removes one state file, treating an absent file as done.
func (j *sweepGateNotifyJob) removeStateFile(ctx context.Context, path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		sweepGateNotifyErrorf(
			"job=%s action=state_error error=%v",
			sweepGateNotifyJobName,
			errors.Wrapf(ctx, err, "remove state file '%s' failed", path),
		)
	}
}

// stateFileExists reports whether a marker file is present.
//
// ⚠️ An unreadable file reads as absent, matching the shell's `[ -e ]`, and the
// error is logged rather than swallowed: publishing a second alert is the safe
// direction, but a permissions problem on the state directory is worth seeing.
func (j *sweepGateNotifyJob) stateFileExists(ctx context.Context, path string) bool {
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	if !os.IsNotExist(err) {
		sweepGateNotifyErrorf(
			"job=%s action=state_error error=%v",
			sweepGateNotifyJobName,
			errors.Wrapf(ctx, err, "stat state file '%s' failed", path),
		)
	}
	return false
}

// tickMarker is the tick file's first line, falling back to the subject.
//
// ⚠️ The tick file is the gate's operator-facing table and its first line is the
// subject's headline, so a notification that names it says which subject moved in
// the gate's own words. An unreadable file falls back to the subject rather than
// dropping the name from the alert.
func (j *sweepGateNotifyJob) tickMarker(ctx context.Context, state string, slug string) string {
	content, err := os.ReadFile(filepath.Join(state, slug+sweepGateTickFileSuffix))
	if err != nil {
		if !os.IsNotExist(err) {
			sweepGateNotifyErrorf(
				"job=%s action=tick_read_error error=%v",
				sweepGateNotifyJobName,
				errors.Wrapf(ctx, err, "read tick file for '%s' failed", slug),
			)
		}
		return slug
	}
	lines := strings.Split(string(content), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return slug
	}
	return lines[0]
}

// subjectStateDir is the per-vault state directory: equal subject names in two
// vaults must never share a digest, a streak or a card.
func (j *sweepGateNotifyJob) subjectStateDir(vault string) string {
	return filepath.Join(j.stateDir, strings.ToLower(filepath.Base(vault)))
}

// sweepGateSlug collapses a subject to the filename stem the gate uses.
//
// ⚠️ The result must match sweep-gate.py's `slug()` byte for byte, or this job
// would clear one card and read another.
func sweepGateSlug(subject string) string {
	return strings.Trim(sweepGateSlugPattern.ReplaceAllString(strings.ToLower(subject), "-"), "-")
}

// sweepGateTimestamp renders an instant the way the shell's `date -u` did.
func sweepGateTimestamp(instant time.Time) string {
	return instant.UTC().Format("2006-01-02T15:04:05Z")
}

// sweepGateLastLine is the verdict the shell took with `tail -1`: the last line
// of the gate's trailing-newline-trimmed output.
func sweepGateLastLine(output string) string {
	trimmed := strings.TrimRight(output, "\r\n")
	if index := strings.LastIndex(trimmed, "\n"); index >= 0 {
		return trimmed[index+1:]
	}
	return trimmed
}

// sweepGateCollapse collapses value onto one line, separated by separator, and
// bounds it to limit characters.
//
// ⚠️ Trailing newlines are trimmed FIRST, mirroring the shell's `$(...)` command
// substitution: without it every ledger field would end in a stray separator. The
// bound counts characters rather than bytes so a multi-byte event cannot be cut
// mid-rune.
func sweepGateCollapse(value string, separator string, limit int) string {
	oneLine := strings.ReplaceAll(strings.TrimRight(value, "\r\n"), "\r\n", "\n")
	oneLine = strings.ReplaceAll(oneLine, "\n", separator)
	runes := []rune(oneLine)
	if len(runes) <= limit {
		return oneLine
	}
	return string(runes[:limit])
}

// sweepGateInvariantAbsent is the invariant line's result for a tick that wrote
// no card.
func sweepGateInvariantAbsent(exitCode int) string {
	if exitCode == sweepGateTickEmptyCode || exitCode == sweepGateTickChangeWakeCode {
		return "no-card"
	}
	return "gate rc=" + strconv.Itoa(exitCode) + " (no verdict)"
}

// sweepGateLedgerParent is the ledger's fifth column: the process that wrote the
// row.
func sweepGateLedgerParent() string {
	return "pid=" + strconv.Itoa(os.Getpid()) +
		"(" + filepath.Base(os.Args[0]) + ")"
}

// NewSweepGateCommandSubjectLister builds the enumerator over its script and the
// declared-manager state directory.
//
// ⚠️ The managers directory is passed to the CHILD's environment, not set for
// this process: a wrapper-only assignment would silently leave the script on the
// real ~/.claude/state/worker-manager, which reads as "the fixture worked" while
// it did not.
func NewSweepGateCommandSubjectLister(python, script, managersDir string) SweepGateSubjectLister {
	return &sweepGateCommandSubjectLister{
		python:      python,
		script:      script,
		managersDir: managersDir,
	}
}

type sweepGateCommandSubjectLister struct {
	python      string
	script      string
	managersDir string
}

// List runs the enumerator once and parses its TSV stream.
//
// ⚠️ Diagnostics go to the child's stderr and the TSV to its stdout, so stderr is
// captured separately and only logged: folding it into the stream would turn a
// diagnostic line into a subject named "subjects:".
func (l *sweepGateCommandSubjectLister) List(ctx context.Context) ([]SweepGateSubject, error) {
	var stdout, stderr bytes.Buffer
	// #nosec G204 -- the interpreter and the script are compile-time constants
	// resolved by the default constructor; no caller or gate output reaches the
	// command line.
	cmd := exec.CommandContext(ctx, l.python, l.script)
	cmd.Env = append(os.Environ(), sweepGateManagersEnv+"="+l.managersDir)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, errors.Wrapf(
			ctx,
			err,
			"run subject enumerator '%s' failed: %s",
			l.script,
			sweepGateCollapse(stderr.String(), ";", sweepGateNotifyOutputLimit),
		)
	}
	return parseSweepGateSubjects(stdout.String()), nil
}

// parseSweepGateSubjects reads the enumerator's `<subject>\t<vault>\t<branch>`
// lines.
//
// ⚠️ A line with no subject or no vault is skipped rather than failing the read:
// one malformed row must not blank the whole pass, which is the fail-soft rule
// this job runs under.
func parseSweepGateSubjects(output string) []SweepGateSubject {
	subjects := make([]SweepGateSubject, 0, 64)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) < 2 || fields[0] == "" || fields[1] == "" {
			continue
		}
		subject := SweepGateSubject{Name: fields[0], Vault: fields[1]}
		if len(fields) > 2 {
			subject.Branch = fields[2]
		}
		subjects = append(subjects, subject)
	}
	return subjects
}

// NewSweepGateCommandGate builds the gate over its script.
func NewSweepGateCommandGate(python, script string) SweepGateGate {
	return &sweepGateCommandGate{python: python, script: script}
}

type sweepGateCommandGate struct {
	python string
	script string
}

// Tick runs `--tick` for one subject and returns its verdict.
//
// ⚠️ A non-zero exit is a RESULT, not an error: `--tick` answers 10 for a changed
// digest, and that is the whole point of calling it. Only a launch failure — no
// interpreter, no script — is an error, and the job folds it back into a 127
// result.
func (g *sweepGateCommandGate) Tick(
	ctx context.Context,
	subject string,
	vault string,
	stateDir string,
) (SweepGateTickResult, error) {
	commandCtx, cancel := context.WithTimeout(ctx, sweepGateTickTimeout)
	defer cancel()
	// #nosec G204 -- the interpreter, the script and the two flags are
	// compile-time values; the subject and vault come from the enumerator's own
	// TSV and are passed as argv words, never through a shell.
	cmd := exec.CommandContext(
		commandCtx,
		g.python,
		g.script,
		"--topic", subject,
		"--vault", vault,
		"--tick",
	)
	cmd.Env = append(os.Environ(), sweepGateStateDirEnv+"="+stateDir)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	if err == nil {
		return SweepGateTickResult{ExitCode: sweepGateTickEmptyCode, Output: output.String()}, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return SweepGateTickResult{ExitCode: exitErr.ExitCode(), Output: output.String()}, nil
	}
	return SweepGateTickResult{}, errors.Wrapf(ctx, err, "run sweep gate '%s' failed", g.script)
}

// NewSweepGateCommandClassifier builds the change classifier over its script.
func NewSweepGateCommandClassifier(python, script string) SweepGateClassifier {
	return &sweepGateCommandClassifier{python: python, script: script}
}

type sweepGateCommandClassifier struct {
	python string
	script string
}

// Classify runs the classifier once for one subject.
//
// ⚠️ It receives the same state dir as the gate: the classifier keeps its
// once-per-error marker there, so a shared dir would let one subject's crash
// silence another's report.
func (c *sweepGateCommandClassifier) Classify(
	ctx context.Context,
	subject string,
	vault string,
	gateVerdict string,
	stateDir string,
) (SweepGateClassifyResult, error) {
	commandCtx, cancel := context.WithTimeout(ctx, sweepGateClassifyTimeout)
	defer cancel()
	// #nosec G204 -- see the gate's note: the script is a compile-time constant and
	// the variable arguments are argv words.
	cmd := exec.CommandContext(
		commandCtx,
		c.python,
		c.script,
		"--topic", subject,
		"--vault", vault,
		"--gate-verdict", gateVerdict,
	)
	cmd.Env = append(os.Environ(), sweepGateStateDirEnv+"="+stateDir)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	if err == nil {
		return SweepGateClassifyResult{
			ExitCode: sweepGateClassifyQuietCode,
			Output:   output.String(),
		}, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return SweepGateClassifyResult{ExitCode: exitErr.ExitCode(), Output: output.String()}, nil
	}
	return SweepGateClassifyResult{}, errors.Wrapf(
		ctx,
		err,
		"run sweep gate classifier '%s' failed",
		c.script,
	)
}

// NewSweepGateCommandAdhocPass builds the ad-hoc park pass over its script.
func NewSweepGateCommandAdhocPass(python, script string) SweepGateAdhocPass {
	return &sweepGateCommandAdhocPass{python: python, script: script}
}

type sweepGateCommandAdhocPass struct {
	python string
	script string
}

// Run runs the ad-hoc pass once against the ad-hoc state dir.
//
// ⚠️ SWEEP_GATE_ADHOC_STATE is set for the CHILD only, and it points under the
// tick's own state dir: the pass consumes parks it has already reported, so a
// shared or default location would let an isolated run (a probe, the
// induced-failure E2E) eat the live parks it was only meant to observe.
func (p *sweepGateCommandAdhocPass) Run(
	ctx context.Context,
	stateDir string,
) (SweepGateAdhocResult, error) {
	commandCtx, cancel := context.WithTimeout(ctx, sweepGateAdhocTimeout)
	defer cancel()
	// #nosec G204 -- the interpreter and the script are compile-time constants;
	// the pass takes no arguments, so no gate output reaches the command line.
	cmd := exec.CommandContext(commandCtx, p.python, p.script)
	cmd.Env = append(
		os.Environ(),
		sweepGateAdhocStateEnv+"="+filepath.Join(stateDir, sweepGateAdhocStateFileName),
	)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	if err == nil {
		return SweepGateAdhocResult{ExitCode: sweepGateTickEmptyCode, Output: output.String()}, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return SweepGateAdhocResult{ExitCode: exitErr.ExitCode(), Output: output.String()}, nil
	}
	return SweepGateAdhocResult{}, errors.Wrapf(
		ctx,
		err,
		"run sweep gate ad-hoc pass '%s' failed",
		p.script,
	)
}

// NewSweepGateCommandCardPoster builds the poster over attention-ask.py.
func NewSweepGateCommandCardPoster(python, script string) SweepGateCardPoster {
	return &sweepGateCommandCardPoster{python: python, script: script}
}

type sweepGateCommandCardPoster struct {
	python string
	script string
}

// Post posts one card and returns the poster's own output.
//
// ⚠️ The script's path, the producer id and the producer kind are constants;
// only the card's own fields are variable. That is what keeps a gate's verdict
// from steering the poster, and it is why the command line is never logged.
func (p *sweepGateCommandCardPoster) Post(
	ctx context.Context,
	card SweepGateCard,
) (string, error) {
	args := make([]string, 0, 14+2*len(card.Options))
	args = append(args,
		p.script,
		"post",
		"--dedup-key", card.DedupKey,
		"--payload", card.Payload,
		"--context", card.Context,
		"--recommend", card.Recommend,
		"--producer-id", sweepGateCardProducerID,
		"--producer-kind", sweepGateCardProducerKind,
	)
	for _, option := range card.Options {
		args = append(args, "--option", option)
	}
	commandCtx, cancel := context.WithTimeout(ctx, sweepGateCardTimeout)
	defer cancel()
	// #nosec G204 -- the script and the producer identity are compile-time
	// constants; every variable argument is an argv word, never a shell word.
	cmd := exec.CommandContext(commandCtx, p.python, args...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return output.String(), errors.Wrapf(
			ctx,
			err,
			"post card '%s' failed",
			card.DedupKey,
		)
	}
	return output.String(), nil
}

// NewDefaultSweepGateNotifyJob builds the job against the paths the launchd job
// used: the three scripts, the write script, the poster, and the state directory.
//
// ⚠️ Resolving them here rather than in main keeps the job's paths beside the
// job's semantics, and returns an error instead of a job with an unresolvable
// home directory — a job armed on a guessed path would tick nothing and look
// exactly like a quiet pass.
func NewDefaultSweepGateNotifyJob(ctx context.Context) (run.Func, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, errors.Wrap(ctx, err, "resolve home dir failed")
	}
	personal := filepath.Join(home, "Documents", "Obsidian", "private-personal")
	scripts := filepath.Join(home, ".claude", "scripts")
	return NewSweepGateNotifyJob(
		NewSweepGateCommandSubjectLister(
			sweepGatePythonBinary,
			filepath.Join(scripts, "sweep-gate-subjects.py"),
			filepath.Join(home, ".claude", "state", "worker-manager"),
		),
		NewSweepGateCommandGate(
			sweepGatePythonBinary,
			filepath.Join(personal, ".claude", "scripts", "sweep-gate.py"),
		),
		NewSweepGateCommandClassifier(
			sweepGatePythonBinary,
			filepath.Join(scripts, "sweep-gate-classify.py"),
		),
		NewSweepGateCommandAdhocPass(
			sweepGatePythonBinary,
			filepath.Join(scripts, "sweep-gate-adhoc.py"),
		),
		// ⚠️ The notifier is the fleet-digest job's implementation: the write
		// script, the environment and the notification path are the same
		// constants, and a second copy would be a second place to update when the
		// write path changes.
		NewFleetDigestCommandNotifier(
			filepath.Join(personal, ".claude", "scripts", "trading-api-write.sh"),
		),
		NewSweepGateCommandCardPoster(
			sweepGatePythonBinary,
			filepath.Join(
				home,
				".claude",
				"plugins",
				"marketplaces",
				"claude-supervisor",
				"scripts",
				"attention-ask.py",
			),
		),
		filepath.Join(home, ".claude", "state", "sweep-gate-loop"),
		SweepGateNotifyDefaultInterval,
		libtime.NewCurrentDateTime(),
	), nil
}
