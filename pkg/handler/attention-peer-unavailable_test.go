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

// ⚠️ This file is the regression guard for the masking this service used to do.
//
// A federated call that failed reached the client as 409 VALIDATION_ERROR, so a
// caller could not tell a missing item from a broken peer — and 409 tells it NOT
// to retry, which is the one instruction that is wrong for an outage.
//
// The chain is exercised whole — a real boltkv store, the real federating
// decorator, and a real HTTP peer — because the defect lived in the seam between
// them. A fake at either end would assert the call shape instead of the status
// the client actually receives, and the status is the whole of what was wrong.
var _ = Describe("a federated peer failure reaching the client", func() {
	var ctx context.Context
	var db libkv.DB

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	// federatingOver wires a real local store to a real HTTP peer that answers
	// `status` to every route.
	//
	// ⚠️ The local store is left EMPTY on purpose. isRemote is "the local store
	// does not hold it", so every id below is remote by the decorator's own rule
	// — which is the branch the peer call is reached from, and the branch the
	// masking lived on.
	federatingOver := func(status int, body string) pkg.AttentionStore {
		peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
		DeferCleanup(peer.Close)

		sessionLivenessChecker := &mocks.SessionLivenessChecker{}
		sessionLivenessChecker.IsLiveReturns(true)

		local := pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			sessionLivenessChecker,
			libtime.NewCurrentDateTime(),
			libtime.Duration(15*60*1e9),
		)
		return pkg.NewFederatingAttentionStore(
			local,
			pkg.NewHTTPRemoteAttentionStore(peer.URL, "peer-token"),
		)
	}

	// itemID is an id the local store does not hold, so the decorator proxies it.
	itemID := pkg.ItemID("0000000000000000000000000000dead")

	// call drives one route. The path var is set with SetURLVars rather than by
	// mounting a router, matching the sibling specs: the handler reads it through
	// mux.Vars, and a router would add a second thing to get wrong without
	// testing anything the handler owns.
	call := func(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = mux.SetURLVars(req, map[string]string{"itemID": itemID.String()})
		resp := httptest.NewRecorder()
		h.ServeHTTP(resp, req)
		return resp
	}

	// ⚠️ Every spec asserts the status, the CODE, and the absence of the old
	// code. The code is what a client dispatches on, so a correct status still
	// carrying VALIDATION_ERROR would be the same defect wearing the right
	// number.
	ExpectPeerUnavailable := func(resp *httptest.ResponseRecorder) {
		Expect(resp.Code).To(Equal(http.StatusServiceUnavailable))
		Expect(resp.Body.String()).To(ContainSubstring(pkg.ErrorCodePeerUnavailable))
		Expect(resp.Body.String()).NotTo(ContainSubstring("VALIDATION_ERROR"))
	}

	Context("when the peer answers 503", func() {
		const unavailableBody = `{"error":{"code":"","message":"peer is down"}}`

		It("answers a failed peer READ as 503, not as a validation failure", func() {
			h := handler.NewAttentionGetHandler(
				federatingOver(http.StatusServiceUnavailable, unavailableBody),
			)

			resp := call(h, http.MethodGet, "/api/1.0/attention/"+itemID.String(), "")

			ExpectPeerUnavailable(resp)
		})

		It("answers a failed peer ANSWER as 503, not as a validation failure", func() {
			h := handler.NewAttentionAnswerHandler(
				federatingOver(http.StatusServiceUnavailable, unavailableBody),
			)

			resp := call(
				h,
				http.MethodPost,
				"/api/1.0/attention/"+itemID.String()+"/answer",
				`{"decision":"allow"}`,
			)

			ExpectPeerUnavailable(resp)
		})

		It("answers a failed peer ESCALATION as 503, not as a validation failure", func() {
			h := handler.NewAttentionEscalateHandler(
				federatingOver(http.StatusServiceUnavailable, unavailableBody),
			)

			resp := call(
				h,
				http.MethodPost,
				"/api/1.0/attention/"+itemID.String()+"/escalate",
				`{"escalated_by":"session-a"}`,
			)

			ExpectPeerUnavailable(resp)
		})
	})

	It("answers a rejected peer token as 503 too, and never as 404", func() {
		// ⚠️ A rejected token is a different OPERATOR problem from an unreachable
		// peer, but the same CLIENT answer: nothing the caller can correct, and
		// not evidence that the item is absent. It must not become a 404 — that
		// would answer a question the read never settled.
		h := handler.NewAttentionGetHandler(federatingOver(
			http.StatusUnauthorized,
			`{"error":{"code":"UNAUTHORIZED","message":"bad token"}}`,
		))

		resp := call(h, http.MethodGet, "/api/1.0/attention/"+itemID.String(), "")

		ExpectPeerUnavailable(resp)
	})

	It("still answers a healthy peer's 404 as 404, so the two are not collapsed", func() {
		// The counterpart guard: this is the reading observed live on the running
		// store (leg 2 of the task's two-leg probe), and it is what the new 503
		// branch must NOT swallow. A peer that answers for an id it does not hold
		// is a settled absence, which is a different answer from an outage.
		h := handler.NewAttentionGetHandler(federatingOver(
			http.StatusNotFound,
			`{"error":{"code":"NOT_FOUND","message":"no such item"}}`,
		))

		resp := call(h, http.MethodGet, "/api/1.0/attention/"+itemID.String(), "")

		Expect(resp.Code).To(Equal(http.StatusNotFound))
		Expect(resp.Body.String()).To(ContainSubstring("NOT_FOUND"))
		Expect(resp.Body.String()).NotTo(ContainSubstring(pkg.ErrorCodePeerUnavailable))
	})
})
