// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package deploy_test

import (
	"context"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg/deploy"
)

var _ = Describe("HTTPProber", func() {
	var (
		prober     *deploy.HTTPProber
		server     *httptest.Server
		readiness  string
		healthCode int
		footer     string
	)

	BeforeEach(func() {
		readiness = "OK"
		healthCode = http.StatusOK
		// Byte-for-byte the live footer's shape: the probe's regexp requires
		// `commit ` immediately before the span, which is what the board emits.
		footer = `commit <span class="bi-value">091b67b59bf5</span>`
		mux := http.NewServeMux()
		mux.HandleFunc("/readiness", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(readiness))
		})
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(healthCode)
		})
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("<html>" + footer + "</html>"))
		})
		server = httptest.NewServer(mux)
		prober = &deploy.HTTPProber{
			ReadinessURL: server.URL + "/readiness",
			HealthURL:    server.URL + "/healthz",
			BoardURL:     server.URL + "/",
		}
	})

	AfterEach(func() {
		server.Close()
	})

	It("is ready when the board answers and serves the expected commit", func() {
		result := prober.Probe(context.Background(), "091b67b59bf5")

		Expect(result.Ready).To(BeTrue())
		Expect(result.ServingCommit).To(Equal("091b67b59bf5"))
	})

	It("is not ready when healthz does not answer 200", func() {
		healthCode = http.StatusNotFound

		result := prober.Probe(context.Background(), "091b67b59bf5")

		Expect(result.Ready).To(BeFalse())
		Expect(result.Detail).To(ContainSubstring("healthz"))
		Expect(result.Detail).To(ContainSubstring("404"))
	})

	It("is not ready when readiness answers something other than OK", func() {
		readiness = "starting"

		result := prober.Probe(context.Background(), "091b67b59bf5")

		Expect(result.Ready).To(BeFalse())
		Expect(result.Detail).To(ContainSubstring("readiness"))
	})

	// The stale-process guard: the board answers and serves, but the process
	// answering is not the build that was installed.
	It("is not ready when the serving footer names another commit", func() {
		result := prober.Probe(context.Background(), "baddc0de1234")

		Expect(result.Ready).To(BeFalse())
		Expect(result.ServingCommit).To(Equal("091b67b59bf5"))
		Expect(result.Detail).To(ContainSubstring("expected baddc0de1234"))
	})

	It("is not ready when the footer carries no commit at all", func() {
		footer = `<span class="bi-label">commit</span> <span class="bi-value">no build identity</span>`

		result := prober.Probe(context.Background(), "091b67b59bf5")

		Expect(result.Ready).To(BeFalse())
		Expect(result.Detail).To(ContainSubstring("no commit"))
	})
})
