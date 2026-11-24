// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package deploy

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/bborbe/errors"

	"github.com/bborbe/attention-controller/pkg/buildidentity"
)

// maxPolls bounds the poll loop independently of the clock. The deadline is the
// real bound; this only stops a frozen or injected clock from spinning forever.
const maxPolls = 1000

// stopProbeTimeout bounds the one board probe taken after a failed stop. It is
// short and independent of the caller's context on purpose: the stop failed
// precisely because that context was cancelled, and the question — is anything
// still serving? — must still get an answer.
const stopProbeTimeout = 10 * time.Second

// Run gates the deploy and returns its outcome.
//
// The order is load-bearing. The identity of the INSTALLED binary is read
// before anything is restarted, so an unstamped build is refused rather than
// started: once it is running, nothing it serves can be proven to be the build
// that was installed, and the board would answer a footer carrying no identity.
func (d *Driver) Run(ctx context.Context) (Outcome, error) {
	d.applyDefaults()
	installed, err := d.Binary.Identity(ctx)
	if err != nil {
		return Aborted, errors.Wrap(ctx, err, "read installed binary identity failed")
	}
	if !installed.Known() {
		d.logf("deploy aborted %s binary=%s", UnstampedCommit, d.Binary.Path())
		return Aborted, errors.Errorf(
			ctx,
			"deploy aborted: %s; binary=%s cannot be proven to serve",
			UnstampedCommit,
			d.Binary.Path(),
		)
	}
	expected := installed.Commit
	if d.Expect != "" && !sameCommit(installed.Commit, d.Expect) {
		// ⚠️ Abort BEFORE the bootout. The build the caller asked for is not the
		// build on disk, so restarting would kill the running process and start
		// the SAME stale binary — a restart dressed as a deploy, which is exactly
		// the exit-0 no-op measured on 2026-11-23 and 2026-11-24.
		d.logf(
			"deploy aborted %s installed=%s expected=%s binary=%s",
			PromoteNotRun, installed.Commit, d.Expect, d.Binary.Path(),
		)
		return Aborted, errors.Errorf(
			ctx,
			"deploy aborted: %s; %s carries commit %s but the checkout is %s",
			PromoteNotRun, d.Binary.Path(), installed.Commit, d.Expect,
		)
	}
	t0 := d.Now()
	d.logf(
		"deploy start t_deploy_start=%s mode=%s commit=%s version=%s binary=%s",
		formatTime(t0), d.Mode, expected, installed.Version, d.Binary.Path(),
	)
	probe, startErr := d.startAndWait(ctx, t0.Add(d.ReadinessTimeout), expected)
	if startErr != nil {
		// The job was never stopped, so nothing was replaced: the old process is
		// still serving and the new binary is only on disk. Reverting would be a
		// silent no-op dressed as a rollback, and classifying it from launchd
		// would read the OLD pid as an alive-no-listener start.
		return d.stopFailed(ctx, installed, startErr)
	}
	if probe.Ready {
		d.logf(
			"deploy ready commit=%s serving_commit=%s elapsed_s=%.1f",
			installed.Commit, probe.ServingCommit, d.Now().Sub(t0).Seconds(),
		)
		return Healthy, nil
	}
	status, statusErr := d.Supervisor.Status(ctx)
	class := classify(status, statusErr)
	d.logf(
		"deploy failed t_fail_detected=%s class=%s pid=%d last_exit=%q serving_commit=%s detail=%q",
		formatTime(d.Now()), class, status.Pid, status.LastExit, probe.ServingCommit, probe.Detail,
	)
	if statusErr != nil {
		d.logf("deploy status unreadable error=%q", statusErr.Error())
	}
	return d.rollback(ctx, Failure{
		Failed:     installed,
		Class:      class,
		Mode:       d.Mode,
		Status:     status,
		BinaryPath: d.Binary.Path(),
	}, t0)
}

