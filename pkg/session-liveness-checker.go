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

//counterfeiter:generate -o ../mocks/session-liveness-checker.go --fake-name SessionLivenessChecker . SessionLivenessChecker

// SessionLivenessChecker reports whether a long-lived producer's session is
// still registered and live.
//
// ⚠️ The schema requires this check but names no registry — a recorded silence.
// The registry read here is `~/.claude/sessions/<pid>.json`, whose entry is
// deleted when the session exits, so its presence is the authoritative
// live-vs-exited probe. It is a *host-local* probe: a session on another host
// reads as gone. That is the store's own resolution of the silence, not a
// schema value, and it is deliberately behind an interface so a remote probe
// can replace it without touching the store.
type SessionLivenessChecker interface {
	IsLive(ctx context.Context, sessionID string) bool
}

// NewSessionLivenessChecker creates a checker reading the given registry
// directory.
func NewSessionLivenessChecker(sessionsDir string) SessionLivenessChecker {
	return &sessionLivenessChecker{sessionsDir: sessionsDir}
}

type sessionLivenessChecker struct {
	sessionsDir string
}

// sessionRegistryEntry is one `<pid>.json` in the session registry. Two fields
// are read across the store: `SessionID` by the liveness check, and `Name` by
// the provenance resolver, which needs the name a session holds *now* to prove
// pane ownership. One definition so the two readers cannot disagree about what
// an entry is.
type sessionRegistryEntry struct {
	SessionID string `json:"sessionId"`
	Name      string `json:"name"`
}

// IsLive reports whether any registry entry carries this session id. The
// registry is authoritative: an entry is deleted on exit, so presence means
// live and absence means gone.
func (s *sessionLivenessChecker) IsLive(ctx context.Context, sessionID string) bool {
	if sessionID == "" {
		return false
	}
	// The registry is opened as an os.Root so every read is confined beneath
	// it — the entry names come from ReadDir, but scoping the handle makes the
	// confinement structural rather than an assumption about those names.
	root, err := os.OpenRoot(s.sessionsDir)
	if err != nil {
		// An unreadable registry cannot prove a session is dead. Reporting
		// "gone" here would sweep every legitimate item the moment the store
		// ran somewhere the registry is absent — so an unreadable registry
		// reads as live, and the failure is logged rather than acted on.
		glog.Warningf("session registry %s unreadable: %v", s.sessionsDir, err)
		return true
	}
	defer root.Close()

	entries, err := root.Open(".")
	if err != nil {
		glog.Warningf("session registry %s unreadable: %v", s.sessionsDir, err)
		return true
	}
	defer entries.Close()

	names, err := entries.Readdirnames(-1)
	if err != nil {
		glog.Warningf("session registry %s listing failed: %v", s.sessionsDir, err)
		return true
	}
	for _, name := range names {
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		if s.entryMatches(root, name, sessionID) {
			return true
		}
	}
	return false
}

func (s *sessionLivenessChecker) entryMatches(root *os.Root, name string, sessionID string) bool {
	content, err := root.ReadFile(name)
	if err != nil {
		glog.V(3).Infof("read session registry entry %s failed: %v", name, err)
		return false
	}
	var entry sessionRegistryEntry
	if err := json.Unmarshal(content, &entry); err != nil {
		glog.V(3).Infof("parse session registry entry %s failed: %v", name, err)
		return false
	}
	return entry.SessionID == sessionID
}

// sessionsDirFromEnv resolves the registry directory, honouring an override so
// the store can be pointed at a different host's registry mount.
func sessionsDirFromEnv() string {
	if value := os.Getenv("SESSIONS_DIR"); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "sessions")
}

// NewSessionLivenessCheckerFromEnv creates a checker reading the registry
// directory from SESSIONS_DIR, falling back to ~/.claude/sessions.
func NewSessionLivenessCheckerFromEnv(ctx context.Context) (SessionLivenessChecker, error) {
	dir := sessionsDirFromEnv()
	if dir == "" {
		return nil, errors.New(ctx, "resolve sessions dir failed")
	}
	return NewSessionLivenessChecker(dir), nil
}
