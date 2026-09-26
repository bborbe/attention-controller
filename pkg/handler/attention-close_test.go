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

var _ = Describe("AttentionCloseHandler", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var httpHandler http.Handler

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		// A real boltkv DB and a real store, not a fake of either: the assertion
		// that matters is what the store ended up recording, and a faked store
		// would assert the call shape instead of the persisted outcome.
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
		httpHandler = handler.NewAttentionCloseHandler(store)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	// pushAck pushes a report-only item, which is the class an arm may
	// acknowledge: it asks nothing and routes nothing back, so the close is the
	// whole of its resolution.
	pushAck := func() *pkg.Item {
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      "session-a",
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:session-a"),
			DedupKey:        "report-1",
			InterruptClass:  "approve",
			Payload:         "the nightly sweep failed",
			AnswerMechanism: pkg.AckAnswerMechanism,
		})
		Expect(err).To(BeNil())
		return item
	}

	// pushQuestion pushes a `message` item, the class whose close rides the
	// answered -> closed row rather than the open -> closed one.
	pushQuestion := func() *pkg.Item {
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      "session-b",
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:session-b"),
			DedupKey:        "gate-1",
			InterruptClass:  "pick",
			Payload:         "deploy prod?",
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		Expect(err).To(BeNil())
		return item
	}

	// closeItem posts a body to the item's close route. The path var is set with
	// SetURLVars rather than by mounting a router: the handler reads it through
	// mux.Vars, and a router would add a second thing to get wrong without
	// testing anything the handler owns.
	//
	// An empty body is passed as an empty reader rather than a nil one, because
	// that is the shape a real request carries — the decoder sees EOF either way,
	// and a nil Body would panic before reaching the code under test.
	closeItem := func(itemID pkg.ItemID, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(
			http.MethodPost,
			"/api/1.0/attention/"+itemID.String()+"/close",
			strings.NewReader(body),
		)
		req = mux.SetURLVars(req, map[string]string{"itemID": itemID.String()})
		resp := httptest.NewRecorder()
		httpHandler.ServeHTTP(resp, req)
		return resp
	}

	It("closes an item on an empty body, the shape every existing caller sends", func() {
		item := pushAck()

		resp := closeItem(item.ItemID, "")

		// ⚠️ This is the regression guard for the body becoming optional. Before
		// the arm was recorded this route took no body at all, so a caller that
		// sends none must still close the item rather than being rejected as
		// malformed.
		Expect(resp.Code).To(Equal(http.StatusOK))
		stored, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(stored.State).To(Equal(pkg.ClosedState))
		Expect(stored.ClosedAt).NotTo(BeNil())
		// No arm was named, so none is recorded — which is what a producer
		// withdrawing its own item reads as.
		Expect(stored.AnsweredBy).To(BeEmpty())
	})

	It("records the arm that caused the close, and leaves answered_at unset", func() {
		item := pushAck()

		resp := closeItem(item.ItemID, `{"answered_by":"attention-board"}`)

		Expect(resp.Code).To(Equal(http.StatusOK))
		stored, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(stored.State).To(Equal(pkg.ClosedState))
		Expect(stored.AnsweredBy).To(Equal("attention-board"))
		// The schema's evidence for an `ack` item is that its state reaches
		// closed with answered_at unset, because an ack routes nothing back to
		// its producer. Asserted rather than assumed: a close that stamped
		// answered_at would make an acknowledgement indistinguishable from an
		// answer on every reader that counts them.
		Expect(stored.AnsweredAt).To(BeNil())
	})

	It("rejects a body that is present but malformed", func() {
		item := pushAck()

		resp := closeItem(item.ItemID, `{"answered_by":`)

		Expect(resp.Code).To(Equal(http.StatusBadRequest))
		// The item is untouched — a rejected request must not have closed it.
		stored, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(stored.State).To(Equal(pkg.OpenState))
	})

	It("does not overwrite the arm that answered when closing an answered item", func() {
		item := pushQuestion()
		_, err := store.Answer(ctx, item.ItemID, "supervisor:attention-next", "", "", nil, nil)
		Expect(err).To(BeNil())

		resp := closeItem(item.ItemID, `{"answered_by":"attention-board"}`)

		Expect(resp.Code).To(Equal(http.StatusOK))
		stored, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(stored.State).To(Equal(pkg.ClosedState))
		// ⚠️ The answered -> closed row must keep the arm that *answered*. The
		// arm is written on the open -> closed row only, so closing an item that
		// was already answered leaves the answering arm in place rather than
		// replacing it with whoever closed it.
		Expect(stored.AnsweredBy).To(Equal("supervisor:attention-next"))
	})

	It("returns 404 for an item that does not exist", func() {
		resp := closeItem(pkg.ItemID("00000000000000000000000000000000"), "")

		Expect(resp.Code).To(Equal(http.StatusNotFound))
	})
})
