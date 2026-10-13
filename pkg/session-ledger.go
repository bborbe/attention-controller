// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/bborbe/validation"
)

// SessionLedgerLocation is where the logged session runs.
//
// ⚠️ It carries the SAME vocabulary as SessionHeartbeatLocation rather than a
// parallel one, because a reader joins the two stores on `session_id` and would
// otherwise have to translate `local` between them. The heartbeat's own comment
// already declares `pod` for the sibling pod task; this is that vocabulary
// reached from the other store, not a second declaration of it.
type SessionLedgerLocation = SessionHeartbeatLocation

// SessionLedgerEntry is one row of the spawn ledger: what was spawned, by whom,
// and where it runs.
//
// ⚠️ **`SessionID` is the identity and the ONLY sound key.** The ledger this
// replaces keyed its roster by `agent_id`, which is a per-process counter that
// restarts at 0 on every supervisor reconnect — so the live ledger holds 1891
// rows across only 187 distinct `agent_id` values, with 215 rows claiming
// `agent_1`. `agent_id` is therefore carried here as a NON-UNIQUE ATTRIBUTE and
// must never key, dedupe or join a row. `session_id` is unique in the same
// ledger (1891 rows / 1891 distinct), and the legacy file name always equalled
// it, so the import maps one file to exactly one row.
//
// ⚠️ **`Status` is NOT liveness.** 1480 of those 1891 rows assert `running`,
// because nothing closes an entry whose supervisor went away. Liveness comes
// from the heartbeat store, read beside this one; a reader that takes `status`
// for liveness resurrects every worker the fleet ever opened.
type SessionLedgerEntry struct {
	// SessionID is the spawned session's own CLAUDE_CODE_SESSION_ID. It is the
	// row's identity and the key every read resolves against.
	SessionID string `json:"session_id"`
	// AgentID is the supervisor's per-process handle for the worker
	// (`agent_1`, `agent_2`, …). ⚠️ NOT unique — see the type comment. It is
	// stored so an operator can correlate a row with a pane's own output, and
	// for nothing else.
	AgentID string `json:"agent_id"`
	// Label is the human-readable name the spawn carried.
	Label string `json:"label"`
	// Mode is how the worker runs: `interactive`, `headless` or `cluster`.
	Mode string `json:"mode"`
	// Launcher is the command the worker was started with, when there was one.
	Launcher string `json:"launcher"`
	// PaneID is the WezTerm pane the worker occupies, empty for a headless
	// worker which has no pane. ⚠️ A pane id is recycled across tab moves and
	// restarts, so it identifies a pane, never a session.
	PaneID string `json:"pane_id"`
	// ResumedFrom is the session this worker was resumed from, empty for a
	// fresh spawn.
	ResumedFrom string `json:"resumed_from"`
	// Policy is the approval-policy file the worker was spawned under, empty
	// when it inherited the server's own.
	Policy string `json:"policy"`
	// SpawnerSessionID is the session that requested the spawn. It is what
	// makes a row attributable: a manager reads the rows it spawned, and an
	// orphan is a row whose spawner is gone.
	SpawnerSessionID string `json:"spawner_session_id"`
	// Role is the role the spawn declared (`agent`, `manager`, `human`), empty
	// for a row written before the field existed.
	Role string `json:"role"`
	// Task is the vault task the session is anchored to, empty when unanchored.
	Task string `json:"task"`
	// Vault is the vault the task lives in, empty when unanchored.
	Vault string `json:"vault"`
	// Location is where the session runs. `local` for every row this task
	// writes; `pod` is declared for the sibling pod task.
	Location SessionLedgerLocation `json:"location"`
	// SpawnedAt is when the worker was spawned.
	SpawnedAt libtime.DateTime `json:"spawned_at"`
	// EndedAt is when the worker was observed to end, absent while it has not.
	// ⚠️ It is a POINTER so "still running" and "ended at the zero instant" are
	// distinguishable on the wire — the same reason Get returns a `found` bool.
	EndedAt *libtime.DateTime `json:"ended_at,omitempty"`
	// Status is the supervisor's own last word on the worker. ⚠️ NOT liveness:
	// see the type comment. It is carried because the ledger carried it, and
	// dropping it would make the import lossy.
	Status string `json:"status"`
	// Result is the worker's terminal result, empty while it has none.
	Result string `json:"result"`
}

// SessionLedgerEntries is a collection of SessionLedgerEntry.
type SessionLedgerEntries []SessionLedgerEntry

// Validate returns an error when the row is not a well-formed ledger entry.
//
// ⚠️ It judges the CALLER's declaration only, and the split is deliberate:
// `ended_at` is the supervisor's to write when a worker ends, so requiring it
// here would reject every live row — the same split SessionHeartbeat.Validate
// makes for `At`.
func (e SessionLedgerEntry) Validate(ctx context.Context) error {
	return validation.All{
		validation.Name("SessionID", SessionID(e.SessionID)),
		validation.Name("Task", validation.HasValidationFunc(e.validateOptionalTask)),
		validation.Name("Vault", validation.HasValidationFunc(e.validateOptionalVault)),
		validation.Name("Location", e.Location),
		validation.Name("SpawnedAt", validation.HasValidationFunc(e.validateSpawnedAt)),
	}.Validate(ctx)
}

// validateOptionalTask enforces that task and vault are declared together.
//
// ⚠️ Both empty is LEGAL, and that is a deliberate divergence from a reading of
// the task's SC6 that would require them. A session with no task anchor is a
// real state — the heartbeat store's sibling rule says so in as many words, and
// the live ledger holds rows spawned outside any task. Requiring the pair would
// make those rows unpostable and push the writer into inventing a task name,
// which is worse than storing an honest empty. What is refused is the
// HALF-declared anchor: a task name with no vault cannot be resolved, because
// task names collide across vaults.
func (e SessionLedgerEntry) validateOptionalTask(ctx context.Context) error {
	if e.Task == "" && e.Vault != "" {
		return errors.Wrapf(
			ctx,
			validation.Error,
			"vault '%s' declared with no task",
			e.Vault,
		)
	}
	return nil
}

// validateOptionalVault is the other half of validateOptionalTask's pairing
// rule. See that method for why the two are declared together or not at all.
func (e SessionLedgerEntry) validateOptionalVault(ctx context.Context) error {
	if e.Vault == "" && e.Task != "" {
		return errors.Wrapf(
			ctx,
			validation.Error,
			"task '%s' declared with no vault",
			e.Task,
		)
	}
	return nil
}

// validateSpawnedAt refuses a zero spawn instant.
//
// ⚠️ The legacy ledger read `spawned_at` from the supervisor's own clock at
// spawn time, so a zero value there meant a writer bug rather than a legitimate
// state. Accepting it would sort the row to the epoch and make every
// age-based read wrong in the same direction.
func (e SessionLedgerEntry) validateSpawnedAt(ctx context.Context) error {
	if e.SpawnedAt.IsZero() {
		return errors.Wrapf(ctx, validation.Error, "spawned_at is unset")
	}
	return nil
}
