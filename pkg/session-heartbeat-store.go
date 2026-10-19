// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

// sessionHeartbeatFileSuffix is the extension every heartbeat record carries.
// The legacy writers already emit it, so the reader and those writers agree on
// what a record is without a migration.
const sessionHeartbeatFileSuffix = ".json"

//counterfeiter:generate -o ../mocks/session-heartbeat-store.go --fake-name SessionHeartbeatStore . SessionHeartbeatStore

// SessionHeartbeatStore is the session-heartbeat store's read and write path.
//
// ⚠️ The store is deliberately DUMB about freshness: Get and List return the
// records as they lie on disk and say nothing about whether a row is still
// live. Freshness is computed at the read by the caller against the window it
// was configured with, so the window has exactly one home and a test can move
// the clock without the store knowing. A store that decided freshness itself
// would put the 60-second constant in two places.
type SessionHeartbeatStore interface {
	// Post records one session's heartbeat, replacing any row that session
	// already holds. A session has one current state, not a history.
	Post(ctx context.Context, heartbeat SessionHeartbeat) error
	// Get returns the row for one session id. The bool is false when no row
	// exists — a distinct answer from a row that exists and is stale, which is
	// what lets a reader tell `absent` from `stale` (SC10).
	Get(ctx context.Context, sessionID string) (SessionHeartbeat, bool, error)
	// List returns every row the store holds, in no guaranteed order.
	List(ctx context.Context) (SessionHeartbeats, error)
}

// SessionHeartbeatService is the store plus its lifecycle: the read/write path
// the handlers need, and the background loop that keeps the in-memory snapshot
// current.
//
// ⚠️ Run is NOT on SessionHeartbeatStore. The handlers never start or own the
// loop, and a handler-facing interface that carried it would force every fake to
// answer a question the handlers never ask. The constructor returns this wider
// type so `main` can hand the same instance to both the router and the runner —
// one store, two consumers, the pattern the task index and the ledger already
// follow in this repo.
type SessionHeartbeatService interface {
	SessionHeartbeatStore
	// Run watches the heartbeat directory until ctx is cancelled, keeping the
	// in-memory snapshot current and pruning rows past the retention bound. It
	// is fail-soft: a watcher that cannot be established logs and leaves the
	// snapshot served as-is, because an unwatchable directory costs freshness
	// and loses nothing.
	Run(ctx context.Context) error
}

// NewSessionHeartbeatStore creates a file-backed store over the given
// directory, stamping each row from the given clock.
//
// ⚠️ The directory is the EXISTING heartbeat store the supervisor scripts
// already read — `~/.local/state/claude-supervisor/live` by default. This
// extends that store rather than building a parallel one: the same files keep
// being read by the same Node readers, and the fields this task adds are
// additive, so a LEGACY row still parses and still means what it did. Legacy
// here means the rows the Node writers produce, which never carried an activity
// under any key.
//
// ⚠️ That promise is ONE-DIRECTIONAL, and the direction it does not cover is
// named here so the two comments in this file cannot be read as contradicting
// each other. A row written by the PREVIOUS RELEASE OF THIS STORE carries
// `state` and no `activity`; it still parses, but `toHeartbeat` reads
// `r.Activity`, so it surfaces with an empty State. Accepted rather than
// repaired: nothing in-repo consumes State beyond serialization, `Post`
// rewrites a live session's row wholesale within one post interval, and a dead
// session's row already reads not-live by age. A `state` fallback would keep
// the collision key alive on the READ path, which is the thing this rename
// exists to remove. Pinned by a spec, so a later fallback refactor is visible.
//
// ⚠️ **List is served from an in-memory snapshot, not from a per-request disk
// scan.** The directory is shared with the legacy Node writers and accumulated
// 1,600 rows; reading every file per request cost ~0.95 s under load and
// serialised concurrent readers, which pushed a 5 s client timeout into the
// spawn path and refused spawns fleet-wide (2026-10-19). The snapshot is loaded
// once, updated by Post, and kept current by Run's watcher.
//
// ⚠️ retention bounds the SNAPSHOT and the directory, not liveness. It is
// deliberately not SessionHeartbeatWindow: a row past the 60 s window is still
// a real row (it answers `stale`, and session-liveness.py resolves prefixes
// against it), so pruning at that boundary would collapse `absent` onto `stale`
// — the one distinction the endpoint exists to keep. Run deletes rows older
// than retention; nothing about the liveness window changes.
func NewSessionHeartbeatStore(
	dir string,
	now libtime.CurrentDateTimeGetter,
	retention libtime.Duration,
) SessionHeartbeatService {
	return &sessionHeartbeatStore{
		dir:       dir,
		now:       now,
		retention: retention,
	}
}

