// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
)

// readSessionNames reads the session registry as `session id -> name`.
//
// ⚠️ ONE reader, two callers — deliberately. The provenance resolver needs a
// session's name to prove pane ownership, and the worker-coverage resolver needs
// the names of the live sessions to decide whether a manager covers a worker.
// Two readers of the same directory would be two answers to "what is this
// session called", and the failure is silent: a card would render under one name
// and be judged covered under another.
//
// It fails open in the direction its callers can survive: an unreadable
// directory or an unparseable entry leaves that session absent from the map, and
// both callers treat an absent session as unprovable rather than as a negative
// answer.
func readSessionNames(ctx context.Context, sessionsDir string) map[string]sessionName {
	names, _ := readSessionNamesWithError(ctx, sessionsDir)
	return names
}

// readSessionNamesWithError is readSessionNames plus the error the fail-open
// callers discard. The who-needs-me feed reports a failed registry read as a
// per-source status, which needs the error; the provenance resolver keeps its
// fail-open rendering and calls readSessionNames. One implementation, so the
// two cannot disagree about which entries exist.
func readSessionNamesWithError(
	ctx context.Context,
	sessionsDir string,
) (map[string]sessionName, error) {
	names := map[string]sessionName{}
	entries, err := readSessionRegistryEntries(ctx, sessionsDir)
	for _, record := range entries {
		names[record.SessionID] = sessionName{
			Name:   record.Name,
			Source: record.NameSource,
		}
	}
	return names, err
}

// readSessionStatuses reads the session registry as `session id -> status`.
//
// The status is the registry's own `idle` / `busy` / `waiting` / `shell` word —
// the fact the gate computation reads for every registered session, and the one
// the transcript cannot supply: the transcript answers WHICH call, the registry
// answers IS IT MOVING.
//
// It returns an error when the directory is unreadable, so a caller can tell a
// failed read from an empty registry. An empty map is a positive claim ("nobody
// is registered"), and the caller acts on it — an unreadable registry makes
// every session without a fresh heartbeat read `unregistered`.
//
// ⚠️ An entry carrying no status is SKIPPED rather than recorded as "". The
// Python watcher's `d.get("status")` answers `None` for such an entry, which its
// gate reads as unregistered; recording "" would instead give it a status word
// that matches no row and hold it silently forever.
func readSessionStatuses(ctx context.Context, sessionsDir string) (map[string]string, error) {
	statuses := map[string]string{}
	entries, err := readSessionRegistryEntries(ctx, sessionsDir)
	for _, record := range entries {
		if record.Status == "" {
			continue
		}
		statuses[record.SessionID] = record.Status
	}
	return statuses, err
}

// readSessionRegistryEntries walks the session registry once and returns every
// entry that parsed.
//
// ⚠️ ONE walk, two projections. readSessionNames needs the name and its source;
// readSessionStatuses needs the status. Two walks of the same directory would be
// two answers to "what is in the registry", and the failure is silent: a session
// could be named from one pass and statused from another.
//
// A failed directory read is an ERROR, never an empty result — an empty registry
// is a positive claim the callers act on. A single unparseable or unnamed entry
// is skipped and logged, which is the direction both callers survive: that
// session is merely absent from the projection.
func readSessionRegistryEntries(
	ctx context.Context,
	sessionsDir string,
) ([]sessionRegistryEntry, error) {
	if sessionsDir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		glog.V(2).Infof("read session registry %s failed: %v", sessionsDir, err)
		return nil, errors.Wrap(ctx, err, "read session registry failed")
	}
	out := make([]sessionRegistryEntry, 0, len(entries))
	for _, entry := range entries {
		select {
		case <-ctx.Done():
			glog.V(3).Infof("session registry scan cancelled")
			// A partial read is not a clean one: report the cancellation so the
			// feed does not stamp sources.registry = "ok" over an incomplete scan.
			return out, errors.Wrap(ctx, ctx.Err(), "session registry scan cancelled")
		default:
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(sessionsDir, entry.Name()))
		if err != nil {
			glog.V(3).Infof("read session registry entry %s failed: %v", entry.Name(), err)
			continue
		}
		var record sessionRegistryEntry
		if err := json.Unmarshal(content, &record); err != nil {
			glog.V(3).Infof("parse session registry entry %s failed: %v", entry.Name(), err)
			continue
		}
		if record.SessionID == "" {
			continue
		}
		out = append(out, record)
	}
	return out, nil
}
