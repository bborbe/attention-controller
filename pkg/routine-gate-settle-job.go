// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

// RoutineGateSettleInterval is how often the originate arm looks for a routine
// gate to settle. Two seconds matches the delivery arm's poll.
const RoutineGateSettleInterval = 2 * time.Second

// RoutineGateHoldRepeatInterval is how long a gate's recorded refusal stands
// before this arm writes it again.
//
// ⚠️ Without it the arm rewrote the SAME refusal on every tick, forever. The
// decision is a pure function of the gate and the policy, so nothing changed
// between one cycle and the next — but `recordAttempt` is a durable write with
// its own fdatasync, and every write notifies the board, which then re-renders
// every row it holds. Measured 2026-11-16: one held card (92392c00) rewritten
// every 2 s, and the render it woke was the process's largest CPU consumer.
// Five minutes keeps the record current — the operator still reads "a policy
// decided, and it declined" — without paying for it 43,200 times a day.
const RoutineGateHoldRepeatInterval = 5 * time.Minute

// routineGateDecision is the behaviour the arm answers with. A routine gate is
// by definition one the policy admitted, so it is always an allow.
const routineGateDecision = "allow"

// RoutineGateSessionReader is the slice of the session registry the settle arm
// needs: the cwd (the policy's workspace boundary) and the tty (to tell a
// wrapper child from a pane session).
type RoutineGateSessionReader interface {
	// SessionEntry returns the session's registry record.
	SessionEntry(ctx context.Context, sessionID string) (SessionEntry, bool)
	// SessionTTY returns the session's tty, or "" when it has none.
	SessionTTY(ctx context.Context, sessionID string) string
}

// RoutineGateSettleJob ORIGINATES an answer for a wrapper child's routine
// permission gate.
//
// ⚠️ It is the arm that answers a gate the operator never sees, and it is
// deliberately minimal: only a `Read`/`NotebookRead` whose own path arguments
// resolve inside the session's workspace, or a `Bash` command claude-supervisor's
// permission classifier rates `allowlist` from a workspace cwd, and only for a
// session the wrapper owns. Everything else is left open with the refusing rule recorded, which is
// what the operator reads as "a policy decided, and it declined".
//
// It exists because a wrapper-spawned child has no pane: the wrapper owns its
// stdin, so the pane carrier has nothing to type into and the wrapper's answer
// route is the only way to move the child.
type RoutineGateSettleJob interface {
	// Run settles routine gates until the context is cancelled. It returns nil
	// on cancellation and logs-and-continues on a failed cycle.
	Run(ctx context.Context) error

	// SettleOnce runs one settle cycle. It is a seam so a spec can drive a
	// cycle deterministically instead of racing a ticker.
	SettleOnce(ctx context.Context) error
}

// RoutineGateSettleOption customises the settle arm at construction.
type RoutineGateSettleOption func(*routineGateSettleJob)

// WithRoutineGateSettleSignaller sets the helper that emits the ANSWERED signal
// after a card is delivered. ⚠️ This arm settles OPEN gates, and an open card
// classifies NOTHING, so the call is a no-op here; it is wired so every
// DeliveryOK site reaches the helper uniformly.
func WithRoutineGateSettleSignaller(
	signaller AnsweredSignaller,
) RoutineGateSettleOption {
	return func(job *routineGateSettleJob) {
		job.signaller = signaller
	}
}

// NewRoutineGateSettleJob creates the originate arm.
//
// currentDateTimeGetter is injected rather than reached for, because the hold
// interval is a decision about elapsed time and a spec has to be able to move
// the clock instead of sleeping through it.
func NewRoutineGateSettleJob(
	store AttentionStore,
	wrapper ClaudeWrapperClient,
	policy RoutinePolicy,
	sessions RoutineGateSessionReader,
	interval time.Duration,
	currentDateTimeGetter libtime.CurrentDateTimeGetter,
	options ...RoutineGateSettleOption,
) RoutineGateSettleJob {
	job := &routineGateSettleJob{
		store:                 store,
		wrapper:               wrapper,
		policy:                policy,
		sessions:              sessions,
		interval:              interval,
		currentDateTimeGetter: currentDateTimeGetter,
	}
	for _, option := range options {
		option(job)
	}
	return job
}

type routineGateSettleJob struct {
	store                 AttentionStore
	wrapper               ClaudeWrapperClient
	policy                RoutinePolicy
	sessions              RoutineGateSessionReader
	interval              time.Duration
	currentDateTimeGetter libtime.CurrentDateTimeGetter
	signaller             AnsweredSignaller
}

// Run settles routine gates until the context is cancelled.
func (j *routineGateSettleJob) Run(ctx context.Context) error {
	if j.interval <= 0 {
		return errors.Errorf(ctx, "routine gate settle interval must be positive")
	}
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			glog.V(2).Infof("routine gate settle stopped")
			return nil
		case <-ticker.C:
			if err := j.SettleOnce(ctx); err != nil {
				glog.Warningf("job=attention-watcher action=settle_cycle_error error=%v", err)
			}
		}
	}
}

// SettleOnce settles every open routine permission gate it may decide.
func (j *routineGateSettleJob) SettleOnce(ctx context.Context) error {
	items, err := j.store.Read(ctx)
	if err != nil {
		return errors.Wrap(ctx, err, "read open items failed")
	}
	// Read once for the whole cycle, and only for the items in hand. ⚠️ NOT
	// DeliveryAttempts: that scans the whole attempts bucket, which has no delete
	// path and so grows with every card the store has ever held — an unbounded
	// read on a fixed two-second cadence. DeliveryReports does one transaction
	// with one key lookup per open item instead.
	reports, err := j.store.DeliveryReports(ctx, items)
	if err != nil {
		return errors.Wrap(ctx, err, "read delivery reports failed")
	}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return nil
		}
		j.settle(ctx, item, reports)
	}
	j.retainOpen(items)
	return nil
}

