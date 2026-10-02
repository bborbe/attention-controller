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

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/handler"
)

// The card's information affordance, observed end to end through the real page
// handler and the real store, with every assertion made on the served row. This
// file carries the acceptance criteria that are observable in the fixture-served
// HTML: the ask leading the card, the machine identity relocating into the
// per-card panel with that card's own values, the navigation spans staying on
// the card face after the ask, and the no-machine-identity card rendering no
// affordance at all. The browser-only halves — the click that reveals the panel
// and its survival across a stream row-swap — belong to the e2e suite and are
// deliberately not asserted here.
var _ = Describe("the card's information affordance on the served page", func() {
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
		)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	// pushCard pushes one declaration, filling in the liveness model that matches
	// its producer id and the interrupt class every fixture shares, so the pruning
	// Read performs is exercised for each card.
	pushCard := func(request pkg.PushRequest) *pkg.Item {
		request.LivenessRef = pkg.LivenessRef("session:" + request.ProducerID.String())
		request.InterruptClass = "approve"
		item, err := store.Push(ctx, request)
		Expect(err).To(BeNil())
		return item
	}

	// get renders the page and returns the recorder.
	get := func(method string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/", nil)
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

	// rendersAfter asserts marker is on the row and after the ask. ⚠️ It asserts
	// presence BEFORE comparing positions on purpose: strings.Index returns -1
	// for an absent marker, and -1 < askAt would pass for exactly the element the
	// assertion exists to place.
	rendersAfter := func(row string, askAt int, marker string) {
		at := strings.Index(row, marker)
		Expect(at).To(BeNumerically(">=", 0), "%s is not on the row", marker)
		Expect(at).To(BeNumerically(">", askAt), "%s renders before the ask", marker)
	}

	// cardFixture is one card's declaration and the provenance its row is served
	// with, plus the item the store returned once it was pushed.
	type cardFixture struct {
		request    pkg.PushRequest
		provenance pkg.Provenance
		item       *pkg.Item
	}

	// cardFixtures is the single definition of the three cards this Describe
	// renders — a question, a permission and a report, each with its own
	// producer and its own resolved provenance. Both the lead-with-the-ask case
	// and the relocation case read this one definition, so the values they assert
	// are distinct and shared rather than retyped.
	cardFixtures := func() []cardFixture {
		return []cardFixture{
			{
				request: pkg.PushRequest{
					ProducerID:      pkg.ProducerID("producer-ask-question"),
					ProducerKind:    pkg.SessionProducerKind,
					DedupKey:        pkg.DedupKey("card-info-question"),
					Payload:         "which task is this?",
					AnswerMechanism: pkg.MessageAnswerMechanism,
				},
				provenance: pkg.Provenance{
					Host:        "burn-a",
					Cwd:         "/w/a",
					Tool:        "AskUserQuestion",
					TaskName:    "Task A",
					TaskPath:    "25 Tasks/Task A.md",
					GoalName:    "Goal A",
					GoalPath:    "24 Goals/Goal A.md",
					TopicName:   "Topic A",
					TopicPath:   "23 Topics/Topic A.md",
					SessionName: "Session A",
				},
			},
			{
				request: pkg.PushRequest{
					ProducerID:      pkg.ProducerID("producer-ask-permission"),
					ProducerKind:    pkg.SessionProducerKind,
					DedupKey:        pkg.DedupKey("card-info-permission"),
					Payload:         "approve the deploy",
					AnswerMechanism: pkg.PermissionAnswerMechanism,
				},
				provenance: pkg.Provenance{Host: "burn-b", Cwd: "/w/b", Tool: "Bash"},
			},
			{
				request: pkg.PushRequest{
					ProducerID:      pkg.ProducerID("producer-ask-report"),
					ProducerKind:    pkg.SessionProducerKind,
					DedupKey:        pkg.DedupKey("card-info-report"),
					Payload:         "the nightly sweep failed",
					AnswerMechanism: pkg.AckAnswerMechanism,
				},
				provenance: pkg.Provenance{Host: "burn-c", Cwd: "/w/c", Tool: "Write"},
			},
		}
	}

	// pushCards pushes every fixture and installs the mocked provenance that
	// resolves each one to its own values, returning the fixtures with their
	// pushed items. ⚠️ Every fixture sets ProducerKind: Item.Validate rejects an
	// empty or unrecognised producer kind, so a fixture that omitted it would
	// fail at store.Push before any assertion ran.
	pushCards := func() []cardFixture {
		fixtures := cardFixtures()
		provenances := pkg.Provenances{}
		for index := range fixtures {
			item := pushCard(fixtures[index].request)
			fixtures[index].item = item
			provenances[item.ItemID] = fixtures[index].provenance
		}
		provenance.ResolveReturns(provenances)
		return fixtures
	}

	// ⚠️ One card is not the bar. Three items — one question-kind and one
	// permission-kind among them — are rendered in ONE render, and the ask leads
	// on every one of them. This forbids the dodge of cleaning one card's face
	// while the rest still scan past their UUID: a build that fixed only the
	// question card would leave the permission and report rows failing here.
	//
	// ⚠️ The card's corner controls — the `i` glyph and the `✕` — render BEFORE
	// the ask by design, and neither is machine identity. There is deliberately no
	// "first text node" assertion; the assertions below are the operational form
	// of "leads with its ask".
	It("a card leads with its ask", func() {
		fixtures := pushCards()

		resp := get("GET")
		Expect(resp.Code).To(Equal(http.StatusOK))
		body := resp.Body.String()

		for _, fixture := range fixtures {
			item := fixture.item
			row := rowOf(body, item.ItemID)

			// Positive control first: a row that failed to render cannot satisfy
			// the rest.
			Expect(row).To(ContainSubstring(item.Payload.String()))

			// The ask is a question div on a message item and a payload div on a
			// permission or ack item. Both are hand-written literals, written as
			// the served markup reads them.
			askMarker := `<div class="payload">`
			if item.AnswerMechanism == pkg.MessageAnswerMechanism {
				askMarker = `<div class="question">`
			}
			askAt := strings.Index(row, askMarker)
			Expect(askAt).To(BeNumerically(">=", 0), "%s is not on the row", askMarker)

			// The machine identity renders after the ask on every card.
			rendersAfter(row, askAt, `class="producer"`)
			rendersAfter(row, askAt, `class="meta"`)
			rendersAfter(row, askAt, `class="host"`)

			// The navigation line is on the card face — after the ask, not inside
			// the panel — and only the message card carries one, because only its
			// provenance resolved a task.
			if item.AnswerMechanism == pkg.MessageAnswerMechanism {
				rendersAfter(row, askAt, `class="provenance"`)
			}

			// The text before the ask carries no machine identity.
			face := row[:askAt]
			Expect(face).NotTo(ContainSubstring(item.ProducerID.String()))
			Expect(face).NotTo(ContainSubstring(fixture.provenance.Host))
			Expect(face).NotTo(ContainSubstring(fixture.provenance.Cwd))
			Expect(face).NotTo(ContainSubstring(fixture.provenance.Tool))
		}
	})

	// ⚠️ This is the negative control the acceptance criterion names. A build
	// that DELETED the machine values satisfies the `face` half and fails the
	// `panel` half; the panel half is what makes this a relocation claim rather
	// than a deletion claim.
	//
	// ⚠️ The criterion's "while its panel is closed" half is read here as "the
	// card face, outside the panel", because this harness serves the panel markup
	// in every response and cannot click. The browser half — the panel invisible
	// until the control is activated — is the e2e suite's, and this comment
	// records the reading so the next reader does not "fix" it into a visibility
	// assertion this harness cannot make.
	It("relocates the machine identity into the panel, carrying that card's own values", func() {
		fixtures := pushCards()

		body := get("GET").Body.String()

		panels := make([]string, 0, len(fixtures))
		for _, fixture := range fixtures {
			item := fixture.item
			row := rowOf(body, item.ItemID)
			Expect(row).To(ContainSubstring(item.Payload.String()))

			panelAt := strings.Index(row, `<div class="info-panel"`)
			Expect(panelAt).To(BeNumerically(">=", 0), "the panel is not on the row")
			face := row[:panelAt]
			panel := row[panelAt:]

			// The card face carries none of the machine identity.
			for _, marker := range []string{`class="producer"`, `class="meta"`, `class="host"`} {
				Expect(strings.Count(face, marker)).To(Equal(0), "%s is on the card face", marker)
			}
			Expect(face).NotTo(ContainSubstring(item.ProducerID.String()))
			Expect(face).NotTo(ContainSubstring(fixture.provenance.Host))
			Expect(face).NotTo(ContainSubstring(fixture.provenance.Cwd))
			Expect(face).NotTo(ContainSubstring(fixture.provenance.Tool))
			Expect(face).NotTo(ContainSubstring(item.CreatedAt.String()))

			// The panel carries all of it, each element exactly once.
			for _, marker := range []string{
				`class="producer"`,
				`class="meta"`,
				`class="host"`,
				`class="cwd"`,
				`class="tool"`,
			} {
				Expect(strings.Count(panel, marker)).To(Equal(1),
					"%s is not exactly once in the panel", marker)
			}
			Expect(panel).To(ContainSubstring(item.ProducerID.String()))
			Expect(panel).To(ContainSubstring(item.ProducerKind.String()))
			Expect(panel).To(ContainSubstring(fixture.provenance.Host))
			Expect(panel).To(ContainSubstring(fixture.provenance.Cwd))
			Expect(panel).To(ContainSubstring(fixture.provenance.Tool))
			Expect(panel).To(ContainSubstring(item.State.String()))
			Expect(panel).To(ContainSubstring(item.CreatedAt.String()))

			panels = append(panels, panel)
		}

		// Across the three items the revealed values are not all equal: a
		// placeholder, a constant, or one card's data repeated on another fails.
		Expect(panels[0]).NotTo(Equal(panels[1]))
		Expect(panels[1]).NotTo(Equal(panels[2]))
		Expect(panels[0]).NotTo(Equal(panels[2]))
	})

	// ⚠️ The mocked resolver is the right instrument here: the resolution chain —
	// a fixture vault and a fixture session registry read by the real
	// NewProvenanceResolver — is already covered end to end by
	// attention-goal-topic-page_test.go and attention-session-name-page_test.go,
	// and this criterion is about where the spans render on the served row.
	It("keeps the navigation spans on the face, after the ask", func() {
		fixtures := pushCards()

		// The ask-question card: its mocked provenance carries a task, the goal
		// that task names and the topic page that lists that goal, plus the
		// session name a `nameSource: user` registry name produces.
		var question cardFixture
		for _, fixture := range fixtures {
			if fixture.request.AnswerMechanism == pkg.MessageAnswerMechanism {
				question = fixture
			}
		}

		row := rowOf(get("GET").Body.String(), question.item.ItemID)
		Expect(row).To(ContainSubstring(question.item.Payload.String()))

		navigation := []string{
			`class="task"`,
			`class="goal"`,
			`class="topic"`,
			`class="session-name"`,
		}

		// Each span renders exactly once on the row.
		for _, marker := range navigation {
			Expect(
				strings.Count(row, marker),
			).To(Equal(1), "%s is not exactly once on the row", marker)
		}

		// Each renders outside the panel — on the card face.
		panelAt := strings.Index(row, `<div class="info-panel"`)
		Expect(panelAt).To(BeNumerically(">=", 0), "the panel is not on the row")
		face := row[:panelAt]
		for _, marker := range navigation {
			Expect(strings.Count(face, marker)).To(Equal(1), "%s is not on the card face", marker)
		}

		// Each appears after the ask in document order.
		askAt := strings.Index(row, `<div class="question">`)
		Expect(askAt).To(BeNumerically(">=", 0), "the ask is not on the row")
		for _, marker := range navigation {
			rendersAfter(row, askAt, marker)
		}

		// The order among themselves is unchanged.
		Expect(strings.Index(row, `class="task"`)).
			To(BeNumerically("<", strings.Index(row, `class="goal"`)))
		Expect(strings.Index(row, `class="goal"`)).
			To(BeNumerically("<", strings.Index(row, `class="topic"`)))
		Expect(strings.Index(row, `class="topic"`)).
			To(BeNumerically("<", strings.Index(row, `class="session-name"`)))
	})

	// ⚠️ The mocked store is deliberate and is the only reachable construction of
	// this input. store.Push sets State and CreatedAt and Item.Validate rejects
	// an empty ProducerID, so no item pushed through the real store can carry
	// none of the machine identity — the criterion's card "must be posted", and
	// the fake's ReadBoard is the only seam that serves one. This is the single
	// deliberate exception to the repo's real-store rule; do not "fix" it back
	// onto the real store, which would lose the case.
	It("a card with no machine identity renders no info affordance", func() {
		store := &mocks.AttentionStore{}
		store.ReadBoardReturns(pkg.Items{
			{ItemID: pkg.ItemID("probe-no-identity")},
			{
				ItemID:       pkg.ItemID("probe-with-identity"),
				ProducerID:   pkg.ProducerID("session-probe"),
				ProducerKind: pkg.SessionProducerKind,
				State:        pkg.OpenState,
				CreatedAt:    libtime.NewCurrentDateTime().Now(),
			},
		}, nil)
		provenance := &mocks.ProvenanceResolver{}
		provenance.ResolveReturns(pkg.Provenances{})
		handlerUnderTest := handler.NewAttentionPageHandler(
			store, provenance, false, vaultDir, testBuildIdentity,
		)

		req := httptest.NewRequest("GET", "/", nil)
		resp := httptest.NewRecorder()
		handlerUnderTest.ServeHTTP(resp, req)

		Expect(resp.Code).To(Equal(http.StatusOK))
		body := resp.Body.String()

		// Both rows render, so a dropped row cannot satisfy the absence below.
		Expect(strings.Count(body, `data-item-id="`)).To(Equal(2))
		noIdentity := rowOf(body, pkg.ItemID("probe-no-identity"))
		withIdentity := rowOf(body, pkg.ItemID("probe-with-identity"))

		// The card carrying no machine identity renders neither the affordance
		// nor the panel.
		Expect(strings.Count(noIdentity, `class="info-toggle"`)).To(Equal(0))
		Expect(strings.Count(noIdentity, `class="info-panel"`)).To(Equal(0))

		// The negative control, in the same render: a build that hides the
		// affordance on every card fails here.
		Expect(strings.Count(withIdentity, `class="info-toggle"`)).To(Equal(1))
		Expect(strings.Count(withIdentity, `class="info-panel"`)).To(Equal(1))
	})
})
