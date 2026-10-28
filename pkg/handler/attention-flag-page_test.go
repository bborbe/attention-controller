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

// The flag control is a second form beside the answer form, never inside it.
// These specs pin the served markup: which rows carry it, which do not, that it
// cannot navigate the board away, and that the script carries the delegated
// listener that owns it. The flag endpoint itself belongs to a different
// prompt, so the state here is written through the store directly.
var _ = Describe("Attention page flag control", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var provenance *mocks.ProvenanceResolver
	var httpHandler http.Handler

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

		sessionLivenessChecker := &mocks.SessionLivenessChecker{}
		sessionLivenessChecker.IsLiveReturns(true)

		store = pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			sessionLivenessChecker,
			libtime.NewCurrentDateTime(),
			libtime.Duration(15*60*1e9),
		)

		provenance = &mocks.ProvenanceResolver{}
		registry := prometheus.NewRegistry()
		httpHandler = handler.NewAttentionPageHandler(
			store,
			provenance,
			false,
			filepath.Join(GinkgoT().TempDir(), "Personal"),
			testBuildIdentity,
			boardmetrics.NewMetrics(registry),
		)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	// producer_kind=all because the board defaults to manager-kind rows and
	// these fixtures are session cards: without it the page renders no row at
	// all and every assertion below would pass vacuously on a missing subtree.
	render := func() string {
		req := httptest.NewRequest(http.MethodGet, "/?producer_kind=all", nil)
		resp := httptest.NewRecorder()
		httpHandler.ServeHTTP(resp, req)
		Expect(resp.Code).To(Equal(http.StatusOK))
		return resp.Body.String()
	}

	// rowOf returns one item's rendered row, so an assertion about the control
	// is scoped to that item rather than to the whole page.
	rowOf := func(body string, itemID pkg.ItemID) string {
		start := strings.Index(body, `data-item-id="`+itemID.String()+`"`)
		Expect(start).To(BeNumerically(">=", 0), "row for %s not found", itemID)
		rest := body[start:]
		end := strings.Index(rest, "</li>")
		Expect(end).To(BeNumerically(">=", 0))
		return rest[:end]
	}

	pushRequest := func(mechanism pkg.AnswerMechanism, dedupKey pkg.DedupKey) pkg.PushRequest {
		producerID := pkg.ProducerID("producer-" + dedupKey.String())
		return pkg.PushRequest{
			ProducerID:      producerID,
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:" + producerID.String()),
			DedupKey:        dedupKey,
			InterruptClass:  "pick",
			Payload:         "Should this card have reached the operator?",
			AnswerMechanism: mechanism,
		}
	}

	It("renders a flag form on an open card, separate from the answer form", func() {
		item, err := store.Push(ctx, pushRequest(pkg.MessageAnswerMechanism, "flag-open"))
		Expect(err).To(BeNil())
		provenance.ResolveReturns(pkg.Provenances{item.ItemID: pkg.Provenance{}})

		block := rowOf(render(), item.ItemID)
		Expect(block).To(ContainSubstring(`<form class="flag" data-flag-control>`))
		Expect(block).To(ContainSubstring(`name="reason"`))
		Expect(block).To(ContainSubstring(`class="flag-submit"`))
		// The answer form is still there, and the flag form is not inside it:
		// the answer form closes before the flag form opens.
		Expect(block).To(ContainSubstring(`<form class="answer"`))
		Expect(
			strings.Index(block, `</form>`),
		).To(BeNumerically("<", strings.Index(block, `<form class="flag"`)))
	})

	It("renders the flag form on a permission card", func() {
		item, err := store.Push(ctx, pushRequest(pkg.PermissionAnswerMechanism, "flag-permission"))
		Expect(err).To(BeNil())
		provenance.ResolveReturns(pkg.Provenances{item.ItemID: pkg.Provenance{}})

		block := rowOf(render(), item.ItemID)
		Expect(block).To(ContainSubstring(`data-flag-control`))
		Expect(block).NotTo(ContainSubstring(`<form class="answer"`))
	})

	It("renders the flag form on an ack card", func() {
		item, err := store.Push(ctx, pushRequest(pkg.AckAnswerMechanism, "flag-ack"))
		Expect(err).To(BeNil())
		provenance.ResolveReturns(pkg.Provenances{item.ItemID: pkg.Provenance{}})

		block := rowOf(render(), item.ItemID)
		Expect(block).To(ContainSubstring(`data-flag-control`))
		Expect(block).NotTo(ContainSubstring(`<form class="answer"`))
	})

	It("withholds the flag form from the dimmed record card", func() {
		item, err := store.Push(ctx, pushRequest(pkg.MessageAnswerMechanism, "flag-dimmed"))
		Expect(err).To(BeNil())
		answer := pkg.Answer{Kind: pkg.TextAnswerKind, Value: "answered"}
		_, err = store.Answer(ctx, item.ItemID, "attention-board", "", "", &answer, nil, nil)
		Expect(err).To(BeNil())
		provenance.ResolveReturns(pkg.Provenances{item.ItemID: pkg.Provenance{}})

		body := render()
		Expect(body).To(ContainSubstring(` dimmed" data-item-id="` + item.ItemID.String() + `"`))
		Expect(rowOf(body, item.ItemID)).NotTo(ContainSubstring(`data-flag-control`))
	})

	It("carries no action and no method on the flag form", func() {
		item, err := store.Push(ctx, pushRequest(pkg.MessageAnswerMechanism, "flag-noaction"))
		Expect(err).To(BeNil())
		provenance.ResolveReturns(pkg.Provenances{item.ItemID: pkg.Provenance{}})

		block := rowOf(render(), item.ItemID)
		start := strings.Index(block, `<form class="flag"`)
		Expect(start).To(BeNumerically(">=", 0))
		rest := block[start:]
		end := strings.Index(rest, `</form>`)
		Expect(end).To(BeNumerically(">=", 0))
		form := rest[:end]
		Expect(form).NotTo(ContainSubstring(`action=`))
		Expect(form).NotTo(ContainSubstring(`method=`))
	})

	It("serves the second delegated listener and its selector", func() {
		item, err := store.Push(ctx, pushRequest(pkg.MessageAnswerMechanism, "flag-listener"))
		Expect(err).To(BeNil())
		provenance.ResolveReturns(pkg.Provenances{item.ItemID: pkg.Provenance{}})

		body := render()
		// The selector literal is prefixed with "form", which only the script
		// carries: data-flag-control alone also appears in the row markup.
		Expect(body).To(ContainSubstring(`form[data-flag-control]`))
		Expect(body).To(ContainSubstring(`var FLAGS_PARAM = 'flags';`))
	})

	It("marks a flagged row and not an unflagged one", func() {
		flagged, err := store.Push(ctx, pushRequest(pkg.MessageAnswerMechanism, "flag-marked"))
		Expect(err).To(BeNil())
		unflagged, err := store.Push(ctx, pushRequest(pkg.MessageAnswerMechanism, "flag-unmarked"))
		Expect(err).To(BeNil())
		_, err = store.Flag(ctx, flagged.ItemID, "misrouted")
		Expect(err).To(BeNil())
		provenance.ResolveReturns(pkg.Provenances{
			flagged.ItemID:   pkg.Provenance{},
			unflagged.ItemID: pkg.Provenance{},
		})

		body := render()
		Expect(rowOf(body, flagged.ItemID)).To(ContainSubstring(`data-flagged="true"`))
		Expect(rowOf(body, unflagged.ItemID)).To(ContainSubstring(`data-flagged="false"`))
	})

	// A flag is reversible, and the card it was written on is the only place the
	// undo can be reached: the flag parks the card out of the default view, so
	// without this control the operator would have to know both the flags=none
	// URL and the item id to take a flag back.
	It("renders the unflag control on a flagged row and not on an unflagged one", func() {
		flagged, err := store.Push(ctx, pushRequest(pkg.MessageAnswerMechanism, "unflag-marked"))
		Expect(err).To(BeNil())
		unflagged, err := store.Push(
			ctx,
			pushRequest(pkg.MessageAnswerMechanism, "unflag-unmarked"),
		)
		Expect(err).To(BeNil())
		_, err = store.Flag(ctx, flagged.ItemID, "misrouted")
		Expect(err).To(BeNil())
		provenance.ResolveReturns(pkg.Provenances{
			flagged.ItemID:   pkg.Provenance{},
			unflagged.ItemID: pkg.Provenance{},
		})

		body := render()
		Expect(rowOf(body, flagged.ItemID)).
			To(ContainSubstring(`<button type="button" class="unflag" data-unflag>Unflag</button>`))
		Expect(rowOf(body, unflagged.ItemID)).NotTo(ContainSubstring(`data-unflag`))
	})

	It("serves the unflag listener and its route", func() {
		item, err := store.Push(ctx, pushRequest(pkg.MessageAnswerMechanism, "unflag-listener"))
		Expect(err).To(BeNil())
		provenance.ResolveReturns(pkg.Provenances{item.ItemID: pkg.Provenance{}})

		body := render()
		// The selector literal is prefixed with "button", which only the script
		// carries: data-unflag alone also appears in the row markup.
		Expect(body).To(ContainSubstring(`button[data-unflag]`))
		Expect(body).To(ContainSubstring(`'/unflag'`))
	})
})
