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

// AttentionWatcherDeliveryInterval is how often the delivery arm looks for an
// answered card to carry. Two seconds matches the Python watcher's poll, and it
// is the pace the board's "answered -> delivered" bar is measured at.
const AttentionWatcherDeliveryInterval = 2 * time.Second

// AttentionDeliveryRetryBackoff is how long a card whose delivery FAILED waits
// before this arm tries it again.
//
// ⚠️ Without it the arm re-attempted every failed card on every tick, forever:
// `failed` is not a terminal outcome, so a card that could not be carried stayed
// deliverable and was retried two seconds later, and again, for the life of the
// process. Measured 2026-11-16: ~40 answered cards whose session has no tty, so
// ~20 refusals a second — each one a delivery-attempt write with its own
// fdatasync (13.7% of the process's CPU) that also notified the board and woke a
// full 772-row render. Five minutes turns that into ~0.13 writes a second while
// still retrying often enough that a session which comes back with a tty gets
// its answer promptly.
const AttentionDeliveryRetryBackoff = 5 * time.Minute

// storeReadTimeout bounds the delivery arm's board read.
//
// ⚠️ The store is in-process bbolt, so there is no HTTP hop to bound and no
// outside-arm/delivery-arm split to preserve: ONE bound covers both reads this
// arm makes. The Python watcher's 10 s / 2 s split bounded a loopback round
// trip that does not exist here, which is why it is deliberately not ported.
//
// ⚠️ Without it a store call that never returns wedges the arm for good: the
// two-second ticker stops firing, ctx.Done() is unreachable while the call is
// in flight, and the whole delivery path is silently dead. The bound turns that
// into a log line instead.
//
// ⚠️ It cannot pre-empt a bbolt transaction that has already stopped honouring
// its context — bbolt checks the context as it starts, not mid-commit — so for
// that one case a log is all that can honestly be offered. Same contract as
// answeredSweepTimeout.
const storeReadTimeout = 2 * time.Second

// attentionWatcherCarrier names the arm in the delivery trail. It is the same
// value the Python watcher wrote, so a card's attempt history stays continuous
// across the port.
const attentionWatcherCarrier = "watcher:attention-watcher"

// attentionWatcherAnsweredBy is the `answered_by` the originate arm stamps on a
// record it settled. The delivery arm must recognise its own answer rather than
// try to carry it into a session that has already moved on.
const attentionWatcherAnsweredBy = "attention-watcher"

// AttentionWatcherJob carries a board answer into the session that asked.
//
// ⚠️ It is the one arm that actually carries an answer INTO a session for a tab
// session, which no server holds: the supervisor's store-polling arms only
// settle a park they hold. It exists in-process because the service already owns
// the store, the pane listing and the attempt trail — the Python watcher was a
// second process reaching back across a loopback hop for all three.
//
// The two other duties the Python watcher carried — event-log ingest and the
// originate/menu/compact arms — are separate jobs; this one only delivers an
// answer that already exists.
type AttentionWatcherJob interface {
	// Run delivers answered cards until the context is cancelled. It returns
	// nil on cancellation and logs-and-continues on a failed cycle, because a
	// missed delivery costs one interval and loses nothing — taking the process
	// down for it would drop the board over something the board survives.
	Run(ctx context.Context) error

	// DeliverOnce runs one delivery cycle. It is a seam so a spec can drive a
	// cycle deterministically instead of racing a ticker.
	DeliverOnce(ctx context.Context) error
}

// NewAttentionWatcherJob creates the delivery arm.
//
// The headless arm is a constructor parameter rather than an option because a
// card whose session has no pane has NO other carrier: omitting it is not a
// smaller configuration, it is the silent drop this arm now removes.
//
// currentDateTimeGetter is injected rather than reached for, because the retry
// backoff is a decision about elapsed time and a spec has to be able to move the
// clock instead of sleeping through it.
func NewAttentionWatcherJob(
	store AttentionStore,
	carrier PaneCarrier,
	headless HeadlessAnswerDeliverer,
	interval time.Duration,
	currentDateTimeGetter libtime.CurrentDateTimeGetter,
) AttentionWatcherJob {
	return &attentionWatcherJob{
		store:                 store,
		carrier:               carrier,
		headless:              headless,
		interval:              interval,
		currentDateTimeGetter: currentDateTimeGetter,
	}
}

