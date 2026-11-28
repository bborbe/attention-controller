// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"net/http"
	"net/http/httptest"
	"testing/fstest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/boardmetrics"
	"github.com/bborbe/attention-controller/pkg/buildidentity"
	"github.com/bborbe/attention-controller/pkg/handler"
)

var _ = Describe("AttentionReactPageHandler", func() {
	var dist fstest.MapFS
	var metrics pkg.Metrics
	var identity buildidentity.Identity

	BeforeEach(func() {
		dist = fstest.MapFS{
			"index.html": &fstest.MapFile{Data: []byte(
				`<!doctype html><html><body><div id="root"></div>` +
					`<div id="build-identity"></div></body></html>`,
			)},
			"assets/app.js": &fstest.MapFile{Data: []byte(`console.log("hi")`)},
		}
		metrics = boardmetrics.NewMetrics(prometheus.NewRegistry())
		identity = buildidentity.Identity{
			Version:    "v9.9.9-test",
			Commit:     "1a2b3c4d5e6f",
			CommitTime: "2026-01-02T03:04:05Z",
		}
	})

	get := func(h http.Handler, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	It("serves the React index with the build-identity footer injected", func() {
		h := handler.NewAttentionReactPageHandler(dist, identity, metrics)
		rec := get(h, "/")
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Header().Get("Content-Type")).To(ContainSubstring("text/html"))
		body := rec.Body.String()
		// The React root the old vanilla page did not have.
		Expect(body).To(ContainSubstring(`<div id="root">`))
		// The deploy gate's probe: commit <span class="bi-value">SHA</span>.
		Expect(body).To(ContainSubstring(`commit <span class="bi-value">1a2b3c4d5e6f</span>`))
		// The marker itself must be gone, replaced by the footer.
		Expect(body).NotTo(ContainSubstring(`id="build-identity"`))
	})

	It("serves a real asset with an immutable cache header", func() {
		h := handler.NewAttentionReactPageHandler(dist, identity, metrics)
		rec := get(h, "/assets/app.js")
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.String()).To(Equal(`console.log("hi")`))
		Expect(rec.Header().Get("Cache-Control")).To(ContainSubstring("immutable"))
	})

	It("falls back to index.html for an unknown client-side route", func() {
		h := handler.NewAttentionReactPageHandler(dist, identity, metrics)
		rec := get(h, "/some/client/route")
		Expect(rec.Code).To(Equal(http.StatusOK))
		Expect(rec.Body.String()).To(ContainSubstring(`<div id="root">`))
	})

	It("answers 503 when the frontend was not built", func() {
		h := handler.NewAttentionReactPageHandler(fstest.MapFS{}, identity, metrics)
		rec := get(h, "/")
		Expect(rec.Code).To(Equal(http.StatusServiceUnavailable))
	})

	It("renders the no-build-identity footer when the binary is unstamped", func() {
		h := handler.NewAttentionReactPageHandler(dist, buildidentity.Identity{}, metrics)
		rec := get(h, "/")
		Expect(rec.Code).To(Equal(http.StatusOK))
		body := rec.Body.String()
		Expect(body).To(ContainSubstring(`bi-absent`))
		Expect(body).NotTo(ContainSubstring(`bi-value`))
	})
})
