// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/fsnotify/fsnotify"
	"github.com/golang/glog"
)

const (
	// sessionHeartbeatReconcileInterval is the backstop rescan. The watcher
	// covers every change an event names; this covers the event that never
	// arrived. 30 s is one post interval, so a missed write is corrected before
	// the next one is due — the bound that matters because an under-count is
	// the one direction a spawn cap must not fail in.
	sessionHeartbeatReconcileInterval = 30 * time.Second
	// sessionHeartbeatBurstWindow drains a burst before applying it. Sessions
	// post on the same tick, and applying them one event at a time would open
	// the same files repeatedly.
	sessionHeartbeatBurstWindow = 100 * time.Millisecond
)

// newSessionHeartbeatFSWatcher is a package variable so a spec can drive the
// construction-failure path: a real inotify failure cannot be produced on
// demand, and an untested fail-soft path is one that can silently become
// fail-hard.
var newSessionHeartbeatFSWatcher = fsnotify.NewWatcher

// Run keeps the in-memory snapshot current from the heartbeat directory until
// ctx is cancelled.
//
// ⚠️ Every failure here is fail-soft. A watcher that cannot be created or whose
// directory cannot be added logs a warning and the loop continues on the
// backstop alone: the endpoint still serves its last good snapshot, and the
// reconcile keeps it converging. A watcher failure must never take the process
// down — a stale count costs one spawn decision, a dead board costs everything.
//
// ⚠️ The first reconcile both loads the snapshot and prunes rows past the
// retention bound, so the first List after startup already serves the bounded
// set. Its failure is logged rather than fatal: List retries the load itself.
func (s *sessionHeartbeatStore) Run(ctx context.Context) error {
	if err := s.reconcile(ctx); err != nil {
		glog.Warningf("load session heartbeat store '%s' failed: %v", s.dir, err)
	}

	watcher, err := s.watch(ctx)
	if err != nil {
		glog.Warningf("watch session heartbeat dir '%s' failed: %v", s.dir, err)
	}

	// nil channels block forever in a select, so a watcher that could not be
	// established simply leaves the backstop as the only trigger.
	var events <-chan fsnotify.Event
	var watchErrors <-chan error
	if watcher != nil {
		defer watcher.Close()
		events = watcher.Events
		watchErrors = watcher.Errors
	}

	ticker := time.NewTicker(sessionHeartbeatReconcileInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			s.applyEventBurst(ctx, event, events)
		case watchErr, ok := <-watchErrors:
			if !ok {
				watchErrors = nil
				continue
			}
			glog.Warningf("watch session heartbeat dir '%s' failed: %v", s.dir, watchErr)
		case <-ticker.C:
			if err := s.reconcile(ctx); err != nil {
				glog.Warningf("reconcile session heartbeat dir '%s' failed: %v", s.dir, err)
			}
		}
	}
}

// watch creates the filesystem watcher over the store directory.
//
// ⚠️ The directory is created first. It may not exist yet on a host where no
// session has posted, and fsnotify refuses to watch a missing path — the same
// answer the store's own Post gives with MkdirAll.
func (s *sessionHeartbeatStore) watch(ctx context.Context) (*fsnotify.Watcher, error) {
	if err := os.MkdirAll(s.dir, 0750); err != nil {
		return nil, errors.Wrapf(ctx, err, "create heartbeat dir '%s' failed", s.dir)
	}
	watcher, err := newSessionHeartbeatFSWatcher()
	if err != nil {
		return nil, errors.Wrap(ctx, err, "create heartbeat watcher failed")
	}
	if err := watcher.Add(s.dir); err != nil {
		_ = watcher.Close()
		return nil, errors.Wrapf(ctx, err, "watch heartbeat dir '%s' failed", s.dir)
	}
	return watcher, nil
}

