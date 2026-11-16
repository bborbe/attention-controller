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
	return result
}

// post re-presents one lost gate on the operator's board.
//
// ⚠️ A finding whose session is no longer live is logged and NOT carded. The
// card's `owner:` liveness ref keeps it on the board indefinitely, but its
// answer can only be delivered back to the session that asked — and that
// session has exited, so the card is a decision the operator can only dismiss.
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
// ⚠️ It mirrors attention-ask.py's post shape, which is what the launchd job
// called: `owner:` liveness so the card outlives the session that lost it, a
// 24 h deadline, the two options the operator saw before, and the ask itself as
// the payload. The measured page-box counts that script appended are not
// ported; the ask is the card's content.
func UnforwardedPushRequest(finding UnforwardedFinding, expiresAt libtime.DateTime) PushRequest {
	payload := finding.Ask
	if payload == "" {
		payload = fmt.Sprintf("lost %s gate — %s", finding.Kind, finding.Why)
	}
	return PushRequest{
		ProducerID:     UnforwardedClosersProducerID,
		ProducerKind:   CronProducerKind,
		LivenessRef:    LivenessRef("owner:" + UnforwardedClosersProducerID.String()),
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
