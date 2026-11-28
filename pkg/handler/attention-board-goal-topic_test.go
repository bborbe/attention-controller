// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/handler"
)

// The whole chain, end to end: a fixture vault on disk, read by the real
// pkg.NewTaskIndex, joined by the real pkg.NewProvenanceResolver and served by
// the real board JSON read, with every assertion made on the card's own entry.
var _ = Describe("the goal and topic links in the board JSON", func() {
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
	// sessionLedger is the supervisor's spawn ledger as the real store, sharing
	// the attention store's own db — one BoltDB handle, two buckets.
	var sessionLedger pkg.SessionLedgerStore
	// clock is frozen in BeforeEach and never advanced, so the task index's
	// refresh window never lapses during a spec. That keeps the "reads the vault
	// once" case deterministic: its two page loads must both be served from the
	// index built at construction, and a wall clock that crossed the window
	// between them would re-read the moved-away vault and lose the links.
	var clock libtime.CurrentDateTime

	// Hand-written literals, never built with the helper the production code
	// uses: a shared helper would agree with itself whatever it produced, so
	// neither the `%20`/`%2F` escaping nor the dropped `.md` would be asserted.
	goalHref := "obsidian://open?vault=Personal&file=24%20Goals%2FFirst%20Goal"
	topicHref := "obsidian://open?vault=Personal&file=23%20Topics%2FAttention%20Board%20Polish"
	soloGoalHref := "obsidian://open?vault=Personal&file=24%20Goals%2FSolo%20Goal"

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
		// ⚠️ The ledger store reuses the attention store's own db — one BoltDB
		// handle, two buckets, which is exactly the production wiring.
		sessionLedger = pkg.NewSessionLedgerStore(db)
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

	// rowOf returns the card's navigation provenance from the board JSON.
	rowOf := func(httpHandler http.Handler, itemID pkg.ItemID) pkg.BoardProvenance {
		return boardEntryOf(httpHandler, itemID).Board.Provenance
	}

	// buildPageWith assembles the real chain over the vault as it stands *now*:
	// the caller's index, the real resolver reading empty state and session
	// directories and an empty pane listing, and the real board read over the
	// real store.
	//
	// ⚠️ It must be called per case and never in the BeforeEach ahead of the
	// fixtures: the index reads the vault once, at construction, so a chain built
	// before the case wrote its files would see an empty vault.
	buildPageWith := func(index pkg.TaskIndex) http.Handler {
		return handler.NewAttentionBoardReadHandler(
			store,
			pkg.NewProvenanceResolver(
				pkg.NewEventLogReader(stateDir),
				sessionsDir,
				sessionLedger,
				panes,
				index,
				libtime.NewCurrentDateTime(),
			),
			false,
			vault,
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

		row := rowOf(buildPage(), item.ItemID)

		// The three spans are three, not one unit: the task link survives beside
		// the goal, and the goal beside the topic.
		Expect(row.TaskHref).NotTo(BeEmpty())
		Expect(row.GoalHref).NotTo(BeEmpty())
		Expect(row.TopicHref).NotTo(BeEmpty())
		Expect(row.GoalHref).To(Equal(goalHref))
		Expect(row.TopicHref).To(Equal(topicHref))
		// ⚠️ The first entry is the one rendered and the second is unrendered —
		// deliberately, not by omission.
		Expect(row.GoalName).To(Equal("First Goal"))
		Expect(row.TopicName).To(Equal("Attention Board Polish"))
	})

	It("a goal no topic lists carries no topic link", func() {
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

		row := rowOf(buildPage(), item.ItemID)

		// The dominant live case: 1,148 of 1,619 goal-carrying tasks resolve a
		// goal span and no topic span.
		Expect(row.GoalHref).NotTo(BeEmpty())
		Expect(row.GoalHref).To(Equal(soloGoalHref))
		Expect(row.TopicHref).To(BeEmpty())
		Expect(row.TaskHref).NotTo(BeEmpty())
	})

	It("carries no goal and no topic for a task carrying goals: []", func() {
		writeVault(vault, "25 Tasks", "Empty Goals Task.md",
			"---\nclaude_session_id: session-emptygoals\ngoals: []\n---\n")
		item := pushItem("session-emptygoals")

		row := rowOf(buildPage(), item.ItemID)

		// The three spans are not one unit: the task link must survive a task that
		// names no goal.
		Expect(row.TaskHref).NotTo(BeEmpty())
		Expect(row.GoalHref).To(BeEmpty())
		Expect(row.TopicHref).To(BeEmpty())
	})

	It("carries no link at all for a session that resolves to no task", func() {
		// The vault is genuinely readable and simply holds nothing for this
		// session, so the absence below is a miss rather than an unread vault.
		writeVault(vault, "25 Tasks", "Someone Else.md",
			"---\nclaude_session_id: session-someone-else\n---\n")
		item := pushItem("session-nowhere")

		row := rowOf(buildPage(), item.ItemID)

		// Nothing resolved for this item, so every navigation value is absent —
		// never a stand-in like `unknown` presented as resolved.
		Expect(row).To(Equal(pkg.BoardProvenance{}))
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

		row := rowOf(buildPage(), item.ItemID)

		Expect(row.GoalHref).To(BeEmpty())
		Expect(row.TopicHref).To(BeEmpty())
		Expect(row.TaskHref).NotTo(BeEmpty())
	})

	It("reads the vault once, so a later read still serves the links", func() {
		writeBoardPolishVault("session-readonce")
		item := pushItem("session-readonce")

		// A counting index whose lookups delegate to the real one, so the count is
		// of the lookups the PAGE made rather than of this spec's own call.
		real := pkg.NewTaskIndex(ctx, vault, clock)
		index := &mocks.TaskIndex{}
		index.LookupStub = real.Lookup
		page := buildPageWith(index)

		first := rowOf(page, item.ItemID)
		Expect(first.GoalHref).To(Equal(goalHref))
		Expect(first.TopicHref).To(Equal(topicHref))
		// The positive half, which a delta-only check cannot supply: a reader that
		// is never invoked at all would pass a delta check vacuously.
		Expect(index.LookupCallCount()).To(BeNumerically(">=", 1))

		// ⚠️ Move the vault away and read the board again. This is what proves the
		// vault is not re-read: a load that re-read the vault would find no
		// `24 Goals/` and no `23 Topics/` and render no spans. The handler was built
		// once and the index holds the vault's contents in memory, so the links
		// survive. The directory is deliberately not restored — Ginkgo's TempDir
		// removes the whole tree.
		Expect(os.Rename(vault, vault+"-moved")).To(BeNil())

		second := rowOf(page, item.ItemID)
		Expect(second.GoalHref).To(Equal(goalHref))
		Expect(second.TopicHref).To(Equal(topicHref))
	})

	It("serves the card unchanged on a host with no vault configured", func() {
		writeBoardPolishVault("session-novault")
		item := pushItem("session-novault")

		// A second handler over the same store, with no vault directory and an
		// index built from none.
		page := handler.NewAttentionBoardReadHandler(
			store,
			pkg.NewProvenanceResolver(
				pkg.NewEventLogReader(stateDir),
				sessionsDir,
				sessionLedger,
				panes,
				pkg.NewTaskIndex(ctx, "", clock),
				libtime.NewCurrentDateTime(),
			),
			false,
			"",
		)

		row := rowOf(page, item.ItemID)

		// No error status, and none of the three links.
		Expect(row.TaskHref).To(BeEmpty())
		Expect(row.GoalHref).To(BeEmpty())
		Expect(row.TopicHref).To(BeEmpty())
	})
})
