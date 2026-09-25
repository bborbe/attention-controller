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

// jumpBaseURL stands in for the fleet-jump server's origin. It is a distinct
// host on purpose: the redirect's Location must be built from the configured
// origin, not from the request's own Host, so a test that used the request host
// would pass on an implementation that echoed the request back.
const jumpBaseURL = "http://fleet-jump.test:1337"

// jumpTokenSentinel is the token written to the temp file. It is deliberately
// distinctive so a "the page must not contain the token" assertion cannot pass
// because the token happens to look like something else on the page.
const jumpTokenSentinel = "TESTTOKEN-DO-NOT-LEAK"

// The Jump handover has two halves that must not be collapsed into one: the
// button's href is a path on *this* board, and following it re-resolves the pane
// and appends the fleet-jump token server-side. These specs hold both halves —
// what the page renders, and what the redirect does with the request — because
// the token's whole reason for existing is that it never reaches the browser,
// and only the pair of them together shows that.
var _ = Describe("Attention jump handover", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var provenance *mocks.ProvenanceResolver
	var tokenDir string
	var tokenPath string

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		// A real boltkv DB and a real store, not a fake of either: the redirect
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
				pkg.NewJumpTokenReader(tokenPath),
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

		It("renders a Jump button whose href is the item's board-relative jump path", func() {
			item := pushItem(nonMessageRequest())
			resolvedPane(item.ItemID, "1907")

			block := renderRow(item.ItemID)

			// The href is a path on this board, never the fleet-jump URL: that URL
			// carries the token, and the token must not reach the document.
			Expect(block).To(ContainSubstring(`class="jump-button"`))
			Expect(block).To(ContainSubstring(`href="/jump/` + item.ItemID.String() + `"`))
			Expect(block).NotTo(ContainSubstring(jumpBaseURL))
		})

		// An unresolvable pane must render absent rather than as a stand-in: a
		// button pointing at a jump that will 404 is a value presented as working
		// that is not. See the attention item schema § silence 7.
		It("renders no Jump button and no placeholder when the pane does not resolve", func() {
			item := pushItem(nonMessageRequest())
			provenance.ResolveReturns(pkg.Provenances{})

			block := renderRow(item.ItemID)

			Expect(block).NotTo(ContainSubstring("jump-button"))
			Expect(block).NotTo(ContainSubstring(`href="/jump/`))
			Expect(block).NotTo(ContainSubstring("/supervisor:jump"))
			Expect(block).NotTo(ContainSubstring(`class="jump"`))
			Expect(block).NotTo(ContainSubstring("Jump to session"))
			// No stand-in text either. A placeholder would be an unresolvable value
			// presented as resolved, which is the one failure this task exists to
			// avoid.
			for _, placeholder := range []string{"unknown", "n/a", "N/A", "—", "??"} {
				Expect(block).NotTo(ContainSubstring(placeholder))
			}
		})

		// The button is for every class, the command only for the classes the board
		// must not answer. On a `message` row the "Approve in the session that
		// asked" label would be false — the board may answer that item in place —
		// so the label and the command both have to be absent while the button
		// stays.
		It("renders the button on a message row without the command or the approval label", func() {
			item := pushItem(messageRequest())
			resolvedPane(item.ItemID, "1907")

			block := renderRow(item.ItemID)

			Expect(block).To(ContainSubstring(`class="jump-button"`))
			Expect(block).To(ContainSubstring(`href="/jump/` + item.ItemID.String() + `"`))
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

		// The button degrades alone. A host with no readable token still gets the
		// copyable command exactly as it rendered before this change — gating the
		// whole handover div on the button would silently drop the command too.
		It("renders no button but keeps the command when the token is unreadable", func() {
			pageHandler = handler.NewAttentionPageHandler(
				store,
				provenance,
				false,
				pkg.NewJumpTokenReader(""),
			)
			item := pushItem(nonMessageRequest())
			resolvedPane(item.ItemID, "1907")

			resp := renderPage()
			Expect(resp.Code).To(Equal(http.StatusOK))
			block := rowBlock(resp.Body.String(), item.ItemID)

			Expect(block).NotTo(ContainSubstring("jump-button"))
			Expect(block).NotTo(ContainSubstring(`href="/jump/`))
			Expect(block).To(ContainSubstring("/supervisor:jump 1907"))
		})

		// The token is a credential. It belongs in the redirect's Location header
		// and nowhere a document the browser renders can carry it.
		It("never renders the token value into the page", func() {
			item := pushItem(nonMessageRequest())
			resolvedPane(item.ItemID, "1907")

			resp := renderPage()
			Expect(resp.Code).To(Equal(http.StatusOK))
			body := resp.Body.String()

			// Positive control first: a page that failed to render the handover at
			// all must not be able to satisfy the leak assertion below.
			Expect(body).To(ContainSubstring(`href="/jump/` + item.ItemID.String() + `"`))
			Expect(body).NotTo(ContainSubstring(jumpTokenSentinel))
		})
	})

	Describe("the jump redirect", func() {
		var jumpHandler http.Handler

		BeforeEach(func() {
			jumpHandler = handler.NewAttentionJumpHandler(
				store,
				provenance,
				pkg.NewJumpTokenReader(tokenPath),
				jumpBaseURL,
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

		It("redirects a same-origin request with the pane and the token in the Location", func() {
			item := pushItem(nonMessageRequest())
			resolvedPane(item.ItemID, "1907")

			resp := jump(item.ItemID, "same-origin")

			Expect(resp.Code).To(Equal(http.StatusFound))
			location := resp.Header().Get("Location")
			Expect(location).To(ContainSubstring(jumpBaseURL + "/jump?"))
			// The pane is re-resolved here rather than carried in the link, so the
			// header is the only place either value appears.
			Expect(location).To(ContainSubstring("pane=1907"))
			Expect(location).To(ContainSubstring("t=" + jumpTokenSentinel))
		})

		It("never writes the token into the redirect's body", func() {
			item := pushItem(nonMessageRequest())
			resolvedPane(item.ItemID, "1907")

			resp := jump(item.ItemID, "same-origin")

			Expect(resp.Code).To(Equal(http.StatusFound))
			Expect(resp.Body.String()).NotTo(ContainSubstring(jumpTokenSentinel))
		})

		// The token's whole purpose is to defeat the cross-origin case where a
		// visited page fires <img src=".../jump?pane=X">. Routing the jump through
		// this origin re-opens that vector, so the route checks the request itself
		// rather than resting on item-id unguessability.
		It("refuses a cross-site request with 403 and no redirect", func() {
			item := pushItem(nonMessageRequest())
			resolvedPane(item.ItemID, "1907")

			resp := jump(item.ItemID, "cross-site")

			Expect(resp.Code).To(Equal(http.StatusForbidden))
			Expect(resp.Header().Get("Location")).To(BeEmpty())
		})

		It("refuses a same-site request with 403 and no redirect", func() {
			item := pushItem(nonMessageRequest())
			resolvedPane(item.ItemID, "1907")

			resp := jump(item.ItemID, "same-site")

			Expect(resp.Code).To(Equal(http.StatusForbidden))
			Expect(resp.Header().Get("Location")).To(BeEmpty())
		})

		// `same-origin` is the button click itself; `none` is a direct address-bar
		// or bookmark entry. Both are allowed — an Origin-only check would reject
		// the very navigation this route exists to serve, since a same-origin <a
		// href> GET sends no Origin header at all.
		DescribeTable("allows a request that is not cross-site",
			func(secFetchSite string) {
				item := pushItem(nonMessageRequest())
				resolvedPane(item.ItemID, "1907")

				resp := jump(item.ItemID, secFetchSite)

				Expect(resp.Code).To(Equal(http.StatusFound))
				Expect(resp.Header().Get("Location")).NotTo(BeEmpty())
			},
			Entry("same-origin", "same-origin"),
			Entry("none", "none"),
		)

		It("returns 404 and no redirect when the pane does not resolve", func() {
			item := pushItem(nonMessageRequest())
			provenance.ResolveReturns(pkg.Provenances{})

			resp := jump(item.ItemID, "same-origin")

			Expect(resp.Code).To(Equal(http.StatusNotFound))
			Expect(resp.Header().Get("Location")).To(BeEmpty())
			// Decoded rather than substring-matched, so a body that merely resembles
			// the standard error shape does not pass.
			var errorResponse libhttp.ErrorResponse
			Expect(json.NewDecoder(resp.Body).Decode(&errorResponse)).To(BeNil())
			Expect(errorResponse.Error.Message).To(ContainSubstring("no resolvable pane"))
		})

		It("returns 503 and no redirect when the token is unreadable", func() {
			jumpHandler = handler.NewAttentionJumpHandler(
				store,
				provenance,
				pkg.NewJumpTokenReader(""),
				jumpBaseURL,
			)
			item := pushItem(nonMessageRequest())
			resolvedPane(item.ItemID, "1907")

			resp := jump(item.ItemID, "same-origin")

			Expect(resp.Code).To(Equal(http.StatusServiceUnavailable))
			Expect(resp.Header().Get("Location")).To(BeEmpty())
			var errorResponse libhttp.ErrorResponse
			Expect(json.NewDecoder(resp.Body).Decode(&errorResponse)).To(BeNil())
			Expect(errorResponse.Error.Message).To(ContainSubstring("jump token unavailable"))
		})
	})
})
