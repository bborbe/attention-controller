// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"encoding/json"
	stdtime "time"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

// Ledger session ids, shaped like the ids the live ledger carries. The
// collision pair below is the fixture the whole suite exists for: they share an
// `agent_id` and must NOT collapse into one row.
const (
	ledgerSessionA = "11111111-1111-4111-8111-111111111111"
	ledgerSessionB = "22222222-2222-4222-8222-222222222222"
	ledgerSessionC = "33333333-3333-4333-8333-333333333333"
	// ledgerSharedAgentID is what `supervisor.mjs` mints on a reconnect: a
	// per-process counter restarting at 0, so two unrelated workers receive it.
	ledgerSharedAgentID = "agent_1"
	// ledgerWrapperSessionID is a `claude-wrapper` session id, carried so a
	// restarted supervisor can find the park it holds.
	ledgerWrapperSessionID = "55555555-5555-4555-8555-555555555555"
)

// mustParseDateTime turns an RFC3339 fixture into the store's clock type. A
// malformed literal is a bug in this file, so it fails the spec rather than
// being skipped — the same discipline the id constants above carry.
func mustParseDateTime(value string) libtime.DateTime {
	parsed, err := stdtime.Parse(stdtime.RFC3339, value)
	Expect(err).To(BeNil())
	return libtime.DateTime(parsed)
}

func ledgerEntry(sessionID string, spawnedAt string) pkg.SessionLedgerEntry {
	return pkg.SessionLedgerEntry{
		SessionID:        sessionID,
		AgentID:          ledgerSharedAgentID,
		Label:            "a worker",
		Mode:             "interactive",
		Location:         pkg.LocalSessionHeartbeatLocation,
		SpawnedAt:        libtime.DateTime(mustParseDateTime(spawnedAt)),
		SpawnerSessionID: "44444444-4444-4444-8444-444444444444",
	}
}

