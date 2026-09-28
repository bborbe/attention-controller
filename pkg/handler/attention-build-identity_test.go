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
	"github.com/bborbe/attention-controller/pkg/buildidentity"
	"github.com/bborbe/attention-controller/pkg/handler"
)

// The footer is the half of this task a human reads, and it is the half the
// sibling page specs cannot cover: they pass the same shared fixture through
// unchanged, which shows the value reaches the template but says nothing about
// how it renders. These cases vary the identity so the ordinary and the absent
// state are both exercised, and they assert on the footer element's own markup
// rather than on the whole document — `version` and `commit` are ordinary words
// that a body-wide substring check could satisfy from elsewhere on the page.
var _ = Describe("Attention page build identity footer", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var provenance *mocks.ProvenanceResolver

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		// A real store, matching the sibling page specs: the footer must render
		// on the page the store actually produces, not on a fixture shaped to
		// suit it.
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
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	// render serves the board once with the given identity and returns the
	// document, so a case asserts on the surface the operator loads.
	render := func(identity buildidentity.Identity) string {
		httpHandler := handler.NewAttentionPageHandler(
			store,
			provenance,
			false,
			"",
			identity,
		)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		resp := httptest.NewRecorder()
		httpHandler.ServeHTTP(resp, req)
		Expect(resp.Code).To(Equal(http.StatusOK))
		return resp.Body.String()
	}

	// footerBlock returns the footer element's own markup. Every value
	// assertion below is scoped to it: `version` and `commit` appear in the
	// page's script and prose too, so an unscoped check would pass on a page
	// whose footer rendered nothing at all.
	footerBlock := func(body string) string {
		start := strings.Index(body, `<footer class="build-identity">`)
		Expect(start).To(BeNumerically(">=", 0), "no build-identity footer rendered")
		rest := body[start:]
		end := strings.Index(rest, "</footer>")
		Expect(end).To(BeNumerically(">=", 0), "footer element was not closed")
		return rest[:end]
	}

	// push puts one open item on the board and returns its id, so a case can
	// assert the ROW rendered rather than only that the page did.
	//
	// ⚠️ The id is what the assertion uses, never the dedup key: the dedup key is
	// the store's collapse key and is not rendered anywhere, so asserting on it
	// fails on a board that rendered the row perfectly — which is how this case
	// was first written.
	push := func(producerID, dedupKey, payload string) pkg.ItemID {
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      pkg.ProducerID(producerID),
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:" + producerID),
			DedupKey:        pkg.DedupKey(dedupKey),
			InterruptClass:  "approve",
			Payload:         pkg.Payload(payload),
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		Expect(err).To(BeNil())
		return item.ItemID
	}

	// ⚠️ Version and commit are DIFFERENT strings here, unlike the shared
	// fixture. That is the point of this case: each value is asserted inside the
	// label that owns it, so a footer rendering the commit under `version` and
	// the version under `commit` — the plausible mistake, since on a tagless
	// build the two are equal and the swap is invisible — fails here.
	It("renders each value under its own label", func() {
		body := render(buildidentity.Identity{
			Version:    "v9.9.9-test",
			Commit:     "1a2b3c4d5e6f",
			CommitTime: "2026-01-02T03:04:05Z",
		})

		footer := footerBlock(body)
		Expect(footer).To(ContainSubstring(`version <span class="bi-value">v9.9.9-test</span>`))
		Expect(footer).To(ContainSubstring(`commit <span class="bi-value">1a2b3c4d5e6f</span>`))
		Expect(footer).To(
			ContainSubstring(`committed <span class="bi-value">2026-01-02T03:04:05Z</span>`),
		)
	})

	// ⚠️ The label is `committed`, not `built`, and the distinction is asserted
	// because it is this task's own signature defect rather than a wording
	// preference: the value is the commit's authored time, so rendering it under
	// a "built" label would put a cheap signal where an expensive question is
	// asked. A later edit that "corrects" the label to `built` must fail here.
	It("labels the time as the commit's, never as the build's", func() {
		body := render(buildidentity.Identity{
			Version:    "v9.9.9-test",
			Commit:     "1a2b3c4d5e6f",
			CommitTime: "2026-01-02T03:04:05Z",
		})

		footer := footerBlock(body)
		Expect(footer).To(ContainSubstring("committed"))
		Expect(footer).NotTo(ContainSubstring("built"))
	})

	// The negative control. A build that never received a commit must say so,
	// because every alternative a reader takes at face value — a dev default, an
	// empty span, a plausible-looking sha — is worse than an explicit absence.
	// Asserting only that some text rendered would pass on each of them.
	It("says so explicitly when the binary carries no stamp, and renders no value", func() {
		body := render(buildidentity.Identity{})

		footer := footerBlock(body)
		Expect(footer).To(ContainSubstring("No build identity"))
		Expect(footer).To(ContainSubstring("carries no VCS stamp"))

		// No value span at all, so there is nothing for a reader to mistake for
		// an answer, and no label left dangling without one.
		Expect(footer).NotTo(ContainSubstring("bi-value"))
		Expect(footer).NotTo(ContainSubstring("version"))
		Expect(footer).NotTo(ContainSubstring("committed"))

		// Warned rather than muted: an identity the binary does not carry is a
		// fault in the deploy, not a normal state.
		Expect(footer).To(ContainSubstring("bi-absent"))
	})

	// The footer describes the build, not the board's contents, so it renders
	// after the template's item branch rather than inside either arm. Both arms
	// are asserted because each alone is satisfied by a footer nested in the
	// other one: an empty board catches a footer inside `{{if .Items}}`, a
	// populated board catches one inside `{{else}}`.
	It("renders on an empty board", func() {
		body := render(testBuildIdentity)

		Expect(body).To(ContainSubstring("Nothing needs attention."))
		Expect(footerBlock(body)).To(ContainSubstring(testBuildIdentity.Commit))
	})

	It("renders on a board carrying an item", func() {
		itemID := push("footer-producer", "footer-gate", "does the footer render?")

		body := render(testBuildIdentity)

		// Positive clause first: the row rendered, so the footer below is
		// asserted on the populated arm rather than on an empty page.
		Expect(body).To(ContainSubstring(`data-item-id="` + itemID.String() + `"`))
		Expect(footerBlock(body)).To(ContainSubstring(testBuildIdentity.Commit))
	})

	// The stream replaces a row's node on every event, so a footer living inside
	// the row template would be destroyed and rebuilt with it — and would render
	// once per row. Two items make both halves visible: the count catches the
	// per-row render, and the position catches a footer that moved above the
	// list. Held here so a later edit that relocates it is caught rather than
	// discovered on a live board.
	It("renders exactly once, below the list, whatever the board holds", func() {
		push("footer-producer-a", "footer-gate-a", "first")
		push("footer-producer-b", "footer-gate-b", "second")

		body := render(testBuildIdentity)

		Expect(strings.Count(body, `<footer class="build-identity">`)).To(Equal(1))
		Expect(strings.Index(body, `<footer class="build-identity">`)).To(
			BeNumerically(">", strings.LastIndex(body, "</ul>")),
		)
	})
})
