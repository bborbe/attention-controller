// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/boardmetrics"
	"github.com/bborbe/attention-controller/pkg/handler"
)

var _ = Describe("the card's corner band", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var sessionLivenessChecker *mocks.SessionLivenessChecker
	var provenance *mocks.ProvenanceResolver
	var httpHandler http.Handler
	var vaultDir string

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		// A real boltkv DB and a real store, not a fake of either: Read prunes
		// open items whose producer is not live, so a faked store would let the
		// page render fixtures that the production read path would have dropped.
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

		sessionLivenessChecker = &mocks.SessionLivenessChecker{}
		sessionLivenessChecker.IsLiveReturns(true)

		store = pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			sessionLivenessChecker,
			libtime.NewCurrentDateTime(),
			libtime.Duration(15*60*1e9),
		)

		// Left returning nil: the layout guard asserts on the served stylesheet,
		// which every card shape carries regardless of provenance resolution.
		provenance = &mocks.ProvenanceResolver{}

		vaultDir = filepath.Join(GinkgoT().TempDir(), "Personal")
		httpHandler = handler.NewAttentionPageHandler(
			store,
			provenance,
			false,
			vaultDir,
			testBuildIdentity,
			boardmetrics.NewMetrics(prometheus.NewRegistry()),
		)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	// get renders the page and returns the recorder.
	get := func(method string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/", nil)
		resp := httptest.NewRecorder()
		httpHandler.ServeHTTP(resp, req)
		return resp
	}

	// ⚠️ This is a served-stylesheet guard, not the behavioural proof. It reddens
	// if the reservation or a control's slot is removed from the served document,
	// but it cannot see a rendered box: the rendered half — the ask's box not
	// intersecting each control's box, the unchanged computed font-size and the
	// ask occupying more than one rendered line — is carried by the browser case
	// in the e2e suite.
	It(
		"reserves the band on the card's text blocks and leaves every corner control in its slot",
		func() {
			resp := get(http.MethodGet)
			Expect(resp.Code).To(Equal(http.StatusOK))
			body := resp.Body.String()

			// The band is stated in the served document, not only in a source comment:
			// html/template strips CSS comments before serving, so the declaration is
			// what carries the fact to the browser and to this assertion.
			Expect(body).To(ContainSubstring("--corner-band: 148px"))
			Expect(body).To(ContainSubstring("margin-right: calc(var(--corner-band) - 12px)"))

			// Each control keeps its exact slot. Counted rather than merely contained,
			// so a slot that drifted onto a second rule would not read as intact.
			Expect(strings.Count(body, "right: 12px")).To(Equal(1))
			Expect(strings.Count(body, "right: 48px")).To(Equal(1))
			Expect(strings.Count(body, "right: 84px")).To(Equal(1))
			Expect(strings.Count(body, "right: 120px")).To(Equal(1))
			Expect(strings.Count(body, "top: 10px")).To(Equal(4))
		},
	)
})
