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
	// List returns every row the store holds, ordered by session id. The order
	// is a property of the implementation, not of this contract — it exists so
	// two reads of an unchanged store agree, which is what makes a diff of two
	// listings meaningful.
	List(ctx context.Context) (SessionHeartbeats, error)
}

// SessionHeartbeats is a collection of SessionHeartbeat.
type SessionHeartbeats []SessionHeartbeat

// NewSessionHeartbeatStore creates a file-backed store over the given
// directory, stamping each row from the given clock.
//
// ⚠️ The directory is the EXISTING heartbeat store the supervisor scripts
// already read — `~/.local/state/claude-supervisor/live` by default. This
// extends that store rather than building a parallel one: the same files keep
// being read by the same Node readers, and the new fields are additive, so a
// record written before this change still parses and still means what it did.
func NewSessionHeartbeatStore(
	dir string,
	now libtime.CurrentDateTimeGetter,
) SessionHeartbeatStore {
	return &sessionHeartbeatStore{dir: dir, now: now}
}

type sessionHeartbeatStore struct {
	dir string
	now libtime.CurrentDateTimeGetter
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
	// Location and State are this task's additions; see SessionHeartbeat.
	Location string `json:"location,omitempty"`
	State    string `json:"state,omitempty"`
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
		State:     heartbeat.State.String(),
	}
	content, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return errors.Wrap(ctx, err, "marshal heartbeat failed")
	}
	return s.writeAtomic(ctx, heartbeat.SessionID, content)
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

// List returns every heartbeat row the store holds.
//
// ⚠️ A file that cannot be parsed is skipped with a log line rather than
// failing the listing. The directory is shared with the legacy writers and
// holds non-heartbeat files (`_cluster-reachability.json`), so one unexpected
// file must not take the whole read path down — but the skip is logged, so a
// genuinely corrupt heartbeat is visible rather than silent.
//
// ⚠️ That skip is DELIBERATELY asymmetric with Get, and the asymmetry is the
// safer direction rather than an oversight. Get is asked about one named
// session, so an unreadable row for it is a failure it must report rather than
// answer "absent" — "absent" is what renders a live session's card Resume.
// List is asked about the whole store, so refusing the entire listing over one
// bad file would take every OTHER session's liveness down with it. The cost is
// real and accepted: a directory of corrupt rows lists as empty, which reads as
// every session dead. The log line is what separates that from a genuinely
// empty store.
func (s *sessionHeartbeatStore) List(ctx context.Context) (SessionHeartbeats, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			// A store that has never been written to holds nothing. That is an
			// empty answer, not a failure.
			return SessionHeartbeats{}, nil
		}
		return nil, errors.Wrapf(ctx, err, "list heartbeat dir '%s' failed", s.dir)
	}
	heartbeats := make(SessionHeartbeats, 0, len(entries))
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
		heartbeats = append(heartbeats, record.toHeartbeat())
	}
	sort.Slice(heartbeats, func(i, j int) bool {
		return heartbeats[i].SessionID < heartbeats[j].SessionID
	})
	return heartbeats, nil
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
//
// ⚠️ The enum fields are passed through VERBATIM and are deliberately NOT
// validated against `Available*`. This is a read of a SHARED store, so a row
// this endpoint did not write is a legitimate input: the legacy cluster writer
// stamps `source: "cluster"`, which is not in this task's write vocabulary, and
// a row predating these fields carries no source at all. Rejecting those on
// read would make the endpoint blind to exactly the rows it exists to surface,
// and inventing a value for them would put a declaration on the wire that no
// producer made. So the reader gets what the file says — and the contract that
// matters is on the CONSUMER: only `mcp-timer` counts as proof of liveness, and
// an unrecognised or empty source is not proof of anything.
func (r sessionHeartbeatFile) toHeartbeat() SessionHeartbeat {
	return SessionHeartbeat{
		SessionID: r.SessionID,
		Task:      r.Task,
		Vault:     r.Vault,
		Location:  SessionHeartbeatLocation(r.Location),
		State:     SessionHeartbeatState(r.State),
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
// to the argument-struct field the HTTP surface wires as
// `-session-heartbeat-dir` — so reading the env var again here would be a
// second config surface for one value, and the copy would be dead anyway: this
// helper is reached only when that field is EMPTY, which is precisely when the
// env var is unset. What is left here is the part a static default cannot
// express, because it depends on the user's home.
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
) (SessionHeartbeatStore, error) {
	dir := sessionHeartbeatDirFromEnv()
	if dir == "" {
		return nil, errors.New(ctx, "resolve heartbeat dir failed")
	}
	return NewSessionHeartbeatStore(dir, now), nil
}