// applyEventBurst drains the events that follow first for one short window,
// then applies every path the burst named.
//
// ⚠️ The drain is why a batch of sessions posting on the same tick costs one
// apply instead of N. It is bounded by the timer, not by the burst's size, so a
// continuous stream cannot starve the apply.
func (s *sessionHeartbeatStore) applyEventBurst(
	ctx context.Context,
	first fsnotify.Event,
	events <-chan fsnotify.Event,
) {
	paths := make([]string, 0, 8)
	add := func(event fsnotify.Event) {
		if !strings.HasSuffix(event.Name, sessionHeartbeatFileSuffix) {
			return
		}
		paths = append(paths, event.Name)
	}
	add(first)

	timer := time.NewTimer(sessionHeartbeatBurstWindow)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				// The outer loop sees the closed channel on its next turn and
				// stops selecting on it; the paths collected so far are applied.
				s.applyPaths(ctx, paths)
				return
			}
			add(event)
		case <-timer.C:
			s.applyPaths(ctx, paths)
			return
		case <-ctx.Done():
			s.applyPaths(ctx, paths)
			return
		}
	}
}

// applyPaths re-reads the named files and applies them to the snapshot.
//
// ⚠️ The files are read and parsed OUTSIDE the lock. A read under the write lock
// would block every List for the duration of a disk read, which is exactly the
// coupling this change exists to remove.
func (s *sessionHeartbeatStore) applyPaths(ctx context.Context, paths []string) {
	if len(paths) == 0 {
		return
	}
	type update struct {
		heartbeat SessionHeartbeat
		present   bool
	}
	updates := make(map[string]update, len(paths))
	for _, path := range paths {
		sessionID := strings.TrimSuffix(filepath.Base(path), sessionHeartbeatFileSuffix)
		if sessionID == "" {
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				// The row was removed (a rename, or the retention prune). Its
				// absence is the answer.
				updates[sessionID] = update{}
				continue
			}
			glog.Warningf("read heartbeat file %s failed: %v", path, err)
			continue
		}
		record, err := parseSessionHeartbeatFile(ctx, content)
		if err != nil {
			glog.Warningf("parse heartbeat file %s failed: %v", path, err)
			updates[sessionID] = update{}
			continue
		}
		if record == nil {
			// Not a heartbeat row (the directory also carries the cluster
			// reachability marker). It must not stay in the snapshot under a
			// session id it does not declare.
			updates[sessionID] = update{}
			continue
		}
		updates[record.SessionID] = update{heartbeat: record.toHeartbeat(), present: true}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded {
		// The lazy load reads the directory wholesale; a partial apply on top of
		// an unloaded snapshot would publish an incomplete one.
		return
	}
	for sessionID, u := range updates {
		if !u.present {
			delete(s.rows, sessionID)
			continue
		}
		s.rows[sessionID] = u.heartbeat
	}
}

// reconcile rebuilds the snapshot from the directory and prunes rows past the
// retention bound.
//
// ⚠️ It replaces the whole map rather than merging. A merge cannot answer "the
// file is gone", and a row that disappears from the directory must disappear
// from the snapshot — otherwise a deleted row would read live forever.
func (s *sessionHeartbeatStore) reconcile(ctx context.Context) error {
	rows, err := s.readAll(ctx)
	if err != nil {
		return errors.Wrap(ctx, err, "reconcile heartbeat snapshot failed")
	}
	s.pruneExpired(rows)
	s.mu.Lock()
	s.rows = rows
	s.loaded = true
	s.mu.Unlock()
	return nil
}

// pruneExpired deletes rows older than the retention bound from both the
// snapshot and the directory.
//
// ⚠️ Retention is NOT the liveness window. A row past SessionHeartbeatWindow is
// still a real row — it answers `stale`, and session-liveness.py resolves
// prefixes against it. Pruning at that boundary would make a posted-and-stale
// id read as never-posted (404), collapsing the one distinction the endpoint
// exists to keep.
//
// ⚠️ A row whose timestamp is ZERO is left alone rather than deleted: it cannot
// be dated, and deleting on no evidence is how a row a reader still needs
// disappears.
func (s *sessionHeartbeatStore) pruneExpired(rows map[string]SessionHeartbeat) {
	if s.retention.Duration() <= 0 {
		return
	}
	cutoff := s.now.Now().Add(libtime.Duration(-s.retention.Duration()))
	for sessionID, heartbeat := range rows {
		if heartbeat.At.IsZero() || !heartbeat.At.Before(cutoff) {
			continue
		}
		delete(rows, sessionID)
		if err := os.Remove(s.path(sessionID)); err != nil && !os.IsNotExist(err) {
			glog.Warningf("prune expired heartbeat '%s' failed: %v", sessionID, err)
		}
	}
}
