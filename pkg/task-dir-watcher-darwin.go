// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build darwin && cgo

package pkg

import (
	stderrors "errors"
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
//
// ⚠️ dir is made absolute first: FSEvents paths are always absolute, so a
// relative dir would share no prefix with any event and the watch would drop
// everything silently.
func newTaskDirWatcher(dir string) (taskDirWatcher, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	dir = absDir
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
	w.wg.Add(1)
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
	wg        sync.WaitGroup
}

// fseventsDropFlags mark a batch in which the kernel or the client queue
// dropped events: the paths named are not the full set of changes.
const fseventsDropFlags = fsevents.MustScanSubDirs | fsevents.KernelDropped | fsevents.UserDropped

// errFSEventsDropped is reported on Errors when FSEvents signals dropped events,
// so the consumer reconciles instead of waiting for the backstop window.
var errFSEventsDropped = stderrors.New("fsevents dropped events; rescan required")

// forward relays each batch's paths, mapped back under dir, until Close.
func (w *fseventsTaskDirWatcher) forward() {
	defer w.wg.Done()
	defer close(w.events)
	defer close(w.errors)
	for {
		select {
		case <-w.done:
			return
		case batch := <-w.stream.Events:
			if !w.relayBatch(batch) {
				return
			}
		}
	}
}

// relayBatch sends one batch's events on, reporting false once Close has run.
// A drop-flagged event becomes an error; an event outside the watched dir is
// skipped.
func (w *fseventsTaskDirWatcher) relayBatch(batch []fsevents.Event) bool {
	for _, event := range batch {
		if event.Flags&fseventsDropFlags != 0 {
			select {
			case w.errors <- errFSEventsDropped:
			case <-w.done:
				return false
			}
			continue
		}
		name, ok := mapFSEventPath(event.Path, w.realDir, w.dir)
		if !ok {
			continue
		}
		select {
		case w.events <- name:
		case <-w.done:
			return false
		}
	}
	return true
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
		w.wg.Wait()
	})
	return nil
}
