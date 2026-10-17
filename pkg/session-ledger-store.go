// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
)

// sessionLedgerBucketName is the bucket the spawn ledger lives in.
//
// ⚠️ It is a SEPARATE bucket from the attention-item store, and that separation
// is load-bearing rather than tidy. The two have different lifetimes — an item
// is closed and eventually swept, a ledger row is permanent — and a shared
// bucket would make any future sweep over one a candidate sweep over the other.
// The bucket's key is `session_id`, never `agent_id`; see SessionLedgerEntry
// for why the latter cannot be a key at all.
var sessionLedgerBucketName = libkv.NewBucketName("session-ledger")

//counterfeiter:generate -o ../mocks/session-ledger-store.go --fake-name SessionLedgerStore . SessionLedgerStore

// SessionLedgerStore is the spawn ledger's read and write path.
//
// ⚠️ **It answers IDENTITY, never LIVENESS.** Nothing here consults a clock or
// a heartbeat, and `List` returns rows whose `status` says `running` for
// workers that died days ago — 1480 of the 1891 rows the legacy ledger holds
// say exactly that, because nothing closes an entry whose supervisor went away.
// A caller that needs liveness reads the heartbeat store BESIDE this one and
// joins on `session_id`; the split is the same one `worker-sessions.mjs` and
// `registry-rehydrate.mjs` already make on the Node side.
type SessionLedgerStore interface {
	// Post records one spawn, replacing any row that session already holds. A
	// session is spawned once, so it has one row and not a history.
	Post(ctx context.Context, entry SessionLedgerEntry) error
	// Patch merges the named fields into the row for one session, leaving every
	// field the patch does not name exactly as its owner wrote it. The bool is
	// false when no row exists — the same `absent` answer Get gives, so a caller
	// can tell "never spawned" from "spawned and patched".
	//
	// ⚠️ **It exists because Post REPLACES a whole row.** The worker a manager
	// stops is often the one it did NOT spawn, so a stop recorded through Post by
	// an adopting server would rebuild the row from identity alone and blank
	// everything the owner had written — the loss the supervisor's single-writer
	// guard prevents by refusing the write altogether, at the cost of never
	// recording the stop. Merging is what lets the write land without the loss.
	Patch(
		ctx context.Context,
		sessionID string,
		patch SessionLedgerPatch,
	) (SessionLedgerEntry, bool, error)
	// Get returns the row for one session id. The bool is false when no row
	// exists — a distinct answer from a row that exists and is stale, which is
	// what lets a reader tell `absent` from `ended`.
	Get(ctx context.Context, sessionID string) (SessionLedgerEntry, bool, error)
	// List returns every row the store holds, in no guaranteed order.
	List(ctx context.Context) (SessionLedgerEntries, error)
	// Import adds the rows that are not already present and returns how many it
	// added. See the implementation for why it is idempotent.
	Import(ctx context.Context, entries SessionLedgerEntries) (int, error)
}

// sessionLedgerStore is the libkv-backed implementation.
type sessionLedgerStore struct {
	db      libkv.DB
	entries libkv.StoreTx[string, SessionLedgerEntry]
}

// NewSessionLedgerStore creates a store over the given database.
//
// ⚠️ It takes a `libkv.DB` rather than a directory, so the ledger shares the
// process's single BoltDB handle. A second handle on the same file is what
// BoltDB refuses, and the refusal arrives as a lock timeout rather than a
// readable error — the same reason NewAttentionStore is constructed this way.
func NewSessionLedgerStore(db libkv.DB) SessionLedgerStore {
	return &sessionLedgerStore{
		db:      db,
		entries: libkv.NewStoreTx[string, SessionLedgerEntry](sessionLedgerBucketName),
	}
}

// Post stores one row, keyed by its session id.
func (s *sessionLedgerStore) Post(ctx context.Context, entry SessionLedgerEntry) error {
	if err := entry.Validate(ctx); err != nil {
		return errors.Wrap(ctx, err, "validate session ledger entry failed")
	}
	err := s.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		if err := s.entries.Add(ctx, tx, entry.SessionID, entry); err != nil {
			return errors.Wrap(ctx, err, "add session ledger entry failed")
		}
		return nil
	})
	if err != nil {
		return errors.Wrap(ctx, err, "post session ledger entry failed")
	}
	return nil
}

