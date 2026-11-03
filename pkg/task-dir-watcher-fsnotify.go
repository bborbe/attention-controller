// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !darwin || !cgo

package pkg

import (
	"sync"

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
		done:    make(chan struct{}),
	}
	w.wg.Add(1)
	go w.forward()
	return w, nil
}

type fsnotifyTaskDirWatcher struct {
	watcher   *fsnotify.Watcher
	events    chan string
	done      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

// forward relays event names until fsnotify closes its channel or Close runs.
//
// ⚠️ The send selects on done: once the consumer has stopped reading, a full
// buffer would otherwise park this goroutine forever after Close.
func (w *fsnotifyTaskDirWatcher) forward() {
	defer w.wg.Done()
	defer close(w.events)
	for event := range w.watcher.Events {
		select {
		case w.events <- event.Name:
		case <-w.done:
			return
		}
	}
}

func (w *fsnotifyTaskDirWatcher) Events() <-chan string { return w.events }

func (w *fsnotifyTaskDirWatcher) Errors() <-chan error { return w.watcher.Errors }

func (w *fsnotifyTaskDirWatcher) Close() error {
	var err error
	w.closeOnce.Do(func() {
		close(w.done)
		err = w.watcher.Close()
		w.wg.Wait()
	})
	return err
}
