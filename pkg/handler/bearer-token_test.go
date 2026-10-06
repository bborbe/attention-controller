// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg/handler"
)

var _ = Describe("NewBearerTokenHandler", func() {
	var httpHandler http.Handler
	var called bool

	BeforeEach(func() {
		token := "s3cret-token"
		called = false
		// next writes a distinctive status rather than 200, so a spec cannot
		// pass because a zero-value recorder happens to read 200.
		next := http.HandlerFunc(func(resp http.ResponseWriter, _ *http.Request) {
			called = true
			resp.WriteHeader(http.StatusTeapot)
		})
		httpHandler = handler.NewBearerTokenHandler(next, token)
	})

	// do runs one request through the handler. Every request is a real
	// httptest request through the real handler — the middleware has no
	// collaborators beyond next.
	do := func(authorization string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/1.0/attention", nil)
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		rec := httptest.NewRecorder()
		httpHandler.ServeHTTP(rec, req)
		return rec
	}

	It("admits a request carrying the correct token, untouched", func() {
		rec := do("Bearer s3cret-token")

		Expect(rec.Code).To(Equal(http.StatusTeapot))
		Expect(called).To(BeTrue())
	})

	It("refuses a request with no Authorization header", func() {
		rec := do("")

		Expect(rec.Code).To(Equal(http.StatusUnauthorized))
		Expect(called).To(BeFalse())
	})

	It("refuses the scheme with no value", func() {
		rec := do("Bearer")

		Expect(rec.Code).To(Equal(http.StatusUnauthorized))
		Expect(called).To(BeFalse())
	})

	It("refuses the scheme with an empty value", func() {
		rec := do("Bearer ")

		Expect(rec.Code).To(Equal(http.StatusUnauthorized))
		Expect(called).To(BeFalse())
	})

	It("refuses a different scheme", func() {
		rec := do("Basic dXNlcjpwYXNz")

		Expect(rec.Code).To(Equal(http.StatusUnauthorized))
		Expect(called).To(BeFalse())
	})

	// The negative control for the prefix check: the correct token without the
	// scheme must NOT be admitted.
	It("refuses a bare token with no scheme", func() {
		rec := do("s3cret-token")

		Expect(rec.Code).To(Equal(http.StatusUnauthorized))
		Expect(called).To(BeFalse())
	})

	It("refuses a wrong token that is correctly shaped", func() {
		rec := do("Bearer wrong-token")

		Expect(rec.Code).To(Equal(http.StatusUnauthorized))
		Expect(called).To(BeFalse())
	})

	// A same-length wrong value is what a length-checking implementation would
	// wrongly admit.
	It("refuses a wrong token of the same length", func() {
		rec := do("Bearer s3cret-tokeX")

		Expect(rec.Code).To(Equal(http.StatusUnauthorized))
		Expect(called).To(BeFalse())
	})

	// The assertion that makes the endpoint not an oracle: the three refusal
	// shapes must be byte-identical.
	It("refuses the three shapes with byte-identical bodies", func() {
		missing := do("").Body.String()
		malformed := do("Bearer").Body.String()
		wrong := do("Bearer wrong-token").Body.String()

		Expect(missing).To(Equal(malformed))
		Expect(malformed).To(Equal(wrong))
	})

	It("names no detail about the expected value in the refusal", func() {
		body := do("Bearer wrong-token").Body.String()

		Expect(body).NotTo(ContainSubstring("s3cret-token"))
		Expect(body).NotTo(ContainSubstring("s3cret"))
	})

	// The empty-configured-token guard, which nothing else reaches: without it
	// the comparison would treat an empty presented value as a match and admit
	// every caller.
	It("fails closed when built with an empty token", func() {
		emptyCalled := false
		next := http.HandlerFunc(func(resp http.ResponseWriter, _ *http.Request) {
			emptyCalled = true
			resp.WriteHeader(http.StatusTeapot)
		})
		emptyHandler := handler.NewBearerTokenHandler(next, "")

		for _, authorization := range []string{"Bearer ", "Bearer x", "Bearer s3cret-token"} {
			req := httptest.NewRequest(http.MethodGet, "/api/1.0/attention", nil)
			req.Header.Set("Authorization", authorization)
			rec := httptest.NewRecorder()
			emptyHandler.ServeHTTP(rec, req)

			Expect(rec.Code).To(Equal(http.StatusUnauthorized))
			Expect(emptyCalled).To(BeFalse())
		}
	})
})
