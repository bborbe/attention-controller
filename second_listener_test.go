// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
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
)

// secondListenerToken is the credential the handler under test is built with.
// A literal rather than a generated value so a spec can name it in the
// authenticated probes and assert its absence from every refusal body.
const secondListenerToken = "s3cret-token"

// businessRoutes is every route the second listener registers, with the method
// it answers.
//
// ⚠️ The method is part of the case: gorilla/mux runs the bearer check only for
// a request that matched a route, so a probe using the wrong method falls to
// the 405 handler without ever reaching the middleware and would silently stop
// testing the gate. Both sweeps below share this one list, so the
// unauthenticated and authenticated probes cannot disagree about which routes
// exist.
//
// ⚠️ The two read-aloud routes (`/{itemID}/speak`, `/{itemID}/cancel`) are
// absent because the handler under test is built with an empty TTSURL and
// registerAttentionAPIRoutes registers them only when one is configured — this
// list is then the whole of what the listener serves. ⚠️ The SSE stream is
// absent because it is registered on the board's router, not this one; here it
// would match `GET /{itemID}` with an id of "stream", so it is not a route of
// this listener and not an isolation probe.
var businessRoutes = []struct{ method, path string }{
	{http.MethodPost, "/api/1.0/attention"},
	{http.MethodGet, "/api/1.0/attention"},
	{http.MethodGet, "/api/1.0/attention/history"},
	{http.MethodPost, "/api/1.0/attention/item-1/answer"},
	{http.MethodPost, "/api/1.0/attention/item-1/escalate"},
	{http.MethodPost, "/api/1.0/attention/item-1/close"},
	{http.MethodGet, "/api/1.0/attention/item-1/attempt"},
	{http.MethodPost, "/api/1.0/attention/item-1/attempt"},
	{http.MethodGet, "/api/1.0/attention/item-1"},
}

var _ = Describe("second listener", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var httpHandler http.Handler
	var do func(method, path, authorization, body string) *httptest.ResponseRecorder

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
		// ⚠️ TTSURL empty so the read-aloud routes are NOT registered; the route
		// table this file pins is then the whole of what the second listener
		// serves.
		httpHandler = (&application{}).createAttentionStoreAPIHandler(store, secondListenerToken)

		// do runs one request through the listener's handler. authorization is
		// set on the request only when non-empty, so a caller can probe the
		// missing-header shape without accidentally sending an empty header.
		do = func(method, path, authorization, body string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(method, path, strings.NewReader(body))
			if authorization != "" {
				req.Header.Set("Authorization", authorization)
			}
			rec := httptest.NewRecorder()
			httpHandler.ServeHTTP(rec, req)
			return rec
		}
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	It("refuses every business route without a token", func() {
		for _, route := range businessRoutes {
			rec := do(route.method, route.path, "", "")
			Expect(rec.Code).To(Equal(http.StatusUnauthorized), "%s %s", route.method, route.path)
		}
	})

	It("admits every business route with the token", func() {
		// ⚠️ "not 401" rather than an exact status for most routes: the
		// middleware's job is to admit the request to the route, and each route's
		// own success and failure semantics are already pinned by its spec in
		// pkg/handler/. The negative-control property is what matters — a build
		// that refused everything would fail this spec, and a build that accepted
		// everything would fail the spec above.
		//
		// ⚠️ 405 is excluded as well as 401. A route probed with a method it does
		// not answer falls to mux's MethodNotAllowedHandler without the middleware
		// ever running, so a table that drifted from registerAttentionAPIRoutes
		// would leave this spec green while testing no gate at all — the exact
		// false pass the method column exists to prevent.
		for _, route := range businessRoutes {
			rec := do(route.method, route.path, "Bearer "+secondListenerToken, "")
			Expect(
				rec.Code,
			).NotTo(BeElementOf(http.StatusUnauthorized, http.StatusMethodNotAllowed),
				"%s %s", route.method, route.path)
		}
	})

	It("returns each body-less GET route's normal status with the token", func() {
		Expect(
			do(http.MethodGet, "/api/1.0/attention", "Bearer "+secondListenerToken, "").Code,
		).To(Equal(http.StatusOK))
		Expect(
			do(
				http.MethodGet,
				"/api/1.0/attention/history",
				"Bearer "+secondListenerToken,
				"",
			).Code,
		).To(Equal(http.StatusOK))
	})

	It("answers a pushed item over HTTP with the token", func() {
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

		rec := do(
			http.MethodPost,
			"/api/1.0/attention/"+item.ItemID.String()+"/answer",
			"Bearer "+secondListenerToken,
			`{"answered_by":"telegram","decision":"allow"}`,
		)
		Expect(rec.Code).To(Equal(http.StatusOK))
	})

	It("refuses a missing header, a malformed header and a wrong token identically", func() {
		missing := do(http.MethodGet, "/api/1.0/attention", "", "").Body.Bytes()
		malformed := do(http.MethodGet, "/api/1.0/attention", "Bearer", "").Body.Bytes()
		wrong := do(http.MethodGet, "/api/1.0/attention", "Bearer wrong-token", "").Body.Bytes()

		// ⚠️ The non-empty assertion matters: three empty bodies are trivially
		// equal, so without it this spec would pass against a handler that wrote
		// nothing at all.
		Expect(missing).NotTo(BeEmpty())
		Expect(missing).To(Equal(malformed))
		Expect(malformed).To(Equal(wrong))

		Expect(do(http.MethodGet, "/api/1.0/attention", "", "").Code).
			To(Equal(http.StatusUnauthorized))
		Expect(do(http.MethodGet, "/api/1.0/attention", "Bearer", "").Code).
			To(Equal(http.StatusUnauthorized))
		Expect(do(http.MethodGet, "/api/1.0/attention", "Bearer wrong-token", "").Code).
			To(Equal(http.StatusUnauthorized))

		// The token is a credential: no refusal may name it, nor even the field
		// it travels in.
		for _, body := range []string{string(missing), string(malformed), string(wrong)} {
			Expect(body).NotTo(ContainSubstring(secondListenerToken))
			Expect(body).NotTo(ContainSubstring("token"))
		}
	})

	It("serves the business API and nothing else", func() {
		// ⚠️ The authenticated probe is the one that carries the evidence:
		// unauthenticated, a 404 proves nothing about what is mounted, because a
		// listener that mounted the whole board router behind the middleware would
		// still answer 404 for an unregistered path. Authenticated, a mounted
		// board page would answer 200 and a mounted /metrics would answer 200, so
		// the 404 is what shows the route is absent.
		for _, path := range []string{"/", "/metrics", "/healthz", "/readiness", "/jump/item-1"} {
			Expect(do(http.MethodGet, path, "", "").Code).
				To(Equal(http.StatusNotFound), "unauthenticated GET %s", path)
			Expect(do(http.MethodGet, path, "Bearer "+secondListenerToken, "").Code).
				To(Equal(http.StatusNotFound), "authenticated GET %s", path)
		}
	})
})
