// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"os"
	"path/filepath"

	"github.com/bborbe/errors"
	"github.com/bborbe/run"
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

// managerWakeSubjectOption builds the wake job's subject-ownership option.
//
// ⚠️ An unresolvable home directory returns the no-op option rather than failing
// startup, for the same reason the transcript option does: a host with no vaults
// tree has nothing to resolve subjects against, and the spawn-edge rule still
// runs. A nil resolver keeps every card, which is the pre-change behaviour.
func managerWakeSubjectOption(
	stateDir string,
	contextDir string,
) pkg.ManagerWakeOption {
	home, err := os.UserHomeDir()
	if err != nil {
		glog.Warningf("resolve home dir for wake subjects failed: %v", err)
		return pkg.WithManagerWakeSubjectResolver(nil)
	}
	return pkg.WithManagerWakeSubjectResolver(pkg.NewDefaultWakeSubjectResolver(
		stateDir,
		contextDir,
		filepath.Join(home, "Documents", "Obsidian"),
	))
}

// attentionWatcherDirs resolves the three directories the ported jobs read.
//
// ⚠️ Each falls back to its own default rather than sharing one: the event logs,
// the statusline context files and the transcripts have different owners and
// different lifetimes, so a bad setting must fail the job that reads it and not
// the other two.
func (a *application) attentionWatcherDirs(ctx context.Context) (string, string, string, error) {
	stateDir := a.AttentionStateDir
	if stateDir == "" {
		resolved, err := defaultAttentionStateDir(ctx)
		if err != nil {
			return "", "", "", errors.Wrap(ctx, err, "resolve attention state dir failed")
		}
		stateDir = resolved
	}
	contextDir := a.AttentionContextDir
	if contextDir == "" {
		resolved, err := defaultAttentionContextDir(ctx)
		if err != nil {
			return "", "", "", errors.Wrap(ctx, err, "resolve attention context dir failed")
		}
		contextDir = resolved
	}
	transcriptRoot, err := defaultTranscriptRoot(ctx)
	if err != nil {
		return "", "", "", errors.Wrap(ctx, err, "resolve transcript root failed")
	}
	return stateDir, contextDir, transcriptRoot, nil
}

// addContextCrossingJob registers the context-crossing detector.
//
// ⚠️ Its book lives under the attention state dir, not the context-state dir:
// the statusline owns the latter, and its files are the readings this job judges
// rather than a place this job writes.
func (a *application) addContextCrossingJob(
	ctx context.Context,
	runner run.ConcurrentRunner,
	store pkg.AttentionStore,
	liveness pkg.SessionLivenessChecker,
	stateDir string,
	contextDir string,
) {
	runner.Add(ctx, pkg.NewContextCrossingJob(
		contextDir,
		pkg.NewStoreContextCrossingSink(store, nil),
		liveness,
		pkg.ContextCrossingTokens,
		pkg.ContextCrossingInterval,
		filepath.Join(stateDir, pkg.ContextCrossingBookFileName),
	).Run)
}
