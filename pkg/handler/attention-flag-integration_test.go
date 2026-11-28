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
	"github.com/bborbe/attention-controller/pkg/factory"
	"github.com/bborbe/attention-controller/pkg/handler"
)

// This is the round trip, not a unit of any one layer. A push, a flag, a read
// back, two renders, an unflag and a third render all run against the real
// store, the real handlers and a real router. The point is the wiring: a
// handler bound to the wrong store field, a route registered with the wrong
// method, or a renderer reading a different field than the writer all pass
// their own unit specs and fail here.
var _ = Describe("Attention flag round trip", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var router *mux.Router
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

		// The real router, in the order registerAttentionAPIRoutes uses: the
		// literal /answer route before the /{itemID} GET, which is registered
		// last on purpose. Serving through ServeHTTP is what makes a forgotten
		// or misordered registration fail here rather than in production.
		router = mux.NewRouter()
		router.Path("/api/1.0/attention").
			Methods(http.MethodPost).
			Handler(factory.CreateAttentionPushHandler(store))
		router.Path("/api/1.0/attention/{itemID}/answer").
			Methods(http.MethodPost).
			Handler(factory.CreateAttentionAnswerHandler(store))
		router.Path("/api/1.0/attention/{itemID}/flag").
			Methods(http.MethodPost).
			Handler(factory.CreateAttentionFlagHandler(store))
		router.Path("/api/1.0/attention/{itemID}/unflag").
			Methods(http.MethodPost).
			Handler(factory.CreateAttentionUnflagHandler(store))
		router.Path("/api/1.0/attention/{itemID}").
			Methods(http.MethodGet).
			Handler(factory.CreateAttentionGetHandler(store))

		provenance = &mocks.ProvenanceResolver{}
		provenance.ResolveReturns(pkg.Provenances{})

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

	post := func(path string, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	getItem := func(itemID pkg.ItemID) *pkg.Item {
		req := httptest.NewRequest(
			http.MethodGet,
			"/api/1.0/attention/"+itemID.String(),
			nil,
		)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusOK))
		var item pkg.Item
		Expect(json.NewDecoder(rec.Body).Decode(&item)).To(BeNil())
		return &item
	}

	errorCode := func(rec *httptest.ResponseRecorder) string {
		var errorResponse libhttp.ErrorResponse
		Expect(json.NewDecoder(rec.Body).Decode(&errorResponse)).To(BeNil())
		return errorResponse.Error.Code
	}

	pushMessage := func(dedupKey string) *pkg.Item {
		rec := post(
			"/api/1.0/attention",
			`{"producer_id":"session-a","producer_kind":"session",`+
				`"liveness_ref":"session:session-a","dedup_key":"`+dedupKey+`",`+
				`"interrupt_class":"pick","payload":"Which surface should this land on?",`+
				`"answer_mechanism":"message"}`,
		)
		Expect(rec.Code).To(Equal(http.StatusCreated))
		var item pkg.Item
		Expect(json.NewDecoder(rec.Body).Decode(&item)).To(BeNil())
		return &item
	}

	It("flags and unflags a card end to end, the board serving it throughout", func() {
		item := pushMessage("flag-round-trip")

		entry := boardEntryOf(boardHandler, item.ItemID)
		Expect(entry.FlagReason).To(BeEmpty())
		Expect(entry.FlaggedAt).To(BeNil())

		flagRec := post(
			"/api/1.0/attention/"+item.ItemID.String()+"/flag",
			`{"reason":"asked the wrong team"}`,
		)
		Expect(flagRec.Code).To(Equal(http.StatusOK))

		// The flag parks the card, it does not answer it.
		flagged := getItem(item.ItemID)
		Expect(flagged.State).To(Equal(pkg.OpenState))
		Expect(flagged.AnsweredAt).To(BeNil())
		Expect(flagged.FlagReason).To(Equal("asked the wrong team"))
		Expect(flagged.FlaggedAt).NotTo(BeNil())

		// The board still serves the flagged card — the flag filter is the
		// client's view, never a server-side omission.
		entry = boardEntryOf(boardHandler, item.ItemID)
		Expect(entry.State).To(Equal(pkg.OpenState))
		Expect(entry.FlagReason).To(Equal("asked the wrong team"))
		Expect(entry.FlaggedAt).NotTo(BeNil())

		unflagRec := post(
			"/api/1.0/attention/"+item.ItemID.String()+"/unflag",
			"",
		)
		Expect(unflagRec.Code).To(Equal(http.StatusOK))
		unflagged := getItem(item.ItemID)
		Expect(unflagged.FlagReason).To(BeEmpty())
		Expect(unflagged.FlaggedAt).To(BeNil())

		entry = boardEntryOf(boardHandler, item.ItemID)
		Expect(entry.FlagReason).To(BeEmpty())
		Expect(entry.FlaggedAt).To(BeNil())
	})

	It("carries the reason verbatim as data, never pre-rendered markup", func() {
		item := pushMessage("flag-escape")
		flagRec := post(
			"/api/1.0/attention/"+item.ItemID.String()+"/flag",
			`{"reason":"<script>alert(1)</script>"}`,
		)
		Expect(flagRec.Code).To(Equal(http.StatusOK))

		Expect(boardEntryOf(boardHandler, item.ItemID).FlagReason).
			To(Equal("<script>alert(1)</script>"))
	})

	It("refuses a flag on an answered card and leaves the record intact", func() {
		item := pushMessage("flag-answered")

		answerRec := post(
			"/api/1.0/attention/"+item.ItemID.String()+"/answer",
			`{"answered_by":"attention-board"}`,
		)
		Expect(answerRec.Code).To(Equal(http.StatusOK))
		// Settled: a held answer is still open and flaggable; the refusal is for
		// the settled answered state.
		_, err := store.SettleAnswer(ctx, item.ItemID)
		Expect(err).To(BeNil())

		flagRec := post(
			"/api/1.0/attention/"+item.ItemID.String()+"/flag",
			`{"reason":"asked the wrong team"}`,
		)
		Expect(flagRec.Code).To(Equal(http.StatusConflict))
		Expect(errorCode(flagRec)).To(Equal(pkg.ErrorCodeItemNotOpen))

		got := getItem(item.ItemID)
		Expect(got.State).To(Equal(pkg.AnsweredState))
		Expect(got.FlagReason).To(BeEmpty())
		Expect(got.FlaggedAt).To(BeNil())
	})
})
