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

// ManagerHoldWindow is how long a manager-tier gate is held before it is posted
// anyway.
//
// ⚠️ It is a delay, never a filter. A gate whose manager never answers is
// posted at the end of the window; the alternative — dropping it — would trade
// a slow card for a lost one.
const ManagerHoldWindow = 5 * time.Minute

// ManagerHoldReleaseInterval is how often the release arm checks the book.
const ManagerHoldReleaseInterval = 10 * time.Second

// ReleaseExpiredHoldsJob posts held gates whose window ran out and drops the
// ones whose session is gone.
//
// It is the in-process replacement for the Python watcher's
// `release_expired_holds` arm. The book it drains is filled by the ingest: a
// gate that belongs to a manager is held instead of pushed, so the manager's
// own prompt can be answered before the board shows a second copy of it.
type ReleaseExpiredHoldsJob interface {
	// Run releases until the context is cancelled.
	Run(ctx context.Context) error

	// ReleaseOnce runs one pass. It is a seam so a spec can drive a pass
	// deterministically instead of racing a ticker.
	ReleaseOnce(ctx context.Context) error
}

// NewReleaseExpiredHoldsJob creates the release arm.
func NewReleaseExpiredHoldsJob(
	book ManagerHoldBook,
	sink EventIngestSink,
	liveness SessionLivenessChecker,
	window time.Duration,
	interval time.Duration,
	now libtime.CurrentDateTimeGetter,
) ReleaseExpiredHoldsJob {
	return &releaseExpiredHoldsJob{
		book:     book,
		sink:     sink,
		liveness: liveness,
		window:   window,
		interval: interval,
		now:      now,
	}
}

type releaseExpiredHoldsJob struct {
	book     ManagerHoldBook
	sink     EventIngestSink
	liveness SessionLivenessChecker
	window   time.Duration
	interval time.Duration
	now      libtime.CurrentDateTimeGetter
}

// Run releases until the context is cancelled.
func (j *releaseExpiredHoldsJob) Run(ctx context.Context) error {
	if j.interval <= 0 {
		return errors.Errorf(ctx, "hold release interval must be positive")
	}
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			glog.V(2).Infof("hold release stopped")
			return nil
		case <-ticker.C:
			if err := j.ReleaseOnce(ctx); err != nil {
				glog.Warningf("job=attention-watcher action=release_error error=%v", err)
			}
		}
	}
}

// ReleaseOnce posts every hold whose window expired and whose session is live.
func (j *releaseExpiredHoldsJob) ReleaseOnce(ctx context.Context) error {
	if j.window <= 0 {
		return errors.Errorf(ctx, "hold window must be positive")
	}
	now := j.now.Now().Time()
	expired := j.book.Expired(now, j.window)
	opened, dropped := 0, 0
	for _, hold := range expired {
		if err := ctx.Err(); err != nil {
			return nil
		}
		if !j.liveness.IsLive(ctx, hold.Record.SessionID) {
			// The session exited before the hand-off expired: there is nobody
			// left to answer, so the hold is dropped rather than posted.
			j.book.Take(hold.Record.ItemID)
			glog.V(2).Infof(
				"job=attention-watcher action=unhold item=%s manager=%s reason=session-gone",
				hold.Record.ItemID,
				hold.Manager,
			)
			dropped++
			continue
		}
		if err := j.sink.Open(ctx, hold.Record); err != nil {
			// The hold stays in the book, so the next pass retries it rather
			// than losing the gate to a transient store failure.
			return errors.Wrapf(ctx, err, "release %s failed", hold.Record.ItemID)
		}
		j.book.Take(hold.Record.ItemID)
		glog.V(2).Infof(
			"job=attention-watcher action=release item=%s manager=%s",
			hold.Record.ItemID,
			hold.Manager,
		)
		opened++
	}
	if len(expired) > 0 {
		glog.Infof(
			"job=attention-watcher action=release files=%d opened=%d dropped=%d",
			len(expired),
			opened,
			dropped,
		)
	}
	return nil
}

// Compile-time guard: the job must satisfy its own interface.
var _ ReleaseExpiredHoldsJob = (*releaseExpiredHoldsJob)(nil)
