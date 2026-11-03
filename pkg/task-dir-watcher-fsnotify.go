// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !darwin || !cgo

package pkg

import (
	"github.com/fsnotify/fsnotify"
)

// newTaskDirWatcher watches dir through fsnotify — inotify on Linux, where a
// change event names the changed file and costs no directory rescan.
func newTaskDirWatcher(dir string) (taskDirWatcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := watcher.Add(dir); err != nil {
		_ = watcher.Close()
		return nil, err
	}
	w := &fsnotifyTaskDirWatcher{
		watcher: watcher,
		events:  make(chan string, 256),
	}
	go w.forward()
	return w, nil
}

type fsnotifyTaskDirWatcher struct {
	watcher *fsnotify.Watcher
	events  chan string
}

// forward relays event names until fsnotify closes its channel.
func (w *fsnotifyTaskDirWatcher) forward() {
	defer close(w.events)
	for event := range w.watcher.Events {
		w.events <- event.Name
	}
}

func (w *fsnotifyTaskDirWatcher) Events() <-chan string { return w.events }

func (w *fsnotifyTaskDirWatcher) Errors() <-chan error { return w.watcher.Errors }

func (w *fsnotifyTaskDirWatcher) Close() error { return w.watcher.Close() }
