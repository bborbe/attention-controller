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

// AttentionDeliveryBound is how long an answered card may sit in a RETRY loop —
// the pane's composer staying busy or unreadable — before this arm marks it
// undeliverable.
//
// ⚠️ It is anchored on the card's ANSWER time, never on the first retry: a card
// can enter the retry loop later than another arm would have, and starting the
// clock at the first retry would quietly widen the bound the operator was
// promised.
//
// ⚠️ It is a MARKER, not a stop. The arm keeps retrying past it, because an
// answer dropped while its gate stays open is worse than a late one (the
// 2026-10-04 age-gate lesson); the marker is what tells the operator to look
// instead of waiting blind. The Python watcher carried this bound on the booked
// arm only and never on the unbooked one — measured 2026-11-14, an unbooked
// answer retried 34 minutes unmarked. The port unified both arms into this one,
// so this constant now governs every retry.
//
// It is defined from AttentionDeliveryRetryBackoff rather than repeating the
// literal, so the tree carries exactly ONE five-minute value and a spec can fail
// on a second one. The bound is one backoff window: an answer that has not been
// carried by the time a failed delivery would be retried has waited a full
// retry cycle. The value itself is pinned by a spec, so widening the backoff
// cannot silently widen the operator's promise.
const AttentionDeliveryBound = AttentionDeliveryRetryBackoff

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

// attentionWatcherUndeliverableCarrier names the bound's own marker in the
// delivery trail. It is deliberately distinct from attentionWatcherCarrier.
//
// ⚠️ A `refuse` writes a FailedOutcome under the plain carrier BEFORE a retry
// loop begins, so an earlier attempt must never be mistaken for the marker.
// Measured 2026-11-17 on card f1b46ed9…: refused 0.4 s after its answer, then
// retried for 1216 s with no marker at all — the guard compared every retry
// against that refuse and suppressed all of them. Naming the marker makes
// "already marked" answerable from the trail itself, instead of inferring it
// from the presence of any attempt.
const attentionWatcherUndeliverableCarrier = attentionWatcherCarrier + ":undeliverable"

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

// AttentionWatcherOption customises the delivery arm at construction.
type AttentionWatcherOption func(*attentionWatcherJob)

// WithAttentionWatcherSignaller sets the helper that emits the ANSWERED signal
// after a card is delivered.
func WithAttentionWatcherSignaller(signaller AnsweredSignaller) AttentionWatcherOption {
	return func(job *attentionWatcherJob) {
		job.signaller = signaller
	}
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
	options ...AttentionWatcherOption,
) AttentionWatcherJob {
	job := &attentionWatcherJob{
		store:                 store,
		carrier:               carrier,
		headless:              headless,
		interval:              interval,
		currentDateTimeGetter: currentDateTimeGetter,
	}
	for _, option := range options {
		option(job)
	}
	return job
}

type attentionWatcherJob struct {
	store                 AttentionStore
	carrier               PaneCarrier
	headless              HeadlessAnswerDeliverer
	interval              time.Duration
	currentDateTimeGetter libtime.CurrentDateTimeGetter
	signaller             AnsweredSignaller
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
	counts, err := j.store.DeliveryAttemptCounts(ctx)
	if err != nil {
		return errors.Wrap(ctx, err, "read delivery attempt counts failed")
	}
	parks, err := j.store.DeliveryParks(ctx)
	if err != nil {
		return errors.Wrap(ctx, err, "read delivery parks failed")
	}
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
		if !j.eligible(ctx, item, counts, parks) {
			continue
		}
		j.deliver(ctx, item, reports[item.ItemID])
	}
	return nil
}

