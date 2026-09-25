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

// The board answers a `message` item over HTTP, so the two rules the schema
// states for that path are asserted at the HTTP layer rather than only in the
// store: a push carrying options on a `message` item is accepted and echoes
// them, a push carrying options on a `permission` item is rejected with 400,
// and the answer body carries the operator's actual answer.
var _ = Describe("Board answers over HTTP", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var pushHandler http.Handler
	var answerHandler http.Handler

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		// A real boltkv DB and a real store. The handlers' own job is to decode a
		// body and hand it on, so the assertion that matters is what the store
		// ended up recording — a faked store would assert the call shape instead
		// of the persisted outcome.
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
		pushHandler = handler.NewAttentionPushHandler(store)
		answerHandler = handler.NewAttentionAnswerHandler(store)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	push := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/1.0/attention", strings.NewReader(body))
		rec := httptest.NewRecorder()
		pushHandler.ServeHTTP(rec, req)
		return rec
	}

	answer := func(itemID pkg.ItemID, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(
			http.MethodPost,
			"/api/1.0/attention/"+itemID.String()+"/answer",
			strings.NewReader(body),
		)
		req = mux.SetURLVars(req, map[string]string{"itemID": itemID.String()})
		rec := httptest.NewRecorder()
		answerHandler.ServeHTTP(rec, req)
		return rec
	}

	// messageBody is the declaration the schema's `options` field exists for.
	messageBody := `{
		"producer_id": "session-a",
		"producer_kind": "session",
		"liveness_ref": "session:session-a",
		"dedup_key": "question-1",
		"interrupt_class": "pick",
		"payload": "Which surface should the answer land on?",
		"context": "The board is the store's own page.",
		"answer_mechanism": "message",
		"options": [{"label": "the board", "recommended": true}, {"label": "the tab"}]
	}`

	Describe("Push", func() {
		It("accepts options on a message item and echoes them with the recommendation", func() {
			rec := push(messageBody)
			Expect(rec.Code).To(Equal(http.StatusCreated))

			var item pkg.Item
			Expect(json.Unmarshal(rec.Body.Bytes(), &item)).To(BeNil())
			Expect(item.Options).To(HaveLen(2))
			Expect(item.Options[0].Label).To(Equal("the board"))
			Expect(item.Options[0].Recommended).To(BeTrue())
			Expect(item.Options[1].Recommended).To(BeFalse())
			Expect(item.Context).To(Equal(pkg.ItemContext("The board is the store's own page.")))
		})

		// The schema's rule, asserted where a producer would meet it: options on
		// a `permission` item describe a choice the mechanism does not offer, so
		// the push is refused rather than stored and ignored.
		It("rejects options on a permission item with 400", func() {
			body := strings.Replace(
				messageBody,
				`"answer_mechanism": "message"`,
				`"answer_mechanism": "permission"`,
				1,
			)
			rec := push(body)
			Expect(rec.Code).To(Equal(http.StatusBadRequest))

			items, err := store.History(ctx)
			Expect(err).To(BeNil())
			Expect(items).To(BeEmpty())
		})

		It("rejects two recommended options with 400", func() {
			body := strings.Replace(
				messageBody,
				`{"label": "the tab"}`,
				`{"label": "the tab", "recommended": true}`,
				1,
			)
			rec := push(body)
			Expect(rec.Code).To(Equal(http.StatusBadRequest))
		})
	})

	Describe("Answer", func() {
		It("stores the operator's chosen option", func() {
			var item pkg.Item
			Expect(json.Unmarshal(push(messageBody).Body.Bytes(), &item)).To(BeNil())

			rec := answer(
				item.ItemID,
				`{"answered_by":"attention-board","answer":{"kind":"option","value":"the board"}}`,
			)
			Expect(rec.Code).To(Equal(http.StatusOK))

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.State).To(Equal(pkg.AnsweredState))
			Expect(got.AnsweredBy).To(Equal("attention-board"))
			Expect(got.Answer).NotTo(BeNil())
			Expect(got.Answer.Kind).To(Equal(pkg.OptionAnswerKind))
			Expect(got.Answer.Value).To(Equal("the board"))
		})

		// Negative control for the spec above: if the handler dropped the answer
		// body and the store defaulted to nil, the option case would still pass
		// only because the assertion reads the value back. A skip must read back
		// as a skip with no value.
		It("stores a skip as a skip with no value", func() {
			var item pkg.Item
			Expect(json.Unmarshal(push(messageBody).Body.Bytes(), &item)).To(BeNil())

			rec := answer(item.ItemID, `{"answered_by":"attention-board","answer":{"kind":"skip"}}`)
			Expect(rec.Code).To(Equal(http.StatusOK))

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.Answer.Kind).To(Equal(pkg.SkipAnswerKind))
			Expect(got.Answer.Value).To(BeEmpty())
		})

		It("stores free text", func() {
			var item pkg.Item
			Expect(json.Unmarshal(push(messageBody).Body.Bytes(), &item)).To(BeNil())

			rec := answer(
				item.ItemID,
				`{"answered_by":"attention-board","answer":{"kind":"text","value":"neither"}}`,
			)
			Expect(rec.Code).To(Equal(http.StatusOK))

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.Answer.Kind).To(Equal(pkg.TextAnswerKind))
			Expect(got.Answer.Value).To(Equal("neither"))
		})

		It("rejects a skip carrying a value with 400", func() {
			var item pkg.Item
			Expect(json.Unmarshal(push(messageBody).Body.Bytes(), &item)).To(BeNil())

			rec := answer(
				item.ItemID,
				`{"answered_by":"attention-board","answer":{"kind":"skip","value":"the board"}}`,
			)
			Expect(rec.Code).To(Equal(http.StatusBadRequest))

			// The item must still be open: a rejected answer is not an answer.
			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.State).To(Equal(pkg.OpenState))
			Expect(got.Answer).To(BeNil())
		})

		It("rejects an unknown answer kind with 400", func() {
			var item pkg.Item
			Expect(json.Unmarshal(push(messageBody).Body.Bytes(), &item)).To(BeNil())

			rec := answer(
				item.ItemID,
				`{"answered_by":"attention-board","answer":{"kind":"bogus","value":"x"}}`,
			)
			Expect(rec.Code).To(Equal(http.StatusBadRequest))
		})
	})
})
