// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"time"

	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

const (
	// UnforwardedClosersProducerID is the producer every re-presented card is
	// pushed under. It is also the launchd job's identity, so a card's producer
	// and the log line's job name are the same string.
	UnforwardedClosersProducerID ProducerID = "attention-unforwarded-closers"
	// unforwardedClosersInterruptClass matches the launchd job's
	// `--interrupt-class` default.
	unforwardedClosersInterruptClass InterruptClass = "pick"
	// unforwardedClosersCardTTL matches attention-ask.py's 24 h default: an
	// unanswered re-presentation is bounded rather than permanent.
	unforwardedClosersCardTTL = 24 * time.Hour
)

// UnforwardedLogf is where the job writes its cycle line. It is a seam rather
// than a direct glog call so a spec can assert the line's exact shape.
type UnforwardedLogf func(format string, args ...any)

// UnforwardedClosersJob runs the check in-process on an interval.
type UnforwardedClosersJob struct {
	scanner  *UnforwardedClosersScanner
	store    AttentionStore
	liveness SessionLivenessChecker
	interval time.Duration
	logf     UnforwardedLogf
	now      libtime.CurrentDateTimeGetter
}

// NewUnforwardedClosersJob builds the job.
//
// ⚠️ A non-positive interval falls back to the 900 s the launchd job used, and
// a nil logger falls back to glog. A nil store disables card re-presentation:
// the launchd job posted with `--post`, so main passes this process's own store,
// while a spec that only exercises the count passes nil and gets exactly one
// line per cycle.
//
// ⚠️ A nil liveness checker means "every session is live", so every finding is
// still carded. That is the deliberate fail-open direction: this arm exists to
// recover a gate that never reached the store, and silently dropping one
// because the probe was not wired would turn a missing card into an invisible
// one — the exact loss the arm exists to prevent. A nil checker therefore mints
// too much, never too little.
func NewUnforwardedClosersJob(
	scanner *UnforwardedClosersScanner,
	store AttentionStore,
	liveness SessionLivenessChecker,
	interval time.Duration,
	logf UnforwardedLogf,
) *UnforwardedClosersJob {
	if interval <= 0 {
		interval = UnforwardedClosersInterval
	}
	if logf == nil {
		logf = glog.Infof
	}
	return &UnforwardedClosersJob{
		scanner:  scanner,
		store:    store,
		liveness: liveness,
		interval: interval,
		logf:     logf,
		now:      libtime.NewCurrentDateTime(),
	}
}

// RunOnce performs one scan, writes its cycle line, and re-presents each lost
// gate on the board when a store is configured.
//
// ⚠️ The error branch is the in-process replacement for the launchd job's exit
// code 2: it writes `action=error` and NEVER falls through to a zero count, so
// a read failure can never read as "nothing lost".
func (j *UnforwardedClosersJob) RunOnce(ctx context.Context) UnforwardedScan {
	result := j.scanner.Scan(ctx)
	if result.Err != nil {
		j.logf("job=%s action=error %v", UnforwardedClosersJobName, result.Err)
		return result
	}
	j.logf(
		"job=%s action=scan scanned=%d unforwarded=%d",
		UnforwardedClosersJobName,
		result.Scanned,
		len(result.Findings),
	)
	for _, finding := range result.Findings {
		j.post(ctx, finding)
	}
	j.reconcile(ctx)
	return result
}

// post re-presents one lost gate on the operator's board.
//
// ⚠️ A finding whose session is no longer live is logged and NOT carded. The
// card would outlive the session that asked (a 24 h deadline, or one scan cycle
// once `reconcile` runs), but its answer can only be delivered back to the
// session that asked — and that session has exited, so the card is a decision
// the operator can only dismiss.
// Measured 2026-11-16: 601 such cards from 112 sessions, none of whose sessions
// could answer. The check is a FILTER, not a disable: a live session's lost gate
// is still re-presented, which is what keeps the arm a recovery path rather than
// a silence. A nil checker fails open (see NewUnforwardedClosersJob), and
// `IsLive` itself fails open on an unreadable registry, so neither can suppress
// a gate that might still be answerable.
//
// The dedup key is the local item id, so a repeated scan of the same window
// re-posts nothing while the card stays open: one lost gate, one card. A failed
// post is logged, never swallowed — a silent failure here would look exactly
// like a clean scan.
func (j *UnforwardedClosersJob) post(ctx context.Context, finding UnforwardedFinding) {
	if j.store == nil {
		return
	}
	if j.liveness != nil && !j.liveness.IsLive(ctx, finding.Session) {
		j.logf(
			"job=%s action=skip_dead_session item=%s session=%s",
			UnforwardedClosersJobName,
			finding.Item,
			firstEight(finding.Session),
		)
		return
	}
	expiresAt := j.now.Now().Add(libtime.Duration(unforwardedClosersCardTTL))
	item, err := j.store.Push(ctx, UnforwardedPushRequest(finding, expiresAt))
	if err != nil {
		j.logf(
			"job=%s action=post_error item=%s error=%v",
			UnforwardedClosersJobName,
			finding.Item,
			err,
		)
		return
	}
	if item == nil {
		j.logf(
			"job=%s action=post_error item=%s error=store returned no item",
			UnforwardedClosersJobName,
			finding.Item,
		)
		return
	}
	j.logf(
		"job=%s action=post item=%s card=%s",
		UnforwardedClosersJobName,
		finding.Item,
		item.ItemID,
	)
}

