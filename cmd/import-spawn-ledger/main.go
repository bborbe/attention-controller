// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command import-spawn-ledger moves the supervisor's file-backed spawn ledger into
// the session-ledger store, ONCE.
//
// ⚠️ **It must run while attention-controller is STOPPED, and that is a hard
// constraint rather than a preference.** The store is a libkv/BoltDB store and the
// service opens it once for the process's life; BoltDB takes an EXCLUSIVE lock on the
// file, so a second handle is refused — as a lock timeout rather than a readable error,
// which `NewSessionLedgerStore`'s own doc comment already states. `Import` WRITES, so a
// read-only open is not available to it. The intended window is therefore the deploy's
// own `launchctl kickstart` outage: stop, import, start.
//
// ⚠️ **The mapping is NOT a pass-through, and the import aborts as a whole if one row
// is bad.** `Import` validates EVERY entry inside a single transaction, and `Validate`
// requires `location` — which the file ledger never carried (present in 0 of the 1895
// live rows). So `location: local` is injected here, and the file's `parent_session` is
// renamed onto the endpoint's `spawner_session_id`. The remaining fields map 1:1.
//
// ⚠️ **Re-running is safe.** `Import` is idempotent by key and deliberately does NOT
// overwrite an existing row, so a second run adds 0 and a stale file can never roll a
// live write backwards.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	libboltkv "github.com/bborbe/boltkv"
	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	bolt "go.etcd.io/bbolt"

	"github.com/bborbe/attention-controller/pkg"
)

// openTimeout bounds the BoltDB lock wait. Long enough that a slow deploy window is not
// mistaken for a held lock, short enough that a mistaken run against a live service reports
// rather than hangs.
const openTimeout = 5 * time.Second

