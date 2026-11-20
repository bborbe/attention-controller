// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"
	"time"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
)

// WatcherReaperInterval is how often the reaper reads the process table.
//
// ⚠️ It is bounded by the manager tick, not chosen freely: a session still on
// an old plugin body re-arms a retired watcher once per tick (≤ 15 min), so a
// 5 min pass keeps each re-armed copy alive for at most one interval.
const WatcherReaperInterval = 5 * time.Minute

//counterfeiter:generate -o ../mocks/watcher-reaper-job.go --fake-name WatcherReaperJob . WatcherReaperJob

// WatcherReaperJob stops claude-supervisor watcher processes whose job moved
// into this process.
//
// A long-lived Claude session keeps the plugin body it loaded and re-arms the
// scripts that body names, so a watcher retired upstream keeps running on
// every host until each session restarts. Deleting the script file stops
// nothing that is already running; this job is the part a deletion cannot do.
type WatcherReaperJob interface {
	// Run reaps until the context is cancelled.
	Run(ctx context.Context) error

	// ReapOnce runs one pass and returns how many processes it stopped. It is
	// a seam so a spec can drive a pass deterministically.
	ReapOnce(ctx context.Context) (int, error)
}

// NewWatcherReaperJob creates the reaper.
func NewWatcherReaperJob(
	lister ProcessLister,
	matcher RetiredWatcherMatcher,
	signaller ProcessSignaller,
	interval time.Duration,
) WatcherReaperJob {
	return &watcherReaperJob{
		lister:    lister,
		matcher:   matcher,
		signaller: signaller,
		interval:  interval,
	}
}

type watcherReaperJob struct {
	lister    ProcessLister
	matcher   RetiredWatcherMatcher
	signaller ProcessSignaller
	interval  time.Duration
}

// Run reaps once at start, then every interval, until the context is cancelled.
func (j *watcherReaperJob) Run(ctx context.Context) error {
	if j.interval <= 0 {
		return errors.Errorf(ctx, "watcher reaper interval must be positive")
	}
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()
	for {
		if _, err := j.ReapOnce(ctx); err != nil {
			glog.Warningf("job=watcher-reaper action=reap_error error=%v", err)
		}
		select {
		case <-ctx.Done():
			glog.V(2).Infof("job=watcher-reaper action=stopped")
			return nil
		case <-ticker.C:
		}
	}
}

// ReapOnce sends SIGTERM to every retired watcher in the process table.
func (j *watcherReaperJob) ReapOnce(ctx context.Context) (int, error) {
	processes, err := j.lister.List(ctx)
	if err != nil {
		return 0, errors.Wrapf(ctx, err, "list processes failed")
	}
	reaped := 0
	for _, p := range processes {
		if ctx.Err() != nil {
			return reaped, nil
		}
		match, ok := j.matcher.Match(p)
		if !ok {
			continue
		}
		if err := j.signaller.Terminate(ctx, p.PID); err != nil {
			if stderrors.Is(err, ErrProcessGone) {
				continue
			}
			// One refusal must not stop the pass: the remaining matches are
			// independent processes.
			glog.Warningf(
				"job=watcher-reaper action=reap_failed pid=%d script=%s version=%s error=%v",
				p.PID, match.Script, match.Version, err,
			)
			continue
		}
		// ⚠️ V0 on purpose: stopping another process is an operator-visible
		// action, and this line is the only record of which pid was stopped.
		glog.Infof(
			"job=watcher-reaper action=reap pid=%d ppid=%d script=%s version=%s",
			p.PID, p.PPID, match.Script, match.Version,
		)
		reaped++
	}
	return reaped, nil
}

// Compile-time guard: the job must satisfy its own interface.
var _ WatcherReaperJob = (*watcherReaperJob)(nil)
