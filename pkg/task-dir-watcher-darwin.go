// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build darwin && cgo

package pkg

import (
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsevents"
)

// taskDirWatchLatency is how long FSEvents coalesces before delivering a batch.
// A burst (an obsidian-git autocommit) arrives as one batch rather than one
// callback per file.
const taskDirWatchLatency = 200 * time.Millisecond

// newTaskDirWatcher watches dir through FSEvents.
//
// ⚠️ Not fsnotify: its darwin backend is kqueue, which re-lists the directory
// and stats every entry on each directory change — see taskDirWatcher.
//
// ⚠️ FSEvents reports the real path, relative to the device root with no
// leading slash. A vault reached through a symlink would then never share a prefix with the vault directory the index
// compares against, and every event would be dropped as outside the vault. The
// real directory is resolved once here and each event is mapped back under dir.
func newTaskDirWatcher(dir string) (taskDirWatcher, error) {
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	stream := &fsevents.EventStream{
		Paths:   []string{realDir},
		Latency: taskDirWatchLatency,
		Flags:   fsevents.FileEvents | fsevents.NoDefer,
	}
	if err := stream.Start(); err != nil {
		return nil, err
	}
	w := &fseventsTaskDirWatcher{
		stream:  stream,
		dir:     dir,
		realDir: realDir,
		events:  make(chan string, 256),
		errors:  make(chan error),
		done:    make(chan struct{}),
	}
	go w.forward()
	return w, nil
}

type fseventsTaskDirWatcher struct {
	stream    *fsevents.EventStream
	dir       string
	realDir   string
	events    chan string
	errors    chan error
	done      chan struct{}
	closeOnce sync.Once
}

// forward relays each batch's paths, mapped back under dir, until Close.
func (w *fseventsTaskDirWatcher) forward() {
	defer close(w.events)
	defer close(w.errors)
	for {
		select {
		case <-w.done:
			return
		case batch, ok := <-w.stream.Events:
			if !ok {
				return
			}
			for _, event := range batch {
				name, ok := mapFSEventPath(event.Path, w.realDir, w.dir)
				if !ok {
					continue
				}
				select {
				case w.events <- name:
				case <-w.done:
					return
				}
			}
		}
	}
}

// mapFSEventPath turns an FSEvents path into the same path under dir, reporting
// false for a path outside realDir (including realDir itself, which is not a
// task file).
func mapFSEventPath(eventPath, realDir, dir string) (string, bool) {
	if !strings.HasPrefix(eventPath, "/") {
		eventPath = "/" + eventPath
	}
	rel, err := filepath.Rel(realDir, eventPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return filepath.Join(dir, rel), true
}

func (w *fseventsTaskDirWatcher) Events() <-chan string { return w.events }

func (w *fseventsTaskDirWatcher) Errors() <-chan error { return w.errors }

func (w *fseventsTaskDirWatcher) Close() error {
	w.closeOnce.Do(func() {
		w.stream.Stop()
		close(w.done)
	})
	return nil
}
