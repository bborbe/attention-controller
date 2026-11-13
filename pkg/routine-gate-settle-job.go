// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"time"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
)

// RoutineGateSettleInterval is how often the originate arm looks for a routine
// gate to settle. Two seconds matches the delivery arm's poll.
const RoutineGateSettleInterval = 2 * time.Second

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
// resolve inside the session's workspace, and only for a session the wrapper
// owns. Everything else is left open with the refusing rule recorded, which is
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

// NewRoutineGateSettleJob creates the originate arm.
func NewRoutineGateSettleJob(
	store AttentionStore,
	wrapper ClaudeWrapperClient,
	policy RoutinePolicy,
	sessions RoutineGateSessionReader,
	interval time.Duration,
) RoutineGateSettleJob {
	return &routineGateSettleJob{
		store:    store,
		wrapper:  wrapper,
		policy:   policy,
		sessions: sessions,
		interval: interval,
	}
}

type routineGateSettleJob struct {
	store    AttentionStore
	wrapper  ClaudeWrapperClient
	policy   RoutinePolicy
	sessions RoutineGateSessionReader
	interval time.Duration
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
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return nil
		}
		j.settle(ctx, item)
	}
	return nil
}

// settle decides and delivers one gate.
func (j *routineGateSettleJob) settle(ctx context.Context, item Item) {
	if item.State != OpenState || item.AnswerMechanism != PermissionAnswerMechanism {
		return
	}
	sessionID, ok := itemSessionID(item)
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
	rule, allowed := j.policy.RoutineRule(question.Raw, entry.CWD)
	if !allowed {
		// Recorded, not silent: the refusal half of the criterion reads the
		// rule that declined, and a policy that only ever said yes would
		// satisfy a presence check while deciding nothing.
		glog.V(2).Infof(
			"job=attention-watcher action=hold_routine_gate item=%s rule=%s",
			item.ItemID,
			rule,
		)
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
	case DeliveryRefuse:
		j.recordAttempt(ctx, item, attentionWatcherCarrier, FailedOutcome)
	default:
		// retry / resolved: nothing to record yet.
	}
}

// recordAttempt writes the trail entry, logging rather than failing.
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
