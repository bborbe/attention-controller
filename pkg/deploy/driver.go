// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package deploy

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/bborbe/errors"

	"github.com/bborbe/attention-controller/pkg/buildidentity"
)

// maxPolls bounds the poll loop independently of the clock. The deadline is the
// real bound; this only stops a frozen or injected clock from spinning forever.
const maxPolls = 1000

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
		return RollbackFailed, errors.Wrap(ctx, err, "read installed binary identity failed")
	}
	if !installed.Known() {
		d.logf("deploy aborted %s binary=%s", UnstampedCommit, d.Binary.Path())
		return RollbackFailed, errors.Errorf(
			ctx,
			"deploy aborted: %s; binary=%s cannot be proven to serve",
			UnstampedCommit,
			d.Binary.Path(),
		)
	}
	t0 := d.Now()
	d.logf(
		"deploy start t_deploy_start=%s mode=%s commit=%s version=%s binary=%s",
		formatTime(t0), d.Mode, installed.Commit, installed.Version, d.Binary.Path(),
	)
	probe := d.startAndWait(ctx, t0.Add(d.ReadinessTimeout), installed.Commit)
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
		return d.dark(failure, buildidentity.Identity{}, PrevUnknown)
	}
	prev, err := d.Binary.PrevIdentity(ctx)
	if err != nil {
		return d.dark(failure, buildidentity.Identity{}, "prev unreadable: "+err.Error())
	}
	failure.Prev = prev
	if err := d.Binary.RestorePrev(ctx); err != nil {
		return d.dark(failure, prev, "restore failed: "+err.Error())
	}
	// The rollback's own deadline is absolute: whatever the failed start spent
	// of the budget is already gone, so readiness plus rollback can never sum
	// past the two minutes however long the first phase ran.
	probe := d.startAndWait(ctx, t0.Add(d.ReadinessTimeout+d.RollbackBudget), prev.Commit)
	if !probe.Ready {
		return d.dark(failure, prev, "prev did not serve: "+probe.Detail)
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
		return RolledBack, newOutcomeError(
			RolledBack,
			"deploy failed: commit %s did not serve (%s); rolled back to %s "+
				"but the card could not be posted: %v",
			failure.Failed.Commit, failure.Class, prev.Commit, err,
		)
	}
	d.logf("deploy card posted dedup_key=%s", card.DedupKey)
	return RolledBack, newOutcomeError(
		RolledBack,
		"deploy failed: commit %s did not serve (%s); rolled back to %s, board serving",
		failure.Failed.Commit, failure.Class, prev.Commit,
	)
}

// dark reports a rollback that could not bring the board back.
//
// The one-line message plus the non-zero exit code IS the whole signal here: no
// store exists to post a card to, so claiming one would be a lie the operator
// could not check.
func (d *Driver) dark(
	failure Failure,
	prev buildidentity.Identity,
	reason string,
) (Outcome, error) {
	d.logf(
		"deploy rollback failed t_rollback_failed=%s commit=%s prev=%s reason=%q",
		formatTime(d.Now()), failure.Failed.Commit, prev.Commit, reason,
	)
	if prev.Known() {
		return RollbackFailed, newOutcomeError(
			RollbackFailed,
			"deploy failed: commit %s did not serve (%s); rollback to %s also failed (%s); "+
				"board is dark",
			failure.Failed.Commit,
			failure.Class,
			prev.Commit,
			reason,
		)
	}
	return RollbackFailed, newOutcomeError(
		RollbackFailed,
		"deploy failed: commit %s did not serve (%s); %s; board is dark",
		failure.Failed.Commit, failure.Class, reason,
	)
}

// startAndWait restarts the job and polls until the board serves the expected
// commit or the deadline passes.
func (d *Driver) startAndWait(
	ctx context.Context,
	deadline time.Time,
	expectedCommit string,
) ProbeResult {
	if err := d.Supervisor.Stop(ctx); err != nil {
		return ProbeResult{Detail: "stop failed: " + err.Error()}
	}
	if err := d.Supervisor.Bootstrap(ctx); err != nil {
		return ProbeResult{Detail: "bootstrap failed: " + err.Error()}
	}
	return d.waitUntil(ctx, deadline, expectedCommit)
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
