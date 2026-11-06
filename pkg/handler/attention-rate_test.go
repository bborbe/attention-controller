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
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/boardmetrics"
	"github.com/bborbe/attention-controller/pkg/handler"
)

var _ = Describe("Attention rate handler and board controls", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var rateHandler http.Handler
	var pageHandler http.Handler
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
		pageHandler = handler.NewAttentionPageHandler(
			store,
			provenance,
			false,
			filepath.Join(GinkgoT().TempDir(), "Personal"),
			testBuildIdentity,
			boardmetrics.NewMetrics(prometheus.NewRegistry()),
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

	render := func() string {
		req := httptest.NewRequest(http.MethodGet, "/?producer_kind=all&hide=none", nil)
		resp := httptest.NewRecorder()
		pageHandler.ServeHTTP(resp, req)
		Expect(resp.Code).To(Equal(http.StatusOK))
		return resp.Body.String()
	}

	rowOf := func(body string, itemID pkg.ItemID) string {
		start := strings.Index(body, `data-item-id="`+itemID.String()+`"`)
		Expect(start).To(BeNumerically(">=", 0), "row for %s not found", itemID)
		rest := body[start:]
		end := strings.Index(rest, "</li>")
		Expect(end).To(BeNumerically(">=", 0))
		return rest[:end]
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

	It("renders both controls on open and answered rows, highlighting the stored value", func() {
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

		body := render()

		openRow := rowOf(body, open.ItemID)
		Expect(openRow).To(ContainSubstring(`data-rate-control data-rating=""`))
		Expect(openRow).To(ContainSubstring(`data-rate="up"`))
		Expect(openRow).To(ContainSubstring(`data-rate="down"`))
		Expect(openRow).NotTo(ContainSubstring(`rate-btn rated`))

		answeredRow := rowOf(body, answered.ItemID)
		Expect(answeredRow).To(ContainSubstring(`data-rating="down"`))
		Expect(answeredRow).To(ContainSubstring(`class="rate-btn rated" data-rate="down"`))
		Expect(answeredRow).To(ContainSubstring(`class="rate-btn" data-rate="up"`))
	})

	It("carries the delegated rate listener that posts to /rate, never /answer", func() {
		body := render()
		Expect(body).To(ContainSubstring(`closest('button[data-rate]')`))
		Expect(body).To(ContainSubstring(`'/rate'`))
	})
})
