// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"path/filepath"
	"regexp"
	"strings"
)

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

//counterfeiter:generate -o ../mocks/retired-watcher-matcher.go --fake-name RetiredWatcherMatcher . RetiredWatcherMatcher

// RetiredWatcherMatcher decides which processes are retired watchers.
type RetiredWatcherMatcher interface {
	// Match reports whether p is a retired watcher this process may stop, and
	// if so which script and plugin version it runs. Every guard must hold:
	//
	//   - p runs as the controller's own uid;
	//   - p is neither the controller itself nor pid 1;
	//   - argv[0] is a Python interpreter;
	//   - the first non-flag argument is a claude-supervisor script path;
	//   - that script's name is on the retired list.
	Match(p Process) (RetiredWatcher, bool)
}

// NewRetiredWatcherMatcher creates a matcher for the retired script names,
// scoped to processes of uid and never matching selfPID.
func NewRetiredWatcherMatcher(retired map[string]bool, uid int, selfPID int) RetiredWatcherMatcher {
	return &retiredWatcherMatcher{retired: retired, uid: uid, selfPID: selfPID}
}

type retiredWatcherMatcher struct {
	retired map[string]bool
	uid     int
	selfPID int
}

// Match applies every guard; see the interface.
func (m *retiredWatcherMatcher) Match(p Process) (RetiredWatcher, bool) {
	if p.UID != m.uid || p.PID == m.selfPID || p.PID <= 1 || len(p.Args) < 2 {
		return RetiredWatcher{}, false
	}
	if !pythonInterpreterPattern.MatchString(filepath.Base(p.Args[0])) {
		return RetiredWatcher{}, false
	}
	script := firstNonFlagArg(p.Args[1:])
	groups := supervisorScriptPattern.FindStringSubmatch(script)
	if groups == nil || !m.retired[groups[2]] {
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

// Compile-time guard: the matcher must satisfy its own interface.
var _ RetiredWatcherMatcher = (*retiredWatcherMatcher)(nil)
