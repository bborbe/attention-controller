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

	Describe("the board JSON", func() {
		var boardHandler http.Handler

		BeforeEach(func() {
			boardHandler = handler.NewAttentionBoardReadHandler(store, provenance, false, "")
		})

		viewOf := func(itemID pkg.ItemID) pkg.BoardView {
			return boardEntryOf(boardHandler, itemID).Board
		}

		It("carries the item's board-relative jump path, never a fleet-jump URL", func() {
			item := pushItem(nonMessageRequest())
			resolvedPane(item.ItemID, "1907")

			view := viewOf(item.ItemID)

			Expect(view.JumpURL).To(Equal("/jump/" + item.ItemID.String()))
			Expect(view.JumpURL).NotTo(ContainSubstring("/jump?pane="))
		})

		It("carries no jump target and no placeholder when the pane does not resolve", func() {
			item := pushItem(nonMessageRequest())
			provenance.ResolveReturns(pkg.Provenances{})

			view := viewOf(item.ItemID)

			Expect(view.JumpURL).To(BeEmpty())
			Expect(view.JumpCommand).To(BeEmpty())
			Expect(view.Provenance.Pane).To(BeEmpty())
			Expect(view.NoJump).NotTo(BeEmpty())
		})

		It("carries the jump path on a message row without the command", func() {
			item := pushItem(messageRequest())
			resolvedPane(item.ItemID, "1907")

			view := viewOf(item.ItemID)

			Expect(view.JumpURL).To(Equal("/jump/" + item.ItemID.String()))
			Expect(view.JumpCommand).To(BeEmpty())
		})

		It("keeps the copyable jump command on a non-message row", func() {
			item := pushItem(nonMessageRequest())
			resolvedPane(item.ItemID, "1907")

			view := viewOf(item.ItemID)

			Expect(view.JumpCommand).To(Equal("/supervisor:jump 1907"))
			Expect(view.JumpURL).To(Equal("/jump/" + item.ItemID.String()))
		})

		It("never carries the token value", func() {
			item := pushItem(nonMessageRequest())
			resolvedPane(item.ItemID, "1907")

			req := httptest.NewRequest(http.MethodGet, "/api/1.0/attention/board", nil)
			resp := httptest.NewRecorder()
			boardHandler.ServeHTTP(resp, req)
			Expect(resp.Code).To(Equal(http.StatusOK))

			Expect(resp.Body.String()).To(ContainSubstring(`"/jump/` + item.ItemID.String() + `"`))
			Expect(resp.Body.String()).NotTo(ContainSubstring(jumpTokenSentinel))
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

		// ⚠️ THE BLOCKING FINDING on PR #115, pinned. The page renders the control
		// whenever jumpPane is non-empty, which includes the recorded-but-unvalidated
		// case — so the handler must resolve through the SAME ordering. Reading
		// `.Pane` alone made the button a functional no-op on exactly the path it
		// was added for: `.Pane` is empty there by construction, so every click
		// 404'd. The two halves of the handover must agree about which pane they
		// mean, and this is the spec that holds them to it.
		It("activates the recorded pane when the listing was unreadable", func() {
			item := pushItem(nonMessageRequest())
			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{
					PaneRecorded:          true,
					PaneListingUnreadable: true,
					RecordedPane:          "58",
					// ⚠️ Routable false and Pane empty, exactly as build leaves them on
					// this path: the id has not been validated against a live listing,
					// which is the whole reason the row marks it `unvalidated`.
					Routable: false,
				},
			})

			resp := jump(item.ItemID, "same-origin")

			Expect(resp.Code).To(Equal(http.StatusNoContent))
			Expect(activator.ActivateCallCount()).To(Equal(1))
			_, pane := activator.ActivateArgsForCall(0)
			Expect(pane).To(Equal("58"))
		})

		// ⚠️ The NEGATIVE companion, and the invariant the spec above rests on:
		// `RecordedPane` alone must NOT authorize a jump. `RecordedPane` is only
		// meaningful as the unvalidated half of the unreadable-listing state; read on
		// its own it would be a defaulted pane — the failure `Pane`'s contract exists
		// to prevent. Without this spec, loosening `jumpPane` to return RecordedPane
		// unconditionally would leave every other spec green while turning the
		// control into exactly that defaulting path.
		It("refuses a recorded pane that was not carried as an unreadable listing", func() {
			item := pushItem(nonMessageRequest())
			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{
					PaneRecorded: true,
					RecordedPane: "58",
					// ⚠️ PaneListingUnreadable deliberately false: the listing WAS
					// readable, so the recorded id was checkable and did not resolve.
					// Offering it here would jump on a pane the board proved is not
					// this session's.
					Routable: false,
				},
			})

			resp := jump(item.ItemID, "same-origin")

			Expect(resp.Code).To(Equal(http.StatusNotFound))
			Expect(activator.ActivateCallCount()).To(Equal(0))
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
