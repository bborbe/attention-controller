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

// AttentionWatcherDeliveryInterval is how often the delivery arm looks for an
// answered card to carry. Two seconds matches the Python watcher's poll, and it
// is the pace the board's "answered -> delivered" bar is measured at.
const AttentionWatcherDeliveryInterval = 2 * time.Second

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
func NewAttentionWatcherJob(
	store AttentionStore,
	carrier PaneCarrier,
	interval time.Duration,
) AttentionWatcherJob {
	return &attentionWatcherJob{
		store:    store,
		carrier:  carrier,
		interval: interval,
	}
}

type attentionWatcherJob struct {
	store    AttentionStore
	carrier  PaneCarrier
	interval time.Duration
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
	attempts, err := j.store.DeliveryAttempts(ctx)
	if err != nil {
		return errors.Wrap(ctx, err, "read delivery attempts failed")
	}
	items, err := j.store.ReadBoard(ctx)
	if err != nil {
		return errors.Wrap(ctx, err, "read board failed")
	}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return nil
		}
		if !j.deliverable(item, attempts) {
			continue
		}
		j.deliver(ctx, item)
	}
	return nil
}

// deliverable reports whether the item is an answered card this arm should carry.
func (j *attentionWatcherJob) deliverable(
	item Item,
	attempts map[ItemID]DeliveryAttempt,
) bool {
	if item.State != AnsweredState || item.AnsweredAt == nil {
		return false
	}
	// The originate arm settled this one; the session has already moved on.
	if item.AnsweredBy == attentionWatcherAnsweredBy {
		return false
	}
	if attempt, ok := attempts[item.ItemID]; ok && attempt.Outcome == DeliveredOutcome {
		return false
	}
	if _, ok := itemSessionID(item); !ok {
		return false
	}
	_, ok := answerText(item)
	return ok
}

// deliver carries one answer and records what it observed.
func (j *attentionWatcherJob) deliver(ctx context.Context, item Item) {
	sessionID, ok := itemSessionID(item)
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
	default:
		// `retry`, `no-pane` and `resolved` record nothing: retry has not failed
		// yet, a pane-less session attempted nothing, and a resolved gate has
		// nothing left to deliver.
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

// itemSessionID returns the session the item belongs to, from its liveness ref.
func itemSessionID(item Item) (string, bool) {
	model, value, err := item.LivenessRef.Parse(context.Background())
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
