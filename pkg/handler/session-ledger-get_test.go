// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	stdtime "time"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	"github.com/gorilla/mux"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/handler"
)

// storedLedgerRow is a row the real store accepts, so the specs below exercise
// the handler against the backend the deployed service opens rather than
// against a double that could answer a shape the store never produces.
func storedLedgerRow(sessionID string) pkg.SessionLedgerEntry {
	return pkg.SessionLedgerEntry{
		SessionID: sessionID,
		AgentID:   "agent_1",
		Label:     "a worker",
		Mode:      "interactive",
		Location:  pkg.LocalSessionHeartbeatLocation,
		SpawnedAt: mustParseLedgerTime("2026-10-13T10:00:00Z"),
	}
}

func mustParseLedgerTime(value string) libtime.DateTime {
	parsed, err := stdtime.Parse(stdtime.RFC3339, value)
	Expect(err).To(BeNil())
	return libtime.DateTime(parsed)
}

// ⚠️ This suite exists because the GET handler had no spec at all: its two
// load-bearing properties — 404 for `absent`, and 500 (NEVER 404) for an
// unreadable store — were verified by reading the code and by nothing else. The
// distinction is the one a caller acts on: 404 is the answer that authorises it
// to behave as though a session never existed, so a store outage reported as
// 404 would make a live worker look like one that was never spawned.
var _ = Describe("SessionLedgerGetHandler", func() {
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

	get := func(target http.Handler, sessionID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/1.0/session-ledger/"+sessionID, nil)
		// SetURLVars rather than mounting a router: the handler reads the id
		// through mux.Vars, and a router would add a second thing to get wrong
		// without proving anything the path var does not already prove.
		req = mux.SetURLVars(req, map[string]string{"sessionID": sessionID})
		rec := httptest.NewRecorder()
		target.ServeHTTP(rec, req)
		return rec
	}

	It("answers the stored row with 200", func() {
		Expect(store.Post(ctx, storedLedgerRow(handlerLedgerSessionID))).To(BeNil())

		rec := get(handler.NewSessionLedgerGetHandler(store), handlerLedgerSessionID)
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.String()).To(ContainSubstring(handlerLedgerSessionID))
		Expect(rec.Body.String()).To(ContainSubstring("agent_1"))
	})

	It("answers 404 when the session has no row", func() {
		rec := get(handler.NewSessionLedgerGetHandler(store), handlerLedgerSessionID)
		Expect(rec.Code).To(Equal(http.StatusNotFound))
	})

	// ⚠️ `absent` and `ended` are DIFFERENT answers, and this spec is what keeps
	// them different: a caller deciding whether to resume a worker needs to tell
	// "never spawned" from "spawned and ended", and collapsing them would make
	// the ledger unable to answer the question it exists for.
	It("answers 200 — not 404 — for a row that exists and has ended", func() {
		entry := storedLedgerRow(handlerLedgerSessionID)
		endedAt := mustParseLedgerTime("2026-10-13T11:00:00Z")
		entry.EndedAt = &endedAt
		Expect(store.Post(ctx, entry)).To(BeNil())

		rec := get(handler.NewSessionLedgerGetHandler(store), handlerLedgerSessionID)
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.String()).To(ContainSubstring("ended_at"))
	})

	// ⚠️ An unreadable store is a FAILURE, never `absent` — the rule the sibling
	// heartbeat handler states and the one `worker-sessions.py` gives as "an
	// unreadable store is UNKNOWN, never 0". A store outage answered as 404
	// would tell every reader that the whole fleet was never spawned.
	It("answers 500 — never 404 — when the store cannot be read", func() {
		failing := &mocks.SessionLedgerStore{}
		failing.GetReturns(
			pkg.SessionLedgerEntry{},
			false,
			errors.New("store unavailable"),
		)

		rec := get(handler.NewSessionLedgerGetHandler(failing), handlerLedgerSessionID)
		Expect(rec.Code).To(Equal(http.StatusInternalServerError))
		Expect(rec.Code).NotTo(Equal(http.StatusNotFound))
	})
})