// retainOpen lets an item-aware policy drop the verdicts of items that are no
// longer open, so its cache holds only what the next cycle can still ask about.
func (j *routineGateSettleJob) retainOpen(items Items) {
	itemPolicy, ok := j.policy.(RoutineItemPolicy)
	if !ok {
		return
	}
	open := make(map[ItemID]struct{}, len(items))
	for _, item := range items {
		if item.State == OpenState {
			open[item.ItemID] = struct{}{}
		}
	}
	itemPolicy.RetainItems(open)
}

// rule asks the policy, through its item-aware entry point when it has one.
func (j *routineGateSettleJob) rule(
	ctx context.Context,
	item Item,
	question *WrapperQuestion,
	cwd string,
) (string, bool) {
	if itemPolicy, ok := j.policy.(RoutineItemPolicy); ok {
		return itemPolicy.RoutineRuleForItem(ctx, item.ItemID, question.Raw, cwd)
	}
	return j.policy.RoutineRule(question.Raw, cwd)
}

// settle decides and delivers one gate.
func (j *routineGateSettleJob) settle(
	ctx context.Context,
	item Item,
	reports map[ItemID]*DeliveryReport,
) {
	if item.State != OpenState || item.AnswerMechanism != PermissionAnswerMechanism {
		return
	}
	sessionID, ok := itemSessionID(ctx, item)
	if !ok {
		return
	}
	entry, ok := j.sessions.SessionEntry(ctx, sessionID)
	if !ok || !processAlive(entry.PID) {
		return
	}
	// A session with a tty is answered by the pane carrier, not here.
	if j.sessions.SessionTTY(ctx, sessionID) != "" {
		return
	}
	state, session := j.wrapper.Probe(ctx, sessionID)
	if state != WrapperProbeOK || session == nil || session.Question == nil {
		return
	}
	question := session.Question
	rule, allowed := j.rule(ctx, item, question, entry.CWD)
	if !allowed {
		// Recorded, not silent: the refusal half of the criterion reads the
		// rule that declined, and a policy that only ever said yes would
		// satisfy a presence check while deciding nothing.
		glog.V(2).Infof(
			"job=attention-watcher action=hold_routine_gate item=%s rule=%s",
			item.ItemID,
			rule,
		)
		// ⚠️ The decision is a pure function of the gate and the policy, so a
		// refusal already on record under the same rule says nothing new. See
		// RoutineGateHoldRepeatInterval: rewriting it every tick was a durable
		// write per cycle whose only other effect was a full board re-render.
		if j.holdRecorded(reports, item, rule) {
			return
		}
		j.recordAttempt(ctx, item, rule, HeldOutcome)
		return
	}
	verdict, reason := j.wrapper.AnswerViaWrapper(
		ctx,
		sessionID,
		routineGateDecision,
		&question.RequestID,
	)
	glog.V(2).Infof(
		"job=attention-watcher action=settle_routine_gate item=%s rule=%s verdict=%s reason=%q",
		item.ItemID,
		rule,
		verdict,
		reason,
	)
	switch verdict {
	case DeliveryOK:
		j.recordAttempt(ctx, item, attentionWatcherCarrier, DeliveredOutcome)
		// Harmless no-op: this arm only sees open gates, and an open card
		// classifies NOTHING. The call keeps every DeliveryOK site uniform.
		if j.signaller != nil {
			j.signaller.Signal(ctx, sessionID, item)
		}
	case DeliveryRefuse:
		j.recordAttempt(ctx, item, attentionWatcherCarrier, FailedOutcome)
	default:
		// retry / resolved: nothing to record yet.
	}
}

// recordAttempt writes the trail entry, logging rather than failing.
// holdRecorded reports whether this gate's refusal is already on record — same
// arm, same rule, recent enough that writing it again would only cost a durable
// write and the board render it wakes.
//
// A different rule re-records: the refusal is a claim about WHY the gate was
// declined, and a policy that started declining for a new reason must be able
// to say so.
func (j *routineGateSettleJob) holdRecorded(
	reports map[ItemID]*DeliveryReport,
	item Item,
	rule string,
) bool {
	report, ok := reports[item.ItemID]
	if !ok || report == nil || report.AttemptedAt == nil {
		return false
	}
	if report.Status != HeldStatus || report.Carrier != rule {
		return false
	}
	last := report.AttemptedAt.Time()
	return j.currentDateTimeGetter.Now().Time().Before(last.Add(RoutineGateHoldRepeatInterval))
}

func (j *routineGateSettleJob) recordAttempt(
	ctx context.Context,
	item Item,
	carrier string,
	outcome DeliveryOutcome,
) {
	if _, err := j.store.RecordAttempt(ctx, item.ItemID, carrier, outcome); err != nil {
		glog.Warningf(
			"job=attention-watcher action=attempt_error item=%s outcome=%s error=%v",
			item.ItemID,
			outcome,
			err,
		)
	}
}

// Compile-time guards.
var (
	_ RoutineGateSettleJob     = (*routineGateSettleJob)(nil)
	_ RoutineGateSessionReader = (*weztermPaneTextWriter)(nil)
)