// rollback restores the previous binary, restarts, and posts the card once the
// board serves again.
//
// ⚠️ The card is posted LAST, and only after the previous board answers. The
// driver runs outside the failed binary, but the card store lives inside it: a
// card posted before the rollback has nowhere to land, which is why the
// rollback-failure path below claims no card at all.
func (d *Driver) rollback(ctx context.Context, failure Failure, t0 time.Time) (Outcome, error) {
	if !d.Binary.HasPrev() {
		return d.dark(ctx, failure, buildidentity.Identity{}, PrevUnknown)
	}
	prev, err := d.Binary.PrevIdentity(ctx)
	if err != nil {
		return d.dark(ctx, failure, buildidentity.Identity{}, "prev unreadable: "+err.Error())
	}
	// ⚠️ A retained copy with no VCS stamp cannot say which build it is, and an
	// empty expected commit makes the prober skip the footer check entirely —
	// so the rollback would report "rolled back to , board serving" while a
	// stale process from the FAILED build could still be the one answering.
	// Refusing here is the only answer that cannot be a false success.
	if !prev.Known() {
		return d.dark(ctx, failure, buildidentity.Identity{}, "prev has no build identity")
	}
	failure.Prev = prev
	if err := d.Binary.RestorePrev(ctx); err != nil {
		return d.dark(ctx, failure, prev, "restore failed: "+err.Error())
	}
	// The rollback's own deadline is absolute: whatever the failed start spent
	// of the budget is already gone, so readiness plus rollback can never sum
	// past the two minutes however long the first phase ran.
	probe, startErr := d.startAndWait(ctx, t0.Add(d.ReadinessTimeout+d.RollbackBudget), prev.Commit)
	if startErr != nil {
		return d.dark(ctx, failure, prev, "restart for rollback failed: "+startErr.Error())
	}
	if !probe.Ready {
		return d.dark(ctx, failure, prev, "prev did not serve: "+probe.Detail)
	}
	tReady := d.Now()
	d.logf(
		"deploy rolled back t_ready_after_rollback=%s commit=%s delta_s=%.1f",
		formatTime(tReady), prev.Commit, tReady.Sub(t0).Seconds(),
	)
	card := failure.Card()
	if err := d.Board.Post(ctx, card); err != nil {
		// The board serves but the operator was not told. That is a failure of
		// the deploy's own report, and never a reason to claim the board is
		// dark: the outcome stays RolledBack.
		return RolledBack, errors.Errorf(
			ctx,
			"deploy failed: commit %s did not serve (%s); rolled back to %s "+
				"but the card could not be posted: %v",
			failure.Failed.Commit, failure.Class, prev.Commit, err,
		)
	}
	d.logf("deploy card posted dedup_key=%s", card.DedupKey)
	return RolledBack, errors.Errorf(
		ctx,
		"deploy failed: commit %s did not serve (%s); rolled back to %s, board serving",
		failure.Failed.Commit, failure.Class, prev.Commit,
	)
}

// stopFailed reports a deploy that never restarted the job.
//
// ⚠️ Nothing is reverted and nothing is classified from launchd. The old process
// is still the one serving, so a "rollback" would copy an identical binary over
// itself, and reading launchd would report the OLD pid as an alive-no-listener
// start and send the operator to a consent pane for a launchctl problem.
func (d *Driver) stopFailed(
	ctx context.Context,
	installed buildidentity.Identity,
	stopErr error,
) (Outcome, error) {
	d.logf(
		"deploy aborted t_aborted=%s class=%s commit=%s reason=%q",
		formatTime(d.Now()), StopFailed, installed.Commit, stopErr.Error(),
	)
	// ⚠️ Probe before claiming the board is untouched. A cancellation inside the
	// bootout wait lands AFTER launchd may already have let the job go, so "the
	// stop did not return cleanly" does not imply "the process still serves".
	// Asking the board — with no expected commit, since whatever runs is the OLD
	// build — is the only way to tell a no-op abort from a dark board.
	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopProbeTimeout)
	defer cancel()
	if !d.Prober.Probe(probeCtx, "").Ready {
		return d.dark(
			ctx,
			Failure{
				Failed:     installed,
				Class:      StopFailed,
				Mode:       d.Mode,
				BinaryPath: d.Binary.Path(),
			},
			buildidentity.Identity{},
			"the job was stopped but the board does not answer",
		)
	}
	failure := Failure{
		Failed:     installed,
		Prev:       installed,
		Class:      StopFailed,
		Mode:       d.Mode,
		BinaryPath: d.Binary.Path(),
	}
	if err := d.Board.Post(ctx, failure.Card()); err != nil {
		d.logf("deploy card failed error=%q", err.Error())
	}
	return Aborted, errors.Errorf(
		ctx,
		"deploy aborted: launchctl did not release the job (%v); commit %s is on disk "+
			"but the running build is untouched",
		stopErr, installed.Commit,
	)
}

