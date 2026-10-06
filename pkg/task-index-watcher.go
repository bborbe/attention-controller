// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/golang/glog"
)

// TaskIndexWatcher keeps a TaskIndex current by watching the vault's task
// directory: it applies the files a change names, and reconciles on a slow
// window for the changes no event delivered.
//
// ⚠️ It watches exactly ONE directory — `<vault>/25 Tasks/` — and adds nothing
// else to the watch set. The index reads the goal and topic rungs too, but those
// change rarely and the reconcile covers them; widening the watch set would make
// the watcher's scope a thing that can drift away from the index it serves.
type TaskIndexWatcher interface {
	// Run watches the task directory until ctx is cancelled, applying each
	// change to the index and reconciling on taskIndexBackstopWindow. It returns
	// nil when ctx is done, and also returns nil — rather than an error — when
	// the watcher cannot be established at all, because a service that cannot
	// watch its vault still serves and the index's backstop window keeps it
	// converging.
	Run(ctx context.Context) error
}

// newTaskIndexFSWatcher constructs the filesystem watcher the task index watcher
// drives.
//
// ⚠️ It is a package variable rather than a direct call so a spec can drive the
// construction-failure path — the fail-soft rule that a watcher which cannot be
// created logs a warning and leaves the service serving. A real inotify failure
// cannot be produced on demand, and an untested fail-soft path is a fail-soft
// path that can silently become a fail-hard one.
var newTaskIndexFSWatcher = fsnotify.NewWatcher

// NewTaskIndexWatcher builds the watcher that keeps index current from the task
// files under vaultDir.
//
// ⚠️ An empty vaultDir yields a watcher that returns immediately: there is no
// vault to watch, and `filepath.Join("", taskDirName)` would resolve to a
// relative path rather than the vault's own directory.
func NewTaskIndexWatcher(index TaskIndex, vaultDir string) TaskIndexWatcher {
	return &taskIndexWatcher{
		index:    index,
		vaultDir: vaultDir,
	}
}

type taskIndexWatcher struct {
	index    TaskIndex
	vaultDir string
}

// Run watches `<vault>/25 Tasks/` and applies each change to the index, until
// ctx is cancelled.
//
// ⚠️ Every failure here is fail-soft. A watcher that cannot be created or whose
// directory cannot be added logs a warning and returns nil: the service still
// serves, and the index's backstop window keeps it converging. A watcher failure
// must never take the process down.
//
// ⚠️ The watcher is closed by defer, so a cancelled context leaves no inotify
// watch behind — the watch lives and dies with the service's runner.
//
// ⚠️ A change costs one file's bytes, not the vault's. The watcher hands
// ApplyPaths the paths the event burst named, and that re-reads exactly those
// files; the wholesale rebuild it replaced read 6,057 files and 61 MB to learn
// the same fact, once per event. The burst is still drained first — a
// `git checkout` or an obsidian-git autocommit names thousands of paths, and
// applying them one event at a time would open the same file repeatedly — but
// draining now coalesces the path list rather than the read.
//
// ⚠️ The ticker is the safety net, and it is the reason this loop has one at
// all. ApplyPaths covers every change an event named; it cannot cover the event
// that never arrived — a dropped inotify event, or a write that landed before
// the watch was established. Reconcile covers exactly that case, at a directory
// listing and one stat per entry rather than a read of every file, which is what
// lets the net stay while the goal's "no timer-driven full vault rescan" holds.
func (w *taskIndexWatcher) Run(ctx context.Context) error {
	if w.vaultDir == "" {
		return nil
	}
	watcher, err := newTaskIndexFSWatcher()
	if err != nil {
		glog.Warningf("create task index watcher failed: %v", err)
		return nil
	}
	defer watcher.Close()

	tasksDir := filepath.Join(w.vaultDir, taskDirName)
	if err := watcher.Add(tasksDir); err != nil {
		// The directory may not exist yet on an empty vault. The backstop covers
		// it, so this is a warning rather than a failure.
		glog.Warningf("watch vault tasks dir %s failed: %v", tasksDir, err)
		return nil
	}
	glog.V(2).Infof("watching vault tasks dir %s", tasksDir)

	// ⚠️ The window is the index's own backstop constant rather than a second
	// one declared here: the watcher's safety net and Lookup's are the same
	// window by design, and two constants that must agree is how they come not
	// to. The conversion is libtime's nanosecond count to time's.
	ticker := time.NewTicker(time.Duration(taskIndexBackstopWindow))
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			w.index.Reconcile(ctx)
		case event, ok := <-watcher.Events:
			if !ok {
				// The watcher was closed underneath the loop; there is nothing
				// left to watch and no error to report.
				return nil
			}
			glog.V(3).Infof("vault tasks dir changed: %s", event.Name)
			paths, drained := w.changedPaths(event, watcher.Events)
			if !drained {
				return nil
			}
			w.index.ApplyPaths(ctx, paths)
		case watchErr, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			glog.Warningf("task index watcher error: %v", watchErr)
		}
	}
}

// changedPaths drains the events already queued behind first and returns their
// vault-relative paths, reporting false when the event channel was closed
// underneath the loop.
//
// ⚠️ Draining is what makes one burst cost one pass, and the dedupe is what
// makes that literally true. A burst — an obsidian-git autocommit, a
// `git checkout` — names the same file more than once and names thousands of
// files; without the dedupe the drained list still carries the repeats, and
// ApplyPaths reads a file once per time the burst named it. The list is
// collected while draining rather than after, because the channel is being
// emptied either way.
//
// ⚠️ The paths are made relative to the watched vault here, at the one place the
// watcher's absolute event names are known, so ApplyPaths can compare them
// against the vault-relative task directory rather than resolving whatever a
// caller hands it. A path that cannot be made relative is dropped rather than
// passed on: it names something outside the vault, which is not a task file.
func (w *taskIndexWatcher) changedPaths(
	first fsnotify.Event,
	events chan fsnotify.Event,
) ([]string, bool) {
	seen := map[string]struct{}{}
	paths := make([]string, 0, 1)
	append := func(name string) {
		rel, err := filepath.Rel(w.vaultDir, name)
		if err != nil {
			// Not under the vault, so not a task file: dropped rather than
			// passed on for ApplyPaths to re-reject.
			return
		}
		if _, ok := seen[rel]; ok {
			return
		}
		seen[rel] = struct{}{}
		paths = append(paths, rel)
	}
	append(first.Name)
	for len(events) > 0 {
		queued, ok := <-events
		if !ok {
			return nil, false
		}
		append(queued.Name)
	}
	return paths, true
}
