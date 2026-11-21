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

	"github.com/golang/glog"
)

// fleetManagerBranch is the `branch` value a Fleet Manager registration carries.
// It is the same constant the Python filter's `FLEET_BRANCH` holds.
const fleetManagerBranch = "fleet"

// FleetManagerRegistration is one `branch: fleet` record from the worker-manager
// state directory.
//
// ⚠️ The file's BASE NAME is the session id — the record itself does not repeat
// it. Reading the id from the JSON would resolve every registration as "" and
// make the whole fleet look unregistered.
type FleetManagerRegistration struct {
	// SessionID is the registering session's own id.
	SessionID string
	// ResolvedAt is the registration's own timestamp. Newest wins, and it is a
	// string because the writer's format is an RFC3339 literal whose ordering is
	// already lexicographic.
	ResolvedAt string
}

// FleetManagerRegistry lists the Fleet Manager registrations the fleet wrote.
//
// ⚠️ An unreadable directory returns an EMPTY slice, not an error: a host with no
// worker-manager state has no Fleet Manager, and the caller keeps the gate. The
// distinction that matters is "readable and empty" vs "unreadable", and both
// answer "no destination" here because the fallback is to keep, never to drop.
type FleetManagerRegistry interface {
	Registrations(ctx context.Context) []FleetManagerRegistration
}

// NewFleetManagerRegistry reads `<workerManagerDir>/*.json`.
func NewFleetManagerRegistry(workerManagerDir string) FleetManagerRegistry {
	return &fleetManagerRegistry{dir: workerManagerDir}
}

type fleetManagerRegistry struct {
	dir string
}

// Registrations implements FleetManagerRegistry.
func (r *fleetManagerRegistry) Registrations(_ context.Context) []FleetManagerRegistration {
	if r.dir == "" {
		return nil
	}
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		glog.V(2).Infof("fleet registry: read %s failed: %v", r.dir, err)
		return nil
	}
	registrations := make([]FleetManagerRegistration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(r.dir, entry.Name()))
		if err != nil {
			continue
		}
		var record struct {
			Branch     string `json:"branch"`
			ResolvedAt string `json:"resolved_at"`
		}
		if err := json.Unmarshal(raw, &record); err != nil {
			continue
		}
		if record.Branch != fleetManagerBranch {
			continue
		}
		registrations = append(registrations, FleetManagerRegistration{
			SessionID:  strings.TrimSuffix(entry.Name(), ".json"),
			ResolvedAt: record.ResolvedAt,
		})
	}
	return registrations
}
