// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"time"

	"github.com/golang/glog"
)

// UnforwardedLogf is where the job writes its cycle line. It is a seam rather
// than a direct glog call so a spec can assert the line's exact shape.
type UnforwardedLogf func(format string, args ...any)

// UnforwardedClosersJob runs the check in-process on an interval.
type UnforwardedClosersJob struct {
	scanner  *UnforwardedClosersScanner
	interval time.Duration
	logf     UnforwardedLogf
}

// NewUnforwardedClosersJob builds the job. A non-positive interval falls back
// to the 900 s the launchd job used, and a nil logger falls back to glog.
func NewUnforwardedClosersJob(
	scanner *UnforwardedClosersScanner,
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
		interval: interval,
		logf:     logf,
	}
}

// RunOnce performs one scan and writes its cycle line.
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
	return result
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
