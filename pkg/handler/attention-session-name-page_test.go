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

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/handler"
)

// The whole chain, end to end: a fixture session registry on disk, read by the
// real provenance resolver and rendered by the real page handler, with every
// assertion made on the served row. This is the file that carries every
// acceptance criterion that observes the served page, because the resolution
// and the template are each only half of what the operator sees — neither a
// resolver-level nor a template-only spec can observe the rendered HTML that
// half produces.
//
// ⚠️ AC 5's negative half — the spec's evidence that
// `git diff origin/master -- pkg/attention-item.go pkg/attention-store.go
// pkg/handler/attention-push.go` is empty — is carried by this grep rather than
// by a `git diff`, because this worktree's `.git` is masked and a `git` command
// dies with `fatal: not a git repository`:
//
//	! grep -Eq 'session_name|sessionName|SessionName' pkg/attention-item.go pkg/attention-store.go pkg/handler/attention-push.go
//
// Its baseline was measured at spec time: that pattern matches nothing in those
// three files today, so the check passes before and after this set. Do not
// "fix" it into a `git` command.
var _ = Describe("the session name on the served page", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var sessionLivenessChecker *mocks.SessionLivenessChecker
	var panes *mocks.PaneLister
	// vault ends in a known name, so the served href's `vault=` value is the
	// hand-written literal `Personal` rather than a value read back from the
	// same helper the code uses.
	var vault string
	// stateDir holds the producers' event logs and sessionsDir holds the session
	// registry. Both are fresh per case: the registry is scanned inside every
	// Resolve call, so a case that writes a record must not see another case's.
	var stateDir string
	var sessionsDir string
	var spawnDir string

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
		// container.
		panes = &mocks.PaneLister{}
		panes.ListReturns(map[int]pkg.Pane{}, nil)

		vault = filepath.Join(GinkgoT().TempDir(), "Personal")
		stateDir = GinkgoT().TempDir()
		sessionsDir = GinkgoT().TempDir()
		spawnDir = GinkgoT().TempDir()
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	// writeSession writes one registry record as `<sessionsDir>/<pid>.json`.
	//
	// ⚠️ Written as raw JSON text, never marshalled from sessionRegistryEntry: the
	// fixture must be the *file shape* the registry actually holds, so a field
	// rename in the resolver's own struct cannot make the fixture agree with
	// itself. The `pid` is inserted unquoted because the registry's own `pid`
	// field is a number. Writing the same pid twice overwrites the record, which
	// is how the rename case is expressed.
	writeSession := func(pid, sessionID, name, nameSource string) {
		Expect(os.WriteFile(
			filepath.Join(sessionsDir, pid+".json"),
			[]byte(`{"sessionId":"`+sessionID+`","name":"`+name+`","nameSource":"`+nameSource+
				`","pid":`+pid+`,"cwd":"/w/x"}`),
			0o600,
		)).To(BeNil())
	}

	// writeEvents writes one producer's event log, as raw JSON text for the same
	// reason writeSession writes raw text.
	writeEvents := func(producerID, content string) {
		Expect(os.WriteFile(
			filepath.Join(stateDir, producerID+".events.jsonl"),
			[]byte(content),
			0o600,
		)).To(BeNil())
	}

	// eventLine writes one event-log line. Written as raw JSON rather than
	// marshalled from a struct, so the fixture is the *file shape* the watcher
	// actually writes and a field rename in the resolver's own struct cannot make
	// the test agree with itself.
	eventLine := func(itemID, sessionID, host, cwd, tool, pane string) string {
		return `{"item_id":"` + itemID + `","session_id":"` + sessionID + `","host":"` + host +
			`","cwd":"` + cwd + `","tool_name":"` + tool + `","pane":"` + pane + `"}` + "\n"
	}

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
			DedupKey:        pkg.DedupKey("session-name-" + sessionID),
			InterruptClass:  "approve",
			Payload:         pkg.Payload("which body of work is this?"),
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		Expect(err).To(BeNil())
		return item
	}

	// rowOf returns the rendered HTML for one item's row, so an assertion about
	// the card is scoped to that item rather than to the whole page: a page-wide
	// grep would pass on a page where another row legitimately carried a value.
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

	// provenanceDivOf returns the row's provenance div. It is needed by the
	// absence cases: the placeholder they are about would stand where a span
	// would, which is inside this div, and the div is the only one with that
	// class and holds no nested div, so the first `</div>` closes it.
	provenanceDivOf := func(row string) string {
		start := strings.Index(row, `<div class="provenance">`)
		Expect(start).To(BeNumerically(">=", 0), "provenance div not found")
		rest := row[start:]
		end := strings.Index(rest, "</div>")
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

	// buildPage assembles the real chain over the vault as it stands *now*: the
	// real index, the real resolver reading the fixture state and session
	// directories, and the real page handler over the real store.
	//
	// ⚠️ It must be called per case and never in the BeforeEach ahead of the
	// fixtures: the index reads the vault once, at construction, so a chain built
	// before the case wrote its files would see an empty vault.
	buildPage := func() http.Handler {
		return handler.NewAttentionPageHandler(
			store,
			pkg.NewProvenanceResolver(
				stateDir,
				sessionsDir,
				spawnDir,
				panes,
				pkg.NewTaskIndex(ctx, vault),
			),
			false,
			vault,
			testBuildIdentity,
		)
	}

	// writeBoardPolishVault writes a vault whose task names a goal a topic lists,
	// which is the shape the span-independence cases need. sessionID parameterises
	// the task file so one helper serves both.
	writeBoardPolishVault := func(sessionID string) {
		writeVault(vault, "25 Tasks", "Board Polish.md",
			"---\nclaude_session_id: "+sessionID+"\ngoals:\n  - \"[[First Goal]]\"\n---\n")
		writeVault(vault, "24 Goals", "First Goal.md", "---\ntitle: First Goal\n---\n")
		writeVault(vault, "23 Topics", "Attention Board Polish.md",
			"---\ntitle: Attention Board Polish\n---\n\n## Goals\n\n- [[First Goal]]\n")
	}

	It("a user-named session renders its name", func() {
		writeSession("101", "session-named", "Board Polish Session", "user")
		// No event log and no vault task, so the name is the only thing this row
		// resolves — and the only reason the provenance line renders at all.
		item := pushItem("session-named")

		row := rowOf(get(buildPage()).Body.String(), item.ItemID)

		// Positive control first: a row that failed to render cannot satisfy the
		// rest.
		Expect(row).To(ContainSubstring(item.Payload.String()))
		Expect(strings.Count(row, `class="session-name"`)).To(Equal(1))
		// The expected markup is a hand-written literal, written as html/template
		// emits it, never one built with a helper the production code uses: a
		// shared helper would agree with itself whatever it produced. Its text
		// equals the `name` value the fixture registry holds for that session id.
		Expect(row).To(ContainSubstring(`<span class="session-name">Board Polish Session</span>`))
		Expect(strings.Count(row, `class="provenance"`)).To(Equal(1))
	})

	It("a derived name renders no session-name span", func() {
		// Two different sessions, two records identical apart from the source.
		// ⚠️ The ids must differ: the registry is keyed by session id and a
		// duplicate id collides last-read-wins, which would leave both rows
		// resolving identically and the paired control unobservable.
		writeSession("102", "session-pair-user", "Shared Name", "user")
		writeSession("103", "session-pair-derived", "Shared Name", "derived")
		userItem := pushItem("session-pair-user")
		derivedItem := pushItem("session-pair-derived")

		body := get(buildPage()).Body.String()
		userRow := rowOf(body, userItem.ItemID)
		derivedRow := rowOf(body, derivedItem.ItemID)

		// Positive control on both rows: each carries its own item's payload, so
		// the pair rendered. This is what makes the zero below a withheld span
		// rather than an absent row — a run in which both rows carry zero spans is
		// the unfixed build and fails this criterion.
		Expect(userRow).To(ContainSubstring(userItem.Payload.String()))
		Expect(derivedRow).To(ContainSubstring(derivedItem.Payload.String()))
		// Exactly one of the two carries a span, and it is the `user` row.
		Expect(strings.Count(userRow, `class="session-name"`)).To(Equal(1))
		Expect(strings.Count(derivedRow, `class="session-name"`)).To(Equal(0))
	})

	It("a peer name renders no session-name span", func() {
		writeSession("104", "session-peer", "Peer Name", "peer")
		// ⚠️ The event log is required rather than incidental: with nothing else
		// resolved the provenance div never renders and the absence assertions
		// below would pass vacuously.
		writeEvents(
			"producer-session-peer",
			eventLine("session-name-session-peer", "session-peer", "burn", "/w/peer", "", ""),
		)
		item := pushItem("session-peer")

		row := rowOf(get(buildPage()).Body.String(), item.ItemID)

		// The line rendered, so the absences below are about a span that was
		// withheld rather than a row that was not drawn.
		Expect(strings.Count(row, `class="provenance"`)).To(Equal(1))
		Expect(strings.Count(row, `class="host"`)).To(Equal(1))
		Expect(strings.Count(row, `class="session-name"`)).To(Equal(0))
		// An unresolved value renders absent, never as a stand-in presented as
		// resolved — § Silence 7.
		Expect(row).NotTo(ContainSubstring("Peer Name"))
		Expect(row).NotTo(ContainSubstring("unknown"))
		Expect(row).NotTo(ContainSubstring("n/a"))
		// ⚠️ The `-` and session-id assertions are scoped to the provenance div,
		// and they must be. The row's own meta line renders
		// `{{ .Item.State }} - {{ .Item.CreatedAt }}`, so a bare dash is present on
		// every row of the board and a row-wide assertion on it could never hold;
		// and the row carries the item's ProducerID, which is
		// `producer-session-peer`, so a row-wide "does not contain the session id"
		// assertion could never hold either. The placeholder this criterion is
		// about would stand where a span would, which is inside the provenance
		// div, so the div is the scope that carries the claim. Do not "fix" this
		// back into a row-wide assertion.
		div := provenanceDivOf(row)
		Expect(div).NotTo(ContainSubstring("session-peer"))
		Expect(div).NotTo(ContainSubstring("unknown"))
		Expect(div).NotTo(ContainSubstring("-"))
	})

	It("an unknown session renders no session-name span", func() {
		// No registry record is written for this session at all.
		writeEvents(
			"producer-session-absent",
			eventLine("session-name-session-absent", "session-absent", "burn", "/w/absent", "", ""),
		)
		item := pushItem("session-absent")

		row := rowOf(get(buildPage()).Body.String(), item.ItemID)

		Expect(row).To(ContainSubstring(item.Payload.String()))
		Expect(strings.Count(row, `class="provenance"`)).To(Equal(1))
		Expect(strings.Count(row, `class="host"`)).To(Equal(1))
		Expect(strings.Count(row, `class="session-name"`)).To(Equal(0))
		Expect(row).NotTo(ContainSubstring("unknown"))
		Expect(row).NotTo(ContainSubstring("n/a"))
		// Same div-scoped reasoning as the peer case above: the row carries
		// `producer-session-absent` as its ProducerID and a dash in its meta line.
		div := provenanceDivOf(row)
		Expect(div).NotTo(ContainSubstring("session-absent"))
		Expect(div).NotTo(ContainSubstring("unknown"))
		Expect(div).NotTo(ContainSubstring("-"))
	})

	It("a name and nothing else still renders the provenance line", func() {
		// No event log, an empty pane listing and no vault task: the name is the
		// only fact this row resolves.
		writeSession("105", "session-name-only", "Lone Name", "user")
		item := pushItem("session-name-only")

		row := rowOf(get(buildPage()).Body.String(), item.ItemID)

		// ⚠️ This is the criterion that covers the spec's 186-row case: measured on
		// the live board 2026-09-30, 186 of 1,095 rows carried no provenance line
		// at all. Without the `SessionName` conjunct in pkg.Provenance.Resolved()
		// the name is resolved, drawn, and never appears, because the line that
		// would carry it is suppressed when nothing else resolves.
		Expect(strings.Count(row, `class="provenance"`)).To(Equal(1))
		Expect(strings.Count(row, `class="session-name"`)).To(Equal(1))
		Expect(row).To(ContainSubstring(`<span class="session-name">Lone Name</span>`))
	})

	It("reads the name at render time, so a rename shows on the next load", func() {
		writeSession("106", "session-renamed", "Before Rename", "user")
		item := pushItem("session-renamed")

		// ⚠️ The handler is built once and used for both loads. No restart, no
		// rebuild and no second handler: the render-time read is the design, and a
		// case that rebuilt the handler between loads would pass even for an
		// implementation that cached the name at construction.
		page := buildPage()

		first := rowOf(get(page).Body.String(), item.ItemID)
		Expect(first).To(ContainSubstring(`<span class="session-name">Before Rename</span>`))
		Expect(strings.Count(first, `class="session-name"`)).To(Equal(1))

		// The same pid, so the same file is overwritten.
		writeSession("106", "session-renamed", "After Rename", "user")

		second := rowOf(get(page).Body.String(), item.ItemID)
		Expect(second).To(ContainSubstring(`<span class="session-name">After Rename</span>`))
		// The first load's name is gone, not merely superseded.
		Expect(second).NotTo(ContainSubstring("Before Rename"))
	})

	It("keeps the task, goal and topic spans beside the session name", func() {
		writeBoardPolishVault("session-both")
		writeSession("107", "session-both", "Board Polish Session", "user")
		item := pushItem("session-both")

		row := rowOf(get(buildPage()).Body.String(), item.ItemID)

		// The four spans are independent: each is gated on its own resolved value,
		// none gating another.
		Expect(strings.Count(row, `class="task"`)).To(Equal(1))
		Expect(strings.Count(row, `class="goal"`)).To(Equal(1))
		Expect(strings.Count(row, `class="topic"`)).To(Equal(1))
		Expect(strings.Count(row, `class="session-name"`)).To(Equal(1))
	})

	It("keeps the task span and draws no name for a derived session", func() {
		writeBoardPolishVault("session-derived-task")
		writeSession("108", "session-derived-task", "Derived Name", "derived")
		item := pushItem("session-derived-task")

		row := rowOf(get(buildPage()).Body.String(), item.ItemID)

		// The gate must not disturb the other spans: the task, goal and topic
		// links survive a session whose name is withheld.
		Expect(strings.Count(row, `class="task"`)).To(Equal(1))
		Expect(strings.Count(row, `class="session-name"`)).To(Equal(0))
		Expect(strings.Count(row, `class="goal"`)).To(Equal(1))
		Expect(strings.Count(row, `class="topic"`)).To(Equal(1))
	})

	It("escapes a crafted name rather than rendering it as markup", func() {
		writeSession("109", "session-markup", "Fix <script>alert(1)</script> & board", "user")
		item := pushItem("session-markup")

		row := rowOf(get(buildPage()).Body.String(), item.ItemID)

		// ⚠️ The escaping is html/template's own, the same escaping the task span
		// already relies on, and the assertion is on the served bytes rather than
		// on any field: a field-level assertion passes whatever the template
		// emits.
		Expect(row).NotTo(ContainSubstring("<script>"))
		Expect(row).To(ContainSubstring(
			`&lt;script&gt;alert(1)&lt;/script&gt; &amp; board`,
		))
	})
})
