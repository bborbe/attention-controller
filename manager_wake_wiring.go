// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"

	libtime "github.com/bborbe/time"
	"github.com/golang/glog"

	"github.com/bborbe/attention-controller/pkg"
)

// managerWakeTranscriptOption builds the wake job's transcript-and-registry
// option.
//
// ⚠️ The sessions directory is resolved the same way createSessionLivenessChecker
// resolves it. An unresolvable home directory returns the no-op option (a nil
// source disables the arm) rather than failing startup: a host with no registry
// has nothing for the second source to read, and the store arm still runs.
//
// ⚠️ It lives beside main.go rather than in it because main.go sits at the
// file-length limit, and the wiring decision belongs beside its own rationale.
func (a *application) managerWakeTranscriptOption(
	ctx context.Context,
	stateDir, transcriptRoot string,
	heartbeats pkg.SessionHeartbeatStore,
) pkg.ManagerWakeOption {
	sessionsDir := a.SessionsDir
	if sessionsDir == "" {
		resolved, err := defaultSessionsDir(ctx)
		if err != nil {
			glog.Warningf("resolve sessions dir failed: %v", err)
			return pkg.WithManagerWakeTranscriptSource(nil, nil)
		}
		sessionsDir = resolved
	}
	return pkg.WithManagerWakeTranscriptSource(
		pkg.NewManagerTranscriptSource(
			sessionsDir,
			heartbeats,
			pkg.NewTranscriptReader(transcriptRoot),
			libtime.NewCurrentDateTime(),
		),
		pkg.NewManagerGateTracker(stateDir, libtime.NewCurrentDateTime()),
	)
}
