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
			// Scoped to the rendered element rather than to the marker: the script
			// now carries `data-kind-hidden` itself (it creates and removes the
			// span), so the bare attribute name appears on every correct page.
			Expect(body).NotTo(ContainSubstring(`<span class="kind-hidden" data-kind-hidden>`))
		})

		It("keeps hide=none on the switch's own link, and points it at all kinds", func() {
			body := get("/?hide=none").Body.String()

			// Two independent default-on view filters must not reset each other:
			// the kind link has to carry the hide state it was rendered under,
			// and the managers-only view's link has to offer the all-kinds view.
			Expect(body).To(ContainSubstring("hide=none"))
			Expect(body).To(ContainSubstring("producer_kind=all"))
		})

		It("points the all-kinds view's link back at managers only, keeping hide=none", func() {
			body := get("/?producer_kind=all&hide=none").Body.String()

			// The reverse direction: a one-way assertion would pass on a link
			// that always writes `all`, leaving no way back to the default.
			Expect(body).To(ContainSubstring("producer_kind=manager"))
			Expect(body).To(ContainSubstring("hide=none"))
		})
	})

	Context("with one open and one answered session-kind card", func() {
		BeforeEach(func() {
			pushCard(pkg.SessionProducerKind, "open-session-producer")
			answeredID := pushCard(pkg.SessionProducerKind, "answered-session-producer")
			answer := pkg.Answer{Kind: pkg.TextAnswerKind, Value: "the board"}
			_, err := store.Answer(ctx, answeredID, "attention-board", "", "", &answer, nil, nil)
			Expect(err).To(BeNil())
		})

		It("counts the answered card only where the board would otherwise show it", func() {
			// ⚠️ Both sides of the answered/`hide` interaction, which is the whole
			// reason the count is drawn on two populations rather than one. With
			// `hide` ON the answered filter has already parked that record, so the
			// kind filter is not what withholds it and the count must not claim it;
			// under `hide=none` the record IS on screen, so the kind filter really
			// is. A single unconditional count cannot satisfy both — it reports 2
			// on the default view — and neither can a single number, which is why
			// both are rendered for the script to choose between.
			defaultBody := get("/").Body.String()
			Expect(defaultBody).To(ContainSubstring(`data-hidden-open="1"`))
			Expect(defaultBody).To(ContainSubstring(`data-hidden-all="2"`))
			Expect(defaultBody).To(ContainSubstring("1 hidden"))

			noneBody := get("/?hide=none").Body.String()
			Expect(noneBody).To(ContainSubstring(`data-hidden-open="1"`))
			Expect(noneBody).To(ContainSubstring(`data-hidden-all="2"`))
			Expect(noneBody).To(ContainSubstring("2 hidden"))
		})
	})

	Context("with only session-kind cards open", func() {
		BeforeEach(func() {
			pushCard(pkg.SessionProducerKind, "session-only-producer")
		})

		It("says no cards of this kind, rather than claiming nothing needs attention", func() {
			body := get("/").Body.String()

			// ⚠️ The production state: no producer can emit `manager`, so the
			// default view is empty while the switch beside it names the hidden
			// cards. "Nothing needs attention." there is a lie the hidden count
			// directly above it contradicts.
			// Scoped to the rendered paragraph, not to the phrase: the script
			// carries the same sentence as the all-kinds fallback, so a bare
			// substring assertion fails on a correct page.
			Expect(body).NotTo(ContainSubstring(`<p class="empty">Nothing needs attention.</p>`))
			Expect(body).To(ContainSubstring("No cards of this kind need attention"))
			Expect(body).To(ContainSubstring("1 hidden"))
		})
	})

	It("pins the script's own copy of the kind parameter", func() {
		body := get("/?producer_kind=all").Body.String()

		// The same mirror the hide parameter keeps: the server renders from the
		// Go constants while the script filters the stream path and rebuilds the
		// kind link. Drift is silent in the direction that matters — the script
		// would remove every row for a kind the server never sends, and the rest
		// of the suite would stay green.
		Expect(body).To(ContainSubstring(`var KIND_PARAM = 'producer_kind';`))
		Expect(body).To(ContainSubstring(`var KIND_ALL = 'all';`))
		Expect(body).To(ContainSubstring(`var KIND_DEFAULT = 'manager';`))
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