// dark reports a rollback that could not bring the board back.
//
// The one-line message plus the non-zero exit code IS the whole signal here: no
// store exists to post a card to, so claiming one would be a lie the operator
// could not check.
func (d *Driver) dark(
	ctx context.Context,
	failure Failure,
	prev buildidentity.Identity,
	reason string,
) (Outcome, error) {
	d.logf(
		"deploy rollback failed t_rollback_failed=%s commit=%s prev=%s reason=%q",
		formatTime(d.Now()), failure.Failed.Commit, prev.Commit, reason,
	)
	if prev.Known() {
		return RollbackFailed, errors.Errorf(
			ctx,
			"deploy failed: commit %s did not serve (%s); rollback to %s also failed (%s); "+
				"board is dark",
			failure.Failed.Commit,
			failure.Class,
			prev.Commit,
			reason,
		)
	}
	return RollbackFailed, errors.Errorf(
		ctx,
		"deploy failed: commit %s did not serve (%s); %s; board is dark",
		failure.Failed.Commit, failure.Class, reason,
	)
}

// startAndWait restarts the job and polls until the board serves the expected
// commit or the deadline passes.
//
// A non-nil error means the job was never stopped: the caller must abort rather
// than roll back, because nothing was replaced. A failed BOOTSTRAP is not that
// case — the old process is gone and the board is dark — so it comes back as a
// not-ready result and takes the ordinary rollback path.
func (d *Driver) startAndWait(
	ctx context.Context,
	deadline time.Time,
	expectedCommit string,
) (ProbeResult, error) {
	if err := d.Supervisor.Stop(ctx); err != nil {
		return ProbeResult{}, err
	}
	if err := d.Supervisor.Bootstrap(ctx); err != nil {
		return ProbeResult{Detail: "bootstrap failed: " + err.Error()}, nil
	}
	return d.waitUntil(ctx, deadline, expectedCommit), nil
}

// waitUntil polls the board every PollInterval until it is ready or the
// deadline passes.
func (d *Driver) waitUntil(
	ctx context.Context,
	deadline time.Time,
	expectedCommit string,
) ProbeResult {
	var result ProbeResult
	for i := 0; i < maxPolls; i++ {
		// A cancelled deploy stops polling rather than running the deadline out:
		// the caller has gone, and continuing would restart nothing but would
		// keep a request stream open against a board that may be the thing that
		// failed.
		if err := ctx.Err(); err != nil {
			result.Detail = "cancelled: " + err.Error()
			return result
		}
		result = d.Prober.Probe(ctx, expectedCommit)
		if result.Ready {
			return result
		}
		if !d.Now().Before(deadline) {
			return result
		}
		d.Sleep(d.PollInterval)
	}
	return result
}

// sameCommit reports whether two commit ids name the same revision.
//
// ⚠️ A plain `==` is wrong here. The installed identity is shortened for display
// (12 hex chars, so the board footer and the log lines stay readable) while a
// checkout's HEAD is the full 40-char sha that `make install` passes as
// `-expect`. An exact comparison therefore aborts a perfectly good deploy — the
// opposite of the stale-binary case this guard exists for. A prefix match in
// either direction, with a floor below git's 7-char abbreviation, is the test.
func sameCommit(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	return len(a) >= 7 && strings.HasPrefix(b, a)
}

// classify names the failure class from launchd's own view of the job.
//
// ⚠️ An unreadable status is classified Exited, and that direction is
// deliberate: the consent literal belongs to a start PROVEN alive, so an
// unknown state must not carry it. The read error reaches the log and the card's
// context, never the payload.
func classify(status ProcessStatus, statusErr error) FailureClass {
	if statusErr != nil {
		return Exited
	}
	if status.Running {
		return AliveNoListener
	}
	return Exited
}

// applyDefaults fills the zero values so a caller may set only what it cares
// about.
func (d *Driver) applyDefaults() {
	if d.ReadinessTimeout == 0 {
		d.ReadinessTimeout = ReadinessTimeout
	}
	if d.RollbackBudget == 0 {
		d.RollbackBudget = RollbackBudget
	}
	if d.PollInterval == 0 {
		d.PollInterval = PollInterval
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Sleep == nil {
		d.Sleep = time.Sleep
	}
	if d.Log == nil {
		d.Log = io.Discard
	}
}

// logf writes one driver line.
func (d *Driver) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(d.Log, format+"\n", args...)
}

// formatTime renders an instant the way the driver lines carry it.
func formatTime(t time.Time) string {
	return t.Format(time.RFC3339)
}