type sessionHeartbeatStore struct {
	dir       string
	now       libtime.CurrentDateTimeGetter
	retention libtime.Duration

	// loadMu serialises the one-time snapshot load so a burst of first requests
	// cannot each scan the directory. It is separate from mu because the load
	// does I/O, which mu must never cover.
	loadMu sync.Mutex

	// mu guards rows and loaded. rows is the snapshot List serves; loaded is
	// false until the first successful load, which is what keeps a store that
	// cannot be read an ERROR rather than an empty answer.
	mu     sync.RWMutex
	rows   map[string]SessionHeartbeat
	loaded bool
}

// sessionHeartbeatFile is the ON-DISK record.
//
// ⚠️ It is deliberately not SessionHeartbeat, because the two shapes differ and
// collapsing them would break the legacy readers. On disk the identity field is
// camelCase `sessionId` and the row carries `pid` and `mode`, which the legacy
// writers emit and the supervisor scripts read; on the wire the identity is
// snake_case `session_id` and the fields this task adds are declared. The wire
// is the endpoint's contract and the file is the shared store's — keeping them
// as two types is what stops a wire rename from silently orphaning every row a
// Node writer can still produce.
type sessionHeartbeatFile struct {
	// SessionID is camelCase on disk. See the type comment for why.
	SessionID string `json:"sessionId"`
	// PID is the posting process, emitted by the legacy writers. Absent on a
	// cluster row, which has no local pid.
	PID *int `json:"pid"`
	// Mode is the legacy writers' own mode (`headless`, `cluster`). Kept so a
	// legacy reader sees the field it expects.
	Mode string `json:"mode"`
	// At is when the row was stamped. RFC3339Nano, matching what the legacy
	// writers emit, so both sides parse each other's files.
	At libtime.DateTime `json:"at"`
	// Source is who wrote the row. The legacy value is `cluster`; this task
	// adds `hook`, `mcp-timer` and `manual`.
	Source string `json:"source"`
	// Task and Vault are this task's additions — the session's anchor, so a
	// reader can say which body of work a live session is advancing.
	Task  string `json:"task,omitempty"`
	Vault string `json:"vault,omitempty"`
	// Location and Activity are this task's additions; see SessionHeartbeat.
	//
	// ⚠️ The FILE calls this `activity` while the WIRE calls it `state`, and the
	// asymmetry is deliberate rather than drift. This directory is shared with
	// four readers that own the word `state` for a LIVENESS VERDICT —
	// `worker-sessions.py`, `fleet-board.py`, `adopt-orphans.py` and
	// `session-liveness.py` all gate on `stamp.get("state", "live") != "live"`,
	// where a MISSING key must read as `live` rather than vanish.
	//
	// ⚠️ **Those four gates never see a raw stamp, and an earlier revision of this
	// comment claimed they did** — asserting that a row carrying `state: "busy"`
	// "reads as NOT-live" and frees a duplicate auto-resume. It does not: the
	// readers' single chokepoint parses a raw stamp and builds a fresh dict
	// carrying its OWN verdict, never reading the file's key. So this field is
	// WRITE-ONLY today, and the rename is PREVENTIVE — it removes a name that
	// collides with the verdict vocabulary before a raw-stamp reader exists to be
	// bitten by it.
	//
	// ⚠️ **The wire can therefore carry an EMPTY `state`.** With the fallback
	// deliberately absent, a row written by the previous release of this store
	// (which used `state`) still parses but projects `SessionHeartbeatState("")`
	// onto GET and LIST responses — a value `SessionHeartbeatState.Validate`
	// itself rejects. Accepted rather than repaired, and worth naming so an API
	// consumer reads it as an expected mid-deploy value rather than a bug: it
	// clears as soon as the writer posts again, and a dead session's row is
	// already not-live by age. See `toHeartbeat`.
	Location string `json:"location,omitempty"`
	Activity string `json:"activity,omitempty"`
}