type attentionWatcherJob struct {
	store                 AttentionStore
	carrier               PaneCarrier
	headless              HeadlessAnswerDeliverer
	interval              time.Duration
	currentDateTimeGetter libtime.CurrentDateTimeGetter
}

// Run delivers answered cards until the context is cancelled.
func (j *attentionWatcherJob) Run(ctx context.Context) error {
	if j.interval <= 0 {
		return errors.Errorf(ctx, "attention watcher interval must be positive")
	}
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			glog.V(2).Infof("attention watcher delivery stopped")
			return nil
		case <-ticker.C:
			if err := j.DeliverOnce(ctx); err != nil {
				// Logged and continued: one failed cycle costs one interval.
				glog.Warningf("job=attention-watcher action=cycle_error error=%v", err)
			}
		}
	}
}

// DeliverOnce delivers every answered card that has not yet been delivered.
func (j *attentionWatcherJob) DeliverOnce(ctx context.Context) error {
	items, reports, err := j.readBoard(ctx)
	if err != nil {
		return err
	}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return nil
		}
		if !j.deliverable(ctx, item, reports) {
			continue
		}
		j.deliver(ctx, item)
	}
	return nil
}

// readBoard reads the board and its delivery reports under storeReadTimeout.
//
// ⚠️ The bound is scoped to the READS, not to the whole cycle: the delivery loop
// below types into panes, which has its own per-call bound, and wrapping that in
// a shared two-second deadline would cut a legitimate delivery.
//
// ⚠️ On a bound the read fails into a log line rather than wedging the arm: the
// error returns to DeliverOnce's caller, whose Run loop logs it and continues on
// the next tick.
func (j *attentionWatcherJob) readBoard(
	ctx context.Context,
) (Items, map[ItemID]*DeliveryReport, error) {
	readCtx, cancel := context.WithTimeout(ctx, storeReadTimeout)
	defer cancel()
	items, err := j.store.ReadBoard(readCtx)
	if err != nil {
		return nil, nil, errors.Wrap(ctx, err, "read board failed")
	}
	// ⚠️ The reports for THESE items, not DeliveryAttempts. That read scans the
	// whole attempts bucket, which has no delete path and so grows with every card
	// the store has ever held — an unbounded scan on a two-second tick. Measured
	// 2026-11-16 after the other arms were fixed: it was the largest single CPU
	// consumer left in the process, 24 % of a 25 s profile.
	reports, err := j.store.DeliveryReports(readCtx, items)
	if err != nil {
		return nil, nil, errors.Wrap(ctx, err, "read delivery reports failed")
	}
	return items, reports, nil
}

// deliverable reports whether the item is an answered card this arm should carry.
func (j *attentionWatcherJob) deliverable(
	ctx context.Context,
	item Item,
	reports map[ItemID]*DeliveryReport,
) bool {
	if item.State != AnsweredState || item.AnsweredAt == nil {
		return false
	}
	// The originate arm settled this one; the session has already moved on.
	if item.AnsweredBy == attentionWatcherAnsweredBy {
		return false
	}
	if report, ok := reports[item.ItemID]; ok && report != nil {
		if report.Status == DeliveredStatus {
			return false
		}
		// ⚠️ A failure of THIS answer is retried, but not on the next tick. See
		// AttentionDeliveryRetryBackoff: without this gate a card that can never
		// be carried is re-attempted every two seconds for the life of the
		// process, and each attempt is a durable write that wakes the board.
		// `retryDue` ignores any record written before the item was answered, so
		// another arm's earlier judgement never delays the operator's answer.
		if !j.retryDue(item, report.AttemptedAt) {
			return false
		}
	}
	if _, ok := itemSessionID(ctx, item); !ok {
		return false
	}
	// A hook menu card is answered by KEYSTROKES into its open menu, not by a
	// prose line — typing text there would submit it into whatever prompt the
	// session is showing. The menu arm owns those; this arm must not race it.
	if _, ok := MenuQuestionsOf(ctx, item); ok {
		return false
	}
	_, ok := answerText(item)
	return ok
}