var _ = Describe("SessionLedgerStore", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.SessionLedgerStore

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		// boltkv, not memorykv — the same reason attention-store_test.go gives:
		// this is the backend the deployed service opens, and `Import`'s
		// read-then-write inside one transaction is only atomic because Bolt
		// serializes write transactions. A map-backed double would assert a
		// guarantee it cannot provide.
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())
		store = pkg.NewSessionLedgerStore(db)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	It("reads back a row it has written", func() {
		entry := ledgerEntry(ledgerSessionA, "2026-10-13T10:00:00Z")
		Expect(store.Post(ctx, entry)).To(BeNil())

		got, found, err := store.Get(ctx, ledgerSessionA)
		Expect(err).To(BeNil())
		Expect(found).To(BeTrue())
		Expect(got.SessionID).To(Equal(ledgerSessionA))
		Expect(got.Label).To(Equal("a worker"))
		Expect(got.SpawnerSessionID).To(Equal("44444444-4444-4444-8444-444444444444"))
	})

	It("carries the wrapper session id through a write and a read", func() {
		// The supervisor writes this field and a restarted supervisor reads it
		// back to find a parked prompt. Dropping it is silent — the row simply
		// reads as "not wrapper-backed" — so the round trip is the only check
		// that catches the struct losing it.
		entry := ledgerEntry(ledgerSessionA, "2026-10-13T10:00:00Z")
		entry.WrapperSessionID = ledgerWrapperSessionID
		Expect(store.Post(ctx, entry)).To(BeNil())

		got, found, err := store.Get(ctx, ledgerSessionA)
		Expect(err).To(BeNil())
		Expect(found).To(BeTrue())
		Expect(got.WrapperSessionID).To(Equal(ledgerWrapperSessionID))
	})

	It("decodes a row written before wrapper_session_id existed as empty", func() {
		// Every row already in the store predates the field. A required field
		// would make all of them unreadable, so "absent" must decode to the
		// empty string rather than failing the decode.
		var entry pkg.SessionLedgerEntry
		payload := `{"session_id":"` + ledgerSessionA + `","spawned_at":"2026-10-13T10:00:00Z"}`
		Expect(json.Unmarshal([]byte(payload), &entry)).To(BeNil())
		Expect(entry.SessionID).To(Equal(ledgerSessionA))
		Expect(entry.WrapperSessionID).To(Equal(""))

		// ⚠️ And it must STAY absent on the way back out. Writing `""` would add
		// the key to every non-wrapper row — all 3470 of them — so a reader
		// testing `has("wrapper_session_id")` could no longer tell a carried id
		// from a worker that simply has none.
		encoded, err := json.Marshal(entry)
		Expect(err).To(BeNil())
		roundTripped := map[string]any{}
		Expect(json.Unmarshal(encoded, &roundTripped)).To(BeNil())
		Expect(roundTripped).NotTo(HaveKey("wrapper_session_id"))
	})

	It("reports an unknown session as absent rather than as an error", func() {
		// ⚠️ The bool is load-bearing: `absent` and `ended` are different
		// answers, and folding this into the error branch would make a missing
		// row indistinguishable from an unreadable store.
		_, found, err := store.Get(ctx, ledgerSessionA)
		Expect(err).To(BeNil())
		Expect(found).To(BeFalse())
	})

	It("replaces a row rather than appending a second one for the same session", func() {
		Expect(store.Post(ctx, ledgerEntry(ledgerSessionA, "2026-10-13T10:00:00Z"))).To(BeNil())
		updated := ledgerEntry(ledgerSessionA, "2026-10-13T10:00:00Z")
		updated.Label = "renamed"
		Expect(store.Post(ctx, updated)).To(BeNil())

		entries, err := store.List(ctx)
		Expect(err).To(BeNil())
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].Label).To(Equal("renamed"))
	})

	// ⚠️ **The regression this whole task exists for.** The roster this store
	// replaces keyed its rows by `agent_id`, which is a per-process counter that
	// restarts at 0 on every supervisor reconnect — so the live ledger holds
	// 1891 rows across only 187 distinct `agent_id` values, and a roster keyed
	// on it collapses them. Keyed by `session_id`, both survive.
	It("keeps two rows that share an agent_id when their session ids differ", func() {
		Expect(store.Post(ctx, ledgerEntry(ledgerSessionA, "2026-10-13T10:00:00Z"))).To(BeNil())
		Expect(store.Post(ctx, ledgerEntry(ledgerSessionB, "2026-10-13T11:00:00Z"))).To(BeNil())

		entries, err := store.List(ctx)
		Expect(err).To(BeNil())
		Expect(entries).To(HaveLen(2), "an agent_id collision must not drop a row")

		ids := make([]string, 0, len(entries))
		for _, entry := range entries {
			Expect(entry.AgentID).To(Equal(ledgerSharedAgentID))
			ids = append(ids, entry.SessionID)
		}
		Expect(ids).To(ConsistOf(ledgerSessionA, ledgerSessionB))
	})

	It("imports rows that are not already present", func() {
		added, err := store.Import(ctx, pkg.SessionLedgerEntries{
			ledgerEntry(ledgerSessionA, "2026-10-13T10:00:00Z"),
			ledgerEntry(ledgerSessionB, "2026-10-13T11:00:00Z"),
		})
		Expect(err).To(BeNil())
		Expect(added).To(Equal(2))

		entries, err := store.List(ctx)
		Expect(err).To(BeNil())
		Expect(entries).To(HaveLen(2))
	})

	// ⚠️ SC5's second clause: a second import changes the row count by 0. The
	// migration runs against a directory the supervisor is still writing to, so
	// a retry after a partial failure must not duplicate the ledger.
	It("adds nothing on a second import of the same rows", func() {
		entries := pkg.SessionLedgerEntries{
			ledgerEntry(ledgerSessionA, "2026-10-13T10:00:00Z"),
			ledgerEntry(ledgerSessionB, "2026-10-13T11:00:00Z"),
		}
		first, err := store.Import(ctx, entries)
		Expect(err).To(BeNil())
		Expect(first).To(Equal(2))

		second, err := store.Import(ctx, entries)
		Expect(err).To(BeNil())
		Expect(second).To(Equal(0), "a re-import must add zero rows")

		got, err := store.List(ctx)
		Expect(err).To(BeNil())
		Expect(got).To(HaveLen(2))
	})

	// ⚠️ The import must not be able to undo a live write. Once the endpoint is
	// the source of truth, a stale file re-imported must leave the newer row
	// alone — an import that overwrote would let a leftover file roll the
	// ledger backwards.
	It("does not overwrite a row that already exists", func() {
		current := ledgerEntry(ledgerSessionA, "2026-10-13T10:00:00Z")
		current.Label = "the live row"
		Expect(store.Post(ctx, current)).To(BeNil())

		stale := ledgerEntry(ledgerSessionA, "2026-10-13T10:00:00Z")
		stale.Label = "the stale file"
		added, err := store.Import(ctx, pkg.SessionLedgerEntries{stale})
		Expect(err).To(BeNil())
		Expect(added).To(Equal(0))

		got, found, err := store.Get(ctx, ledgerSessionA)
		Expect(err).To(BeNil())
		Expect(found).To(BeTrue())
		Expect(got.Label).To(Equal("the live row"))
	})

	It("refuses to import a row that is missing a required field", func() {
		broken := ledgerEntry(ledgerSessionC, "2026-10-13T10:00:00Z")
		broken.SpawnedAt = libtime.DateTime{}
		_, err := store.Import(ctx, pkg.SessionLedgerEntries{broken})
		Expect(err).ToNot(BeNil())
	})
})
