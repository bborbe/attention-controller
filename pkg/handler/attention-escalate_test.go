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

var _ = Describe("AttentionEscalateHandler", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var httpHandler http.Handler

	// § Escalation requires a session id to be a dashed UUID and the store
	// refuses anything else, so a readable fixture such as "session-manager"
	// cannot be posted here. The actor name lives in the constant name instead.
	const managerSession = "00000000-0000-4000-8000-000000000003"

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		// A real boltkv DB and a real store, not a fake of either: the handler's
		// own job is to decode a body and hand it on, so the assertion that
		// matters is what the store recorded. A faked store would assert the call
		// shape instead of the persisted outcome, and would not notice a value
		// that never reached the store at all.
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
		httpHandler = handler.NewAttentionEscalateHandler(store)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	// pushParkedGate pushes the class the escalation path is for: a manager
	// carrying someone else's item to the operator.
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

	// escalate posts a body to the item's escalate route. The path var is set
	// with SetURLVars rather than by mounting a router, as the answer handler's
	// spec does: the handler reads it through mux.Vars, and a router would add a
	// second thing to get wrong without testing anything the handler owns.
	escalate := func(itemID pkg.ItemID, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(
			http.MethodPost,
			"/api/1.0/attention/"+itemID.String()+"/escalate",
			strings.NewReader(body),
		)
		req = mux.SetURLVars(req, map[string]string{"itemID": itemID.String()})
		rec := httptest.NewRecorder()
		httpHandler.ServeHTTP(rec, req)
		return rec
	}

	It("records who escalated and when, and leaves the item open", func() {
		item := pushParkedGate()

		rec := escalate(item.ItemID, `{"escalated_by":"`+managerSession+`"}`)
		Expect(rec.Code).To(Equal(http.StatusOK))

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.EscalatedBy).To(Equal(managerSession))
		// The pair is written together: a reader measuring the operator rung
		// subtracts one from the other, so a stamp without its timestamp is the
		// silence this field exists to close.
		Expect(got.EscalatedAt).NotTo(BeNil())
		// Escalation is not a transition — the item stays where it was.
		Expect(got.State).To(Equal(pkg.OpenState))
	})

	// The store owns the session-id rule rather than the handler, so this proves
	// the handler surfaces the store's refusal as a 400 rather than swallowing it
	// into the default conflict arm.
	It("rejects a value that is not a well-formed session id", func() {
		item := pushParkedGate()

		rec := escalate(item.ItemID, `{"escalated_by":"session-a"}`)
		Expect(rec.Code).To(Equal(http.StatusBadRequest))

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.EscalatedBy).To(BeEmpty())
		Expect(got.EscalatedAt).To(BeNil())
		Expect(got.State).To(Equal(pkg.OpenState))
	})

	It("rejects an empty escalated_by", func() {
		item := pushParkedGate()

		rec := escalate(item.ItemID, `{"escalated_by":""}`)
		Expect(rec.Code).To(Equal(http.StatusBadRequest))

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.EscalatedBy).To(BeEmpty())
	})

	// The self-stamp rule: a manager re-running its own sweep must not be
	// blocked by its own stamp.
	It("lets the escalating session re-escalate its own item", func() {
		item := pushParkedGate()

		Expect(escalate(item.ItemID, `{"escalated_by":"`+managerSession+`"}`).Code).
			To(Equal(http.StatusOK))
		Expect(escalate(item.ItemID, `{"escalated_by":"`+managerSession+`"}`).Code).
			To(Equal(http.StatusOK))
	})
})
