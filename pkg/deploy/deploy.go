// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package deploy gates a launchd deploy of the attention board on the board
// actually serving, and restores the previous binary when it does not.
//
// The outage this exists to end (2026-11-17, 06:30–07:04) was not a crash: nine
// consecutive starts reached `application started` and never bound 18080, and a
// tenth sat alive and silent for 22 minutes before serving. Every one of them
// was a successful `make install` by every check the deploy had. The gate is
// therefore defined on the observable — does the board answer, and does the
// process answering carry the build that was just installed — rather than on
// the exit status of the build.
//
// ⚠️ Readiness is proven from the SERVING process's own footer, not from the
// file on disk. `go version -m <path>` answers "which build is installed", which
// is the wrong question: a stale process keeps answering the old commit after
// the file has been replaced, so a rollback that silently failed would look
// identical to one that worked.
package deploy

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/buildidentity"
)

const (
	// ReadinessTimeout is how long a freshly started binary may take to serve
	// before its start counts as a failure.
	ReadinessTimeout = 90 * time.Second

	// RollbackBudget is the most a rollback may add on top of the readiness
	// timeout. The two are chosen to sum to two minutes, so the deploy's whole
	// outage is bounded by ReadinessTimeout + RollbackBudget however long the
	// build took — `t_deploy_start` is the launchctl bootstrap, never the
	// compile.
	RollbackBudget = 30 * time.Second

	// PollInterval is how often the board is probed while a start is pending.
	// Two seconds keeps a healthy start's detection inside a few seconds while
	// costing one request per interval during a failure.
	PollInterval = 2 * time.Second

	// ConsentAction is the operator action an alive-no-listener start needs. It
	// is the literal the failure card must carry, because the observable
	// (alive, no listener, resuming without a restart) matches the macOS TCC
	// "Documents access" signature recorded on 2026-10-03 and again on
	// 2026-11-17. The driver names the action and never asserts the cause: the
	// unified log carried no tccd line, so the cause is unproven while the
	// action is the one that clears the signature.
	ConsentAction = "Grant Documents access to attention-controller " +
		"(System Settings › Privacy & Security › Documents)"

	// PrevUnknown is what a rollback-failure line carries when no previous
	// binary was retained. With no copy there is no previous commit to name,
	// and inventing one would be worse than saying so.
	PrevUnknown = "prev=unknown (no .prev)"

	// UnstampedCommit is what the driver reports for a binary that carries no
	// VCS stamp. A deploy cannot be gated on such a binary: nothing it serves
	// can be proven to be the build that was installed, and the board would
	// render "no build identity" in its footer.
	UnstampedCommit = "commit=unknown (no vcs stamp)"
)

// FailureClass is which kind of failed start the driver observed. The two are
// never conflated, because they call for different operator actions: one is a
// consent grant, the other is a crash with a status to read.
type FailureClass string

const (
	// AliveNoListener is a process launchd still holds with no listener on the
	// board port. It is the TCC signature's observable.
	AliveNoListener FailureClass = "alive-no-listener"

	// Exited is a process that is gone, with launchd's last exit code if it
	// recorded one.
	Exited FailureClass = "exited"

	// StopFailed is a job launchd never let go of, so no new process was ever
	// started. It is its own class because the two observables above would both
	// lie here: launchd still reports the OLD pid, so `alive-no-listener` would
	// send the operator to a consent pane for what is a launchctl problem, and
	// `exited` would claim a crash that never happened.
	StopFailed FailureClass = "stop-failed"
)

// Mode is which entry point drove the deploy. It reaches the log lines only —
// the gate and the rollback are identical for all of them, which is the point
// of wiring them through one target.
type Mode string

const (
	// InstallMode is `make install`: a build, a binary swap, then a restart.
	InstallMode Mode = "install"

	// ReloadMode is `make launchd-reload`: a restart of whatever binary is in
	// place, with no rebuild. It carries the same gate because a restart alone
	// strands the board exactly the same way.
	ReloadMode Mode = "reload"
)

// Outcome is what a deploy ended as, and the exit code the command maps it to.
type Outcome int

const (
	// Healthy is a start that served the installed build inside the timeout.
	Healthy Outcome = iota

	// RolledBack is a start that failed and whose previous binary now serves.
	// The board is up; the deploy is not.
	RolledBack

	// RollbackFailed is a start that failed and left the board dark, either
	// because no previous binary was retained or because that one failed too.
	RollbackFailed

	// Aborted is a deploy refused BEFORE anything was restarted, so the board is
	// exactly as it was. It is a separate outcome because the exit code is the
	// alerting signal: reporting `RollbackFailed` for it would fire a
	// dark-board alarm for a deploy that never touched the running process.
	Aborted
)