// fileRecord is the shape of one `~/.local/state/claude-supervisor/sessions/<sid>.json`.
//
// ⚠️ It is declared HERE rather than reusing the supervisor's own builder, because this
// tool reads a store the supervisor has already stopped writing — a shape that changes
// under it must fail loudly at parse time rather than silently import a truncated row.
type fileRecord struct {
	SessionID     string `json:"session_id"`
	AgentID       string `json:"agent_id"`
	Label         string `json:"label"`
	Mode          string `json:"mode"`
	ModeSource    string `json:"mode_source"`
	Cwd           string `json:"cwd"`
	Launcher      string `json:"launcher"`
	PaneID        string `json:"pane_id"`
	ResumedFrom   string `json:"resumed_from"`
	Policy        string `json:"policy"`
	ParentSession string `json:"parent_session"`
	Role          string `json:"role"`
	SpawnedAt     string `json:"spawned_at"`
	EndedAt       string `json:"ended_at"`
	Status        string `json:"status"`
	// ⚠️ An OBJECT in every row that carries one (245 of 1898, measured 2026-10-14), never a
	// string — a `string` here fails to unmarshal those rows and the importer drops them.
	Result         json.RawMessage `json:"result"`
	WorkerID       string          `json:"worker_id"`
	Target         string          `json:"target"`
	UnknownReason  string          `json:"unknown_reason"`
	SupervisorExit string          `json:"supervisor_exited_at"`
	Parked         json.RawMessage `json:"parked_permission"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "import-spawn-ledger: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()
	home, err := os.UserHomeDir()
	if err != nil {
		return errors.Wrap(ctx, err, "resolve home failed")
	}

	ledgerDir := os.Getenv("LEDGER_DIR")
	if ledgerDir == "" {
		ledgerDir = filepath.Join(home, ".local", "state", "claude-supervisor", "sessions")
	}
	dataDir := os.Getenv("DATADIR")
	if dataDir == "" {
		return errors.Wrap(
			ctx,
			errors.New(ctx, "DATADIR is required — the store cannot be opened without it"),
			"read DATADIR failed",
		)
	}

	entries, unparsable, noID, err := readLedgerDir(ctx, ledgerDir)
	if err != nil {
		return err
	}
	// ⚠️ The two skip reasons are reported SEPARATELY, and that is the point rather than
	// tidiness: they mean opposite things. An unparseable row is a file this tool could not
	// read — a defect worth chasing, and each one is named on stderr as it is hit. A row with
	// no `session_id` is a legitimate skip, since `session_id` is the identity and its only
	// join key. Folding them into one "no session_id" count, which this did, reports every
	// unreadable row under a reason that is false for it.
	fmt.Printf("read %d row(s) from %s\n", len(entries), ledgerDir)
	if unparsable > 0 {
		fmt.Printf("  %d row(s) SKIPPED — unreadable JSON, each named above\n", unparsable)
	}
	if noID > 0 {
		fmt.Printf("  %d row(s) skipped — no session_id\n", noID)
	}

	// ⚠️ A TIMEOUT, and without it this command HANGS rather than failing. `OpenDir` passes
	// `bolt.DefaultOptions`, whose `Timeout` is 0, and bbolt's `flock` with a zero timeout
	// loops on `LOCK_NB` forever — so a run against a live service would sit silently with
	// no output and the diagnostic below would be unreachable. This comment previously
	// claimed the refusal "arrives as a lock timeout"; it does not, and the option below is
	// what makes that claim true rather than aspirational.
	db, err := libboltkv.OpenDir(
		ctx,
		dataDir,
		func(opts *bolt.Options) { opts.Timeout = openTimeout },
	)
	if err != nil {
		return errors.Wrap(
			ctx,
			err,
			"open the store failed — is attention-controller still running? it holds the BoltDB lock for its whole life, so this command must run in the deploy's stop window",
		)
	}
	store := pkg.NewSessionLedgerStore(db)

	added, err := store.Import(ctx, entries)
	if err != nil {
		return errors.Wrap(
			ctx,
			err,
			"import failed — the whole import is one transaction, so nothing was written",
		)
	}
	fmt.Printf(
		"imported %d new row(s); %d already present and left untouched\n",
		added,
		len(entries)-added,
	)
	return nil
}

// readLedgerDir maps every readable file in the ledger directory onto a store entry.
//
// ⚠️ A row with no `session_id` is COUNTED AND SKIPPED rather than imported under a
// synthesised key: `session_id` is the row's identity and its only join key, so an
// invented one would be addressable but meaningless. The count is reported because a
// silently dropped row is indistinguishable from one that was never there.
func readLedgerDir(ctx context.Context, dir string) (pkg.SessionLedgerEntries, int, int, error) {
	names, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0, 0, errors.Wrapf(ctx, err, "read ledger dir %s failed", dir)
	}
	entries := pkg.SessionLedgerEntries{}
	unparsable, noID := 0, 0
	for _, name := range names {
		if filepath.Ext(name.Name()) != ".json" {
			continue
		}
		//nolint:gosec // G703 is a false positive here: `dir` is the operator-supplied
		// ROOT and `name` comes from os.ReadDir of that SAME directory, so there is no
		// untrusted path component — a name containing a separator cannot appear in a
		// ReadDir result.
		raw, err := os.ReadFile(filepath.Join(dir, name.Name()))
		if err != nil {
			return nil, 0, 0, errors.Wrapf(ctx, err, "read %s failed", name.Name())
		}
		var record fileRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			// ⚠️ Skipped rather than fatal — one unreadable file must not abort a migration
			// of 1895 good ones — but NAMED on stderr, because a silently dropped row is
			// indistinguishable from one that was never there.
			fmt.Fprintf(os.Stderr, "  unreadable: %s: %v\n", name.Name(), err)
			unparsable++
			continue
		}
		if record.SessionID == "" {
			noID++
			continue
		}
		entry, err := toEntry(ctx, record)
		if err != nil {
			return nil, 0, 0, err
		}
		entries = append(entries, entry)
	}
	return entries, unparsable, noID, nil
}

// toEntry is the mapping, and the two differences from the file shape are the whole of
// it: `location` is injected (the file never carried it and the store requires it), and
// `parent_session` becomes `spawner_session_id` (one name for one thing on the wire).
func toEntry(ctx context.Context, record fileRecord) (pkg.SessionLedgerEntry, error) {
	spawnedAt, err := time.Parse(time.RFC3339, record.SpawnedAt)
	if err != nil {
		return pkg.SessionLedgerEntry{}, errors.Wrapf(
			ctx,
			err,
			"parse spawned_at %q for %s failed",
			record.SpawnedAt,
			record.SessionID,
		)
	}
	entry := pkg.SessionLedgerEntry{
		SessionID:        record.SessionID,
		AgentID:          record.AgentID,
		Label:            record.Label,
		Mode:             record.Mode,
		ModeSource:       record.ModeSource,
		Launcher:         record.Launcher,
		Cwd:              record.Cwd,
		PaneID:           record.PaneID,
		ResumedFrom:      record.ResumedFrom,
		Policy:           record.Policy,
		SpawnerSessionID: record.ParentSession,
		Role:             record.Role,
		// Injected: every row the file ledger holds is a local one. `pod` belongs to
		// the sibling pod task and nothing in this ledger can be one.
		Location:      pkg.LocalSessionHeartbeatLocation,
		SpawnedAt:     libtime.DateTime(spawnedAt),
		Status:        record.Status,
		Result:        record.Result,
		WorkerID:      record.WorkerID,
		Target:        record.Target,
		UnknownReason: record.UnknownReason,
	}
	// ⚠️ Carried opaquely, matching how the writer publishes it: the store never reads
	// inside it, and reshaping it here would be a second implementation of the writer's
	// secret-redaction rule — the drift the redaction cannot survive.
	//
	// ⚠️ And a row whose `parked_permission` is JSON `null` (75 of 1898, measured
	// 2026-10-14) unmarshals to a NON-NIL RawMessage holding the four bytes `null`, so
	// `omitempty` would not drop it and the imported row would serve
	// `"parked_permission": null` where the field is documented as ABSENT when no gate is
	// held. The store's own spec asserts that absence, so the guard is what keeps the
	// imported rows reading the way the written ones do.
	if !isJSONNull(record.Parked) {
		entry.ParkedPermission = record.Parked
	}
	if record.EndedAt != "" {
		endedAt, err := time.Parse(time.RFC3339, record.EndedAt)
		if err != nil {
			return pkg.SessionLedgerEntry{}, errors.Wrapf(
				ctx,
				err,
				"parse ended_at %q for %s failed",
				record.EndedAt,
				record.SessionID,
			)
		}
		converted := libtime.DateTime(endedAt)
		entry.EndedAt = &converted
	}
	if record.SupervisorExit != "" {
		exitedAt, err := time.Parse(time.RFC3339, record.SupervisorExit)
		if err != nil {
			return pkg.SessionLedgerEntry{}, errors.Wrapf(
				ctx,
				err,
				"parse supervisor_exited_at %q for %s failed",
				record.SupervisorExit,
				record.SessionID,
			)
		}
		converted := libtime.DateTime(exitedAt)
		entry.SupervisorExitedAt = &converted
	}
	return entry, nil
}

// isJSONNull reports whether a raw JSON value is the literal `null`, or absent entirely.
//
// ⚠️ It exists because `json.RawMessage` cannot tell "absent" from "present and null":
// both arrive as a non-nil slice for the latter and a nil one for the former, and
// `omitempty` drops only the nil. A field documented as ABSENT when it holds nothing must
// therefore be guarded explicitly rather than left to the struct tag.
func isJSONNull(raw json.RawMessage) bool {
	return len(raw) == 0 || string(raw) == "null"
}
