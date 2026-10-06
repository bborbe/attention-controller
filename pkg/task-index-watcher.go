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
// ⚠️ There is deliberately no debounce or timer: one event rebuilds once. Task
// files are written occasionally rather than in bursts, and the rebuild is
// single-flight anyway, so a debounce would add a delay to the common case to
// smooth a case that does not occur. The backstop bounds whatever a dropped
// event leaves behind.
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
			w.index.Rebuild(ctx)
		case watchErr, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			glog.Warningf("task index watcher error: %v", watchErr)
		}
	}
}