// ProcessStatus is what launchd says about the job right now.
type ProcessStatus struct {
	// Running is whether launchd holds a live pid for the job.
	Running bool
	// Pid is the live pid, or 0 when Running is false.
	Pid int
	// LastExit is launchd's own rendering of the last exit code, e.g. "1" or
	// "(never exited)". It is a string because that is the form launchd prints
	// and because "(never exited)" is a real state rather than a missing value.
	LastExit string
}

// ProbeResult is one observation of the board.
type ProbeResult struct {
	// Ready is whether the board answered and the commit it serves is the one
	// expected.
	Ready bool
	// ServingCommit is the commit the serving process reports in its footer, or
	// empty when the footer could not be read.
	ServingCommit string
	// Detail explains a not-ready result in one line.
	Detail string
}

// Supervisor is the launchd control surface the driver drives.
type Supervisor interface {
	// Stop boots the job out and returns once launchd no longer holds it.
	Stop(ctx context.Context) error
	// Bootstrap starts the job and returns once launchd has accepted it.
	Bootstrap(ctx context.Context) error
	// Status reports what launchd says about the job.
	Status(ctx context.Context) (ProcessStatus, error)
}

// Prober reports whether the board serves the build that was installed.
type Prober interface {
	Probe(ctx context.Context, expectedCommit string) ProbeResult
}

// Binary is the installed artifact and the previous one retained beside it.
type Binary interface {
	// Path is the installed binary's path.
	Path() string
	// Identity is the installed binary's own build identity.
	Identity(ctx context.Context) (buildidentity.Identity, error)
	// HasPrev reports whether a previous binary was retained.
	HasPrev() bool
	// PrevIdentity is the retained binary's own build identity.
	PrevIdentity(ctx context.Context) (buildidentity.Identity, error)
	// RestorePrev puts the retained binary back in place, atomically.
	RestorePrev(ctx context.Context) error
}

// CardPoster posts the operator-facing failure card to the board.
type CardPoster interface {
	Post(ctx context.Context, request pkg.PushRequest) error
}

// Driver gates one deploy on the board serving and rolls back when it does not.
type Driver struct {
	// Mode is which entry point drove this deploy; it reaches the log only.
	Mode Mode
	// Supervisor drives the launchd job.
	Supervisor Supervisor
	// Prober observes the board.
	Prober Prober
	// Binary is the installed artifact.
	Binary Binary
	// Board posts the failure card. It is only ever called after a rollback has
	// the board serving again, because until then no store exists to post to.
	Board CardPoster
	// ReadinessTimeout bounds the first start. Zero means ReadinessTimeout.
	ReadinessTimeout time.Duration
	// RollbackBudget bounds the rollback. Zero means RollbackBudget.
	RollbackBudget time.Duration
	// PollInterval is how often the board is probed. Zero means PollInterval.
	PollInterval time.Duration
	// Now reports the current time. Defaults to time.Now.
	Now func() time.Time
	// Sleep waits. Defaults to time.Sleep.
	Sleep func(time.Duration)
	// Log receives the timestamped driver lines. Defaults to io.Discard.
	Log io.Writer
}

// outcomeError carries an Outcome alongside the one-line reason.
//
// It exists so the command can map an outcome to an exit code without parsing
// the message: a rolled-back deploy and a dark board are both non-zero exits but
// they mean different things to whatever called the deploy.
type outcomeError struct {
	outcome Outcome
	message string
}

// Error returns the one-line reason.
//
// ⚠️ The message must stay single-line: it is printed to stderr as the whole
// signal for a dark board, and a multi-line error would turn one line into
// several for no gain.
func (e *outcomeError) Error() string {
	return e.message
}

// Outcome returns the deploy's outcome.
func (e *outcomeError) Outcome() Outcome {
	return e.outcome
}

// newOutcomeError builds a classified error. Every failure the driver returns
// after it has decided what happened goes through here, so the outcome and the
// message can never disagree.
func newOutcomeError(outcome Outcome, format string, args ...any) error {
	return &outcomeError{outcome: outcome, message: fmt.Sprintf(format, args...)}
}

// OutcomeOf reads the Outcome an error carries, defaulting to RollbackFailed
// for an error the driver did not classify — a gate that cannot say what
// happened must never report the board as serving.
func OutcomeOf(err error) Outcome {
	if classified, ok := err.(*outcomeError); ok {
		return classified.outcome
	}
	return RollbackFailed
}