// deliver carries one answer and records what it observed.
func (j *attentionWatcherJob) deliver(ctx context.Context, item Item) {
	sessionID, ok := itemSessionID(ctx, item)
	if !ok {
		return
	}
	text, ok := answerText(item)
	if !ok {
		return
	}
	// Pane 0 makes the carrier resolve the pane from the session's own tty,
	// which is the correct path for a card whose recorded pane was never ours.
	delivery := &PaneDelivery{SessionID: sessionID, StoreItemID: string(item.ItemID)}
	verdict, reason := j.carrier.TypeIntoPane(ctx, delivery, text, false)
	glog.V(2).Infof(
		"job=attention-watcher action=deliver item=%s verdict=%s reason=%q",
		item.ItemID,
		verdict,
		reason,
	)
	switch verdict {
	case DeliveryOK:
		j.recordAttempt(ctx, item, DeliveredOutcome)
	case DeliveryRefuse:
		j.recordAttempt(ctx, item, FailedOutcome)
	case DeliveryNoPane:
		// ⚠️ A pane-less session is not a failing carrier — it is the normal
		// shape for every headless `sdk-cli` worker, which has no pane by
		// construction. Recording nothing here is what dropped the operator's
		// answer; the wrapper owns that child's stdin, so the turn goes there.
		if j.headless != nil {
			j.headless.DeliverHeadless(ctx, item)
		}
	default:
		// `retry` and `resolved` record nothing: retry has not failed yet, and a
		// resolved gate has nothing left to deliver.
	}
}

// recordAttempt writes the trail entry, logging rather than failing: the
// delivery has already happened, and a missing trail entry must not be reported
// as a failed delivery.
func (j *attentionWatcherJob) recordAttempt(
	ctx context.Context,
	item Item,
	outcome DeliveryOutcome,
) {
	if _, err := j.store.RecordAttempt(ctx, item.ItemID, attentionWatcherCarrier, outcome); err != nil {
		glog.Warningf(
			"job=attention-watcher action=attempt_error item=%s outcome=%s error=%v",
			item.ItemID,
			outcome,
			err,
		)
	}
}

// retryDue reports whether enough time has passed since this item's last attempt
// for another one to be worth making.
//
// ⚠️ An attempt recorded BEFORE the item was answered says nothing about whether
// THIS answer can be carried — it is some arm's earlier judgement about a gate
// that was still open. Gating on it withheld real answers: the routine-gate
// settle arm records a HeldOutcome (and, when its wrapper answer fails, a
// FailedOutcome under this very carrier) while the gate is OPEN, so once the
// operator answered, the delivery arm found a recent non-delivered record and
// sat on the answer for up to the whole backoff. Only a failure of THIS answer
// justifies withholding it, so the comparison is against AnsweredAt, not against
// the carrier name — which the settle arm shares.
func (j *attentionWatcherJob) retryDue(item Item, attemptedAt *libtime.DateTime) bool {
	if attemptedAt == nil {
		return true
	}
	last := attemptedAt.Time()
	if item.AnsweredAt != nil && last.Before(item.AnsweredAt.Time()) {
		return true
	}
	return !j.currentDateTimeGetter.Now().Time().Before(last.Add(AttentionDeliveryRetryBackoff))
}

// itemSessionID returns the session the item belongs to, from its liveness ref.
func itemSessionID(ctx context.Context, item Item) (string, bool) {
	model, value, err := item.LivenessRef.Parse(ctx)
	if err != nil || value == "" {
		return "", false
	}
	if model != SessionLivenessModel && model != OwnerLivenessModel {
		return "", false
	}
	return value, true
}

// answerText returns the operator's answer in the form the session receives.
//
// A `permission` item carries Decision; a `message` item carries Answer.Value.
// An item with neither has no answer to carry, which is what a `skip` reads as.
func answerText(item Item) (string, bool) {
	if item.Decision != "" {
		return string(item.Decision), true
	}
	if item.Answer != nil && item.Answer.Value != "" {
		return item.Answer.Value, true
	}
	return "", false
}

// Compile-time guard: the job must satisfy its own interface.
var _ AttentionWatcherJob = (*attentionWatcherJob)(nil)