// Post writes one heartbeat, replacing any row the session already holds.
//
// ⚠️ The write is atomic — a temp file in the same directory, then a rename.
// A reader polls this directory every couple of seconds, and a partial file
// read at that rate would parse as absent, which reads as *the session died*.
// The rename is what makes a row either wholly old or wholly new, never half.
func (s *sessionHeartbeatStore) Post(ctx context.Context, heartbeat SessionHeartbeat) error {
	if err := heartbeat.Validate(ctx); err != nil {
		return errors.Wrap(ctx, err, "validate heartbeat failed")
	}
	if err := validateSessionID(ctx, heartbeat.SessionID); err != nil {
		return err
	}
	// 0750 rather than 0755: the directory is only ever read by this process and
	// the supervisor scripts running as the same user, and the rows inside it
	// name each session's task and vault.
	if err := os.MkdirAll(s.dir, 0750); err != nil {
		return errors.Wrapf(ctx, err, "create heartbeat dir '%s' failed", s.dir)
	}
	record := sessionHeartbeatFile{
		SessionID: heartbeat.SessionID,
		At:        s.now.Now(),
		Source:    heartbeat.Source.String(),
		Task:      heartbeat.Task,
		Vault:     heartbeat.Vault,
		Location:  heartbeat.Location.String(),
		Activity:  heartbeat.State.String(),
	}
	content, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return errors.Wrap(ctx, err, "marshal heartbeat failed")
	}
	if err := s.writeAtomic(ctx, heartbeat.SessionID, content); err != nil {
		return errors.Wrap(ctx, err, "write heartbeat failed")
	}
	// ⚠️ The snapshot is updated only AFTER the write succeeds, so a failed post
	// cannot leave the cache claiming a row the directory does not hold. A
	// not-yet-loaded snapshot stays not-loaded: the lazy load re-reads the
	// directory wholesale and would otherwise race this single-row update.
	s.mu.Lock()
	if s.loaded {
		s.rows[heartbeat.SessionID] = record.toHeartbeat()
	}
	s.mu.Unlock()
	return nil
}

// writeAtomic writes the record to a temp file in the store directory and
// renames it over the target. See Post for why the rename is load-bearing.
func (s *sessionHeartbeatStore) writeAtomic(
	ctx context.Context,
	sessionID string,
	content []byte,
) error {
	temp, err := os.CreateTemp(s.dir, ".heartbeat-*.tmp")
	if err != nil {
		return errors.Wrapf(ctx, err, "create temp heartbeat file in '%s' failed", s.dir)
	}
	tempName := temp.Name()
	// Best-effort cleanup: after a successful rename the temp name no longer
	// exists, and Remove's error is therefore not worth reporting.
	defer func() { _ = os.Remove(tempName) }()

	if _, err := temp.Write(content); err != nil {
		_ = temp.Close()
		return errors.Wrapf(ctx, err, "write temp heartbeat file '%s' failed", tempName)
	}
	if err := temp.Close(); err != nil {
		return errors.Wrapf(ctx, err, "close temp heartbeat file '%s' failed", tempName)
	}
	// 0600 rather than the legacy 0644: the row names the session's task and
	// vault, which is more than the legacy shape carried.
	if err := os.Chmod(tempName, 0600); err != nil {
		return errors.Wrapf(ctx, err, "chmod temp heartbeat file '%s' failed", tempName)
	}
	target := s.path(sessionID)
	if err := os.Rename(tempName, target); err != nil {
		return errors.Wrapf(ctx, err, "rename heartbeat file to '%s' failed", target)
	}
	return nil
}

