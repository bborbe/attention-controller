// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"encoding/json"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

// The carried fields the reader switch added.
//
// ⚠️ **These are not decoration, and the spec is a regression guard rather than
// a round-trip formality.** Every field below is read by a supervisor reader
// that the switch moves onto this endpoint, so a field silently dropped in
// storage would not fail anything here — it would make the reader answer
// `null`/absent for a fact the file ledger used to carry. The one that is
// outright functional is `parked_permission`: the resume path reads it to carry
// an operator's decision to a worker's next turn, and an absent park makes
// every decision-carrying resume refused.
var _ = Describe("SessionLedgerEntry carried fields", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.SessionLedgerStore

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		// boltkv, matching session-ledger-store_test.go: this is the backend the
		// deployed service opens, and a map-backed double would not prove the
		// row survives the same encoding the real store applies.
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())
		store = pkg.NewSessionLedgerStore(db)
	})

	It("round-trips every field the readers consume", func() {
		spawned := mustParseDateTime("2026-10-13T20:00:00Z")
		exited := libtime.DateTime(mustParseDateTime("2026-10-13T21:00:00Z"))
		entry := pkg.SessionLedgerEntry{
			SessionID:          "11111111-1111-4111-8111-111111111111",
			AgentID:            "agent_1",
			Label:              "a worker",
			Mode:               "headless",
			ModeSource:         "argument",
			Launcher:           "/usr/local/bin/cc-agent",
			Cwd:                "/Users/operator/Documents/workspaces/claude-supervisor",
			PaneID:             "42",
			ResumedFrom:        "99999999-9999-4999-8999-999999999999",
			Policy:             "/tmp/policy.json",
			SpawnerSessionID:   "44444444-4444-4444-8444-444444444444",
			Role:               "agent",
			Location:           pkg.LocalSessionHeartbeatLocation,
			SpawnedAt:          libtime.DateTime(spawned),
			SupervisorExitedAt: &exited,
			UnknownReason:      "headless worker terminated with its supervisor mid-turn",
			Status:             "unknown",
			// An OBJECT, matching every non-null `result` in the live ledger (245 of 1898,
			// measured 2026-10-14) — a string here is the shape that silently dropped them.
			Result:   json.RawMessage(`{"subtype":"success","num_turns":3}`),
			WorkerID: "worker-7",
			Target:   "docker",
		}
		Expect(store.Post(ctx, entry)).To(BeNil())

		got, found, err := store.Get(ctx, entry.SessionID)
		Expect(err).To(BeNil())
		Expect(found).To(BeTrue())
		Expect(got).To(Equal(entry))
	})

	It("keeps supervisor_exited_at absent when the supervisor never exited", func() {
		// ⚠️ The pointer is the whole point: "the supervisor is still running" and
		// "it exited at the zero instant" must stay distinguishable, so an absent
		// field must come back absent rather than as a zero time that sorts to the
		// epoch and makes every age-based read wrong in the same direction.
		entry := ledgerEntry(ledgerSessionB, "2026-10-13T20:00:00Z")
		Expect(store.Post(ctx, entry)).To(BeNil())

		got, found, err := store.Get(ctx, ledgerSessionB)
		Expect(err).To(BeNil())
		Expect(found).To(BeTrue())
		Expect(got.SupervisorExitedAt).To(BeNil())
	})

	It("carries the park record opaquely and byte-identically", func() {
		// ⚠️ The store must not interpret this value. It is written by the
		// supervisor's own redaction pass, and a store that reshaped it would be a
		// second implementation of that rule — the drift a redaction cannot
		// survive. So the assertion is on the exact bytes, not on a decoded shape.
		park := json.RawMessage(
			`{"request_id":"req-1","tool":"Bash","input":{"command":"ls"},"reason":"needs approval","blocked_path":null,"requested_at":"2026-10-13T20:30:00Z"}`,
		)
		entry := ledgerEntry(ledgerSessionC, "2026-10-13T20:00:00Z")
		entry.ParkedPermission = park
		Expect(store.Post(ctx, entry)).To(BeNil())

		got, found, err := store.Get(ctx, ledgerSessionC)
		Expect(err).To(BeNil())
		Expect(found).To(BeTrue())
		Expect(got.ParkedPermission).ToNot(BeNil())

		var want, have map[string]any
		Expect(json.Unmarshal(park, &want)).To(BeNil())
		Expect(json.Unmarshal(got.ParkedPermission, &have)).To(BeNil())
		Expect(have).To(Equal(want))
		// The nested tool input survives too — it is what a resume replays, so
		// flattening it would trade a leak for a resume that no longer works.
		Expect(have["input"]).To(Equal(map[string]any{"command": "ls"}))
	})

	It("keeps parked_permission absent for a worker holding no gate", func() {
		entry := ledgerEntry(ledgerSessionA, "2026-10-13T20:00:00Z")
		Expect(store.Post(ctx, entry)).To(BeNil())

		got, found, err := store.Get(ctx, ledgerSessionA)
		Expect(err).To(BeNil())
		Expect(found).To(BeTrue())
		Expect(got.ParkedPermission).To(BeNil())
	})
})
