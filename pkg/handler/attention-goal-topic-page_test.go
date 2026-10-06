// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
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

// The whole chain, end to end: a fixture vault on disk, read by the real
// pkg.NewTaskIndex, joined by the real pkg.NewProvenanceResolver and rendered by
// the real page handler, with every assertion made on the served row. This is
// the file that carries the spec's served-page acceptance criteria, because the
// resolution and the template are each only half of what the operator sees —
// neither a resolver-level nor a template-only spec can observe the rendered
// HTML that half produces.
var _ = Describe("the goal and topic spans on the served page", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var sessionLivenessChecker *mocks.SessionLivenessChecker
	var panes *mocks.PaneLister
	// vault ends in a known name, so the served href's `vault=` value is the
	// hand-written literal `Personal` rather than a value read back from the
	// same helper the code uses.
	var vault string
	// stateDir and sessionsDir are empty on purpose: they make the item resolve
	// nothing but its task, which is the row this feature is about and the one
	// that exercises the task block's independence from the pane branches. No
	// event log and no session-registry entry is written by any case here.
	var stateDir string
	var sessionsDir string
	var spawnDir string
	// clock is frozen in BeforeEach and never advanced, so the task index's
	// refresh window never lapses during a spec. That keeps the "reads the vault
	// once" case deterministic: its two page loads must both be served from the
	// index built at construction, and a wall clock that crossed the window
	// between them would re-read the moved-away vault and lose the links.
	var clock libtime.CurrentDateTime

	// The served markup each link is asserted against, written as html/template
	// actually emits it. ⚠️ Hand-written literals, never ones built with
	// vaultFileURL or any other helper the production code uses: a shared helper
	// would agree with itself whatever it produced, so neither the `%20`/`%2F`
	// escaping nor the dropped `.md` would be asserted at all. The `&amp;` is the
	// template's own HTML-escaping of the `&` in the attribute, which is why the
	// assertions are raw-string matches over the served HTML.
	goalAnchor := `<span class="goal"><a href="obsidian://open?vault=Personal&amp;file=24%20Goals%2FFirst%20Goal">First Goal</a></span>`
	topicAnchor := `<span class="topic"><a href="obsidian://open?vault=Personal&amp;file=23%20Topics%2FAttention%20Board%20Polish">Attention Board Polish</a></span>`
	soloGoalAnchor := `<span class="goal"><a href="obsidian://open?vault=Personal&amp;file=24%20Goals%2FSolo%20Goal">Solo Goal</a></span>`

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		// A real boltkv DB and a real store, never a mocked libkv.DB or a mocked
		// AttentionStore: Read prunes open items whose producer is not live, so a
		// faked store would let the page render fixtures the production read path
		// would have dropped.
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

		// The mock defaults to false. Left at its default, every fixture item is
		// pruned during Read and the page renders empty — so every positive
		// assertion below would fail and every absence assertion would pass
		// vacuously. Pinning it to true is what makes the pruning real rather than
		// incidental.
		sessionLivenessChecker = &mocks.SessionLivenessChecker{}
		sessionLivenessChecker.IsLiveReturns(true)

		store = pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			sessionLivenessChecker,
			libtime.NewCurrentDateTime(),
			libtime.Duration(15*60*1e9),
		)

		// An empty-but-readable listing, so every row makes no pane claim at all
		// rather than being marked unroutable. ⚠️ pkg.NewWeztermPaneLister() is
		// deliberately not used: it shells out to a terminal that is not in this
		// container, and the vault is the integration seam this file is about.
		panes = &mocks.PaneLister{}
		panes.ListReturns(map[int]pkg.Pane{}, nil)

		vault = filepath.Join(GinkgoT().TempDir(), "Personal")
		stateDir = GinkgoT().TempDir()
		sessionsDir = GinkgoT().TempDir()
		spawnDir = GinkgoT().TempDir()
		clock = libtime.NewCurrentDateTime()
		clock.SetNow(clock.Now())
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	// writeVault writes one fixture file under <vault>/<dir>/, as raw text rather
	// than through a parser: the fixture is the *file shape* the vault actually
	// holds, so a rename in the reader cannot make a fixture agree with itself.
	writeVault := func(vault, dir, name, content string) {
		path := filepath.Join(vault, dir)
		Expect(os.MkdirAll(path, 0o750)).To(BeNil())
		Expect(os.WriteFile(filepath.Join(path, name), []byte(content), 0o600)).To(BeNil())
	}

	// pushItem pushes one `message` declaration whose session id is recoverable
	// by pkg.sessionIDFromItem, which reads the `session:<id>` marker on the
	// LivenessRef. The item carries no session field of its own.
	pushItem := func(sessionID string) *pkg.Item {
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      pkg.ProducerID("producer-" + sessionID),
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:" + sessionID),
			DedupKey:        pkg.DedupKey("goal-topic-" + sessionID),
			InterruptClass:  "approve",
			Payload:         pkg.Payload("which body of work is this?"),
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		Expect(err).To(BeNil())
		return item
	}

	// rowOf returns the rendered HTML for one item's row, so an assertion about
	// the card is scoped to that item rather than to the whole page: a page-wide
	// grep would pass on a page where another row legitimately carried a link.
	// It is a copy of the local closure in attention-page_test.go, which cannot be
	// imported.
	rowOf := func(body string, itemID pkg.ItemID) string {
		start := strings.Index(body, `data-item-id="`+itemID.String()+`"`)
		Expect(start).To(BeNumerically(">=", 0), "row for %s not found", itemID)
		rest := body[start:]
		end := strings.Index(rest, "</li>")
		Expect(end).To(BeNumerically(">=", 0))
		return rest[:end]
	}

	// get renders the page through the given handler and returns the recorder.
	get := func(httpHandler http.Handler) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/", nil)
		resp := httptest.NewRecorder()
		httpHandler.ServeHTTP(resp, req)
		return resp
	}

	// buildPageWith assembles the real chain over the vault as it stands *now*:
	// the caller's index, the real resolver reading empty state and session
	// directories and an empty pane listing, and the real page handler over the
	// real store.
	//
	// ⚠️ It must be called per case and never in the BeforeEach ahead of the
	// fixtures: the index reads the vault once, at construction, so a chain built
	// before the case wrote its files would see an empty vault.
	buildPageWith := func(index pkg.TaskIndex) http.Handler {
		return handler.NewAttentionPageHandler(
			store,
			pkg.NewProvenanceResolver(
				pkg.NewEventLogReader(stateDir),
				sessionsDir,
				spawnDir,
				panes,
				index,
				libtime.NewCurrentDateTime(),
			),
			false,
			vault,
			testBuildIdentity,
			boardmetrics.NewMetrics(prometheus.NewRegistry()),
		)
	}

	// buildPage is buildPageWith over an index built from the fixture vault.
	buildPage := func() http.Handler {
		return buildPageWith(pkg.NewTaskIndex(ctx, vault, clock))
	}

	// writeBoardPolishVault writes a vault whose task names a goal a topic lists,
	// which is the shape every read-once and vault-less case needs. sessionID
	// parameterises the task file so one helper serves both.
	writeBoardPolishVault := func(sessionID string) {
		writeVault(vault, "25 Tasks", "Board Polish.md",
			"---\nclaude_session_id: "+sessionID+"\ngoals:\n  - \"[[First Goal]]\"\n---\n")
		writeVault(vault, "24 Goals", "First Goal.md", "---\ntitle: First Goal\n---\n")
		writeVault(vault, "23 Topics", "Attention Board Polish.md",
			"---\ntitle: Attention Board Polish\n---\n\n## Goals\n\n- [[First Goal]]\n")
	}

	It("goal and topic resolve from the first goals entry", func() {
		writeVault(
			vault,
			"25 Tasks",
			"Board Polish.md",
			"---\nclaude_session_id: session-both\ngoals:\n  - \"[[First Goal]]\"\n  - \"[[Second Goal]]\"\n---\n\nbody\n",
		)
		writeVault(vault, "24 Goals", "First Goal.md", "---\ntitle: First Goal\n---\n")
		// Both goal files exist, and only the first entry's topic lists anything,
		// so a reader that took the second entry would resolve a goal and fail the
		// absence assertion below rather than quietly passing.
		writeVault(vault, "24 Goals", "Second Goal.md", "---\ntitle: Second Goal\n---\n")
		writeVault(vault, "23 Topics", "Attention Board Polish.md",
			"---\ntitle: Attention Board Polish\n---\n\n## Goals\n\n- [[First Goal]]\n")
		item := pushItem("session-both")

		row := rowOf(get(buildPage()).Body.String(), item.ItemID)

		// The three spans are three, not one unit: the task link survives beside
		// the goal, and the goal beside the topic.
		Expect(strings.Count(row, `class="task"`)).To(Equal(1))
		Expect(strings.Count(row, `class="goal"`)).To(Equal(1))
		Expect(strings.Count(row, `class="topic"`)).To(Equal(1))
		Expect(row).To(ContainSubstring(goalAnchor))
		Expect(row).To(ContainSubstring(topicAnchor))
		// ⚠️ The first entry is the one rendered and the second is unrendered —
		// deliberately, not by omission.
		Expect(row).NotTo(ContainSubstring("Second Goal"))
		// The href is a real URL rather than the URL filter's sentinel, which is
		// what a plain-string field would render while every field-level assertion
		// still passed.
		Expect(row).NotTo(ContainSubstring("#ZgotmplZ"))
	})

	It("a goal no topic lists renders no topic span", func() {
		writeVault(vault, "25 Tasks", "Solo Goal Task.md",
			"---\nclaude_session_id: session-solo\ngoals:\n  - \"[[Solo Goal]]\"\n---\n")
		writeVault(vault, "24 Goals", "Solo Goal.md", "---\ntitle: Solo Goal\n---\n")
		// ⚠️ The prose wikilink is the discriminating half: the topic page mentions
		// the goal in its body and carries no `## Goals` heading at all, so an
		// implementation that scanned the whole page for wikilinks instead of only
		// the `## Goals` section would resolve a topic here and fail this case.
		writeVault(
			vault,
			"23 Topics",
			"Loose Notes.md",
			"---\ntitle: Loose Notes\n---\n\nSolo Goal is on the radar - see [[Solo Goal]] for the write-up.\n",
		)
		item := pushItem("session-solo")

		row := rowOf(get(buildPage()).Body.String(), item.ItemID)

		// The dominant live case: 1,148 of 1,619 goal-carrying tasks resolve a
		// goal span and no topic span.
		Expect(strings.Count(row, `class="goal"`)).To(Equal(1))
		Expect(row).To(ContainSubstring(soloGoalAnchor))
		Expect(strings.Count(row, `class="topic"`)).To(Equal(0))
		Expect(strings.Count(row, `class="task"`)).To(Equal(1))
	})

	It("draws no goal and no topic for a task carrying goals: []", func() {
		writeVault(vault, "25 Tasks", "Empty Goals Task.md",
			"---\nclaude_session_id: session-emptygoals\ngoals: []\n---\n")
		item := pushItem("session-emptygoals")

		row := rowOf(get(buildPage()).Body.String(), item.ItemID)

		// The three spans are not one unit: the task link must survive a task that
		// names no goal.
		Expect(strings.Count(row, `class="task"`)).To(Equal(1))
		Expect(strings.Count(row, `class="goal"`)).To(Equal(0))
		Expect(strings.Count(row, `class="topic"`)).To(Equal(0))
	})

	It("draws no span at all for a session that resolves to no task", func() {
		// The vault is genuinely readable and simply holds nothing for this
		// session, so the absence below is a miss rather than an unread vault.
		writeVault(vault, "25 Tasks", "Someone Else.md",
			"---\nclaude_session_id: session-someone-else\n---\n")
		item := pushItem("session-nowhere")

		row := rowOf(get(buildPage()).Body.String(), item.ItemID)

		// Positive control first: a row that failed to render at all must not be
		// able to satisfy the absences below.
		Expect(row).To(ContainSubstring(item.Payload.String()))
		Expect(strings.Count(row, `class="task"`)).To(Equal(0))
		Expect(strings.Count(row, `class="goal"`)).To(Equal(0))
		Expect(strings.Count(row, `class="topic"`)).To(Equal(0))
		// Nothing resolved for this item, so the provenance line itself is absent
		// rather than drawn empty.
		//
		// ⚠️ The provenance DIV is what is asserted, and deliberately not the
		// absence of `-`. The spec lists `-` among the placeholders, but the row's
		// own meta line renders `{{ .Meta }}` (inside the info panel), so a
		// bare dash is present on every row of the board and an assertion on it
		// could never hold. The placeholder this case is about would stand where a
		// span would — inside the provenance div — so the div's absence is the
		// assertion, and it is the correct one. Do not "fix" this back.
		Expect(strings.Count(row, `class="provenance"`)).To(Equal(0))
		// An unresolvable value renders absent, never as a stand-in presented as
		// resolved.
		Expect(row).NotTo(ContainSubstring("unknown"))
		Expect(row).NotTo(ContainSubstring("n/a"))
	})

	It("keeps a task title from resolving as a goal", func() {
		// ⚠️ This is the only case in the whole set that asserts the
		// `24 Goals/<title>.md` existence guard. A `## Goals` section mixes goals
		// and tasks — measured: `23 Topics/Attention Board Polish.md` lists 8
		// entries, all tasks and zero goals — so without this case an
		// implementation that trusted the section passes every other case.
		writeVault(vault, "25 Tasks", "Collision Task.md",
			"---\nclaude_session_id: session-collision\ngoals:\n  - \"[[Collision Title]]\"\n---\n")
		// The title exists as a TASK file, recording a different session, so the
		// index's session tie-break cannot pick it and this case still resolves
		// `Collision Task`.
		writeVault(vault, "25 Tasks", "Collision Title.md",
			"---\nclaude_session_id: session-other\n---\n")
		writeVault(vault, "23 Topics", "Collision Topic.md",
			"---\ntitle: Collision Topic\n---\n\n## Goals\n\n- [[Collision Title]]\n")

		// The fixture's own precondition, asserted so the case cannot pass on a
		// vault that simply failed to write.
		_, err := os.Stat(filepath.Join(vault, "24 Goals", "Collision Title.md"))
		Expect(os.IsNotExist(err)).To(BeTrue(), "no goal file of that title may exist")
		_, err = os.Stat(filepath.Join(vault, "25 Tasks", "Collision Title.md"))
		Expect(err).To(BeNil())

		item := pushItem("session-collision")

		row := rowOf(get(buildPage()).Body.String(), item.ItemID)

		Expect(strings.Count(row, `class="goal"`)).To(Equal(0))
		Expect(strings.Count(row, `class="topic"`)).To(Equal(0))
		Expect(strings.Count(row, `class="task"`)).To(Equal(1))
	})

	It("reads the vault once, so a later load still serves the links", func() {
		writeBoardPolishVault("session-readonce")
		item := pushItem("session-readonce")

		// A counting index whose lookups delegate to the real one, so the count is
		// of the lookups the PAGE made rather than of this spec's own call.
		real := pkg.NewTaskIndex(ctx, vault, clock)
		index := &mocks.TaskIndex{}
		index.LookupStub = real.Lookup
		page := buildPageWith(index)

		first := rowOf(get(page).Body.String(), item.ItemID)
		Expect(first).To(ContainSubstring(goalAnchor))
		Expect(first).To(ContainSubstring(topicAnchor))
		// The positive half, which a delta-only check cannot supply: a reader that
		// is never invoked at all would pass a delta check vacuously.
		Expect(index.LookupCallCount()).To(BeNumerically(">=", 1))

		// ⚠️ Move the vault away and load the page again. This is what proves the
		// vault is not re-read: a load that re-read the vault would find no
		// `24 Goals/` and no `23 Topics/` and render no spans. The handler was built
		// once and the index holds the vault's contents in memory, so the links
		// survive. The directory is deliberately not restored — Ginkgo's TempDir
		// removes the whole tree.
		Expect(os.Rename(vault, vault+"-moved")).To(BeNil())

		second := rowOf(get(page).Body.String(), item.ItemID)
		Expect(second).To(ContainSubstring(goalAnchor))
		Expect(second).To(ContainSubstring(topicAnchor))
	})

	It("serves the card unchanged on a host with no vault configured", func() {
		writeBoardPolishVault("session-novault")
		item := pushItem("session-novault")

		// A second handler over the same store, with no vault directory and an
		// index built from none.
		page := handler.NewAttentionPageHandler(
			store,
			pkg.NewProvenanceResolver(
				pkg.NewEventLogReader(stateDir),
				sessionsDir,
				spawnDir,
				panes,
				pkg.NewTaskIndex(ctx, "", clock),
				libtime.NewCurrentDateTime(),
			),
			false,
			"",
			testBuildIdentity,
			boardmetrics.NewMetrics(prometheus.NewRegistry()),
		)

		resp := get(page)
		Expect(resp.Code).To(Equal(http.StatusOK))

		row := rowOf(resp.Body.String(), item.ItemID)

		// The card renders exactly what it rendered before this change: no error
		// status, no partial line, and none of the three links.
		Expect(row).To(ContainSubstring(item.Payload.String()))
		Expect(strings.Count(row, `class="task"`)).To(Equal(0))
		Expect(strings.Count(row, `class="goal"`)).To(Equal(0))
		Expect(strings.Count(row, `class="topic"`)).To(Equal(0))
	})
})
