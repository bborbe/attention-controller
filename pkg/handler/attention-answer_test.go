// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
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

var _ = Describe("AttentionAnswerHandler", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var httpHandler http.Handler

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		// A real boltkv DB and a real store, not a fake of either. The handler's
		// own job is to decode a body and hand it on, so the assertion that
		// matters is what the store ended up recording — a faked store would
		// assert the call shape instead of the persisted outcome, and would not
		// notice a field decoded into the wrong slot.
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
		httpHandler = handler.NewAttentionAnswerHandler(store)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	// pushParkedGate pushes a `permission`-class item, which is the class the
	// decision field exists for.
	pushParkedGate := func() *pkg.Item {
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      "session-a",
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:session-a"),
			DedupKey:        "gate-1",
			InterruptClass:  "approve",
			Payload:         "deploy prod?",
			AnswerMechanism: pkg.PermissionAnswerMechanism,
		})
		Expect(err).To(BeNil())
		return item
	}

	// answer posts a body to the item's answer route. The path var is set with
	// SetURLVars rather than by mounting a router: the handler reads it through
	// mux.Vars, and a router would add a second thing to get wrong without
	// testing anything the handler owns.
	answer := func(itemID pkg.ItemID, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(
			http.MethodPost,
			"/api/1.0/attention/"+itemID.String()+"/answer",
			strings.NewReader(body),
		)
		req = mux.SetURLVars(req, map[string]string{"itemID": itemID.String()})
		rec := httptest.NewRecorder()
		httpHandler.ServeHTTP(rec, req)
		return rec
	}

	It("decodes the decision and the store records it", func() {
		item := pushParkedGate()

		rec := answer(
			item.ItemID,
			`{"answered_by":"telegram","resolved_by":"manager-1","decision":"allow"}`,
		)
		Expect(rec.Code).To(Equal(http.StatusOK))

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.Decision).To(Equal(pkg.AllowDecision))
		Expect(got.AnsweredBy).To(Equal("telegram"))
		Expect(got.ResolvedBy).To(Equal("manager-1"))
		Expect(got.State).To(Equal(pkg.AnsweredState))
	})

	// Negative control for the spec above: if the handler ignored the body and
	// the store defaulted to allow, the allow case would still pass. A deny must
	// read back as a deny.
	It("records a deny as a deny", func() {
		item := pushParkedGate()

		rec := answer(item.ItemID, `{"answered_by":"telegram","decision":"deny"}`)
		Expect(rec.Code).To(Equal(http.StatusOK))

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.Decision).To(Equal(pkg.DenyDecision))
	})

	// The schema adds no write-time rejection for an omitted decision, so a body
	// without one must still answer the item — it records no verdict rather than
	// failing the request.
	It("answers an item whose body omits the decision, recording no verdict", func() {
		item := pushParkedGate()

		rec := answer(item.ItemID, `{"answered_by":"telegram"}`)
		Expect(rec.Code).To(Equal(http.StatusOK))

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.State).To(Equal(pkg.AnsweredState))
		Expect(got.Decision).To(BeEmpty())
	})

	// A decision outside the enum is the one value here the store could not
	// notice, so it is rejected before the store is touched and the item stays
	// open — the same shape validatePushRequest gives a bad declaration.
	It("rejects a decision outside AvailableDecisions and leaves the item open", func() {
		item := pushParkedGate()

		rec := answer(item.ItemID, `{"answered_by":"telegram","decision":"maybe"}`)
		Expect(rec.Code).To(Equal(http.StatusBadRequest))

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.State).To(Equal(pkg.OpenState))
		Expect(got.Decision).To(BeEmpty())
	})
})
