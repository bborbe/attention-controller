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

// The board's producer-kind filter, observed end to end through the real page
// handler and the real store.
//
// ⚠️ The fixture carries a card of EACH kind on purpose. The live store holds
// no `manager`-kind card at all — no producer can emit one — so a test built
// from the live population would pass on a handler that ignores the parameter
// entirely. Both sides of every assertion here come from cards this suite
// pushed, which is what makes an ignored parameter fail rather than pass.
var _ = Describe("the board's producer-kind filter", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var sessionLivenessChecker *mocks.SessionLivenessChecker
	var provenance *mocks.ProvenanceResolver
	var httpHandler http.Handler

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

		// Left at its default the checker reports not-live and Read prunes every
		// fixture, which would make every absence assertion pass vacuously.
		sessionLivenessChecker = &mocks.SessionLivenessChecker{}
		sessionLivenessChecker.IsLiveReturns(true)

		store = pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			sessionLivenessChecker,
			libtime.NewCurrentDateTime(),
			libtime.Duration(15*60*1e9),
		)

		provenance = &mocks.ProvenanceResolver{}
		provenance.ResolveReturns(map[pkg.ItemID]pkg.Provenance{})

		httpHandler = handler.NewAttentionPageHandler(
			store,
			provenance,
			false,
			filepath.Join(GinkgoT().TempDir(), "Personal"),
			testBuildIdentity,
			boardmetrics.NewMetrics(prometheus.NewRegistry()),
		)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	// pushCard pushes one declaration of the given kind and returns the item's
	// id, which the assertions look for in the served markup.
	pushCard := func(kind pkg.ProducerKind, producerID string) pkg.ItemID {
		request := pkg.PushRequest{
			ProducerID:      pkg.ProducerID(producerID),
			ProducerKind:    kind,
			LivenessRef:     pkg.LivenessRef("session:" + producerID),
			DedupKey:        pkg.DedupKey("dedup-" + producerID),
			InterruptClass:  "approve",
			Payload:         pkg.Payload("payload-" + producerID),
			AnswerMechanism: pkg.MessageAnswerMechanism,
		}
		item, err := store.Push(ctx, request)
		Expect(err).To(BeNil())
		return item.ItemID
	}

	// get renders the page for one request target and returns the recorder.
	get := func(target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		resp := httptest.NewRecorder()
		httpHandler.ServeHTTP(resp, req)
		return resp
	}

	Context("with one manager-kind and one session-kind card open", func() {
		var managerID, sessionID pkg.ItemID

		BeforeEach(func() {
			managerID = pushCard(pkg.ManagerProducerKind, "manager-producer")
			sessionID = pushCard(pkg.SessionProducerKind, "session-producer")
		})

		It("shows only the manager-kind card when no parameter is given", func() {
			resp := get("/")

			Expect(resp.Code).To(Equal(http.StatusOK))
			body := resp.Body.String()
			Expect(body).To(ContainSubstring(managerID.String()))
			Expect(body).NotTo(ContainSubstring(sessionID.String()))
		})

		It("names how many open cards the default filter is hiding", func() {
			body := get("/").Body.String()

			// One card is hidden, so the switch has to say so rather than leaving
			// the operator to read an empty-looking board as an empty queue.
			Expect(body).To(ContainSubstring("1 hidden"))
		})

		It("shows only the session-kind card for producer_kind=session", func() {
			body := get("/?producer_kind=session").Body.String()

			Expect(body).To(ContainSubstring(sessionID.String()))
			Expect(body).NotTo(ContainSubstring(managerID.String()))
		})

		It("shows both cards for producer_kind=all", func() {
			body := get("/?producer_kind=all").Body.String()

			Expect(body).To(ContainSubstring(managerID.String()))
			Expect(body).To(ContainSubstring(sessionID.String()))
			// Scoped to the marker rather than to the word "hidden": the page
			// carries `hidden` attributes on controls of its own, so a bare
			// substring would fail on a correct page.
			Expect(body).NotTo(ContainSubstring("data-kind-hidden"))
		})

		It("keeps hide=none on the switch's own link", func() {
			body := get("/?hide=none").Body.String()

			// Two independent default-on view filters must not reset each other:
			// the kind switch has to carry the hide state it was rendered under.
			Expect(body).To(ContainSubstring("hide=none"))
		})
	})

	It("rejects an unknown producer kind instead of silently ignoring it", func() {
		resp := get("/?producer_kind=bogus")

		Expect(resp.Code).To(Equal(http.StatusBadRequest))
		Expect(resp.Body.String()).To(ContainSubstring("producerKind"))
	})

	It("defaults to the manager kind when the parameter is absent", func() {
		sessionID := pushCard(pkg.SessionProducerKind, "session-only-producer")

		body := get("/").Body.String()

		Expect(body).NotTo(ContainSubstring(sessionID.String()))
		Expect(strings.TrimSpace(body)).NotTo(BeEmpty())
	})
})
