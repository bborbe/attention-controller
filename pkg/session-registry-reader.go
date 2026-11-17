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
	if sessionsDir == "" {
		return names, nil
	}
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		glog.V(2).Infof("read session registry %s failed: %v", sessionsDir, err)
		return names, errors.Wrap(ctx, err, "read session registry failed")
	}
	for _, entry := range entries {
		select {
		case <-ctx.Done():
			glog.V(3).Infof("session registry scan cancelled")
			return names, nil
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
		if record.SessionID != "" {
			names[record.SessionID] = sessionName{
				Name:   record.Name,
				Source: record.NameSource,
			}
		}
	}
	return names, nil
}