// Patch merges the given fields into the row for one session id.
//
// ⚠️ **The read and the write share one transaction, and that is load-bearing
// rather than tidy.** A merge is a read-modify-write, so a read outside the
// transaction would let two patches that arrive together each merge into the
// pre-write row and the later one silently drop the earlier one's field — the same
// lost update the endpoint exists to prevent, reintroduced inside the store.
func (s *sessionLedgerStore) Patch(
	ctx context.Context,
	sessionID string,
	patch SessionLedgerPatch,
) (SessionLedgerEntry, bool, error) {
	var merged SessionLedgerEntry
	found := false
	err := s.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		merged = SessionLedgerEntry{}
		found = false
		got, err := s.entries.Get(ctx, tx, sessionID)
		if err != nil {
			// ⚠️ A missing key is a NORMAL answer here, not an error — the same
			// rule Get states, and the reason this reports `found` instead of
			// folding absence into the error branch. A patch against a row that was
			// never spawned is `absent` (404), not a failure.
			if isNotFound(err) {
				return nil
			}
			return errors.Wrap(ctx, err, "get session ledger entry failed")
		}
		next, err := patch.Apply(ctx, *got)
		if err != nil {
			return err
		}
		// ⚠️ The MERGED row is validated even though the patch alone is not.
		// `Post` validates every row it stores, so skipping this would make PATCH
		// the one route able to write a row the store's own readers cannot use:
		// clearing `spawned_at` would sort the row to the epoch and make every
		// age-based read wrong in the same direction.
		if err := next.Validate(ctx); err != nil {
			return errors.Wrapf(
				ctx,
				ErrSessionLedgerPatchInvalid,
				"merged row would be invalid: %v",
				err,
			)
		}
		if err := s.entries.Add(ctx, tx, sessionID, next); err != nil {
			return errors.Wrap(ctx, err, "add session ledger entry failed")
		}
		merged = next
		found = true
		return nil
	})
	if err != nil {
		return SessionLedgerEntry{}, false, err
	}
	return merged, found, nil
}

// Get returns the row for one session id.
func (s *sessionLedgerStore) Get(
	ctx context.Context,
	sessionID string,
) (SessionLedgerEntry, bool, error) {
	var entry SessionLedgerEntry
	found := false
	err := s.db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		got, err := s.entries.Get(ctx, tx, sessionID)
		if err != nil {
			// ⚠️ A missing key is a NORMAL answer here, not an error: the
			// endpoint's whole contract is that `absent` and `ended` are
			// different answers. Folding it into the error branch would make the
			// 404 indistinguishable from a store failure.
			if isNotFound(err) {
				return nil
			}
			return errors.Wrap(ctx, err, "get session ledger entry failed")
		}
		entry = *got
		found = true
		return nil
	})
	if err != nil {
		return SessionLedgerEntry{}, false, errors.Wrap(ctx, err, "get session ledger entry failed")
	}
	return entry, found, nil
}

// List returns every row the store holds.
func (s *sessionLedgerStore) List(ctx context.Context) (SessionLedgerEntries, error) {
	var entries SessionLedgerEntries
	err := s.db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		got, err := s.entries.List(ctx, tx)
		if err != nil {
			return errors.Wrap(ctx, err, "list session ledger entries failed")
		}
		entries = got
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "list session ledger entries failed")
	}
	return entries, nil
}

// Import adds the rows that are not already present.
//
// ⚠️ **It is idempotent by `session_id`, and that is the criterion rather than a
// nicety.** The one-time migration runs against a directory the supervisor is
// still writing to, so a second run — a retry after a partial failure, or an
// operator re-running it by hand — must add zero rows rather than duplicate the
// ledger under itself. Presence is decided by the key alone, so a re-import of
// a CHANGED row is skipped rather than applied: the endpoint is the source of
// truth once the switch lands, and an import that could overwrite it would let
// a stale file undo a live write.
//
// ⚠️ `added` is reset inside the transaction body, and the reset is DEFENSIVE rather
// than load-bearing. The justification this comment carried ("`db.Update` may run the
// closure more than once on a write conflict") is FALSE and was corrected 2026-10-14:
// `boltkv.Update` calls bbolt's `db.Update`, which runs its closure exactly once and
// does not retry. It is kept because counting outside the body would be wrong the day a
// retry is introduced, but it must not be read as a description of bbolt's behaviour
// today.
func (s *sessionLedgerStore) Import(
	ctx context.Context,
	entries SessionLedgerEntries,
) (int, error) {
	added := 0
	err := s.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		added = 0
		for _, entry := range entries {
			if err := entry.Validate(ctx); err != nil {
				return errors.Wrap(ctx, err, "validate session ledger entry failed")
			}
			_, err := s.entries.Get(ctx, tx, entry.SessionID)
			switch {
			case err == nil:
				continue
			case !isNotFound(err):
				return errors.Wrap(ctx, err, "get existing session ledger entry failed")
			}
			if err := s.entries.Add(ctx, tx, entry.SessionID, entry); err != nil {
				return errors.Wrap(ctx, err, "add session ledger entry failed")
			}
			added++
		}
		return nil
	})
	if err != nil {
		return 0, errors.Wrap(ctx, err, "import session ledger entries failed")
	}
	return added, nil
}
