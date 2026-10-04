// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/handler"
)

// The history endpoint is the one counting surface, and it is paginated now, so
// the bounds are asserted at the HTTP layer rather than only in the store: what
// a caller gets for an absent, a malformed and a valid bound are three different
// answers, and only the third is a page.
var _ = Describe("Attention history over HTTP", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var pushHandler http.Handler
	var historyHandler http.Handler

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
		pushHandler = handler.NewAttentionPushHandler(store)
		historyHandler = handler.NewAttentionHistoryHandler(store)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	// seed pushes n items, so the ordering and paging specs have a population
	// with a known creation order.
	seed := func(n int) {
		for i := 0; i < n; i++ {
			body := fmt.Sprintf(`{
				"producer_id": "session-a",
				"producer_kind": "session",
				"liveness_ref": "session:session-a",
				"dedup_key": "gate-%d",
				"interrupt_class": "pick",
				"payload": "Which surface should the answer land on?",
				"answer_mechanism": "message"
			}`, i)
			req := httptest.NewRequest(
				http.MethodPost, "/api/1.0/attention", strings.NewReader(body),
			)
			rec := httptest.NewRecorder()
			pushHandler.ServeHTTP(rec, req)
			Expect(rec.Code).To(Equal(http.StatusCreated))
		}
	}

	get := func(query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/1.0/attention/history"+query, nil)
		rec := httptest.NewRecorder()
		historyHandler.ServeHTTP(rec, req)
		return rec
	}

	itemsOf := func(rec *httptest.ResponseRecorder) []pkg.Item {
		var items []pkg.Item
		Expect(json.Unmarshal(rec.Body.Bytes(), &items)).To(BeNil())
		return items
	}

	// ⚠️ The response shape is a compatibility contract, not a detail: three
	// callers in the wild unmarshal this endpoint as a bare array, and the REST
	// guide records that a pagination envelope is not yet standardized. A spec
	// that only counted items would pass on an envelope that broke all three.
	It("returns a bare JSON array, not an envelope", func() {
		seed(2)
		rec := get("")
		Expect(rec.Code).To(Equal(http.StatusOK))

		var envelope map[string]any
		Expect(json.Unmarshal(rec.Body.Bytes(), &envelope)).NotTo(BeNil(),
			"history must not be a JSON object")
		Expect(itemsOf(rec)).To(HaveLen(2))
	})

	It("returns every item when no limit is given and the store is small", func() {
		seed(3)
		rec := get("")
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(itemsOf(rec)).To(HaveLen(3))
	})

	It("bounds the page to limit", func() {
		seed(3)
		rec := get("?limit=1")
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(itemsOf(rec)).To(HaveLen(1))
	})

	It("skips the first offset", func() {
		seed(3)
		all := itemsOf(get(""))
		page := itemsOf(get("?offset=1"))
		Expect(page).To(HaveLen(2))
		Expect(page[0].ItemID).To(Equal(all[1].ItemID))
		Expect(page[1].ItemID).To(Equal(all[2].ItemID))
	})

	It("returns items newest-first", func() {
		seed(3)
		items := itemsOf(get(""))
		Expect(items).To(HaveLen(3))
		for i := 1; i < len(items); i++ {
			Expect(
				items[i].CreatedAt.Time().After(items[i-1].CreatedAt.Time()),
			).To(BeFalse(), "history is not newest-first at index %d", i)
		}
	})

	// ⚠️ A malformed bound is a 400 rather than a fallback, and that is the
	// assertion worth having. Falling back would answer a typo with a plausible
	// page, so a caller counting resolutions would silently count a page — the
	// failure this endpoint exists to avoid, arriving as a wrong number rather
	// than an error.
	It("rejects a non-numeric limit with 400", func() {
		seed(1)
		rec := get("?limit=1o00")
		Expect(rec.Code).To(Equal(http.StatusBadRequest))
	})

	It("rejects a negative limit with 400", func() {
		seed(1)
		rec := get("?limit=-1")
		Expect(rec.Code).To(Equal(http.StatusBadRequest))
	})

	It("rejects a negative offset with 400", func() {
		seed(1)
		rec := get("?offset=-1")
		Expect(rec.Code).To(Equal(http.StatusBadRequest))
	})

	// The unbounded compatibility path, asserted so a caller that needs the whole
	// store can still be shown to get it.
	It("returns every item when the limit is explicitly zero", func() {
		seed(3)
		rec := get("?limit=0")
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(itemsOf(rec)).To(HaveLen(3))
	})
})