// UnforwardedPushRequest builds the card a lost gate is re-presented as.
//
// ⚠️ The liveness ref is `session:<id>` for a finding that names a session, so
// the reconciler can read the session back off the stored item and the renewal
// stops the moment the session exits. A session-less finding falls back to the
// `owner:` ref such a row has always carried — the only model that keeps it on
// the board at all. A 24 h deadline bounds an unanswered re-presentation.
func UnforwardedPushRequest(finding UnforwardedFinding, expiresAt libtime.DateTime) PushRequest {
	payload := finding.Ask
	if payload == "" {
		payload = fmt.Sprintf("lost %s gate — %s", finding.Kind, finding.Why)
	}
	return PushRequest{
		ProducerID:     UnforwardedClosersProducerID,
		ProducerKind:   CronProducerKind,
		LivenessRef:    unforwardedLivenessRef(finding.Session),
		DedupKey:       DedupKey("unforwarded:" + finding.Item),
		InterruptClass: unforwardedClosersInterruptClass,
		Payload:        Payload(payload),
		Context: ItemContext(fmt.Sprintf(
			"A %s gate from session %s never reached the store (%s). "+
				"Re-presented by the unforwarded-closer scanner.",
			finding.Kind,
			firstEight(finding.Session),
			finding.Why,
		)),
		AnswerMechanism: MessageAnswerMechanism,
		Options: AnswerOptions{
			{Label: "re-present it to me", Recommended: true},
			{Label: "dismiss"},
		},
		ExpiresAt: &expiresAt,
	}
}

// unforwardedLivenessRef is the card's survival subject. A finding that names a
// session uses `session:<id>` so the reconciler can read the session back off the
// stored item and the renewal stops when the session exits. A finding with no
// session id falls back to `owner:` — the ref a session-less row has always
// carried, and the only model that keeps such a card on the board at all.
func unforwardedLivenessRef(session string) LivenessRef {
	if session != "" {
		return LivenessRef("session:" + session)
	}
	return LivenessRef("owner:" + UnforwardedClosersProducerID.String())
}

// reconcile closes every open card this producer owns whose `session:` subject
// is no longer live.
//
// ⚠️ It is the second half of the mint-time filter. `post` refuses to mint for a
// session that has ALREADY exited, but a card minted while its session was live
// survives that session's exit: nothing in the store withdraws it, and only its
// own 24 h `expires_at` clears it. So the operator holds an unanswerable card for
// up to a day. This pass bounds that to one scan cycle.
//
// ⚠️ It closes only on a POSITIVE absence. `liveness.IsLive` fails open on an
// unreadable registry, and a nil checker disables the pass entirely, so an
// UNKNOWN or unreadable verdict keeps the card. Closing on a probe that could not
// read would delete a gate that might still be answerable — the loss this arm
// exists to prevent. Only a `session:` ref is reconciled; an `owner:` ref probes
// nothing by design and is left alone.
//
// A close here is a producer withdrawal: no arm is named, because the schema sets
// `answered_by` only on an arm-caused close.
func (j *UnforwardedClosersJob) reconcile(ctx context.Context) {
	if j.store == nil || j.liveness == nil {
		return
	}
	items, err := j.store.Read(ctx)
	if err != nil {
		j.logf("job=%s action=reconcile_error error=%v", UnforwardedClosersJobName, err)
		return
	}
	for _, item := range items {
		if item.ProducerID != UnforwardedClosersProducerID || item.State != OpenState {
			continue
		}
		model, value, err := item.LivenessRef.Parse(ctx)
		if err != nil || model != SessionLivenessModel {
			continue
		}
		if j.liveness.IsLive(ctx, value) {
			continue
		}
		if _, err := j.store.Close(ctx, item.ItemID, "", nil); err != nil {
			j.logf(
				"job=%s action=reconcile_close_error item=%s error=%v",
				UnforwardedClosersJobName,
				item.ItemID,
				err,
			)
			continue
		}
		j.logf(
			"job=%s action=reconcile_close item=%s session=%s",
			UnforwardedClosersJobName,
			item.ItemID,
			firstEight(value),
		)
	}
}

// Run scans once at startup, then on every interval until the context is done.
//
// ⚠️ The first scan runs BEFORE the first tick, so the line is present right
// after a restart and a bad state dir is visible without waiting a full 900 s.
func (j *UnforwardedClosersJob) Run(ctx context.Context) error {
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()
	j.RunOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			j.RunOnce(ctx)
		}
	}
}
