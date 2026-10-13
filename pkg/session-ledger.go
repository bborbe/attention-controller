// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"

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
	// ModeSource is which of argument / env / config / default decided Mode. It
	// is carried because the mode alone cannot say whether a headless worker
	// was ASKED for or merely inherited, which is the question asked when a
	// fleet turns out to be running the wrong way.
	ModeSource string `json:"mode_source"`
	// Launcher is the command the worker was started with, when there was one.
	Launcher string `json:"launcher"`
	// Cwd is the directory the worker was spawned in.
	//
	// ⚠️ It is carried because `list_agents` renders it and the roster's own
	// reader (`registry-rehydrate.mjs`) maps it; a row without it reads back as
	// a worker whose directory is unknown, which is indistinguishable from one
	// that has none.
	Cwd string `json:"cwd"`
	// WorkerID is the A2A controller's id for a worker running as a container
	// or pod, empty for every local and cluster mode.
	//
	// ⚠️ **NOTHING ELSE CAN MAP A CONTROLLER WORKER BACK TO ITS SESSION.** The
	// liveness mirror lists the controller's workers by id and joins them to
	// ledger rows on this field alone, so dropping it makes every A2A worker
	// read dead after a supervisor restart. The worker's TOKEN is deliberately
	// NOT carried: it authenticates turns, and this row is served to any reader.
	WorkerID string `json:"worker_id"`
	// Target is the spawn target whose name doubles as an A2A worker's status
	// (`docker` / `k8s`), empty for every other mode.
	Target string `json:"target"`
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
	// SupervisorExitedAt is when the supervisor that owned the worker went away
	// without observing its outcome, absent when that never happened.
	//
	// ⚠️ A POINTER for the same reason EndedAt is: "the supervisor is still
	// running" and "it exited at the zero instant" must stay distinguishable.
	SupervisorExitedAt *libtime.DateTime `json:"supervisor_exited_at,omitempty"`
	// UnknownReason says WHY the supervisor stopped watching, and it is the half
	// that makes SupervisorExitedAt readable: a headless worker dies with its
	// supervisor mid-turn while an interactive one outlives it, so the same
	// `unknown` status means opposite things in the two cases.
	UnknownReason string `json:"unknown_reason"`
	// Result is the worker's terminal result, absent while it has none.
	//
	// ⚠️ **CARRIED OPAQUELY, and a `string` here was a real defect rather than a
	// tidiness question.** Every one of the 245 non-null `result` values in the live
	// ledger is a JSON OBJECT (`{"subtype":…,"num_turns":…,"permission_denials":…}` —
	// measured 2026-10-14, 245 object / 0 string), so a `string` field fails to
	// unmarshal them: the one-time import would silently DROP 245 of 1898 rows, and the
	// POST route would answer 400 to every write carrying a terminal result — which is
	// every cluster worker's and every finished worker's. The store never reads inside
	// the value; the supervisor's reader does (`result: record.result ?? null`), and an
	// opaque blob is exactly the shape it expects.
	Result json.RawMessage `json:"result,omitempty"`
	// ParkedPermission is the permission prompt the worker was parked on when
	// its supervisor last wrote, absent when it holds none.
	//
	// ⚠️ **CARRIED OPAQUELY, and the opacity is deliberate.** This is the
	// durable half of a parked gate: it outlives the spawner so a resume can
	// carry the operator's decision to the worker's next turn. The store never
	// reads inside it — only the supervisor that wrote it does — and typing it
	// here would mean re-implementing that writer's secret-redaction rule in a
	// second language, which is a drift the redaction cannot survive. A blob the
	// store does not interpret cannot drift from the one that wrote it.
	ParkedPermission json.RawMessage `json:"parked_permission,omitempty"`
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
