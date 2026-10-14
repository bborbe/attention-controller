// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/handler"
)

// handlerLedgerSecondSessionID is a second well-formed id, so a list spec can
// assert that EVERY row comes back rather than that the first one does.
const handlerLedgerSecondSessionID = "66666666-6666-4666-8666-666666666666"

// ⚠️ This suite exists because the list handler had no spec at all, and it is
// the endpoint that replaces the frozen roster — the defect this whole task
// exists to close. Two of its properties are load-bearing and neither was
// asserted anywhere: it must answer an ARRAY for an empty store (the reader's
// three-state contract distinguishes `[]` from an unreachable store), and it
// must return EVERY row with no liveness or staleness filter, because a ledger
// row is permanent by contract.
var _ = Describe("SessionLedgerListHandler", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.SessionLedgerStore

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())
		store = pkg.NewSessionLedgerStore(db)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	list := func(target http.Handler) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/1.0/session-ledger", nil)
		rec := httptest.NewRecorder()
		target.ServeHTTP(rec, req)
		return rec
	}

	It("answers the stored rows with 200", func() {
		Expect(store.Post(ctx, storedLedgerRow(handlerLedgerSessionID))).To(BeNil())
		Expect(store.Post(ctx, storedLedgerRow(handlerLedgerSecondSessionID))).To(BeNil())

		rec := list(handler.NewSessionLedgerListHandler(store))
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.String()).To(ContainSubstring(handlerLedgerSessionID))
		Expect(rec.Body.String()).To(ContainSubstring(handlerLedgerSecondSessionID))
	})

	// ⚠️ An empty store answers `[]`, and `null` would be a DEFECT rather than a
	// cosmetic difference: the reader that consumes this endpoint distinguishes
	// three states — a `200 []` body means "nobody has ever been spawned", while
	// an unreachable or non-array answer means UNKNOWN, and the two must never
	// collapse because UNKNOWN REFUSES a spawn where an empty ledger permits
	// one. A `null` body is neither an array nor an error, so it would land in
	// the caller's unknown branch and make a working service read as a broken
	// one — or, read the other way, make a broken fleet read as an empty one.
	It("answers an empty ARRAY — never null — for an empty store", func() {
		rec := list(handler.NewSessionLedgerListHandler(store))
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.String()).To(MatchJSON("[]"))
	})

	// ⚠️ No liveness filter and no staleness window, unlike the heartbeat list
	// beside it. 1480 of the legacy rows still say `running` for workers long
	// dead, and a caller that needs liveness joins the heartbeat store on
	// `session_id` — so a handler that filtered on `status` here would silently
	// drop most of the fleet while looking like it worked.
	It("returns a row that still claims running and has no ended_at", func() {
		entry := storedLedgerRow(handlerLedgerSessionID)
		entry.Status = "running"
		Expect(store.Post(ctx, entry)).To(BeNil())

		rec := list(handler.NewSessionLedgerListHandler(store))
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.String()).To(ContainSubstring(handlerLedgerSessionID))
		Expect(rec.Body.String()).To(ContainSubstring("running"))
	})

	// ⚠️ An unreadable store is a FAILURE, never an empty fleet — the same rule
	// the get handler states, and the one the readers depend on to tell
	// "nobody was spawned" from "I could not find out".
	It("answers 500 — never an empty array — when the store cannot be read", func() {
		failing := &mocks.SessionLedgerStore{}
		failing.ListReturns(nil, errors.New("store unavailable"))

		rec := list(handler.NewSessionLedgerListHandler(failing))
		Expect(rec.Code).To(Equal(http.StatusInternalServerError))
		Expect(rec.Body.String()).NotTo(MatchJSON("[]"))
	})
})