// eligible reports whether an item may be attempted NOW, applying the backoff
// and the park.
//
// ⚠️ The park is a LIVENESS gate, never a verdict class. `refuse` and `no-pane`
// both describe a session whose process may come back — a reaped headless worker
// is `no-pane` and resumable, and an exited tab session is `refuse` — so the
// re-open turns on SessionLivenessChecker, not on which verdict parked the item.
// Gating on the verdict instead would abandon exactly the population the
// headless arm exists to serve.
func (j *attentionWatcherJob) eligible(
	ctx context.Context,
	item Item,
	counts map[ItemID]DeliveryAttemptCount,
	parks map[ItemID]DeliveryPark,
) bool {
	park, parked := parks[item.ItemID]
	if parked {
		sessionID, ok := itemSessionID(ctx, item)
		if !ok || !j.store.IsSessionLive(ctx, sessionID) {
			return false
		}
		// Live again: the park is over. Clearing it also forgets the attempts
		// that earned it, so the item starts a fresh backoff cycle instead of
		// re-parking on the old count the next time round.
		if err := j.store.ClearDeliveryPark(ctx, item.ItemID); err != nil {
			glog.Warningf(
				"job=attention-watcher action=reopen_error item=%s error=%v",
				item.ItemID,
				err,
			)
			return false
		}
		glog.V(2).Infof(
			"job=attention-watcher action=reopen item=%s attempts=%d verdict=%s reason=%q",
			item.ItemID,
			park.Attempts,
			park.Verdict,
			park.Reason,
		)
		return true
	}
	count, ok := counts[item.ItemID]
	if !ok || count.Count == 0 {
		return true
	}
	if count.Count >= DeliveryAttemptParkThreshold {
		// The threshold was reached but no park record exists — the process died
		// between the attempt and the park write. Park now rather than attempt a
		// ninth time.
		j.park(ctx, item, count)
		return false
	}
	return !j.currentDateTimeGetter.Now().Time().Before(
		count.LastAttemptedAt.Time().Add(DeliveryAttemptBackoff),
	)
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
func (j *attentionWatcherJob) deliver(
	ctx context.Context,
	item Item,
	report *DeliveryReport,
) {
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
	delivery := &PaneDelivery{
		SessionID:       sessionID,
		StoreItemID:     string(item.ItemID),
		AnswerMechanism: item.AnswerMechanism,
	}
	verdict, reason := j.carrier.TypeIntoPane(ctx, delivery, text, false)
	glog.V(2).Infof(
		"job=attention-watcher action=deliver item=%s verdict=%s reason=%q",
		item.ItemID,
		verdict,
		reason,
	)
	// ⚠️ Counted BEFORE the verdict switch, and for EVERY verdict. The
	// `no-pane` and `retry` arms write no attempt record — they are the
	// population that loops forever, because no carrier can reach them — so a
	// counter that incremented only on an observed outcome would read ~1 per
	// window while the loop it exists to bound ran untouched.
	count := j.recordAttemptCount(ctx, item, verdict, reason)
	switch verdict {
	case DeliveryOK:
		j.recordAttempt(ctx, item, DeliveredOutcome)
		// Signal only after the delivered outcome is on record: the durable
		// DeliveryReport is the dedupe state the next tick reads.
		if j.signaller != nil {
			j.signaller.Signal(ctx, sessionID, item)
		}
		// Delivered: any park is over, and the count it took is moot.
		j.clearPark(ctx, item)
	case DeliveryRefuse:
		j.refuse(ctx, item, sessionID, count, reason)
	case DeliveryNoPane:
		// ⚠️ A pane-less session is not a failing carrier — it is the normal
		// shape for every headless `sdk-cli` worker, which has no pane by
		// construction. Recording nothing here is what dropped the operator's
		// answer; the wrapper owns that child's stdin, so the turn goes there.
		if j.headless != nil {
			j.headless.DeliverHeadless(ctx, item)
		}
	case DeliveryRetry:
		// A busy or unreadable composer has not failed yet, so it is not marked
		// as a failure — but it must not retry unmarked forever either. Past
		// AttentionDeliveryBound the operator is told, and the retry continues.
		j.markUndeliverableIfOverdue(ctx, item, report, reason)
		j.maybePark(ctx, item, count, verdict, reason)
	default:
		// `resolved`: the gate was already gone — nothing left to deliver. It is
		// still counted and still parked at the threshold, because a verdict that
		// never resolves is the same hot loop under another name.
		j.maybePark(ctx, item, count, verdict, reason)
	}
}

// refuse handles a pane refusal.
//
// ⚠️ A refusal is not always undeliverable. When the session is gone the pane
// carrier reports "no live process with a tty" and the answer used to be
// refused for good, although the wrapper keeps an exited session's record and
// can resume it — the menu arm already takes that route. Only a session the
// wrapper reports as headless is resumed; anything else keeps its refusal, now
// with a reason that names the missing fallback so the log tells the two apart.
//
// The retry bound is unchanged: the attempt was counted before this call, so a
// card that stays undeliverable backs off and parks like any other refusal.
func (j *attentionWatcherJob) refuse(
	ctx context.Context,
	item Item,
	sessionID string,
	count int,
	reason string,
) {
	if j.headless != nil && j.headless.CanResume(ctx, sessionID) {
		if !j.headless.DeliverHeadless(ctx, item) {
			// Still pending: the headless arm logged why. The attempt is already
			// counted, so the park bound applies here too.
			j.maybePark(ctx, item, count, DeliveryRetry, "headless resume pending: "+reason)
		}
		return
	}
	reason += " — and the session cannot be resumed through the headless path"
	glog.V(2).Infof(
		"job=attention-watcher action=refuse_unresumable item=%s reason=%q",
		item.ItemID,
		reason,
	)
	j.recordAttempt(ctx, item, FailedOutcome)
	j.maybePark(ctx, item, count, DeliveryRefuse, reason)
}

// recordAttemptCount bumps how many times this item's answer has been tried and
// returns the new total.
//
// ⚠️ Logged rather than returned, for the same reason recordAttempt is: the
// attempt has already happened, and a missing counter must not be reported as a
// failed delivery. A failed write returns 0, which never reaches the park
// threshold — the item is retried rather than parked on an unrecorded attempt.
func (j *attentionWatcherJob) recordAttemptCount(
	ctx context.Context,
	item Item,
	verdict DeliveryVerdict,
	reason string,
) int {
	count, err := j.store.RecordAttemptCount(ctx, item.ItemID, verdict, reason)
	if err != nil {
		glog.Warningf(
			"job=attention-watcher action=attempt_count_error item=%s error=%v",
			item.ItemID,
			err,
		)
		return 0
	}
	return count
}

// maybePark parks an item that has exhausted its attempts.
func (j *attentionWatcherJob) maybePark(
	ctx context.Context,
	item Item,
	count int,
	verdict DeliveryVerdict,
	reason string,
) {
	if count < DeliveryAttemptParkThreshold {
		return
	}
	j.park(ctx, item, DeliveryAttemptCount{
		ItemID:      item.ItemID,
		Count:       count,
		LastVerdict: verdict,
		LastReason:  reason,
	})
}

// park writes the park record, logging rather than failing: the attempt has
// already happened, and a missing park only costs one more cycle.
func (j *attentionWatcherJob) park(
	ctx context.Context,
	item Item,
	count DeliveryAttemptCount,
) {
	verdict := count.LastVerdict
	if verdict == "" {
		verdict = DeliveryRetry
	}
	reason := count.LastReason
	if reason == "" {
		// Only reachable from the crash-recovery branch on a count record whose
		// reason was never written. A park must still name why it stopped, and an
		// empty reason would fail Validate and leave the item attempted-never,
		// parked-never.
		reason = "attempt threshold reached without a recorded verdict"
	}
	park := DeliveryPark{
		ItemID:   item.ItemID,
		Verdict:  verdict,
		Reason:   reason,
		Attempts: count.Count,
		ParkedAt: j.currentDateTimeGetter.Now(),
	}
	if err := j.store.ParkDelivery(ctx, park); err != nil {
		glog.Warningf(
			"job=attention-watcher action=park_error item=%s error=%v",
			item.ItemID,
			err,
		)
		return
	}
	glog.V(2).Infof(
		"job=attention-watcher action=park item=%s verdict=%s attempts=%d reason=%q",
		item.ItemID,
		park.Verdict,
		park.Attempts,
		park.Reason,
	)
}

// clearPark ends a park on a delivered item, logging rather than failing.
func (j *attentionWatcherJob) clearPark(ctx context.Context, item Item) {
	if err := j.store.ClearDeliveryPark(ctx, item.ItemID); err != nil {
		glog.Warningf(
			"job=attention-watcher action=clear_park_error item=%s error=%v",
			item.ItemID,
			err,
		)
	}
}

// markUndeliverableIfOverdue records a terminal `failed` attempt for an answer
// that has sat in a retry loop past AttentionDeliveryBound, so the operator is
// told instead of waiting blind.
//
// ⚠️ It MARKS, it does not stop. The `failed` record is not terminal for the
// arm: `retryDue` spaces the next attempt by AttentionDeliveryRetryBackoff, and
// a later DeliveryOK overwrites the record with a settled `delivered`
// (RecordAttempt is first-write-wins on the settlement). Dropping the entry here
// would lose the answer while its gate stays open — the 2026-10-04 age-gate
// lesson.
//
// ⚠️ Exactly ONE marker per answer, and "already marked" is answered by the
// marker's OWN carrier — not by the presence of any attempt at or after the
// answer. A `refuse` records a FailedOutcome under the plain carrier before a
// retry loop starts, so the earlier rule suppressed the marker for every card
// that was refused once and then looped (measured 2026-11-17 on f1b46ed9…:
// 1216 s of retries, zero markers). Without the per-answer guard the marker
// would be re-written every backoff window for the life of the card.
//
// ⚠️ The attempt bucket holds ONE record per item, so this marker's carrier
// survives only until another arm writes that record (a refuse, a headless
// settle). A card that alternates refuse/retry after the bound therefore writes
// one extra marker per alternation — bounded, not a loop, and the same property
// the pre-fix code had. The retry loop alone never writes the record, which is
// what makes the carrier a reliable "already marked" for the loop it bounds.
func (j *attentionWatcherJob) markUndeliverableIfOverdue(
	ctx context.Context,
	item Item,
	report *DeliveryReport,
	reason string,
) {
	if item.AnsweredAt == nil {
		return
	}
	answeredAt := item.AnsweredAt.Time()
	if j.currentDateTimeGetter.Now().Time().Before(answeredAt.Add(AttentionDeliveryBound)) {
		return
	}
	if report != nil && report.Carrier == attentionWatcherUndeliverableCarrier &&
		report.AttemptedAt != nil &&
		!report.AttemptedAt.Time().Before(answeredAt) {
		return
	}
	glog.V(2).Infof(
		"job=attention-watcher action=undeliverable item=%s bound=%s reason=%q",
		item.ItemID,
		AttentionDeliveryBound,
		reason,
	)
	j.recordAttemptAs(ctx, item, attentionWatcherUndeliverableCarrier, FailedOutcome)
}

// recordAttempt writes the trail entry, logging rather than failing: the
// delivery has already happened, and a missing trail entry must not be reported
// as a failed delivery.
func (j *attentionWatcherJob) recordAttempt(
	ctx context.Context,
	item Item,
	outcome DeliveryOutcome,
) {
	j.recordAttemptAs(ctx, item, attentionWatcherCarrier, outcome)
}

// recordAttemptAs writes the trail entry under a named carrier, logging rather
// than failing for the same reason recordAttempt does.
//
// The carrier is a parameter because the undeliverable marker must be
// distinguishable from an ordinary attempt: see
// attentionWatcherUndeliverableCarrier.
func (j *attentionWatcherJob) recordAttemptAs(
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
