// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

// ⚠️ **This file exists because its absence let a severe defect reach review.** The
// store's own specs covered the round-trip of the ROW TYPE, so every one of them passed
// while the importer's MAPPING was wrong: `result` was declared a `string` while every
// non-null value in the live ledger is a JSON OBJECT (245 of 1898, measured 2026-10-14),
// so `json.Unmarshal` failed on exactly those rows and the import would have silently
// dropped 245 of them. A type covered incidentally by another package's round-trip spec
// is not a mapping that is covered.
var _ = Describe("toEntry", func() {
	ctx := context.Background()

	It("carries a result that is a JSON object", func() {
		var record fileRecord
		Expect(json.Unmarshal([]byte(`{
			"session_id": "abc-123",
			"spawned_at": "2026-10-14T06:00:00Z",
			"result": {"subtype":"success","num_turns":7,"permission_denials":[]}
		}`), &record)).To(Succeed())

		entry, err := toEntry(ctx, record)
		Expect(err).To(BeNil())

		var result map[string]any
		Expect(json.Unmarshal(entry.Result, &result)).To(Succeed())
		Expect(result).To(HaveKeyWithValue("subtype", "success"))
		Expect(result).To(HaveKeyWithValue("num_turns", BeNumerically("==", 7)))
	})

	It("maps the file shape onto the wire shape", func() {
		var record fileRecord
		Expect(json.Unmarshal([]byte(`{
			"session_id": "abc-123",
			"agent_id": "agent_1",
			"label": "worker",
			"mode": "headless",
			"mode_source": "config",
			"cwd": "/tmp",
			"worker_id": "w-9",
			"target": "docker",
			"unknown_reason": "supervisor exited",
			"parent_session": "parent-456",
			"spawned_at": "2026-10-14T06:00:00Z",
			"status": "running"
		}`), &record)).To(Succeed())

		entry, err := toEntry(ctx, record)
		Expect(err).To(BeNil())
		Expect(entry.SessionID).To(Equal("abc-123"))
		Expect(entry.AgentID).To(Equal("agent_1"))
		Expect(entry.Label).To(Equal("worker"))
		Expect(entry.Mode).To(Equal("headless"))
		Expect(entry.ModeSource).To(Equal("config"))
		Expect(entry.Cwd).To(Equal("/tmp"))
		Expect(entry.WorkerID).To(Equal("w-9"))
		Expect(entry.Target).To(Equal("docker"))
		Expect(entry.UnknownReason).To(Equal("supervisor exited"))
		Expect(entry.Status).To(Equal("running"))

		// ⚠️ The one RENAME: the file calls it `parent_session`, the wire calls it
		// `spawner_session_id`. One name for one thing.
		Expect(entry.SpawnerSessionID).To(Equal("parent-456"))

		// ⚠️ INJECTED, and the import depends on it: the file ledger never carried
		// `location` (present in 0 of the 1895 live rows) while `Validate` requires it,
		// and `Import` validates every entry inside one transaction — so a missing
		// location would not skip a row, it would abort the whole migration.
		Expect(entry.Location).To(Equal(pkg.LocalSessionHeartbeatLocation))
	})

	It("drops a parked_permission that is JSON null", func() {
		var record fileRecord
		Expect(json.Unmarshal([]byte(`{
			"session_id": "abc-123",
			"spawned_at": "2026-10-14T06:00:00Z",
			"parked_permission": null
		}`), &record)).To(Succeed())

		entry, err := toEntry(ctx, record)
		Expect(err).To(BeNil())
		// ⚠️ `json.RawMessage` cannot tell "absent" from "present and null" — both
		// arrive as a non-nil slice for the latter — so the guard is what keeps the
		// imported row reading the way a written one does. The field is documented as
		// ABSENT when no gate is held, and the store's own spec asserts that absence.
		Expect(entry.ParkedPermission).To(BeEmpty())

		raw, err := json.Marshal(entry)
		Expect(err).To(BeNil())
		Expect(string(raw)).NotTo(ContainSubstring("parked_permission"))
	})

	It("carries a parked_permission byte-identically", func() {
		var record fileRecord
		Expect(json.Unmarshal([]byte(`{
			"session_id": "abc-123",
			"spawned_at": "2026-10-14T06:00:00Z",
			"parked_permission": {"tool":"Bash","input":{"command":"ls"}}
		}`), &record)).To(Succeed())

		entry, err := toEntry(ctx, record)
		Expect(err).To(BeNil())

		var parked map[string]any
		Expect(json.Unmarshal(entry.ParkedPermission, &parked)).To(Succeed())
		Expect(parked).To(HaveKeyWithValue("tool", "Bash"))
	})

	It("leaves the optional instants absent when the file has none", func() {
		var record fileRecord
		Expect(json.Unmarshal([]byte(`{
			"session_id": "abc-123",
			"spawned_at": "2026-10-14T06:00:00Z"
		}`), &record)).To(Succeed())

		entry, err := toEntry(ctx, record)
		Expect(err).To(BeNil())
		// ⚠️ Pointers, so "still running" and "ended at the zero instant" stay
		// distinguishable — the same reason the store's Get returns a `found` bool.
		Expect(entry.EndedAt).To(BeNil())
		Expect(entry.SupervisorExitedAt).To(BeNil())
	})

	It("parses the optional instants when the file has them", func() {
		var record fileRecord
		Expect(json.Unmarshal([]byte(`{
			"session_id": "abc-123",
			"spawned_at": "2026-10-14T06:00:00Z",
			"ended_at": "2026-10-14T07:00:00Z",
			"supervisor_exited_at": "2026-10-14T08:00:00Z"
		}`), &record)).To(Succeed())

		entry, err := toEntry(ctx, record)
		Expect(err).To(BeNil())
		Expect(entry.EndedAt).NotTo(BeNil())
		Expect(
			entry.EndedAt.Time().UTC().Format("2006-01-02T15:04:05Z"),
		).To(Equal("2026-10-14T07:00:00Z"))
		Expect(entry.SupervisorExitedAt).NotTo(BeNil())
		Expect(
			entry.SupervisorExitedAt.Time().UTC().Format("2006-01-02T15:04:05Z"),
		).To(Equal("2026-10-14T08:00:00Z"))
	})

	It("refuses a row whose spawned_at does not parse, naming the session", func() {
		var record fileRecord
		Expect(json.Unmarshal([]byte(`{
			"session_id": "abc-123",
			"spawned_at": "not-a-timestamp"
		}`), &record)).To(Succeed())

		_, err := toEntry(ctx, record)
		Expect(err).NotTo(BeNil())
		Expect(err.Error()).To(ContainSubstring("abc-123"))
	})
})

