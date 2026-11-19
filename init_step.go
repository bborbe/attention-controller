// Copyright (c) 2025 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"time"

	"github.com/bborbe/errors"
	librun "github.com/bborbe/run"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"

	"github.com/bborbe/attention-controller/pkg/handler"
)

// initStepTimeout bounds how long an init step may run before it is failed.
//
// ⚠️ A step blocked in a syscall (measured 2026-11-19: openat(2) on the vault,
// parked for 6 h 37 m at 0 % CPU) never returns, so a synchronous call reports
// nothing and the startup gate it names stays closed for the life of the
// process. The bound turns that silent indefinite hang into an `action=failed`
// line naming the step and a non-zero exit, so launchd's KeepAlive restarts
// instead of an operator noticing hours later. It sits far above the measured
// walk (2.2–2.9 s) so no healthy start is cut. A var, not a const, so a test
// drives a cut deterministically — the same pattern as historyPageCacheBudget.
var initStepTimeout = 60 * time.Second

// runInitStep runs one init step under its name: the gate names it as pending,
// and its start, finish and duration are logged at default verbosity so a step
// that hangs is the last `action=start` line in the stderr log.
//
// ⚠️ The step runs under librun.Run so a step that never returns — a syscall
// the kernel will not release, which no context can cancel — still fails the
// process at initStepTimeout instead of holding the gate at `starting` forever.
// The abandoned goroutine is deliberate: the process is exiting, so nothing
// reads what it would have written.
func runInitStep(
	ctx context.Context,
	gate handler.StartupGate,
	now libtime.CurrentDateTimeGetter,
	name string,
	fn func(ctx context.Context) error,
) error {
	gate.Step(name)
	glog.V(0).Infof("init step=%s action=start", name)
	start := now.Now()
	results := librun.Run(ctx, fn)
	timer := time.NewTimer(initStepTimeout)
	defer timer.Stop()
	select {
	case err := <-results:
		if err != nil {
			glog.Errorf(
				"init step=%s action=failed duration=%s error=%v",
				name, time.Duration(now.Now().Sub(start)), err,
			)
			return errors.Wrapf(ctx, err, "init step %s failed", name)
		}
		glog.V(0).Infof(
			"init step=%s action=done duration=%s",
			name, time.Duration(now.Now().Sub(start)),
		)
		return nil
	case <-timer.C:
		err := errors.Errorf(ctx, "init step %s did not finish within %s", name, initStepTimeout)
		glog.Errorf(
			"init step=%s action=failed duration=%s error=%v",
			name, time.Duration(now.Now().Sub(start)), err,
		)
		return err
	}
}
