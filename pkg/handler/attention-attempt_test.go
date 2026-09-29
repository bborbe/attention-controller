// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

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

// The attempt routes are the delivery trail's surface: a GET that answers "did
// this item's answer reach the session?" and a POST the attempting arm calls.
//
// These specs exist because the handler is the ONLY consumer of
// pkg.ErrItemNotFound — that sentinel is what turns the store's refusal into a
// 404, and nothing else in the codebase maps it. Drop the mapping and every
// caller sees a 400 for an item that is simply absent, with no test failing.
var _ = Describe("AttentionAttemptHandler", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var getHandler http.Handler
	var recordHandler http.Handler

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		// A real boltkv DB and a real store, not a fake of either: the handler's
		// job is to decode a body and hand it on, so the assertion that matters is
		// what the store recorded. A faked store would assert the call shape
		// instead of the persisted outcome.
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

		sessionLivenessChecker := &mocks.SessionLivenessChecker{}
		sessionLivenessChecker.IsLiveReturns(true)

		store = pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			sessionLivenessChecker,
			libtime.NewCurrentDateTime(),
			libtime.Duration(15*60*1e9),
		)
		getHandler = handler.NewAttentionAttemptGetHandler(store)
		recordHandler = handler.NewAttentionAttemptRecordHandler(store)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	push := func() *pkg.Item {
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      "session-a",
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:session-a"),
			DedupKey:        "gate-1",
			InterruptClass:  "approve",
			Payload:         "deploy prod?",
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		Expect(err).To(BeNil())
		return item
	}

	// call drives one handler with the itemID path var set, as the sibling specs
	// do: the handler reads it through mux.Vars, and a mounted router would add a
	// second thing to get wrong without testing anything the handler owns.
	call := func(
		h http.Handler,
		method string,
		itemID pkg.ItemID,
		body string,
	) *httptest.ResponseRecorder {
		req := httptest.NewRequest(
			method,
			"/api/1.0/attention/"+itemID.String()+"/attempt",
			strings.NewReader(body),
		)
		req = mux.SetURLVars(req, map[string]string{"itemID": itemID.String()})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	It("returns the derived status for a known item", func() {
		item := push()

		rec := call(getHandler, http.MethodGet, item.ItemID, "")
		Expect(rec.Code).To(Equal(http.StatusOK))

		var report pkg.DeliveryReport
		Expect(json.Unmarshal(rec.Body.Bytes(), &report)).To(BeNil())
		Expect(report.ItemID).To(Equal(item.ItemID))
		Expect(report.Status).To(Equal(pkg.NeverAttemptedStatus))
	})

	// The store refuses an attempt against an unknown item; the handler is the
	// only place that refusal becomes a 404.
	It("returns 404 when the item is unknown", func() {
		rec := call(getHandler, http.MethodGet, pkg.ItemID("no-such-item"), "")
		Expect(rec.Code).To(Equal(http.StatusNotFound))
	})

	It("returns 404 when recording an attempt against an unknown item", func() {
		rec := call(
			recordHandler,
			http.MethodPost,
			pkg.ItemID("no-such-item"),
			`{"carrier":"arm","outcome":"delivered"}`,
		)
		Expect(rec.Code).To(Equal(http.StatusNotFound))
	})

	It("records an attempt, returns it, and it is readable back through the query", func() {
		item := push()

		rec := call(
			recordHandler,
			http.MethodPost,
			item.ItemID,
			`{"carrier":"supervisor:attention-next","outcome":"delivered"}`,
		)
		Expect(rec.Code).To(Equal(http.StatusOK))

		var attempt pkg.DeliveryAttempt
		Expect(json.Unmarshal(rec.Body.Bytes(), &attempt)).To(BeNil())
		Expect(attempt.Carrier).To(Equal("supervisor:attention-next"))
		Expect(attempt.Outcome).To(Equal(pkg.DeliveredOutcome))

		// The whole point of the record: one query, and the answer's fate is
		// readable without inference from which files exist.
		readBack := call(getHandler, http.MethodGet, item.ItemID, "")
		Expect(readBack.Code).To(Equal(http.StatusOK))
		var report pkg.DeliveryReport
		Expect(json.Unmarshal(readBack.Body.Bytes(), &report)).To(BeNil())
		Expect(report.Status).To(Equal(pkg.DeliveredStatus))
		Expect(report.Carrier).To(Equal("supervisor:attention-next"))
	})

	It("returns 400 when the carrier is empty", func() {
		item := push()

		rec := call(recordHandler, http.MethodPost, item.ItemID, `{"outcome":"delivered"}`)
		Expect(rec.Code).To(Equal(http.StatusBadRequest))
	})

	It("returns 400 for an outcome that is neither delivered nor failed", func() {
		item := push()

		rec := call(
			recordHandler,
			http.MethodPost,
			item.ItemID,
			`{"carrier":"arm","outcome":"maybe"}`,
		)
		Expect(rec.Code).To(Equal(http.StatusBadRequest))
	})
})
