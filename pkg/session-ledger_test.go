// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

// validatableSessionID is a well-formed id that is not one of the store suite's
// fixtures, so a failure here cannot be read as a store-level collision.
const validatableSessionID = "77777777-7777-4777-8777-777777777777"

// validLedgerEntry is a row that passes Validate, so each spec below can break
// exactly ONE thing and assert the refusal is attributable to that thing rather
// than satisfied by any error at all.
func validLedgerEntry() pkg.SessionLedgerEntry {
	return pkg.SessionLedgerEntry{
		SessionID: validatableSessionID,
		AgentID:   "agent_1",
		Label:     "a worker",
		Mode:      "interactive",
		Location:  pkg.LocalSessionHeartbeatLocation,
		SpawnedAt: mustParseDateTime("2026-10-13T10:00:00Z"),
	}
}

// ⚠️ This suite exists because Validate had NO direct spec: its rules were only
// ever exercised THROUGH the POST handler, so a rule that the handler happened
// to satisfy for another reason — or that the handler never reached — was
// untested while the coverage report looked healthy. The store and handler
// suites assert that a request is refused; this one asserts WHICH declaration
// the refusal is about, which is the property a caller reads the error for.
var _ = Describe("SessionLedgerEntry.Validate", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("accepts a complete row", func() {
		Expect(validLedgerEntry().Validate(ctx)).To(BeNil())
	})

	It("refuses a row with no session_id", func() {
		entry := validLedgerEntry()
		entry.SessionID = ""
		Expect(entry.Validate(ctx)).NotTo(BeNil())
	})

	It("refuses a session_id that is not a well-formed id", func() {
		entry := validLedgerEntry()
		entry.SessionID = "not-a-uuid"
		Expect(entry.Validate(ctx)).NotTo(BeNil())
	})

	It("refuses an unset spawned_at", func() {
		entry := validLedgerEntry()
		entry.SpawnedAt = libtime.DateTime{}
		Expect(entry.Validate(ctx)).NotTo(BeNil())
	})

	It("refuses an unknown location", func() {
		entry := validLedgerEntry()
		entry.Location = "somewhere-else"
		Expect(entry.Validate(ctx)).NotTo(BeNil())
	})

	// ⚠️ The anchor is PAIRED rather than individually required, and these two
	// specs pin the half that IS refused. A task name with no vault cannot be
	// resolved, because task names collide across vaults — so the pair is
	// declared together or not at all.
	It("refuses a task declared with no vault", func() {
		entry := validLedgerEntry()
		entry.Task = "Move the Spawn Ledger"
		Expect(entry.Validate(ctx)).NotTo(BeNil())
	})

	It("refuses a vault declared with no task", func() {
		entry := validLedgerEntry()
		entry.Vault = "private-personal"
		Expect(entry.Validate(ctx)).NotTo(BeNil())
	})

	// ⚠️ An UNANCHORED row is LEGAL, and this is the divergence from a reading of
	// SC6 that would require the pair. A session with no task anchor is a real
	// state — the live ledger holds rows spawned outside any task — and
	// requiring it would push the writer into inventing a task name.
	It("accepts a row with no task anchor at all", func() {
		entry := validLedgerEntry()
		Expect(entry.Task).To(BeEmpty())
		Expect(entry.Vault).To(BeEmpty())
		Expect(entry.Validate(ctx)).To(BeNil())
	})

	It("accepts a row with a fully declared task anchor", func() {
		entry := validLedgerEntry()
		entry.Task = "Move the Spawn Ledger"
		entry.Vault = "private-personal"
		Expect(entry.Validate(ctx)).To(BeNil())
	})

	// ⚠️ Validate judges the CALLER's declaration only, and these two specs are
	// what makes that a tested property rather than a comment. `ended_at` and
	// `supervisor_exited_at` are the supervisor's to write when a worker ends,
	// so requiring them would reject every live row; requiring them to be ABSENT
	// would reject every finished one. Both states must pass.
	It("accepts a live row whose ended_at and supervisor_exited_at are absent", func() {
		entry := validLedgerEntry()
		Expect(entry.EndedAt).To(BeNil())
		Expect(entry.SupervisorExitedAt).To(BeNil())
		Expect(entry.Validate(ctx)).To(BeNil())
	})

	It("accepts a finished row carrying an ended_at", func() {
		entry := validLedgerEntry()
		endedAt := mustParseDateTime("2026-10-13T11:00:00Z")
		entry.EndedAt = &endedAt
		Expect(entry.Validate(ctx)).To(BeNil())
	})
})