// Get returns the row for one session id, and false when there is none.
//
// ⚠️ An unreadable or unparseable file is reported as an ERROR, never as
// absent. "Absent" is the answer that makes a live session's card read Resume,
// so a read failure must not be able to produce it — the same rule
// worker-sessions.py states as "an unreadable store is UNKNOWN, never 0".
// A row whose file simply does not exist is the only absent case.
func (s *sessionHeartbeatStore) Get(
	ctx context.Context,
	sessionID string,
) (SessionHeartbeat, bool, error) {
	if err := validateSessionID(ctx, sessionID); err != nil {
		return SessionHeartbeat{}, false, err
	}
	content, err := os.ReadFile(s.path(sessionID))
	if err != nil {
		if os.IsNotExist(err) {
			return SessionHeartbeat{}, false, nil
		}
		return SessionHeartbeat{}, false, errors.Wrapf(
			ctx, err, "read heartbeat file for '%s' failed", sessionID,
		)
	}
	record, err := parseSessionHeartbeatFile(ctx, content)
	if err != nil {
		return SessionHeartbeat{}, false, err
	}
	if record == nil {
		// The file exists and carries no session id — not a heartbeat row.
		return SessionHeartbeat{}, false, nil
	}
	return record.toHeartbeat(), true, nil
}

// List returns every heartbeat row the store holds, from the in-memory snapshot.
//
// ⚠️ The snapshot is the whole point of this read path: a per-request directory
// scan cost ~0.95 s under load and serialised concurrent readers, which pushed
// a 5 s client timeout into the spawn path. The disk is read once (here, lazily)
// and thereafter by the watcher — never by a request.
//
// ⚠️ A load failure is an ERROR, never an empty list. "No rows" is the answer
// that renders a live session's card Resume.
func (s *sessionHeartbeatStore) List(ctx context.Context) (SessionHeartbeats, error) {
	if err := s.ensureLoaded(ctx); err != nil {
		return nil, errors.Wrap(ctx, err, "ensure heartbeat snapshot loaded failed")
	}
	s.mu.RLock()
	heartbeats := make(SessionHeartbeats, 0, len(s.rows))
	for _, heartbeat := range s.rows {
		heartbeats = append(heartbeats, heartbeat)
	}
	s.mu.RUnlock()
	sort.Slice(heartbeats, func(i, j int) bool {
		return heartbeats[i].SessionID < heartbeats[j].SessionID
	})
	return heartbeats, nil
}

// ensureLoaded loads the snapshot on first use, and returns the load's error on
// every call until a load succeeds.
//
// ⚠️ A failed load is retried by the next List rather than latched. The
// directory can be unreadable transiently (a permission flip, a full disk during
// a rename), and a latched failure would make the endpoint answer 500 for the
// rest of the process's life. It is also never turned into an empty answer:
// "no rows" is what renders a live session's card Resume.
func (s *sessionHeartbeatStore) ensureLoaded(ctx context.Context) error {
	s.mu.RLock()
	loaded := s.loaded
	s.mu.RUnlock()
	if loaded {
		return nil
	}
	s.loadMu.Lock()
	defer s.loadMu.Unlock()
	// Re-check under the load lock: a burst of first requests must scan once,
	// and the winner publishes a snapshot the rest then serve.
	s.mu.RLock()
	loaded = s.loaded
	s.mu.RUnlock()
	if loaded {
		return nil
	}
	rows, err := s.readAll(ctx)
	if err != nil {
		return errors.Wrap(ctx, err, "load heartbeat snapshot failed")
	}
	s.mu.Lock()
	s.rows = rows
	s.loaded = true
	s.mu.Unlock()
	return nil
}

// readAll scans the directory into a fresh snapshot map. It is the ONLY place
// that reads every row, and it runs off the request path after the first load.
//
// ⚠️ A file that cannot be parsed is skipped with a log line rather than
// failing the listing. The directory is shared with the legacy writers and
// holds non-heartbeat files (`_cluster-reachability.json`), so one unexpected
// file must not take the whole read path down — but the skip is logged, so a
// genuinely corrupt heartbeat is visible rather than silent.
func (s *sessionHeartbeatStore) readAll(
	ctx context.Context,
) (map[string]SessionHeartbeat, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			// A store that has never been written to holds nothing. That is an
			// empty answer, not a failure.
			return map[string]SessionHeartbeat{}, nil
		}
		return nil, errors.Wrapf(ctx, err, "list heartbeat dir '%s' failed", s.dir)
	}
	rows := make(map[string]SessionHeartbeat, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), sessionHeartbeatFileSuffix) {
			continue
		}
		content, err := os.ReadFile(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			glog.Warningf("read heartbeat file %s failed: %v", entry.Name(), err)
			continue
		}
		record, err := parseSessionHeartbeatFile(ctx, content)
		if err != nil {
			glog.Warningf("parse heartbeat file %s failed: %v", entry.Name(), err)
			continue
		}
		if record == nil {
			// Not a heartbeat row — the store directory also carries the
			// cluster reachability marker. Silence is right here; a warning
			// per poll would be noise about a file that is meant to be there.
			continue
		}
		rows[record.SessionID] = record.toHeartbeat()
	}
	return rows, nil
}

