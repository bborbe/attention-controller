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

// The attention item schema's silence 19 made `answered_client` store-written
// from the request itself, and the derivation is the half that only exists here:
// the store has no request to read, so the handlers compose the record.
//
// ⚠️ The load-bearing rule is that `user_agent` and `remote_addr` come from the
// `*http.Request` and never from the body — a caller can lie about its own
// automation, but cannot lie about its own source address. `automation` is the
// one member the body carries, because only the page's own script can read
// `navigator.webdriver`, and it is explicitly the weaker member.
//
// Both routes that set the field are asserted here rather than only the answer
// route: `POST /close` is a different handler on a different row of the schema's
// transitions table, and the same property applies there.
var _ = Describe("Answered client over HTTP", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var pushHandler http.Handler
	var answerHandler http.Handler
	var closeHandler http.Handler

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		// A real boltkv DB and a real store, as in the board-answer specs: the
		// assertion that matters is what the store ended up recording, and a
		// faked store would assert the call shape instead of the persisted
		// outcome.
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
		closeHandler = handler.NewAttentionCloseHandler(store)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	// push stores the item under test and returns what the store wrote.
	push := func(body string) *pkg.Item {
		req := httptest.NewRequest(http.MethodPost, "/api/1.0/attention", strings.NewReader(body))
		rec := httptest.NewRecorder()
		pushHandler.ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusCreated))

		var item pkg.Item
		Expect(json.Unmarshal(rec.Body.Bytes(), &item)).To(BeNil())
		return &item
	}

	pushMessage := func() *pkg.Item {
		return push(`{
			"producer_id": "session-a",
			"producer_kind": "session",
			"liveness_ref": "session:session-a",
			"dedup_key": "question-1",
			"interrupt_class": "pick",
			"payload": "Which surface should the answer land on?",
			"answer_mechanism": "message"
		}`)
	}

	pushAck := func() *pkg.Item {
		return push(`{
			"producer_id": "session-a",
			"producer_kind": "session",
			"liveness_ref": "session:session-a",
			"dedup_key": "report-1",
			"interrupt_class": "approve",
			"payload": "the nightly sweep failed",
			"answer_mechanism": "ack"
		}`)
	}

	// post sends a body to one of the item's routes with an explicit User-Agent
	// and source address, which are exactly the two members the store derives and
	// the body must not be able to set. The path var is set with SetURLVars
	// rather than by mounting a router: the handlers read it through mux.Vars, and
	// a router would add a second thing to get wrong without testing anything
	// they own.
	post := func(
		h http.Handler,
		route string,
		itemID pkg.ItemID,
		body string,
		userAgent string,
		remoteAddr string,
	) *httptest.ResponseRecorder {
		req := httptest.NewRequest(
			http.MethodPost,
			"/api/1.0/attention/"+itemID.String()+route,
			strings.NewReader(body),
		)
		req = mux.SetURLVars(req, map[string]string{"itemID": itemID.String()})
		req.Header.Set("User-Agent", userAgent)
		req.RemoteAddr = remoteAddr
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	answer := func(itemID pkg.ItemID, body string) *httptest.ResponseRecorder {
		return post(answerHandler, "/answer", itemID, body, "curl/8.7.1", "198.51.100.7:41000")
	}

	closeItem := func(itemID pkg.ItemID, body string) *httptest.ResponseRecorder {
		return post(closeHandler, "/close", itemID, body, "Mozilla/5.0", "203.0.113.9:52000")
	}

	Describe("The answer route", func() {
		It("records the client the request itself identifies", func() {
			item := pushMessage()

			rec := answer(item.ItemID, `{"answered_by":"attention-board"}`)
			Expect(rec.Code).To(Equal(http.StatusOK))

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.AnsweredClient).NotTo(BeNil())
			Expect(got.AnsweredClient.UserAgent).To(Equal("curl/8.7.1"))
			Expect(got.AnsweredClient.RemoteAddr).To(Equal("198.51.100.7:41000"))
		})

		// ⚠️ The load-bearing spec. A body that names the two derived members
		// must not be able to set either of them — if it could, the field would
		// be a declaration like answered_by rather than a fact the store holds,
		// and it would carry exactly the spoofing it exists to defeat. Both
		// spellings are attempted: flat, and nested under the item's own field
		// name, in case the body is later routed straight onto the item.
		It("does not take user_agent or remote_addr from the body", func() {
			item := pushMessage()

			rec := answer(item.ItemID, `{
				"answered_by": "attention-board",
				"user_agent": "evil-agent",
				"remote_addr": "10.0.0.1:1",
				"answered_client": {
					"user_agent": "evil-agent",
					"remote_addr": "10.0.0.1:1"
				}
			}`)
			Expect(rec.Code).To(Equal(http.StatusOK))

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.AnsweredClient).NotTo(BeNil())
			Expect(got.AnsweredClient.UserAgent).To(Equal("curl/8.7.1"))
			Expect(got.AnsweredClient.UserAgent).NotTo(Equal("evil-agent"))
			Expect(got.AnsweredClient.RemoteAddr).To(Equal("198.51.100.7:41000"))
			Expect(got.AnsweredClient.RemoteAddr).NotTo(Equal("10.0.0.1:1"))
		})

		It("round-trips the automation hint the page reported", func() {
			item := pushMessage()

			rec := answer(item.ItemID, `{"answered_by":"attention-board","automation":true}`)
			Expect(rec.Code).To(Equal(http.StatusOK))

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.AnsweredClient).NotTo(BeNil())
			Expect(got.AnsweredClient.Automation).NotTo(BeNil())
			Expect(*got.AnsweredClient.Automation).To(BeTrue())
		})

		// Negative control for the spec above: if an omitted hint decoded to the
		// zero value, every request that cannot read navigator.webdriver would
		// read as a positive claim that the client was not automated.
		It("leaves the automation hint absent when the body omits it", func() {
			item := pushMessage()

			rec := answer(item.ItemID, `{"answered_by":"attention-board"}`)
			Expect(rec.Code).To(Equal(http.StatusOK))

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.AnsweredClient).NotTo(BeNil())
			Expect(got.AnsweredClient.Automation).To(BeNil())

			encoded, err := json.Marshal(got.AnsweredClient)
			Expect(err).To(BeNil())
			Expect(string(encoded)).NotTo(ContainSubstring("automation"))
		})
	})

	Describe("The close route", func() {
		It("records the client on an arm-caused close", func() {
			item := pushAck()

			// The body names the two derived members as well, so the close route
			// carries the same load-bearing rule the answer route does.
			rec := closeItem(item.ItemID, `{
				"answered_by": "attention-board",
				"automation": true,
				"user_agent": "evil-agent",
				"remote_addr": "10.0.0.1:1"
			}`)
			Expect(rec.Code).To(Equal(http.StatusOK))

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.State).To(Equal(pkg.ClosedState))
			Expect(got.AnsweredBy).To(Equal("attention-board"))
			Expect(got.AnsweredClient).NotTo(BeNil())
			Expect(got.AnsweredClient.UserAgent).To(Equal("Mozilla/5.0"))
			Expect(got.AnsweredClient.RemoteAddr).To(Equal("203.0.113.9:52000"))
			Expect(got.AnsweredClient.Automation).NotTo(BeNil())
			Expect(*got.AnsweredClient.Automation).To(BeTrue())
		})

		// Negative control: the schema sets the field on an arm-caused
		// open -> closed, so a close that names no arm — a producer withdrawing
		// its own item, the shape the route's empty body has always carried —
		// records no client either.
		It("records no client when the close names no arm", func() {
			item := pushAck()

			rec := closeItem(item.ItemID, "")
			Expect(rec.Code).To(Equal(http.StatusOK))

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.State).To(Equal(pkg.ClosedState))
			Expect(got.AnsweredClient).To(BeNil())
		})
	})
})
