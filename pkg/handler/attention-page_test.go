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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/handler"
)

var _ = Describe("AttentionPageHandler", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var sessionLivenessChecker *mocks.SessionLivenessChecker
	var httpHandler http.Handler

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		// A real boltkv DB and a real store, not a fake of either: Read prunes
		// open items whose producer is not live, so a faked store would let the
		// page render fixtures that the production read path would have dropped.
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

		// The mock defaults to false. Left at its default, every fixture item is
		// pruned during Read and the page renders empty — the empty-store case
		// would pass vacuously and the positive assertions would fail. Pinning it
		// to true is what makes the pruning real rather than incidental.
		sessionLivenessChecker = &mocks.SessionLivenessChecker{}
		sessionLivenessChecker.IsLiveReturns(true)

		store = pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			sessionLivenessChecker,
			libtime.NewCurrentDateTime(),
			libtime.Duration(15*60*1e9),
		)

		httpHandler = handler.NewAttentionPageHandler(store)
	})

	AfterEach(func() {
		// bbolt's Close is idempotent, so the read-failure case closing the db
		// first does not turn this into a second-close failure.
		Expect(db.Close()).To(BeNil())
	})

	// pushRequest builds a declaration whose liveness model matches its producer
	// id and whose answer mechanism makes it a question rather than a report, so
	// the pruning Read performs is exercised for every fixture.
	pushRequest := func(producerID pkg.ProducerID, dedupKey pkg.DedupKey, payload pkg.Payload) pkg.PushRequest {
		return pkg.PushRequest{
			ProducerID:      producerID,
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:" + producerID.String()),
			DedupKey:        dedupKey,
			InterruptClass:  "approve",
			Payload:         payload,
			AnswerMechanism: pkg.MessageAnswerMechanism,
		}
	}

	// get renders the page and returns the recorder.
	get := func(method string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/", nil)
		resp := httptest.NewRecorder()
		httpHandler.ServeHTTP(resp, req)
		return resp
	}

	It("renders one row per open item, carrying every rendered field", func() {
		first, err := store.Push(ctx, pushRequest("producer-a", "gate-a", "deploy prod?"))
		Expect(err).To(BeNil())
		second, err := store.Push(ctx, pushRequest("producer-b", "gate-b", "approve the migration"))
		Expect(err).To(BeNil())

		resp := get("GET")
		Expect(resp.Code).To(Equal(http.StatusOK))
		Expect(resp.Header().Get("Content-Type")).To(Equal("text/html; charset=utf-8"))

		body := resp.Body.String()
		// Gomega has no count matcher, so the row count is counted directly.
		Expect(strings.Count(body, `data-item-id="`)).To(Equal(2))

		for _, item := range []*pkg.Item{first, second} {
			Expect(body).To(ContainSubstring(item.ProducerID.String()))
			Expect(body).To(ContainSubstring(item.ProducerKind.String()))
			Expect(body).To(ContainSubstring(item.Payload.String()))
			Expect(body).To(ContainSubstring(item.State.String()))
			Expect(body).To(ContainSubstring(item.CreatedAt.String()))
			Expect(body).To(ContainSubstring(`data-item-id="` + item.ItemID.String() + `"`))
		}
	})

	It("renders an empty page for an empty store", func() {
		resp := get("GET")

		Expect(resp.Code).To(Equal(http.StatusOK))
		Expect(strings.Count(resp.Body.String(), "data-item-id=")).To(Equal(0))
	})

	It("escapes producer-supplied text instead of emitting it raw", func() {
		// ProducerID is a free string validated only by NotEmptyString, so this is
		// the exact value the html/template boundary exists to neutralise.
		_, err := store.Push(
			ctx,
			pushRequest(`"><script>alert(1)</script>`, "gate-escape", "escaping fixture"),
		)
		Expect(err).To(BeNil())

		body := get("GET").Body.String()

		Expect(body).NotTo(ContainSubstring("<script>"))
		Expect(body).To(ContainSubstring("&lt;script&gt;"))
	})

	It("omits closed items while keeping open ones", func() {
		open, err := store.Push(ctx, pushRequest("producer-open", "gate-open", "still open"))
		Expect(err).To(BeNil())
		closed, err := store.Push(
			ctx,
			pushRequest("producer-closed", "gate-closed", "already done"),
		)
		Expect(err).To(BeNil())
		_, err = store.Close(ctx, closed.ItemID)
		Expect(err).To(BeNil())

		body := get("GET").Body.String()

		Expect(body).To(ContainSubstring(open.ProducerID.String()))
		Expect(body).NotTo(ContainSubstring(closed.ProducerID.String()))
	})

	It("offers no way to answer, close or change anything", func() {
		_, err := store.Push(ctx, pushRequest("producer-readonly", "gate-readonly", "read me"))
		Expect(err).To(BeNil())

		// A non-empty page first, so the absence assertions below are made against
		// a rendered document rather than against an empty body.
		resp := get("GET")
		Expect(resp.Body.String()).NotTo(BeEmpty())

		body := strings.ToLower(resp.Body.String())
		Expect(body).NotTo(ContainSubstring("<form"))
		Expect(body).NotTo(ContainSubstring("<script"))
		Expect(body).NotTo(ContainSubstring(`method="post"`))

		// HEAD is routed to this handler too; it is read-only and a link checker
		// or browser may issue it, so it is asserted rather than merely declared.
		Expect(get("HEAD").Code).To(Equal(http.StatusOK))
	})

	It("returns the standard JSON error body when the read fails", func() {
		Expect(db.Close()).To(BeNil())

		resp := get("GET")

		Expect(resp.Code).To(Equal(http.StatusInternalServerError))

		// Decoded rather than substring-matched, so a body that merely resembles
		// the standard error shape does not pass. Only code and message are
		// asserted: details is omitempty and this error carries no data.
		var errorResponse libhttp.ErrorResponse
		Expect(json.NewDecoder(resp.Body).Decode(&errorResponse)).To(BeNil())
		Expect(errorResponse.Error.Code).To(Equal(libhttp.ErrorCodeInternal))
		Expect(errorResponse.Error.Message).To(ContainSubstring("read failed"))
	})
})
