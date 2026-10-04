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

	"github.com/bborbe/collection"
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

// sessionRegistryEntry is one `<pid>.json` in the session registry. Three
// fields are read across the store: `SessionID` by the liveness check, and
// `SessionID`, `Name` and `NameSource` by the provenance resolver, which needs
// the name a session holds *now* to prove pane ownership and the source that
// says who chose that name before it renders it. One definition so the two
// readers cannot disagree about what an entry is.
type sessionRegistryEntry struct {
	SessionID  string `json:"sessionId"`
	Name       string `json:"name"`
	NameSource string `json:"nameSource"`
}

// SessionIDs is a collection of live session ids, as one listing of the
// registry produced them.
type SessionIDs []string

// Contains reports whether the collection holds the given session id. The empty
// id is never a member: no registry entry carries it, so a caller asking about
// it is told "not live" rather than matching an entry that recorded nothing.
func (s SessionIDs) Contains(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	return collection.Contains(s, sessionID)
}

// sessionSnapshotter is the one-listing capability the real checker also
// satisfies. ⚠️ It is deliberately NOT a member of SessionLivenessChecker: a
// zero-value Counterfeiter fake of that interface would return "unreadable"
// from a stubbed LiveSessions, and an unreadable registry reads as *everything
// is live*, which would silently disable pruning in every test that injects
// one. Keeping it separate lets the read path take a snapshot from the real
// checker while every injected fake keeps working through the fallback.
type sessionSnapshotter interface {
	// LiveSessions lists the live sessions in one read of the registry. The
	// second result is false when the registry could not be read at all.
	LiveSessions(ctx context.Context) (SessionIDs, bool)
}

// IsLive reports whether any registry entry carries this session id. The
// registry is authoritative: an entry is deleted on exit, so presence means
// live and absence means gone.
func (s *sessionLivenessChecker) IsLive(ctx context.Context, sessionID string) bool {
	if sessionID == "" {
		return false
	}
	ids, readable := s.LiveSessions(ctx)
	if !readable {
		// An unreadable registry cannot prove a session is dead. Reporting
		// "gone" here would sweep every legitimate item the moment the store
		// ran somewhere the registry is absent — so an unreadable registry
		// reads as live, and the failure is logged rather than acted on.
		return true
	}
	return ids.Contains(sessionID)
}

// LiveSessions lists every session id the registry holds, in one pass. It is
// the ONLY place Readdirnames is called on the registry, so a read of the store
// pays one listing rather than one per item.
//
// The second result is false when the registry could not be read at all — an
// absent directory, an unopenable one, or a failed listing. That is a distinct
// answer from an empty set: an empty readable registry proves every session is
// gone, while an unreadable one proves nothing and reads as live.
func (s *sessionLivenessChecker) LiveSessions(ctx context.Context) (SessionIDs, bool) {
	// The registry is opened as an os.Root so every read is confined beneath
	// it — the entry names come from ReadDir, but scoping the handle makes the
	// confinement structural rather than an assumption about those names.
	root, err := os.OpenRoot(s.sessionsDir)
	if err != nil {
		glog.Warningf("session registry %s unreadable: %v", s.sessionsDir, err)
		return nil, false
	}
	defer root.Close()

	entries, err := root.Open(".")
	if err != nil {
		glog.Warningf("session registry %s unreadable: %v", s.sessionsDir, err)
		return nil, false
	}
	defer entries.Close()

	names, err := entries.Readdirnames(-1)
	if err != nil {
		glog.Warningf("session registry %s listing failed: %v", s.sessionsDir, err)
		return nil, false
	}
	ids := make(SessionIDs, 0, len(names))
	for _, name := range names {
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		if sessionID, ok := s.entrySessionID(root, name); ok {
			ids = append(ids, sessionID)
		}
	}
	return ids, true
}

// entrySessionID reads one registry entry and returns the session id it
// carries. ok is false for an unreadable file, an unparseable one, or one that
// records no session id — none of which contributes an id to the listing.
func (s *sessionLivenessChecker) entrySessionID(root *os.Root, name string) (string, bool) {
	content, err := root.ReadFile(name)
	if err != nil {
		glog.V(3).Infof("read session registry entry %s failed: %v", name, err)
		return "", false
	}
	var entry sessionRegistryEntry
	if err := json.Unmarshal(content, &entry); err != nil {
		glog.V(3).Infof("parse session registry entry %s failed: %v", name, err)
		return "", false
	}
	if entry.SessionID == "" {
		return "", false
	}
	return entry.SessionID, true
}

// sessionLiveness answers "is this session live?" for one caller. Both the
// checker itself and a one-read snapshot satisfy it, so a caller can be handed
// either without knowing which.
type sessionLiveness interface {
	IsLive(ctx context.Context, sessionID string) bool
}

// sessionSnapshot is one read's view of the session registry: the ids the
// listing collected, and whether the listing could be read at all. An
// unreadable snapshot reports every session live, the same direction the
// checker takes, so a read that could not see the registry keeps everything
// rather than sweeping it.
type sessionSnapshot struct {
	ids      SessionIDs
	readable bool
}

// IsLive reports whether the snapshot holds this session id.
func (s sessionSnapshot) IsLive(ctx context.Context, sessionID string) bool {
	if sessionID == "" {
		return false
	}
	if !s.readable {
		return true
	}
	return s.ids.Contains(sessionID)
}

// readSessionLiveness answers liveness for ONE caller, resolving the registry at
// most once. The read path builds one for its classification and the prune
// builds its own for the re-check, so a read holds two of these and the registry
// is listed at most twice per read.
//
// ⚠️ The snapshot is taken LAZILY, on the first session-liveness lookup. A read
// that tests no session-model item — one whose items all carry heartbeat refs —
// must not list the registry at all, and resolving in the constructor would
// list it for every read regardless of what the read contains.
type readSessionLiveness struct {
	checker  SessionLivenessChecker
	resolved sessionLiveness
}

// newReadSessionLiveness creates a per-caller liveness source over a checker.
func newReadSessionLiveness(checker SessionLivenessChecker) *readSessionLiveness {
	return &readSessionLiveness{checker: checker}
}

// IsLive resolves the source on the first call and answers from it thereafter.
func (r *readSessionLiveness) IsLive(ctx context.Context, sessionID string) bool {
	r.resolveNow(ctx)
	return r.resolved.IsLive(ctx, sessionID)
}

// resolveNow takes the one snapshot this source will use, if it has not been
// taken already.
//
// ⚠️ It exists for the prune, which must resolve BEFORE it opens its write
// transaction: resolving lazily inside that transaction would hold the writer
// lock across a whole registry listing, which is the lock-hold the read split
// exists to avoid. The snapshot is still lazy in the sense that a read that
// prunes nothing never builds a source at all.
func (r *readSessionLiveness) resolveNow(ctx context.Context) {
	if r.resolved == nil {
		r.resolved = r.resolve(ctx)
	}
}

// resolve takes the one snapshot this read will use. A checker that can list
// the registry is snapshotted once; one that cannot — a Counterfeiter fake, or
// any future remote probe — is used directly, so the read still answers and the
// interface stays the only contract a caller must satisfy.
func (r *readSessionLiveness) resolve(ctx context.Context) sessionLiveness {
	snapshotter, ok := r.checker.(sessionSnapshotter)
	if !ok {
		return r.checker
	}
	ids, readable := snapshotter.LiveSessions(ctx)
	return sessionSnapshot{ids: ids, readable: readable}
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
