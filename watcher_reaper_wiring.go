// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"os"

	"github.com/bborbe/run"
	"github.com/golang/glog"

	"github.com/bborbe/attention-controller/pkg"
)

// addWatcherReaperJob registers the reaper that stops retired claude-supervisor
// watchers.
//
// ⚠️ Registered beside the manager wake job, on the host-local wiring only: the
// watchers it stops run on the operator's Mac, where the sessions are. An empty
// RetiredWatchers registers nothing, so the job can be switched off without a
// rebuild.
func (a *application) addWatcherReaperJob(ctx context.Context, runner run.ConcurrentRunner) {
	// ⚠️ `SESSION_LIVENESS=off` is the cluster backend's mode: its image is
	// `FROM scratch` with no `ps`, and no session runs beside it, so a reaper
	// there could only log a list failure every interval.
	if a.SessionLiveness == "off" {
		glog.V(2).Infof("job=watcher-reaper action=disabled reason=cluster-backend")
		return
	}
	retired := pkg.ParseRetiredWatchers(a.RetiredWatchers)
	if len(retired) == 0 {
		glog.V(2).Infof("job=watcher-reaper action=disabled reason=empty-retired-list")
		return
	}
	runner.Add(ctx, pkg.NewWatcherReaperJob(
		pkg.NewPsProcessLister(),
		pkg.NewSyscallProcessSignaller(),
		retired,
		os.Getuid(),
		os.Getpid(),
		pkg.WatcherReaperInterval,
	).Run)
}
