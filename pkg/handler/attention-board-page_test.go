// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/handler"
)

// The board renders answer controls for `message` items and nothing for every
// other class. The negative half is the load-bearing one: a control on a
// `permission` item would let the board answer a gate that only the operator
// may answer, in the session that raised it, which the schema calls permission
// laundering. See the attention item schema § Answer routing and silence 12.
var _ = Describe("Attention page board controls", func() {
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
		httpHandler = handler.NewAttentionPageHandler(store, provenance)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	// renderPage pushes the given declaration, resolves the given provenance for
	// it, and returns the rendered document.
	renderPage := func(request pkg.PushRequest, resolved pkg.Provenance) (string, *pkg.Item) {
		item, err := store.Push(ctx, request)
		Expect(err).To(BeNil())
		provenance.ResolveReturns(pkg.Provenances{item.ItemID: resolved})

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		resp := httptest.NewRecorder()
		httpHandler.ServeHTTP(resp, req)
		Expect(resp.Code).To(Equal(http.StatusOK))
		return resp.Body.String(), item
	}

	// rowBlock returns the rendered HTML for one item's row, so an assertion
	// about controls is scoped to that item rather than to the whole page. A
	// page-wide grep would pass on a page where the wrong item carried the
	// controls.
	rowBlock := func(body string, itemID pkg.ItemID) string {
		start := strings.Index(body, `data-item-id="`+itemID.String()+`"`)
		Expect(start).To(BeNumerically(">=", 0), "row for %s not found", itemID)
		rest := body[start:]
		end := strings.Index(rest, "</li>")
		Expect(end).To(BeNumerically(">=", 0))
		return rest[:end]
	}

	messageRequest := func() pkg.PushRequest {
		return pkg.PushRequest{
			ProducerID:      "session-a",
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:session-a"),
			DedupKey:        "question-1",
			InterruptClass:  "pick",
			Payload:         "Which surface should the answer land on?",
			Context:         "The board is the store's own page.",
			AnswerMechanism: pkg.MessageAnswerMechanism,
			Options: pkg.AnswerOptions{
				{Label: "the board", Recommended: true},
				{Label: "the tab"},
			},
		}
	}

	permissionRequest := func() pkg.PushRequest {
		return pkg.PushRequest{
			ProducerID:      "session-b",
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:session-b"),
			DedupKey:        "gate-1",
			InterruptClass:  "approve",
			Payload:         "Deploy to prod?",
			AnswerMechanism: pkg.PermissionAnswerMechanism,
		}
	}

	Describe("a message item", func() {
		It(
			"renders the question, the context and the options with the recommendation marked",
			func() {
				body, item := renderPage(messageRequest(), pkg.Provenance{})
				block := rowBlock(body, item.ItemID)

				Expect(block).To(ContainSubstring("Which surface should the answer land on?"))
				Expect(block).To(ContainSubstring("The board is the store&#39;s own page."))
				Expect(block).To(ContainSubstring("the board"))
				Expect(block).To(ContainSubstring("the tab"))
				Expect(block).To(ContainSubstring(`class="recommended"`))
			},
		)

		It("renders a form, a skip control and a free-text field", func() {
			body, item := renderPage(messageRequest(), pkg.Provenance{})
			block := rowBlock(body, item.ItemID)

			Expect(block).To(ContainSubstring("<form"))
			Expect(block).To(ContainSubstring(`value="skip"`))
			Expect(block).To(ContainSubstring(`type="text"`))
		})

		It("renders no jump command", func() {
			body, item := renderPage(messageRequest(), pkg.Provenance{Pane: "1907"})
			Expect(rowBlock(body, item.ItemID)).NotTo(ContainSubstring("/supervisor:jump"))
		})
	})

	Describe("a permission item", func() {
		It("renders zero answer controls", func() {
			body, item := renderPage(permissionRequest(), pkg.Provenance{})
			block := rowBlock(body, item.ItemID)

			Expect(block).NotTo(ContainSubstring("<form"))
			Expect(block).NotTo(ContainSubstring("<button"))
		})

		It("renders the copyable jump command when a pane resolved", func() {
			body, item := renderPage(permissionRequest(), pkg.Provenance{
				Pane:         "1907",
				PaneRecorded: true,
				Routable:     true,
			})
			block := rowBlock(body, item.ItemID)

			Expect(block).To(ContainSubstring("/supervisor:jump 1907"))
			Expect(block).NotTo(ContainSubstring("<form"))
			Expect(block).NotTo(ContainSubstring("<button"))
		})

		// An unresolvable pane must render absent rather than as a stand-in: a
		// jump command naming no pane is a value presented as resolved that is
		// not. See the attention item schema § silence 7.
		It("renders no jump command when no pane resolved", func() {
			body, item := renderPage(permissionRequest(), pkg.Provenance{})
			Expect(rowBlock(body, item.ItemID)).NotTo(ContainSubstring("/supervisor:jump"))
		})
	})

	// The two classes must not bleed into each other: a page carrying both is
	// the case where a page-wide grep would pass on the wrong row.
	It(
		"renders controls on the message row and none on the permission row of the same page",
		func() {
			message, err := store.Push(ctx, messageRequest())
			Expect(err).To(BeNil())
			permission, err := store.Push(ctx, permissionRequest())
			Expect(err).To(BeNil())
			provenance.ResolveReturns(pkg.Provenances{
				permission.ItemID: pkg.Provenance{Pane: "1907", PaneRecorded: true, Routable: true},
			})

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			resp := httptest.NewRecorder()
			httpHandler.ServeHTTP(resp, req)
			body := resp.Body.String()

			Expect(rowBlock(body, message.ItemID)).To(ContainSubstring("<form"))
			Expect(rowBlock(body, permission.ItemID)).NotTo(ContainSubstring("<form"))
			Expect(rowBlock(body, permission.ItemID)).NotTo(ContainSubstring("<button"))
		},
	)
})