var _ = Describe("readLedgerDir", func() {
	ctx := context.Background()

	// ⚠️ **The two skip reasons are asserted SEPARATELY, and that is the point.** They
	// mean opposite things: an unreadable row is a defect worth chasing, a row with no
	// `session_id` is a legitimate skip because `session_id` is the identity and its only
	// join key. Folding them into one count reports every unreadable row under a reason
	// that is false for it.
	It("separates unreadable rows from rows with no session_id", func() {
		dir := GinkgoT().TempDir()
		Expect(os.WriteFile(
			filepath.Join(dir, "aaa-good.json"),
			[]byte(
				`{"session_id":"abc-123","spawned_at":"2026-10-14T06:00:00Z","result":{"subtype":"success"}}`,
			),
			0o600,
		)).To(Succeed())
		Expect(os.WriteFile(
			filepath.Join(dir, "bbb-broken.json"),
			[]byte(`{"session_id":`),
			0o600,
		)).To(Succeed())
		Expect(os.WriteFile(
			filepath.Join(dir, "ccc-noid.json"),
			[]byte(`{"spawned_at":"2026-10-14T06:00:00Z"}`),
			0o600,
		)).To(Succeed())
		Expect(os.WriteFile(
			filepath.Join(dir, "ddd-notjson.txt"),
			[]byte(`ignored`),
			0o600,
		)).To(Succeed())

		entries, unparsable, noID, err := readLedgerDir(ctx, dir)
		Expect(err).To(BeNil())
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].SessionID).To(Equal("abc-123"))
		Expect(unparsable).To(Equal(1))
		Expect(noID).To(Equal(1))
	})
})

var _ = Describe("isJSONNull", func() {
	DescribeTable("reports absent and JSON null alike",
		func(raw string, expected bool) {
			Expect(isJSONNull(json.RawMessage(raw))).To(Equal(expected))
		},
		Entry("absent", "", true),
		Entry("null", "null", true),
		Entry("an object", `{"a":1}`, false),
		Entry("an empty object", `{}`, false),
	)
})
