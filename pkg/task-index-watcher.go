// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
	"github.com/golang/glog"
)

// TaskIndexWatcher keeps a TaskIndex current by watching the vault's task
// directory and rebuilding on every change.
//
// ⚠️ It watches exactly ONE directory — `<vault>/25 Tasks/` — and adds nothing
// else to the watch set. The index reads the goal and topic rungs too, but those
// change rarely and the backstop covers them; widening the watch set would make
// the watcher's scope a thing that can drift away from the index it serves.
type TaskIndexWatcher interface {
	// Run watches the task directory until ctx is cancelled, rebuilding the
	// index on every change. It returns nil when ctx is done, and also returns
	// nil — rather than an error — when the watcher cannot be established at
	// all, because a service that cannot watch its vault still serves and the
	// index's backstop window keeps it converging.
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

// Run watches `<vault>/25 Tasks/` and rebuilds the index on every change, until
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
// ⚠️ There is deliberately no debounce or timer, but every event already queued
// is drained before a single rebuild. A rebuild reads the whole vault — 25
// Tasks/, 24 Goals/ and 23 Topics/ — so rebuilding once per event turns a burst
// into one full-vault read per file. A timer would add a delay to the common
// single-file case to smooth a case the drain already handles. The backstop
// bounds whatever a dropped event leaves behind.
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

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-watcher.Events:
			if !ok {
				// The watcher was closed underneath the loop; there is nothing
				// left to watch and no error to report.
				return nil
			}
			glog.V(3).Infof("vault tasks dir changed: %s", event.Name)
			// ⚠️ Drain the events already queued before rebuilding. One rebuild
			// reads the whole vault (25 Tasks/, 24 Goals/ and 23 Topics/), so
			// rebuilding once per event turns a burst — an obsidian-git
			// autocommit, a `git checkout` — into one full-vault read per file.
			// That is the pathology this change exists to remove, and it would be
			// a regression against the two-second window it replaced. One rebuild
			// after the drain observes every change the burst made; a change that
			// lands mid-rebuild stays queued for the next pass.
			for len(watcher.Events) > 0 {
				if _, ok := <-watcher.Events; !ok {
					return nil
				}
			}
			w.index.Rebuild(ctx)
		case watchErr, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			glog.Warningf("task index watcher error: %v", watchErr)
		}
	}
}
