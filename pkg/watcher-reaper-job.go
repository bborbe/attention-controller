// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"
	"path/filepath"
	"regexp"
	"strings"
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

// DefaultRetiredWatchers names the claude-supervisor scripts whose job moved
// into this process. `manager-attention-watch` is replaced by the manager wake
// job; `permission-answer-poll` was deleted upstream (claude-supervisor #1036).
const DefaultRetiredWatchers = "manager-attention-watch,permission-answer-poll"

// supervisorScriptPattern matches a claude-supervisor script path and captures
// the plugin version (empty for the marketplace clone) and the script name.
//
// ⚠️ Anchored on both the plugin layout and the `.py` suffix: a path that only
// *contains* a retired name — a log file, an editor buffer, a test fixture —
// is never a match.
var supervisorScriptPattern = regexp.MustCompile(
	`/\.claude/plugins/(?:cache/claude-supervisor/supervisor/([^/]+)|marketplaces/claude-supervisor)/scripts/([A-Za-z0-9_.-]+)\.py$`,
)

// pythonInterpreterPattern matches argv[0]'s basename for a Python interpreter:
// `python`, `python3`, `python3.14`, and the framework build's `Python`.
var pythonInterpreterPattern = regexp.MustCompile(`^(?i)python[0-9.]*$`)

// RetiredWatcher is one process the reaper would stop.
type RetiredWatcher struct {
	// Process is the matched row.
	Process Process
	// Script is the script basename without `.py`.
	Script string
	// Version is the plugin cache version, or `marketplace` for the clone.
	Version string
}

// ParseRetiredWatchers splits a comma-separated list into a set, ignoring
// blanks and a trailing `.py`.
func ParseRetiredWatchers(raw string) map[string]bool {
	result := map[string]bool{}
	for name := range strings.SplitSeq(raw, ",") {
		name = strings.TrimSuffix(strings.TrimSpace(name), ".py")
		if name != "" {
			result[name] = true
		}
	}
	return result
}

// MatchRetiredWatcher reports whether p is a retired watcher this process may
// stop. Every guard must hold:
//
//   - p runs as uid, the controller's own user;
//   - p is not selfPID;
//   - argv[0] is a Python interpreter;
//   - the first non-flag argument is a claude-supervisor script path;
//   - that script's name is in retired.
func MatchRetiredWatcher(
	p Process,
	uid int,
	selfPID int,
	retired map[string]bool,
) (RetiredWatcher, bool) {
	if p.UID != uid || p.PID == selfPID || p.PID <= 1 || len(p.Args) < 2 {
		return RetiredWatcher{}, false
	}
	if !pythonInterpreterPattern.MatchString(filepath.Base(p.Args[0])) {
		return RetiredWatcher{}, false
	}
	script := firstNonFlagArg(p.Args[1:])
	groups := supervisorScriptPattern.FindStringSubmatch(script)
	if groups == nil || !retired[groups[2]] {
		return RetiredWatcher{}, false
	}
	version := groups[1]
	if version == "" {
		version = "marketplace"
	}
	return RetiredWatcher{Process: p, Script: groups[2], Version: version}, true
}

// firstNonFlagArg returns the script argument: interpreter flags (`-I`, `-u`)
// come before it, and everything after it belongs to the script.
func firstNonFlagArg(args []string) string {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			return arg
		}
	}
	return ""
}

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
	signaller ProcessSignaller,
	retired map[string]bool,
	uid int,
	selfPID int,
	interval time.Duration,
) WatcherReaperJob {
	return &watcherReaperJob{
		lister:    lister,
		signaller: signaller,
		retired:   retired,
		uid:       uid,
		selfPID:   selfPID,
		interval:  interval,
	}
}

type watcherReaperJob struct {
	lister    ProcessLister
	signaller ProcessSignaller
	retired   map[string]bool
	uid       int
	selfPID   int
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
	if len(j.retired) == 0 {
		return 0, nil
	}
	processes, err := j.lister.List(ctx)
	if err != nil {
		return 0, errors.Wrapf(ctx, err, "list processes failed")
	}
	reaped := 0
	for _, p := range processes {
		if ctx.Err() != nil {
			return reaped, nil
		}
		match, ok := MatchRetiredWatcher(p, j.uid, j.selfPID, j.retired)
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
