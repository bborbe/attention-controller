// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	stdtime "time"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/handler"
)

const handlerLedgerSessionID = "55555555-5555-4555-8555-555555555555"

// completeLedgerBody is a row carrying every required field, so each spec below
// can delete exactly one and assert the refusal is attributable to that field.
func completeLedgerBody() map[string]any {
	return map[string]any{
		"session_id": handlerLedgerSessionID,
		"spawned_at": stdtime.Date(2026, stdtime.October, 13, 10, 0, 0, 0, stdtime.UTC).
			Format(stdtime.RFC3339),
		"location": "local",
		"agent_id": "agent_1",
		"label":    "a worker",
	}
}

var _ = Describe("SessionLedgerPostHandler", func() {
	var ctx context.Context
	var db libkv.DB
	var target http.Handler

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())
		target = handler.NewSessionLedgerPostHandler(pkg.NewSessionLedgerStore(db))
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	post := func(body map[string]any) *httptest.ResponseRecorder {
		raw, err := json.Marshal(body)
		Expect(err).To(BeNil())
		req := httptest.NewRequest(http.MethodPost, "/api/1.0/session-ledger", bytes.NewReader(raw))
		rec := httptest.NewRecorder()
		target.ServeHTTP(rec, req)
		return rec
	}

	It("stores a complete row and answers 200", func() {
		rec := post(completeLedgerBody())
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.String()).To(ContainSubstring(handlerLedgerSessionID))
	})

	// ⚠️ SC6's contract: one spec per required field, so a refusal names the
	// field it is about rather than being satisfied by any 400 at all. Deleting
	// the key is what makes the attribution real — a body that merely sets the
	// value to "" would pass for a different reason on a field whose zero value
	// happens to be legal.
	DescribeTable("refuses a write missing a required field",
		func(field string) {
			body := completeLedgerBody()
			delete(body, field)
			rec := post(body)
			Expect(rec.Code).To(Equal(http.StatusBadRequest), "missing %s must be refused", field)
		},
		Entry("session_id", "session_id"),
		Entry("spawned_at", "spawned_at"),
		Entry("location", "location"),
	)

	It("refuses a session_id that is not a well-formed id", func() {
		body := completeLedgerBody()
		body["session_id"] = "not-a-uuid"
		Expect(post(body).Code).To(Equal(http.StatusBadRequest))
	})

	It("refuses an unknown location", func() {
		body := completeLedgerBody()
		body["location"] = "somewhere-else"
		Expect(post(body).Code).To(Equal(http.StatusBadRequest))
	})

	It("refuses a body that is not valid JSON", func() {
		req := httptest.NewRequest(
			http.MethodPost,
			"/api/1.0/session-ledger",
			strings.NewReader("{"),
		)
		rec := httptest.NewRecorder()
		target.ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusBadRequest))
	})

	// ⚠️ A row with no task anchor is LEGAL, and this spec pins that: `task` and
	// `vault` are both-or-neither, and the live ledger holds rows spawned
	// outside any task. Requiring them would make SC5's import reject the whole
	// legacy ledger — measured 2026-10-13: 0 of 1893 rows carry either field.
	It("accepts a row with no task anchor", func() {
		rec := post(completeLedgerBody())
		Expect(rec.Code).To(Equal(http.StatusOK))
	})

	It("refuses a half-declared task anchor", func() {
		body := completeLedgerBody()
		body["task"] = "some task"
		Expect(post(body).Code).To(Equal(http.StatusBadRequest))
	})

	// The store stamps nothing and the caller owns `spawned_at`, so a round-trip
	// through the handler must preserve the instant it was given rather than
	// replacing it with the read-back time.
	It("preserves the caller's spawned_at rather than stamping its own", func() {
		rec := post(completeLedgerBody())
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.String()).To(ContainSubstring("2026-10-13T10:00:00Z"))

		stored, found, err := pkg.NewSessionLedgerStore(db).Get(ctx, handlerLedgerSessionID)
		Expect(err).To(BeNil())
		Expect(found).To(BeTrue())
		Expect(stdtime.Time(stored.SpawnedAt).UTC()).
			To(Equal(stdtime.Date(2026, stdtime.October, 13, 10, 0, 0, 0, stdtime.UTC)))
	})
})
