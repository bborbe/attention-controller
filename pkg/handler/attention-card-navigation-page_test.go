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

// The card's navigation line — its task / goal / topic / session-name row —
// observed end to end through the real page handler and the real store, with
// every assertion made on the served row. This file carries the acceptance
// criteria that are observable in the fixture-served HTML: the navigation line
// leading the ask and sitting outside the info panel, each distinct navigation
// value rendering once, and a card with nothing navigational rendering no line
// at all. ⚠️ The rendered-offset half of the placement claim and the rendered
// geometry belong to the browser suite and are deliberately not asserted here.
var _ = Describe("the card's navigation line on the served page", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var sessionLivenessChecker *mocks.SessionLivenessChecker
	var provenance *mocks.ProvenanceResolver
	var httpHandler http.Handler
	// vaultDir ends in a known name so a served `obsidian://` href can be
	// asserted against a hand-written literal. The directory itself need not
	// exist: the handler only reads its base name and the provenance is mocked.
	var vaultDir string

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		// A real boltkv DB and a real store, not a fake of either: Read prunes
		// open items whose producer is not live, so a faked store would let the
		// page render fixtures that the production read path would have dropped.
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

		// The mock defaults to false. Left at its default, every fixture item is
		// pruned during Read and the page renders empty — which would make every
		// positive assertion fail and every absence assertion pass vacuously.
		sessionLivenessChecker = &mocks.SessionLivenessChecker{}
		sessionLivenessChecker.IsLiveReturns(true)

		store = pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			sessionLivenessChecker,
			libtime.NewCurrentDateTime(),
			libtime.Duration(15*60*1e9),
		)

		// Set per case with ResolveReturns, so each fixture's row is served with
		// its own resolved values.
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

	// pushCard pushes one declaration, filling in the liveness model that matches
	// its producer id and the interrupt class every fixture shares, so the pruning
	// Read performs is exercised for each card. ⚠️ Every fixture sets
	// ProducerKind: Item.Validate rejects an empty producer kind, so a fixture
	// that omitted it would fail at store.Push before any assertion ran.
	pushCard := func(request pkg.PushRequest) *pkg.Item {
		request.LivenessRef = pkg.LivenessRef("session:" + request.ProducerID.String())
		request.InterruptClass = "approve"
		item, err := store.Push(ctx, request)
		Expect(err).To(BeNil())
		return item
	}

	// get renders the page and returns the recorder.
	get := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/", nil)
		resp := httptest.NewRecorder()
		httpHandler.ServeHTTP(resp, req)
		return resp
	}

	// rowOf returns the rendered HTML for one item's row, so an assertion about
	// the card is scoped to that item rather than to the whole page: a page-wide
	// grep would pass on a page where the wrong item carried the values.
	rowOf := func(body string, itemID pkg.ItemID) string {
		start := strings.Index(body, `data-item-id="`+itemID.String()+`"`)
		Expect(start).To(BeNumerically(">=", 0), "row for %s not found", itemID)
		rest := body[start:]
		end := strings.Index(rest, "</li>")
		Expect(end).To(BeNumerically(">=", 0))
		return rest[:end]
	}

	// navigation is the four span classes the navigation line renders, spelled
	// exactly as the served markup spells them. They are frozen because the
	// acceptance criteria key on them.
	navigation := []string{
		`class="task"`,
		`class="goal"`,
		`class="topic"`,
		`class="session-name"`,
	}

	// ⚠️ This is the markup half of the placement criterion. The acceptance
	// criterion requires a second half — the provenance element's rendered top
	// offset being less than the ask's in a browser load — and that belongs to the
	// browser prompt. This case does NOT cover the whole criterion; do not read it
	// as if it did.
	It("renders the navigation line above the ask", func() {
		item := pushCard(pkg.PushRequest{
			ProducerID:      pkg.ProducerID("producer-nav-above"),
			ProducerKind:    pkg.SessionProducerKind,
			DedupKey:        pkg.DedupKey("nav-above"),
			Payload:         "which task is this?",
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		provenance.ResolveReturns(pkg.Provenances{
			item.ItemID: {
				Host:        "burn-nav-a",
				Cwd:         "/w/nav-a",
				Tool:        "AskUserQuestion",
				TaskName:    "Fix the board",
				TaskPath:    "25 Tasks/Fix the board.md",
				GoalName:    "First Goal",
				GoalPath:    "24 Goals/First Goal.md",
				TopicName:   "Attention Board Polish",
				TopicPath:   "23 Topics/Attention Board Polish.md",
				SessionName: "Board Polish Session",
			},
		})

		row := rowOf(get().Body.String(), item.ItemID)

		// Positive control first: a row that failed to render cannot satisfy the
		// rest.
		Expect(row).To(ContainSubstring(item.Payload.String()))

		// All four navigation spans render exactly once — which is what makes the
		// line below the real one rather than an empty div.
		for _, marker := range navigation {
			Expect(
				strings.Count(row, marker),
			).To(Equal(1), "%s is not exactly once on the row", marker)
		}

		// The ask is a question div on a single-question message item.
		askAt := strings.Index(row, `<div class="question">`)
		Expect(askAt).To(BeNumerically(">=", 0), "the ask is not on the row")

		// The navigation line leads the ask in document order.
		navAt := strings.Index(row, `class="provenance"`)
		Expect(navAt).To(BeNumerically(">=", 0), "the navigation line is not on the row")
		Expect(navAt).To(BeNumerically("<", askAt), "the navigation line renders after the ask")

		// And it is on the card face, outside the panel.
		panelAt := strings.Index(row, `<div class="info-panel"`)
		Expect(panelAt).To(BeNumerically(">=", 0), "the panel is not on the row")
		Expect(
			navAt,
		).To(BeNumerically("<", panelAt), "the navigation line renders inside the panel")
	})

	It("renders each distinct navigation value once", func() {
		equal := pushCard(pkg.PushRequest{
			ProducerID:      pkg.ProducerID("producer-nav-equal"),
			ProducerKind:    pkg.SessionProducerKind,
			DedupKey:        pkg.DedupKey("nav-equal"),
			Payload:         "which task is this?",
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		differing := pushCard(pkg.PushRequest{
			ProducerID:      pkg.ProducerID("producer-nav-differing"),
			ProducerKind:    pkg.SessionProducerKind,
			DedupKey:        pkg.DedupKey("nav-differing"),
			Payload:         "what next?",
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		provenance.ResolveReturns(pkg.Provenances{
			// The session registry name equals the resolved task title, which is
			// what this vault's own `/rename <task name>` convention produces. The
			// fixture carries no goal and no topic, so nothing else can contribute
			// a second occurrence of the title.
			equal.ItemID: {
				TaskName:    "Fix the board",
				TaskPath:    "25 Tasks/Fix the board.md",
				SessionName: "Fix the board",
			},
			differing.ItemID: {
				TaskName:    "Fix the board",
				TaskPath:    "25 Tasks/Fix the board.md",
				SessionName: "Board Polish Session",
			},
		})

		body := get().Body.String()

		equalRow := rowOf(body, equal.ItemID)
		Expect(equalRow).To(ContainSubstring(equal.Payload.String()))

		// The title renders exactly once. ⚠️ This is a raw-substring count on the
		// served row: the task link's href carries the path percent-encoded
		// (`25%20Tasks%2FFix%20the%20board`), not the spaced title, so the count is
		// the span's text alone.
		Expect(strings.Count(equalRow, "Fix the board")).To(Equal(1))
		// The task link survives, since hiding it behind the affordance is the
		// alternative the schema rejected.
		Expect(strings.Count(equalRow, `class="task"`)).To(Equal(1))
		// The span is what goes.
		Expect(strings.Count(equalRow, `class="session-name"`)).To(Equal(0))
		// The line still renders, because the task link carries the value.
		Expect(strings.Count(equalRow, `class="provenance"`)).To(Equal(1))

		differingRow := rowOf(body, differing.ItemID)
		Expect(differingRow).To(ContainSubstring(differing.Payload.String()))
		Expect(strings.Count(differingRow, `class="task"`)).To(Equal(1))
		Expect(strings.Count(differingRow, `class="session-name"`)).To(Equal(1))
		Expect(differingRow).To(ContainSubstring("Fix the board"))
		Expect(differingRow).To(ContainSubstring("Board Polish Session"))
		// ⚠️ The `differing` fixture is the negative control the acceptance
		// criterion names, and it is load-bearing: a build that deletes the
		// session-name span unconditionally passes the `equal` half and fails this
		// one, and it would silently drop a navigation value.
	})

	It("renders no navigation line when nothing navigational resolves", func() {
		noNav := pushCard(pkg.PushRequest{
			ProducerID:      pkg.ProducerID("producer-nav-none"),
			ProducerKind:    pkg.SessionProducerKind,
			DedupKey:        pkg.DedupKey("nav-none"),
			Payload:         "no navigation here",
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		nav := pushCard(pkg.PushRequest{
			ProducerID:      pkg.ProducerID("producer-nav-neighbour"),
			ProducerKind:    pkg.SessionProducerKind,
			DedupKey:        pkg.DedupKey("nav-neighbour"),
			Payload:         "a navigable card",
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		provenance.ResolveReturns(pkg.Provenances{
			// Machine values only, so nothing navigational resolves.
			noNav.ItemID: {Host: "burn-none", Cwd: "/w/none"},
			// A neighbour in the same render that does resolve a navigation value.
			nav.ItemID: {TaskName: "Fix the board", TaskPath: "25 Tasks/Fix the board.md"},
		})

		body := get().Body.String()

		noNavRow := rowOf(body, noNav.ItemID)
		// Positive controls first: a row that failed to render cannot satisfy the
		// absences below.
		Expect(noNavRow).To(ContainSubstring(noNav.Payload.String()))
		Expect(strings.Count(noNavRow, `class="info-panel"`)).To(Equal(1))
		Expect(strings.Count(noNavRow, `class="info-toggle"`)).To(Equal(1))
		Expect(noNavRow).To(ContainSubstring(`<span class="host">burn-none</span>`))

		// No navigation line, no span.
		Expect(strings.Count(noNavRow, `class="provenance"`)).To(Equal(0))
		for _, marker := range navigation {
			Expect(
				strings.Count(noNavRow, marker),
			).To(Equal(0), "%s renders on a row with nothing navigational", marker)
		}
		// No bare separator. ⚠️ The line's separator is CSS-generated
		// (`.provenance span + span::before`), so a served row carries none anyway;
		// this assertion exists to catch a placeholder separator someone renders
		// literally.
		Expect(noNavRow).NotTo(ContainSubstring("·"))

		// ⚠️ This is the negative control: a build that suppresses the line on
		// every card passes the `no-nav` half and fails here.
		navRow := rowOf(body, nav.ItemID)
		Expect(strings.Count(navRow, `class="provenance"`)).To(Equal(1))

		// The page's own guard is case-scoped, so this render is not otherwise
		// covered: an unresolvable value renders as nothing, never as a stand-in.
		for _, placeholder := range []string{"unknown", "n/a", "N/A", "—", "??"} {
			Expect(body).NotTo(ContainSubstring(placeholder))
		}
	})
})
