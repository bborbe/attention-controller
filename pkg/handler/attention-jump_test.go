// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	libboltkv "github.com/bborbe/boltkv"
	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	"github.com/gorilla/mux"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/handler"
)

// jumpTokenSentinel is the token written to the temp file. It is deliberately
// distinctive so a "the page must not contain the token" assertion cannot pass
// because the token happens to look like something else on the page.
const jumpTokenSentinel = "TESTTOKEN-DO-NOT-LEAK"

// The Jump handover has two halves that must not be collapsed into one: the
// button's data-jump is a path on *this* board, and following it re-resolves the
// pane and performs the jump against the fleet-jump server with the token
// server-side. These specs hold both halves — what the page renders, and what
// the handler does with the request — because the token's whole reason for
// existing is that it never reaches the browser, and only the pair of them
// together shows that.
//
// ⚠️ The handler answers 204 and performs the jump itself; it no longer
// redirects. A redirect navigated the browser away from the board — the exact
// behaviour the operator asked to remove — and published the token in the
// Location header of every click.
var _ = Describe("Attention jump handover", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var provenance *mocks.ProvenanceResolver
	var activator *mocks.PaneActivator
	var tokenDir string
	var tokenPath string

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		// A real boltkv DB and a real store, not a fake of either: the handler
		// reads through the store's Get, so a faked store would assert the call
		// shape instead of the item the production read path would return.
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
		activator = &mocks.PaneActivator{}

		// A real readable token file, so the readable-token cases exercise the
		// same read path production does. The sentinel is what the leak
		// assertions grep for.
		tokenDir, err = os.MkdirTemp("", "attention-jump-token-*")
		Expect(err).To(BeNil())
		tokenPath = filepath.Join(tokenDir, "jump-token")
		Expect(os.WriteFile(tokenPath, []byte(jumpTokenSentinel+"\n"), 0o600)).To(BeNil())
	})

	AfterEach(func() {
		Expect(os.RemoveAll(tokenDir)).To(BeNil())
		Expect(db.Close()).To(BeNil())
	})

	// nonMessageRequest builds the class that renders the copyable command
	// beside the button: a `permission` item, which the board must not answer.
	nonMessageRequest := func() pkg.PushRequest {
		return pkg.PushRequest{
			ProducerID:      "session-a",
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:session-a"),
			DedupKey:        "gate-1",
			InterruptClass:  "approve",
			Payload:         "Deploy to prod?",
			AnswerMechanism: pkg.PermissionAnswerMechanism,
		}
	}

	// messageRequest builds the class the board may answer in place, which
	// renders the button without the command.
	messageRequest := func() pkg.PushRequest {
		return pkg.PushRequest{
			ProducerID:      "session-b",
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:session-b"),
			DedupKey:        "question-1",
			InterruptClass:  "pick",
			Payload:         "Which surface should the answer land on?",
			AnswerMechanism: pkg.MessageAnswerMechanism,
			Options: pkg.AnswerOptions{
				{Label: "the board", Recommended: true},
				{Label: "the tab"},
			},
		}
	}

	pushItem := func(request pkg.PushRequest) *pkg.Item {
		item, err := store.Push(ctx, request)
		Expect(err).To(BeNil())
		return item
	}

	// resolvedPane pins the resolver's answer for one item to a validated pane,
	// which is the only shape that yields both halves of the handover.
	resolvedPane := func(itemID pkg.ItemID, pane string) {
		provenance.ResolveReturns(pkg.Provenances{
			itemID: pkg.Provenance{Pane: pane, PaneRecorded: true, Routable: true},
		})
	}

	// rowBlock returns the rendered HTML for one item's row, so an assertion
	// about the handover is scoped to that item rather than to the whole page. A
	// page-wide grep would pass on a page where the wrong item carried the
	// control.
	rowBlock := func(body string, itemID pkg.ItemID) string {
		start := strings.Index(body, `data-item-id="`+itemID.String()+`"`)
		Expect(start).To(BeNumerically(">=", 0), "row for %s not found", itemID)
		rest := body[start:]
		end := strings.Index(rest, "</li>")
		Expect(end).To(BeNumerically(">=", 0))
		return rest[:end]
	}

	Describe("the board page", func() {
		var pageHandler http.Handler

		BeforeEach(func() {
			pageHandler = handler.NewAttentionPageHandler(
				store,
				provenance,
				false,
				"",
				testBuildIdentity,
			)
		})

		renderPage := func() *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			resp := httptest.NewRecorder()
			pageHandler.ServeHTTP(resp, req)
			return resp
		}

		renderRow := func(itemID pkg.ItemID) string {
			resp := renderPage()
			Expect(resp.Code).To(Equal(http.StatusOK))
			return rowBlock(resp.Body.String(), itemID)
		}

		// The control is a button carrying data-jump, never an anchor with an
		// href: a link navigates the browser away from the board, which is the
		// behaviour the operator asked to remove. The path is a path on this
		// board, never the fleet-jump URL — that URL carries the token, and the
		// token must not reach the document.
		It("renders a Jump button whose data-jump is the item's board-relative jump path", func() {
			item := pushItem(nonMessageRequest())
			resolvedPane(item.ItemID, "1907")

			resp := renderPage()
			Expect(resp.Code).To(Equal(http.StatusOK))
			block := rowBlock(resp.Body.String(), item.ItemID)

			Expect(block).To(ContainSubstring(`class="jump-corner"`))
			Expect(block).To(ContainSubstring(`<button type="button"`))
			Expect(block).To(ContainSubstring(`data-jump="/jump/` + item.ItemID.String() + `"`))
			Expect(block).NotTo(ContainSubstring("/jump?pane="))
			// No anchor to the jump route anywhere on the page: an href would
			// navigate the board away on a click.
			Expect(resp.Body.String()).NotTo(ContainSubstring(`href="/jump/`))
		})

		// An unresolvable pane must not render a control that promises a jump it
		// cannot deliver: a button pointing at a jump that will 404 is a value
		// presented as working that is not. See the attention item schema
		// § silence 7.
		// ⚠️ AMENDED 2026-09-27 — the control now RENDERS here, disabled, because
		// the operator asked for it in the same place on every card. What this
		// spec guards is unchanged and is asserted below: no *working* jump, no
		// copyable command, and no placeholder standing in for the missing pane.
		It(
			"renders no working Jump control and no placeholder when the pane does not resolve",
			func() {
				item := pushItem(nonMessageRequest())
				provenance.ResolveReturns(pkg.Provenances{})

				block := renderRow(item.ItemID)

				Expect(block).NotTo(ContainSubstring("jump-button"))
				Expect(block).NotTo(ContainSubstring(`data-jump="/jump/`))
				Expect(block).NotTo(ContainSubstring("/supervisor:jump"))
				Expect(block).NotTo(ContainSubstring(`class="jump"`))
				// The control is present and unavailable, and "unavailable" is the
				// whole of the claim: a disabled button cannot dispatch, and it
				// carries no data-jump for the document-delegated handler to match
				// even if it somehow did.
				Expect(block).To(ContainSubstring(`class="jump-corner"`))
				Expect(block).To(ContainSubstring(`disabled`))
				Expect(block).NotTo(ContainSubstring(`data-jump=`))
				// No stand-in text either. A placeholder would be an unresolvable value
				// presented as resolved, which is the one failure this task exists to
				// avoid.
				for _, placeholder := range []string{"unknown", "n/a", "N/A", "—", "??"} {
					Expect(block).NotTo(ContainSubstring(placeholder))
				}
			},
		)

		// The button is for every class, the command only for the classes the board
		// must not answer. On a `message` row the "Approve in the session that
		// asked" label would be false — the board may answer that item in place —
		// so the label and the command both have to be absent while the button
		// stays.
		It("renders the button on a message row without the command or the approval label", func() {
			item := pushItem(messageRequest())
			resolvedPane(item.ItemID, "1907")

			block := renderRow(item.ItemID)

			Expect(block).To(ContainSubstring(`class="jump-corner"`))
			Expect(block).To(ContainSubstring(`data-jump="/jump/` + item.ItemID.String() + `"`))
			Expect(block).NotTo(ContainSubstring("/supervisor:jump"))
			Expect(block).NotTo(ContainSubstring("Approve in the session that asked"))
		})

		// The regression guard for the half that already shipped: adding the button
		// must not drop the copyable command an operator may still want to paste.
		It("keeps the copyable jump command on a non-message row", func() {
			item := pushItem(nonMessageRequest())
			resolvedPane(item.ItemID, "1907")

			block := renderRow(item.ItemID)

			Expect(block).To(ContainSubstring("/supervisor:jump 1907"))
			Expect(block).To(ContainSubstring("Approve in the session that asked"))
		})

		// ⚠️ INVERTED by the jump fold, and the inversion is the assertion.
		// This spec used to require that an unreadable token suppressed the
		// button. It now requires the opposite: the endpoint the button points
		// at performs the jump in-process and reads no token, so a button
		// withheld over a token would be a working control hidden by a
		// credential it no longer touches.
		It("renders the button on a pane-resolving row regardless of token readability", func() {
			item := pushItem(nonMessageRequest())
			resolvedPane(item.ItemID, "1907")

			block := renderRow(item.ItemID)

			Expect(block).To(ContainSubstring(`data-jump="/jump/` + item.ItemID.String() + `"`))
			Expect(block).To(ContainSubstring("/supervisor:jump 1907"))
		})

		// The token is a credential. It stays on the server, in the request the
		// handler builds and nowhere a document the browser renders can carry it.
		It("never renders the token value into the page", func() {
			item := pushItem(nonMessageRequest())
			resolvedPane(item.ItemID, "1907")

			resp := renderPage()
			Expect(resp.Code).To(Equal(http.StatusOK))
			body := resp.Body.String()

			// Positive control first: a page that failed to render the handover at
			// all must not be able to satisfy the leak assertion below.
			Expect(body).To(ContainSubstring(`data-jump="/jump/` + item.ItemID.String() + `"`))
			Expect(body).NotTo(ContainSubstring(jumpTokenSentinel))
		})
	})

	Describe("the jump handler", func() {
		var jumpHandler http.Handler

		BeforeEach(func() {
			jumpHandler = handler.NewAttentionJumpHandler(
				store,
				provenance,
				activator,
			)
		})

		// jump issues a GET to the item's jump route with the given Sec-Fetch-Site
		// header. The path var is set with SetURLVars rather than by mounting a
		// router: the handler reads it through mux.Vars, and a router would add a
		// second thing to get wrong without testing anything the handler owns.
		jump := func(itemID pkg.ItemID, secFetchSite string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodGet, "/jump/"+itemID.String(), nil)
			req = mux.SetURLVars(req, map[string]string{"itemID": itemID.String()})
			if secFetchSite != "" {
				req.Header.Set("Sec-Fetch-Site", secFetchSite)
			}
			resp := httptest.NewRecorder()
			jumpHandler.ServeHTTP(resp, req)
			return resp
		}

		// The jump happens server-side and the response carries no body. A 204 is
		// what keeps the browser on the board: it is not a navigation, so the
		// page's fetch() resolves in place and the operator's screen stays put.
		It(
			"performs the jump server-side and answers 204 with no Location and an empty body",
			func() {
				item := pushItem(nonMessageRequest())
				resolvedPane(item.ItemID, "1907")

				resp := jump(item.ItemID, "same-origin")

				Expect(resp.Code).To(Equal(http.StatusNoContent))
				// No Location: a redirect would navigate the board away and publish
				// the token in the header.
				Expect(resp.Header().Get("Location")).To(BeEmpty())
				Expect(resp.Body.String()).To(BeEmpty())
			},
		)

		// The pane is re-resolved here rather than carried in the request, so the
		// activator's argument is the only place it appears.
		It("activates the resolved pane once, in-process", func() {
			item := pushItem(nonMessageRequest())
			resolvedPane(item.ItemID, "1907")

			resp := jump(item.ItemID, "same-origin")

			Expect(resp.Code).To(Equal(http.StatusNoContent))
			Expect(activator.ActivateCallCount()).To(Equal(1))
			_, pane := activator.ActivateArgsForCall(0)
			Expect(pane).To(Equal("1907"))
		})

		// ⚠️ The handler no longer reads a token, so this is now purely a
		// regression guard: it fires only if a future implementation starts
		// reading the credential again AND writing it into a response. Kept
		// rather than deleted, because the cost of the assertion is one line and
		// the cost of the leak it guards is the operator's "credentials never
		// leak" rule.
		It("never writes the token into the response body", func() {
			item := pushItem(nonMessageRequest())
			resolvedPane(item.ItemID, "1907")

			resp := jump(item.ItemID, "same-origin")

			Expect(resp.Code).To(Equal(http.StatusNoContent))
			Expect(resp.Body.String()).NotTo(ContainSubstring(jumpTokenSentinel))
		})

		// ⚠️ This check is now the board route's ONLY cross-site defence: the
		// token used to sit behind it and the handler no longer reads one. Any
		// page the operator visits can fire <img src=".../jump/<itemID>">, so the
		// route checks the request itself rather than resting on item-id
		// unguessability.
		It("refuses a cross-site request with 403 and does not activate", func() {
			item := pushItem(nonMessageRequest())
			resolvedPane(item.ItemID, "1907")

			resp := jump(item.ItemID, "cross-site")

			Expect(resp.Code).To(Equal(http.StatusForbidden))
			Expect(resp.Header().Get("Location")).To(BeEmpty())
			Expect(activator.ActivateCallCount()).To(Equal(0))
		})

		It("refuses a same-site request with 403 and does not activate", func() {
			item := pushItem(nonMessageRequest())
			resolvedPane(item.ItemID, "1907")

			resp := jump(item.ItemID, "same-site")

			Expect(resp.Code).To(Equal(http.StatusForbidden))
			Expect(resp.Header().Get("Location")).To(BeEmpty())
			Expect(activator.ActivateCallCount()).To(Equal(0))
		})

		// `same-origin` is the button click itself; `none` is a direct address-bar
		// or bookmark entry. Both are allowed — an Origin-only check would reject
		// the very navigation this route exists to serve, since a same-origin GET
		// sends no Origin header at all.
		DescribeTable("allows a request that is not cross-site",
			func(secFetchSite string) {
				item := pushItem(nonMessageRequest())
				resolvedPane(item.ItemID, "1907")

				resp := jump(item.ItemID, secFetchSite)

				Expect(resp.Code).To(Equal(http.StatusNoContent))
				Expect(activator.ActivateCallCount()).To(Equal(1))
			},
			Entry("same-origin", "same-origin"),
			Entry("none", "none"),
		)

		It("returns 404 and does not activate when the pane does not resolve", func() {
			item := pushItem(nonMessageRequest())
			provenance.ResolveReturns(pkg.Provenances{})

			resp := jump(item.ItemID, "same-origin")

			Expect(resp.Code).To(Equal(http.StatusNotFound))
			Expect(resp.Header().Get("Location")).To(BeEmpty())
			Expect(activator.ActivateCallCount()).To(Equal(0))
			// Decoded rather than substring-matched, so a body that merely resembles
			// the standard error shape does not pass.
			var errorResponse libhttp.ErrorResponse
			Expect(json.NewDecoder(resp.Body).Decode(&errorResponse)).To(BeNil())
			Expect(errorResponse.Error.Message).To(ContainSubstring("no resolvable pane"))
		})

		// ⚠️ REPLACED by the fold, and the replacement is the point of it. This
		// spec used to require a 503 when the token was unreadable. The handler
		// no longer reads a token at all — the jump is in-process — so the
		// assertion is now that no token reader exists to fail. A handler
		// constructed without one is the sharpest form of that: if any code path
		// still read a token, this request could not be built.
		It("performs the jump with no token reader in the handler at all", func() {
			item := pushItem(nonMessageRequest())
			resolvedPane(item.ItemID, "1907")

			resp := jump(item.ItemID, "same-origin")

			Expect(resp.Code).To(Equal(http.StatusNoContent))
			Expect(activator.ActivateCallCount()).To(Equal(1))
		})

		// A failed jump is reported, never swallowed into a 204. The body carries
		// the failure.
		It("returns 502 when the activator fails", func() {
			activator.ActivateReturns(errors.New(ctx, "pane is not a live pane"))
			item := pushItem(nonMessageRequest())
			resolvedPane(item.ItemID, "1907")

			resp := jump(item.ItemID, "same-origin")

			Expect(resp.Code).To(Equal(http.StatusBadGateway))
			Expect(resp.Body.String()).NotTo(ContainSubstring(jumpTokenSentinel))
			var errorResponse libhttp.ErrorResponse
			Expect(json.NewDecoder(resp.Body).Decode(&errorResponse)).To(BeNil())
			Expect(errorResponse.Error.Message).To(ContainSubstring("jump failed"))
		})
	})
})
