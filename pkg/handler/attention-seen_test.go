// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	stdtime "time"

	libboltkv "github.com/bborbe/boltkv"
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

// The seen write records that the board painted a card. Its whole contract is
// store-stamped and first-write-wins: the caller supplies no time, the first
// call stamps, every later call is inert, and the write is LOCAL — never
// proxied to a peer.
var _ = Describe("Attention seen handler", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var seenHandler http.Handler
	var pageHandler http.Handler

	// now is the store's clock, movable so the first-write-wins half can be
	// observed: a second call under a later clock must leave the first instant.
	now := stdtime.Date(2026, 11, 11, 10, 0, 0, 0, stdtime.UTC)
	clock := func() libtime.DateTime { return libtime.DateTime(now) }

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

		now = stdtime.Date(2026, 11, 11, 10, 0, 0, 0, stdtime.UTC)
		store = pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			sessionLivenessChecker,
			libtime.CurrentDateTimeGetterFunc(clock),
			libtime.Duration(15*60*1e9),
		)
		seenHandler = handler.NewAttentionSeenHandler(store)
		pageHandler = handler.NewAttentionPageHandler(
			store,
			&mocks.ProvenanceResolver{},
			false,
			"",
			testBuildIdentity,
			boardmetrics.NewMetrics(prometheus.NewRegistry()),
		)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	push := func() *pkg.Item {
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      "session-a",
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:session-a"),
			DedupKey:        "gate-1",
			InterruptClass:  "approve",
			Payload:         "deploy prod?",
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		Expect(err).To(BeNil())
		return item
	}

	// seen posts to the item's seen route with no body, through the router the
	// handler is mounted on so the {itemID} path var is resolved as it is in
	// production.
	seen := func(h http.Handler, itemID pkg.ItemID) *httptest.ResponseRecorder {
		router := mux.NewRouter()
		router.Path("/api/1.0/attention/{itemID}/seen").
			Methods(http.MethodPost).
			Handler(h)
		req := httptest.NewRequest(
			http.MethodPost,
			"/api/1.0/attention/"+itemID.String()+"/seen",
			nil,
		)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	It("stamps the store's clock on the first call and returns 204", func() {
		item := push()

		rec := seen(seenHandler, item.ItemID)
		Expect(rec.Code).To(Equal(http.StatusNoContent))
		Expect(rec.Body.String()).To(BeEmpty())

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.DisplayedAt).NotTo(BeNil())
		Expect(got.DisplayedAt.String()).To(Equal(libtime.DateTime(now).String()))
	})

	It("leaves the stamp unchanged on a second call", func() {
		item := push()

		Expect(seen(seenHandler, item.ItemID).Code).To(Equal(http.StatusNoContent))

		// Move the clock: a second call must not move the stamp. Comparing
		// .String() values rather than pointers is what makes this the
		// first-write-wins assertion rather than an identity check.
		now = now.Add(stdtime.Hour)
		Expect(seen(seenHandler, item.ItemID).Code).To(Equal(http.StatusNoContent))

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.DisplayedAt).NotTo(BeNil())
		Expect(got.DisplayedAt.String()).
			To(Equal(libtime.DateTime(stdtime.Date(2026, 11, 11, 10, 0, 0, 0, stdtime.UTC)).String()))
	})

	It("answers 404 for an item this store does not own and writes nothing", func() {
		rec := seen(seenHandler, pkg.ItemID("no-such-item"))
		Expect(rec.Code).To(Equal(http.StatusNotFound))

		_, err := store.Get(ctx, pkg.ItemID("no-such-item"))
		Expect(err).NotTo(BeNil())
		Expect(errors.Is(err, pkg.ErrItemNotFound)).To(BeTrue())
	})

	It("answers 404 for a federated item and writes nothing locally", func() {
		remote := &mocks.RemoteAttentionStore{}
		federating := pkg.NewFederatingAttentionStore(store, remote)
		federatingHandler := handler.NewAttentionSeenHandler(federating)

		rec := seen(federatingHandler, pkg.ItemID("peer-owned-item"))
		Expect(rec.Code).To(Equal(http.StatusNotFound))

		// The write is local-only: a proxied write would have needed a peer
		// call, and a local write would have created a row.
		_, err := store.Get(ctx, pkg.ItemID("peer-owned-item"))
		Expect(err).NotTo(BeNil())
		Expect(errors.Is(err, pkg.ErrItemNotFound)).To(BeTrue())
		Expect(remote.FlagCallCount()).To(Equal(0))
	})

	It("issues the seen call before the filter parks a row", func() {
		push()

		req := httptest.NewRequest(http.MethodGet, "/?producer_kind=all", nil)
		rec := httptest.NewRecorder()
		pageHandler.ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusOK))
		body := rec.Body.String()

		Expect(body).To(ContainSubstring("'/seen'"))
		Expect(body).To(ContainSubstring("function markSeen("))
		Expect(body).To(ContainSubstring("function markEverySeen("))
		// The call is adjacent to the load-time filter block, in this exact
		// order: a script that stamped only after the filter would leave every
		// parked card unstamped, which is the failure the ordering prevents.
		Expect(body).To(ContainSubstring(
			"markEverySeen();\n  applyFilter();\n  syncKindHref();\n  updateKindCount();",
		))
	})
})
