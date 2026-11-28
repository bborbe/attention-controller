// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

var _ = Describe("Attention rate handler and board controls", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var rateHandler http.Handler
	var boardHandler http.Handler
	var provenance *mocks.ProvenanceResolver

	BeforeEach(func() {
		ctx = context.Background()
		var err error
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
		rateHandler = handler.NewAttentionRateHandler(store)
		provenance = &mocks.ProvenanceResolver{}
		boardHandler = handler.NewAttentionBoardReadHandler(
			store,
			provenance,
			false,
			filepath.Join(GinkgoT().TempDir(), "Personal"),
		)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	push := func(dedupKey pkg.DedupKey) *pkg.Item {
		producerID := pkg.ProducerID("producer-" + dedupKey.String())
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      producerID,
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:" + producerID.String()),
			DedupKey:        dedupKey,
			InterruptClass:  "pick",
			Payload:         "rate me",
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		Expect(err).To(BeNil())
		return item
	}

	rate := func(itemID pkg.ItemID, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(
			http.MethodPost,
			"/api/1.0/attention/"+itemID.String()+"/rate",
			strings.NewReader(body),
		)
		req = mux.SetURLVars(req, map[string]string{"itemID": itemID.String()})
		rec := httptest.NewRecorder()
		rateHandler.ServeHTTP(rec, req)
		return rec
	}

	errorCode := func(rec *httptest.ResponseRecorder) string {
		var errorResponse libhttp.ErrorResponse
		Expect(json.NewDecoder(rec.Body).Decode(&errorResponse)).To(BeNil())
		return errorResponse.Error.Code
	}

	It("stores up and answers 200 without answering the item", func() {
		item := push("rate-http-up")

		rec := rate(item.ItemID, `{"rating":"up"}`)
		Expect(rec.Code).To(Equal(http.StatusOK))

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.Rating).To(Equal(pkg.UpRating))
		Expect(got.State).To(Equal(pkg.OpenState))
		Expect(got.AnsweredAt).To(BeNil())
	})

	It("clears the rating on an empty value", func() {
		item := push("rate-http-clear")
		Expect(rate(item.ItemID, `{"rating":"down"}`).Code).To(Equal(http.StatusOK))

		Expect(rate(item.ItemID, `{"rating":""}`).Code).To(Equal(http.StatusOK))

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.Rating).To(Equal(pkg.NoRating))
		Expect(got.RatedAt).To(BeNil())
	})

	It("refuses an unknown value with 400 RATING_INVALID", func() {
		item := push("rate-http-bad")
		rec := rate(item.ItemID, `{"rating":"meh"}`)
		Expect(rec.Code).To(Equal(http.StatusBadRequest))
		Expect(errorCode(rec)).To(Equal(pkg.ErrorCodeRatingInvalid))
	})

	It("refuses a non-JSON body with 400 RATING_INVALID", func() {
		item := push("rate-http-nojson")
		rec := rate(item.ItemID, `not json`)
		Expect(rec.Code).To(Equal(http.StatusBadRequest))
		Expect(errorCode(rec)).To(Equal(pkg.ErrorCodeRatingInvalid))
	})

	It("answers 404 for an unknown item", func() {
		rec := rate(pkg.ItemID("no-such-item"), `{"rating":"up"}`)
		Expect(rec.Code).To(Equal(http.StatusNotFound))
	})

	It("carries the stored rating on open and answered cards in the board JSON", func() {
		open := push("rate-page-open")
		answered := push("rate-page-answered")
		_, err := store.Answer(ctx, answered.ItemID, "attention-board", "", "", nil, nil, nil)
		Expect(err).To(BeNil())
		_, err = store.Rate(ctx, answered.ItemID, pkg.DownRating)
		Expect(err).To(BeNil())
		provenance.ResolveReturns(pkg.Provenances{
			open.ItemID:     pkg.Provenance{},
			answered.ItemID: pkg.Provenance{},
		})

		board := readBoardByID(boardHandler)

		Expect(board).To(HaveKey(open.ItemID))
		Expect(board[open.ItemID].Rating).To(BeEmpty())
		// A rating is a measurement on any state, so the answered record keeps it.
		Expect(board).To(HaveKey(answered.ItemID))
		Expect(board[answered.ItemID].State).To(Equal(pkg.AnsweredState))
		Expect(board[answered.ItemID].Rating).To(Equal(pkg.DownRating))
	})
})
