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
	libhttp "github.com/bborbe/http"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	"github.com/gorilla/mux"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/handler"
)

var _ = Describe("Attention flag handlers", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var flagHandler http.Handler
	var unflagHandler http.Handler

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		// A real boltkv DB and a real store: the assertion that matters is what
		// the store recorded, and a faked store would assert the call shape
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
		flagHandler = handler.NewAttentionFlagHandler(store)
		unflagHandler = handler.NewAttentionUnflagHandler(store)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	pushItem := func() *pkg.Item {
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

	// flag posts a body to the item's flag route. The path var is set with
	// SetURLVars rather than by mounting a router, matching the sibling specs.
	flag := func(itemID pkg.ItemID, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(
			http.MethodPost,
			"/api/1.0/attention/"+itemID.String()+"/flag",
			strings.NewReader(body),
		)
		req = mux.SetURLVars(req, map[string]string{"itemID": itemID.String()})
		rec := httptest.NewRecorder()
		flagHandler.ServeHTTP(rec, req)
		return rec
	}

	// unflag posts the unflag route, which takes no body.
	unflag := func(itemID pkg.ItemID) *httptest.ResponseRecorder {
		req := httptest.NewRequest(
			http.MethodPost,
			"/api/1.0/attention/"+itemID.String()+"/unflag",
			nil,
		)
		req = mux.SetURLVars(req, map[string]string{"itemID": itemID.String()})
		rec := httptest.NewRecorder()
		unflagHandler.ServeHTTP(rec, req)
		return rec
	}

	// errorCode decodes the standard error body and returns its machine-readable
	// code. Decoded rather than substring-matched, so a body that merely
	// resembles the shape does not pass.
	errorCode := func(rec *httptest.ResponseRecorder) string {
		var errorResponse libhttp.ErrorResponse
		Expect(json.NewDecoder(rec.Body).Decode(&errorResponse)).To(BeNil())
		return errorResponse.Error.Code
	}

	It("records the reason and answers 200", func() {
		item := pushItem()

		rec := flag(item.ItemID, `{"reason":"asked the wrong team"}`)
		Expect(rec.Code).To(Equal(http.StatusOK))

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.FlagReason).To(Equal("asked the wrong team"))
		Expect(got.FlaggedAt).NotTo(BeNil())
		// A flag is not a transition and not an answer.
		Expect(got.State).To(Equal(pkg.OpenState))
		Expect(got.AnsweredAt).To(BeNil())
	})

	It("refuses an empty reason with 400 and FLAG_REASON_REQUIRED", func() {
		item := pushItem()

		rec := flag(item.ItemID, `{"reason":""}`)
		Expect(rec.Code).To(Equal(http.StatusBadRequest))
		Expect(errorCode(rec)).To(Equal(pkg.ErrorCodeFlagReasonRequired))

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.FlagReason).To(BeEmpty())
		Expect(got.FlaggedAt).To(BeNil())
	})

	It("refuses a whitespace-only reason with 400 and FLAG_REASON_REQUIRED", func() {
		item := pushItem()

		rec := flag(item.ItemID, `{"reason":"   "}`)
		Expect(rec.Code).To(Equal(http.StatusBadRequest))
		Expect(errorCode(rec)).To(Equal(pkg.ErrorCodeFlagReasonRequired))

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.FlagReason).To(BeEmpty())
		Expect(got.FlaggedAt).To(BeNil())
	})

	It("refuses a body carrying no reason key with 400 and FLAG_REASON_REQUIRED", func() {
		item := pushItem()

		rec := flag(item.ItemID, `{}`)
		Expect(rec.Code).To(Equal(http.StatusBadRequest))
		Expect(errorCode(rec)).To(Equal(pkg.ErrorCodeFlagReasonRequired))

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.FlagReason).To(BeEmpty())
		Expect(got.FlaggedAt).To(BeNil())
	})

	It("refuses a malformed body with 400 and FLAG_REASON_REQUIRED", func() {
		item := pushItem()

		rec := flag(item.ItemID, `{"reason":`)
		Expect(rec.Code).To(Equal(http.StatusBadRequest))
		Expect(errorCode(rec)).To(Equal(pkg.ErrorCodeFlagReasonRequired))

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.FlagReason).To(BeEmpty())
		Expect(got.FlaggedAt).To(BeNil())
	})

	It("answers 404 for an unknown item", func() {
		rec := flag("0000000000000000000000000000dead", `{"reason":"asked the wrong team"}`)
		Expect(rec.Code).To(Equal(http.StatusNotFound))
	})

	It("answers 409 ITEM_NOT_OPEN for an answered item", func() {
		item := pushItem()
		_, err := store.Answer(ctx, item.ItemID, "telegram", "", "", nil, nil, nil)
		Expect(err).To(BeNil())

		rec := flag(item.ItemID, `{"reason":"asked the wrong team"}`)
		Expect(rec.Code).To(Equal(http.StatusConflict))
		Expect(errorCode(rec)).To(Equal(pkg.ErrorCodeItemNotOpen))

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.State).To(Equal(pkg.AnsweredState))
		Expect(got.FlagReason).To(BeEmpty())
		Expect(got.FlaggedAt).To(BeNil())
	})

	It("clears the flag on unflag", func() {
		item := pushItem()
		Expect(flag(item.ItemID, `{"reason":"asked the wrong team"}`).Code).
			To(Equal(http.StatusOK))

		rec := unflag(item.ItemID)
		Expect(rec.Code).To(Equal(http.StatusOK))

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.FlagReason).To(BeEmpty())
		Expect(got.FlaggedAt).To(BeNil())
	})

	It("answers 200 when unflagging an item that carries no flag", func() {
		item := pushItem()

		rec := unflag(item.ItemID)
		Expect(rec.Code).To(Equal(http.StatusOK))

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.FlagReason).To(BeEmpty())
		Expect(got.FlaggedAt).To(BeNil())
	})

	It("answers 404 when unflagging an unknown item", func() {
		rec := unflag("0000000000000000000000000000dead")
		Expect(rec.Code).To(Equal(http.StatusNotFound))
	})
})
