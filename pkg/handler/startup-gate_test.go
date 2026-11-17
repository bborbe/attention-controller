// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg/handler"
)

var _ = Describe("StartupGate", func() {
	var (
		gate     handler.StartupGate
		liveness http.Handler
		ready    http.Handler
	)

	serve := func(method, path string) *httptest.ResponseRecorder {
		resp := httptest.NewRecorder()
		gate.ServeHTTP(resp, httptest.NewRequest(method, path, nil))
		return resp
	}

	BeforeEach(func() {
		liveness = handler.NewHealthzHandler()
		ready = http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
			_, _ = resp.Write([]byte("ready:" + req.URL.Path))
		})
		gate = handler.NewStartupGate(map[string]http.Handler{"/healthz": liveness})
	})

	Context("while starting", func() {
		BeforeEach(func() {
			gate.Step("open-db")
		})

		It("reports not ready with the pending step", func() {
			isReady, step := gate.Status()
			Expect(isReady).To(BeFalse())
			Expect(step).To(Equal("open-db"))
		})

		It("renders a starting page on GET / naming the pending step", func() {
			resp := serve(http.MethodGet, "/")
			Expect(resp.Code).To(Equal(http.StatusServiceUnavailable))
			Expect(resp.Header().Get("Content-Type")).To(ContainSubstring("text/html"))
			Expect(resp.Header().Get("Retry-After")).To(Equal("2"))
			Expect(resp.Body.String()).To(ContainSubstring("is starting"))
			Expect(resp.Body.String()).To(ContainSubstring("<code>open-db</code>"))
			Expect(resp.Body.String()).To(ContainSubstring(`http-equiv="refresh" content="2"`))
		})

		It("answers an API route with 503 starting JSON", func() {
			resp := serve(http.MethodGet, "/api/1.0/attention")
			Expect(resp.Code).To(Equal(http.StatusServiceUnavailable))
			Expect(resp.Header().Get("Content-Type")).To(Equal("application/json"))
			var body map[string]string
			Expect(json.Unmarshal(resp.Body.Bytes(), &body)).To(Succeed())
			Expect(body).To(Equal(map[string]string{"status": "starting", "step": "open-db"}))
		})

		It("answers a POST to / with JSON, not the page", func() {
			resp := serve(http.MethodPost, "/")
			Expect(resp.Code).To(Equal(http.StatusServiceUnavailable))
			Expect(resp.Header().Get("Content-Type")).To(Equal("application/json"))
		})

		It("answers the readiness probe with 503", func() {
			resp := serve(http.MethodGet, "/readiness")
			Expect(resp.Code).To(Equal(http.StatusServiceUnavailable))
		})

		It("serves the liveness probe with 200", func() {
			resp := serve(http.MethodGet, "/healthz")
			Expect(resp.Code).To(Equal(http.StatusOK))
			Expect(resp.Body.String()).To(Equal(`{"status":"ok"}`))
		})

		It("names the newest step after a transition", func() {
			gate.Step("build-task-index")
			resp := serve(http.MethodGet, "/api/1.0/attention")
			Expect(resp.Body.String()).To(ContainSubstring(`"step":"build-task-index"`))
		})
	})

	Context("once opened", func() {
		BeforeEach(func() {
			gate.Step("start-jobs")
			gate.Open(ready)
		})

		It("reports ready", func() {
			isReady, _ := gate.Status()
			Expect(isReady).To(BeTrue())
		})

		It("routes every request to the real handler", func() {
			for _, path := range []string{"/", "/api/1.0/attention", "/readiness", "/healthz"} {
				resp := serve(http.MethodGet, path)
				Expect(resp.Code).To(Equal(http.StatusOK), path)
				Expect(resp.Body.String()).To(Equal("ready:"+path), path)
			}
		})

		It("returns to starting JSON after Close", func() {
			gate.Close("shutting-down")
			resp := serve(http.MethodGet, "/api/1.0/attention")
			Expect(resp.Code).To(Equal(http.StatusServiceUnavailable))
			Expect(resp.Body.String()).To(ContainSubstring(`"step":"shutting-down"`))
		})
	})
})
