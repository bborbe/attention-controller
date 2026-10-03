// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"

	"github.com/bborbe/errors"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/handler"
)

// legacyTokenSentinel is the token written to the temp file. Distinctive on
// purpose, so the "never rendered" assertion cannot pass because the token
// happens to resemble something else on the page.
const legacyTokenSentinel = "LEGACYTOKEN-DO-NOT-LEAK"

// The legacy pane-addressed route is the fold's compatibility surface: the
// manager layer's jump links point at it, and the Python server that used to
// answer them is retired. These specs hold the three controls that server
// carried — loopback Host, required constant-time token, live-pane validation —
// because each is load-bearing and each was the reason the route was safe to
// expose on loopback at all.
var _ = Describe("Legacy pane-addressed jump", func() {
	var ctx context.Context
	var activator *mocks.PaneActivator
	var tokenDir string
	var tokenPath string
	var legacyHandler http.Handler

	BeforeEach(func() {
		ctx = context.Background()
		activator = &mocks.PaneActivator{}

		tokenDir, err := os.MkdirTemp("", "attention-legacy-jump-token-*")
		Expect(err).To(BeNil())
		tokenPath = filepath.Join(tokenDir, "jump-token")
		Expect(os.WriteFile(tokenPath, []byte(legacyTokenSentinel+"\n"), 0o600)).To(BeNil())

		legacyHandler = handler.NewLegacyJumpHandler(
			pkg.NewJumpTokenReader(tokenPath),
			activator,
		)
	})

	AfterEach(func() {
		Expect(os.RemoveAll(tokenDir)).To(BeNil())
	})

	// request issues a GET against the route. host defaults to the loopback
	// form the Python server required; a spec that needs a rebinding-shaped
	// Host passes its own.
	request := func(target, host string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		if host != "" {
			req.Host = host
		}
		resp := httptest.NewRecorder()
		legacyHandler.ServeHTTP(resp, req)
		return resp
	}

	It("activates the pane and answers 200 when the token is valid", func() {
		resp := request("/jump?pane=1907&t="+legacyTokenSentinel, "127.0.0.1:1337")

		Expect(resp.Code).To(Equal(http.StatusOK))
		Expect(activator.ActivateCallCount()).To(Equal(1))
		_, pane := activator.ActivateArgsForCall(0)
		Expect(pane).To(Equal("1907"))
	})

	// ⚠️ The activation is what the manager layer's link is FOR. A 200 that did
	// not activate would be a page that reports success while nothing moved,
	// which is the exact failure the jump button's history is made of.
	It("never answers 200 without having activated", func() {
		activator.ActivateReturns(errors.New(ctx, "pane is not a live pane"))

		resp := request("/jump?pane=1907&t="+legacyTokenSentinel, "127.0.0.1:1337")

		Expect(resp.Code).NotTo(Equal(http.StatusOK))
		Expect(resp.Code).To(Equal(http.StatusBadGateway))
	})

	// The DNS-rebinding guard. A browser that resolves an attacker domain to
	// 127.0.0.1 still sends that domain in Host, so the request arrives at this
	// port claiming to be somewhere else.
	It("refuses a non-loopback Host header and does not activate", func() {
		resp := request("/jump?pane=1907&t="+legacyTokenSentinel, "evil.example.com")

		Expect(resp.Code).To(Equal(http.StatusForbidden))
		Expect(activator.ActivateCallCount()).To(Equal(0))
	})

	// ⚠️ Loopback is a FAMILY, not two strings. The first version of this guard
	// accepted only `127.0.0.1` and `localhost`, which refused every IPv6 form —
	// and `-jump-listen` takes any address, so a host on `[::1]:1337` would have
	// had every jump refused by its own guard. The mapped form is included
	// because a dual-stack listener reports a v4 peer that way.
	DescribeTable("accepts every loopback form",
		func(host string) {
			resp := request("/jump?pane=1907&t="+legacyTokenSentinel, host)

			Expect(resp.Code).To(Equal(http.StatusOK))
			Expect(activator.ActivateCallCount()).To(Equal(1))
		},
		Entry("IPv4 with port", "127.0.0.1:1337"),
		Entry("IPv4 bare", "127.0.0.1"),
		Entry("another 127/8 address", "127.0.0.2:1337"),
		Entry("localhost with port", "localhost:1337"),
		Entry("localhost bare", "localhost"),
		Entry("IPv6 with port", "[::1]:1337"),
		Entry("IPv6 bare, bracketed", "[::1]"),
		Entry("IPv4-mapped IPv6", "[::ffff:127.0.0.1]:1337"),
	)

	// The guard must still refuse a non-loopback address in either family — the
	// table above widens what is accepted, and these are what stop the widening
	// from becoming a hole.
	DescribeTable("refuses a non-loopback Host in either family",
		func(host string) {
			resp := request("/jump?pane=1907&t="+legacyTokenSentinel, host)

			Expect(resp.Code).To(Equal(http.StatusForbidden))
			Expect(activator.ActivateCallCount()).To(Equal(0))
		},
		Entry("a public IPv4", "93.184.216.34:1337"),
		Entry("a public IPv6", "[2001:db8::1]:1337"),
		Entry("a domain that merely starts with 127", "127.0.0.1.evil.example.com"),
		Entry("a bracketed non-loopback", "[::2]"),
	)

	DescribeTable("refuses a request without a matching token",
		func(target string) {
			resp := request(target, "127.0.0.1:1337")

			Expect(resp.Code).To(Equal(http.StatusForbidden))
			Expect(activator.ActivateCallCount()).To(Equal(0))
		},
		Entry("no token at all", "/jump?pane=1907"),
		Entry("an empty token", "/jump?pane=1907&t="),
		Entry("a wrong token", "/jump?pane=1907&t=not-the-token"),
		Entry("a token that is a prefix of the real one", "/jump?pane=1907&t=LEGACYTOKEN"),
	)

	// ⚠️ Fail closed. A server that cannot read its own token must refuse every
	// request rather than admit them — the alternative is an unauthenticated
	// local action endpoint, which is the pattern the token exists to avoid.
	It("refuses every request when the token file cannot be read", func() {
		legacyHandler = handler.NewLegacyJumpHandler(
			pkg.NewJumpTokenReader(filepath.Join(tokenDir, "does-not-exist")),
			activator,
		)

		resp := request("/jump?pane=1907&t="+legacyTokenSentinel, "127.0.0.1:1337")

		Expect(resp.Code).To(Equal(http.StatusForbidden))
		Expect(activator.ActivateCallCount()).To(Equal(0))
	})

	It("refuses a non-integer pane and does not activate", func() {
		resp := request("/jump?pane=abc&t="+legacyTokenSentinel, "127.0.0.1:1337")

		Expect(resp.Code).To(Equal(http.StatusBadRequest))
		Expect(resp.Body.String()).To(ContainSubstring(`<h1 class="warn">Bad request</h1>`))
		Expect(activator.ActivateCallCount()).To(Equal(0))
	})

	// The route's path is part of its contract, not an accident of wiring: it
	// answers /jump and nothing else.
	It("answers 404 for a path other than /jump", func() {
		resp := request("/other?pane=1907&t="+legacyTokenSentinel, "127.0.0.1:1337")

		Expect(resp.Code).To(Equal(http.StatusNotFound))
		Expect(resp.Body.String()).To(ContainSubstring(`<h1 class="warn">Not found</h1>`))
		Expect(activator.ActivateCallCount()).To(Equal(0))
	})

	// The token is a credential, and this route is the one that still reads it.
	// A page is the only place it could land in a response.
	It("never renders the token into the response", func() {
		resp := request("/jump?pane=1907&t="+legacyTokenSentinel, "127.0.0.1:1337")

		// Positive control first: a page that failed to render must not be able
		// to satisfy the leak assertion below.
		Expect(resp.Code).To(Equal(http.StatusOK))
		Expect(resp.Body.String()).To(ContainSubstring("1907"))
		Expect(resp.Body.String()).NotTo(ContainSubstring(legacyTokenSentinel))
	})

	// The page is the house-style page of the local services, not bare markup:
	// a tone-coloured heading, and on success a card naming the pane.
	It("renders the styled page with an ok heading and a pane card", func() {
		resp := request("/jump?pane=1907&t="+legacyTokenSentinel, "127.0.0.1:1337")

		Expect(resp.Code).To(Equal(http.StatusOK))
		body := resp.Body.String()
		Expect(body).To(ContainSubstring("<style>"))
		Expect(body).To(ContainSubstring(`<h1 class="ok">Jumped</h1>`))
		Expect(body).To(ContainSubstring(`<p class="value mono">1907</p>`))
	})

	It("renders a refusal with an err heading and no pane card", func() {
		resp := request("/jump?pane=1907&t=wrong", "127.0.0.1:1337")

		Expect(resp.Code).To(Equal(http.StatusForbidden))
		body := resp.Body.String()
		Expect(body).To(ContainSubstring(`<h1 class="err">Forbidden</h1>`))
		Expect(body).NotTo(ContainSubstring(`class="card"`))
	})
})

// The legacy listener's liveness probe. It answers the plain-text contract of
// the Python server it replaces, not the board's JSON `/healthz` — a consumer
// probing the port it has always probed must keep seeing what it saw.
var _ = Describe("Legacy jump listener health", func() {
	It("answers 200 with a plain-text ok", func() {
		resp := httptest.NewRecorder()
		handler.NewLegacyHealthHandler().
			ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/health", nil))

		Expect(resp.Code).To(Equal(http.StatusOK))
		Expect(resp.Body.String()).To(Equal("ok"))
		Expect(resp.Header().Get("Content-Type")).To(ContainSubstring("text/plain"))
	})
})