// parseSessionHeartbeatFile parses one file's bytes, returning (nil, nil) when
// the file is not a heartbeat row — which is the case for any file in the
// shared directory that carries no session id.
func parseSessionHeartbeatFile(
	ctx context.Context,
	content []byte,
) (*sessionHeartbeatFile, error) {
	var record sessionHeartbeatFile
	if err := json.Unmarshal(content, &record); err != nil {
		return nil, errors.Wrap(ctx, err, "unmarshal heartbeat file failed")
	}
	if record.SessionID == "" {
		return nil, nil
	}
	return &record, nil
}

// toHeartbeat projects the on-disk record onto the wire shape.
func (r sessionHeartbeatFile) toHeartbeat() SessionHeartbeat {
	return SessionHeartbeat{
		SessionID: r.SessionID,
		Task:      r.Task,
		Vault:     r.Vault,
		Location:  SessionHeartbeatLocation(r.Location),
		State:     SessionHeartbeatState(r.Activity),
		Source:    SessionHeartbeatSource(r.Source),
		At:        r.At,
	}
}

// path resolves one session's file inside the store directory.
func (s *sessionHeartbeatStore) path(sessionID string) string {
	return filepath.Join(s.dir, sessionID+sessionHeartbeatFileSuffix)
}

// validateSessionID rejects an id that cannot be a filename.
//
// ⚠️ The id arrives from the network, so it is the one input that could escape
// the store directory: `..` or a separator would let a post write anywhere the
// process can. The check is on the SHAPE (a plain token), not on a UUID
// pattern, because the legacy rows are keyed by whatever id the writer held and
// tightening it here would orphan them.
func validateSessionID(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return errors.Wrap(ctx, ErrInvalidSessionID, "session id is empty")
	}
	if sessionID == "." || sessionID == ".." {
		return errors.Wrapf(
			ctx,
			ErrInvalidSessionID,
			"session id '%s' is not a filename",
			sessionID,
		)
	}
	if strings.ContainsAny(sessionID, `/\`) {
		return errors.Wrapf(
			ctx, ErrInvalidSessionID, "session id '%s' contains a path separator", sessionID,
		)
	}
	return nil
}

// sessionHeartbeatDirFromEnv resolves the DEFAULT store directory.
//
// ⚠️ It deliberately does NOT read SESSION_HEARTBEAT_DIR. That override belongs
// to the `-session-heartbeat-dir` argument-struct field, which the arg parser
// already fills from that env var — so reading it again here would be a second
// config surface for one value, and the copy would be dead: this helper is only
// reached when the field is EMPTY, which is precisely when the env var is unset.
// What is left here is the part a static default cannot express, because it
// depends on the user's home.
func sessionHeartbeatDirFromEnv() string {
	stateDir := os.Getenv("XDG_STATE_HOME")
	if stateDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		stateDir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(stateDir, "claude-supervisor", "live")
}

// NewSessionHeartbeatStoreFromEnv creates a store over the default heartbeat
// directory. An explicit directory is passed to NewSessionHeartbeatStore
// instead — see sessionHeartbeatDirFromEnv for why this helper reads no
// override of its own.
func NewSessionHeartbeatStoreFromEnv(
	ctx context.Context,
	now libtime.CurrentDateTimeGetter,
	retention libtime.Duration,
) (SessionHeartbeatService, error) {
	dir := sessionHeartbeatDirFromEnv()
	if dir == "" {
		return nil, errors.New(ctx, "resolve heartbeat dir failed")
	}
	return NewSessionHeartbeatStore(dir, now, retention), nil
}
