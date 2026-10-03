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

// The whole chain, end to end: a fixture spawn ledger on disk, read by the real
// provenance resolver and rendered by the real page handler, with every
// assertion made on the served row. This is the file that carries this
// feature's acceptance criteria that observe the served page, because the
// resolution and the template are each only half of what the operator sees —
// neither a resolver-level nor a template-only spec can observe the rendered
// HTML that half produces.
//
// ⚠️ The gate this file pins is fail-closed: every uncertainty about a
// session's mode renders NO control. The four absence cases below (no record, a
// missing directory, an unparseable record, an unrecognised mode) are the
// evidence for that invariant, and each is preceded by a positive control so an
// absence-shaped case cannot pass on a page that dropped the row.
var _ = Describe("the Allow / Deny pair on the served page", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var sessionLivenessChecker *mocks.SessionLivenessChecker
	var panes *mocks.PaneLister
	// vault ends in a known name, so the served task href's `vault=` value is the
	// hand-written literal `Personal` rather than a value read back from the same
	// helper the code uses.
	var vault string
	// stateDir holds the producers' event logs, sessionsDir the session registry
	// and spawnDir the supervisor's spawn ledger. All three are fresh per case:
	// the ledger is scanned inside every Resolve call, so a case that writes a
	// record must not see another case's.
	var stateDir string
	var sessionsDir string
	var spawnDir string
	// clock drives the provenance resolver's host-snapshot cache. Frozen in
	// BeforeEach so the cases that change the ledger between loads can advance
	// it with SetNow rather than sleeping past the cache window.
	var clock libtime.CurrentDateTime

	// The Allow / Deny control's identity is frozen, and this is the exact markup
	// the template emits for it. Written as a hand-written literal, never built
	// with a helper the production code uses: a shared helper would agree with
	// itself whatever it produced.
	const allowDenyPair = `<div class="actions"><button type="button" class="dismiss" data-decision="deny">✕ Deny</button><button type="button" class="next" data-decision="allow">✓ Allow</button></div>`

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
		clock = libtime.NewCurrentDateTime()
		clock.SetNow(clock.Now())
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	// writeSpawn writes one spawn-ledger record as `<spawnDir>/<sessionID>.json`.
	//
	// ⚠️ Written as raw JSON text, never marshalled from spawnRecord: the fixture
	// must be the *file shape* the supervisor's ledger actually holds, so a field
	// rename in the resolver's own struct cannot make the fixture agree with
	// itself. The record carries only the two fields the page reads; the live
	// ledger carries more.
	writeSpawn := func(sessionID, mode string) {
		Expect(os.WriteFile(
			filepath.Join(spawnDir, sessionID+".json"),
			[]byte(`{"session_id":"`+sessionID+`","mode":"`+mode+`"}`),
			0o600,
		)).To(BeNil())
	}

	// writeVault writes one fixture file under <vault>/<dir>/, as raw text rather
	// than through a parser: the fixture is the *file shape* the vault actually
	// holds, so a rename in the reader cannot make a fixture agree with itself.
	writeVault := func(vault, dir, name, content string) {
		path := filepath.Join(vault, dir)
		Expect(os.MkdirAll(path, 0o750)).To(BeNil())
		Expect(os.WriteFile(filepath.Join(path, name), []byte(content), 0o600)).To(BeNil())
	}

	// writeBoardPolishVault writes a vault whose task names a goal a topic lists.
	// It mirrors the sibling helper of the same name in
	// attention-session-name-page_test.go, and sessionID parameterises the task
	// file so the row resolves a task for exactly one session.
	writeBoardPolishVault := func(sessionID string) {
		writeVault(vault, "25 Tasks", "Board Polish.md",
			"---\nclaude_session_id: "+sessionID+"\ngoals:\n  - \"[[First Goal]]\"\n---\n")
		writeVault(vault, "24 Goals", "First Goal.md", "---\ntitle: First Goal\n---\n")
		writeVault(vault, "23 Topics", "Attention Board Polish.md",
			"---\ntitle: Attention Board Polish\n---\n\n## Goals\n\n- [[First Goal]]\n")
	}

	// pushPermission pushes one `permission` declaration whose session id is
	// recoverable by pkg.sessionIDFromItem, which reads the `session:<id>` marker
	// on the LivenessRef. The item carries no session field of its own.
	pushPermission := func(sessionID string) *pkg.Item {
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      pkg.ProducerID("producer-" + sessionID),
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:" + sessionID),
			DedupKey:        pkg.DedupKey("headless-" + sessionID),
			InterruptClass:  "approve",
			Payload:         pkg.Payload("Write: /tmp/headless-fixture"),
			AnswerMechanism: pkg.PermissionAnswerMechanism,
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

	// get renders the page through the given handler and returns the recorder.
	get := func(httpHandler http.Handler) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/", nil)
		resp := httptest.NewRecorder()
		httpHandler.ServeHTTP(resp, req)
		return resp
	}

	// buildPageWithSpawn assembles the real chain over the vault as it stands
	// *now*, reading the spawn ledger at the given directory: the real index, the
	// real resolver reading the fixture state, session and spawn directories, and
	// the real page handler over the real store.
	//
	// ⚠️ It must be called per case and never in the BeforeEach ahead of the
	// fixtures: the index reads the vault once, at construction, so a chain built
	// before the case wrote its files would see an empty vault. The spawn dir is a
	// parameter so the missing-directory case can point the resolver at a path
	// that does not exist.
	buildPageWithSpawn := func(spawn string) http.Handler {
		return handler.NewAttentionPageHandler(
			store,
			pkg.NewProvenanceResolver(
				stateDir,
				sessionsDir,
				spawn,
				panes,
				pkg.NewTaskIndex(ctx, vault),
				clock,
			),
			false,
			vault,
			testBuildIdentity,
		)
	}

	// buildPage is buildPageWithSpawn over the case's own spawn ledger.
	buildPage := func() http.Handler {
		return buildPageWithSpawn(spawnDir)
	}

	It(
		"renders the Allow / Deny pair on a headless worker's permission row and no control on a tab worker's",
		func() {
			// Two records identical apart from `mode`. ⚠️ The session ids must differ:
			// the ledger is keyed by session id and a duplicate id collides
			// last-read-wins, which would leave both rows resolving identically and the
			// paired control unobservable.
			writeSpawn("session-headless", "headless")
			writeSpawn("session-tab", "interactive")
			headless := pushPermission("session-headless")
			tab := pushPermission("session-tab")

			resp := get(buildPage())
			Expect(resp.Code).To(Equal(http.StatusOK))
			body := resp.Body.String()
			headlessRow := rowOf(body, headless.ItemID)
			tabRow := rowOf(body, tab.ItemID)

			// Positive control on both rows: each carries its own item's payload, so
			// the pair rendered. This is what makes the zero below a withheld control
			// rather than an absent row — a run in which both rows carry zero
			// data-decision attributes is the unfixed build and fails here.
			Expect(headlessRow).To(ContainSubstring(headless.Payload.String()))
			Expect(tabRow).To(ContainSubstring(tab.Payload.String()))

			// Exactly one of the two carries the pair, and it is the headless row. The
			// positive assertion is what stops an absence-shaped pass.
			Expect(strings.Count(headlessRow, `data-decision="allow"`)).To(Equal(1))
			Expect(strings.Count(headlessRow, `data-decision="deny"`)).To(Equal(1))
			Expect(headlessRow).To(ContainSubstring(allowDenyPair))
			Expect(strings.Count(tabRow, `data-decision=`)).To(Equal(0))
		},
	)

	It("renders no control when the session has no spawn ledger record", func() {
		// The ledger holds records for other sessions, but none for this one — an
		// older spawn, a pruned ledger. The row is a tab worker's by default.
		writeSpawn("session-somebody-else", "headless")
		item := pushPermission("session-unrecorded")

		resp := get(buildPage())
		Expect(resp.Code).To(Equal(http.StatusOK))
		row := rowOf(resp.Body.String(), item.ItemID)

		// Positive control: the card rendered, so the absence below is a withheld
		// control rather than a dropped row.
		Expect(row).To(ContainSubstring(item.Payload.String()))
		Expect(strings.Count(row, `data-decision=`)).To(Equal(0))
	})

	It("renders no control when the spawn ledger directory does not exist", func() {
		item := pushPermission("session-no-ledger")

		// A host with no claude-supervisor: the configured directory is absent.
		resp := get(buildPageWithSpawn(filepath.Join(spawnDir, "does-not-exist")))
		Expect(resp.Code).To(Equal(http.StatusOK))
		row := rowOf(resp.Body.String(), item.ItemID)

		Expect(row).To(ContainSubstring(item.Payload.String()))
		Expect(strings.Count(row, `data-decision=`)).To(Equal(0))
	})

	It("renders no control when a spawn ledger record does not parse", func() {
		// Truncated JSON: the file exists but is mid-write or corrupt. Written as
		// raw bytes rather than through writeSpawn, because the point is that the
		// record does not parse.
		Expect(os.WriteFile(
			filepath.Join(spawnDir, "session-broken.json"),
			[]byte(`{"session_id":"session-broken","mode":`),
			0o600,
		)).To(BeNil())
		item := pushPermission("session-broken")

		resp := get(buildPage())
		Expect(resp.Code).To(Equal(http.StatusOK))
		row := rowOf(resp.Body.String(), item.ItemID)

		Expect(row).To(ContainSubstring(item.Payload.String()))
		Expect(strings.Count(row, `data-decision=`)).To(Equal(0))
	})

	It("renders no control when a record's mode is outside the headless/interactive set", func() {
		// A third mode the ledger has not been taught. Fail-closed means it reads
		// as not headless: the operator loses a control rather than gaining a
		// laundering one.
		writeSpawn("session-daemon", "daemon")
		item := pushPermission("session-daemon")

		resp := get(buildPage())
		Expect(resp.Code).To(Equal(http.StatusOK))
		row := rowOf(resp.Body.String(), item.ItemID)

		Expect(row).To(ContainSubstring(item.Payload.String()))
		Expect(strings.Count(row, `data-decision=`)).To(Equal(0))
	})

	It(
		"reads the spawn ledger at render time, so a record added between loads adds the control",
		func() {
			item := pushPermission("session-late")

			// ⚠️ The handler is built once and used for all three loads. No restart, no
			// rebuild and no second handler: the render-time read is the design, and a
			// case that rebuilt the handler between loads would pass even for an
			// implementation that cached the ledger at construction.
			page := buildPage()

			first := rowOf(get(page).Body.String(), item.ItemID)
			// Positive control: the card rendered, so the zero below is a withheld
			// control rather than a dropped row.
			Expect(first).To(ContainSubstring(item.Payload.String()))
			Expect(strings.Count(first, `data-decision=`)).To(Equal(0))

			// The record appears between the two loads. The clock is advanced past
			// the resolver's two-second host-snapshot window first: the ledger is
			// read at render time, but it is served from that cache inside the
			// window, so without this the second load would legitimately still see
			// the pre-change ledger.
			writeSpawn("session-late", "headless")
			clock.SetNow(clock.Now().Add(libtime.Duration(3 * 1e9)))
			second := rowOf(get(page).Body.String(), item.ItemID)
			Expect(second).To(ContainSubstring(item.Payload.String()))
			Expect(second).To(ContainSubstring(`data-decision="allow"`))

			// And disappears again: the control is gone on the third load.
			Expect(os.Remove(filepath.Join(spawnDir, "session-late.json"))).To(BeNil())
			clock.SetNow(clock.Now().Add(libtime.Duration(3 * 1e9)))
			third := rowOf(get(page).Body.String(), item.ItemID)
			Expect(third).To(ContainSubstring(item.Payload.String()))
			Expect(strings.Count(third, `data-decision=`)).To(Equal(0))
		},
	)

	It(
		"keeps the jump corner, the corner X and the provenance line on a tab worker's permission row",
		func() {
			// The tab session resolves a vault task, so its row draws a provenance line
			// with the task span — the elements this case asserts are unchanged.
			writeBoardPolishVault("session-tab-unchanged")
			writeSpawn("session-tab-unchanged", "interactive")
			// ⚠️ A SECOND permission item, whose session IS recorded headless, is
			// required: without a headless row there is no row carrying the pair, and
			// the exact-markup assertion below would have nothing to observe.
			writeSpawn("session-headless-unchanged", "headless")

			tab := pushPermission("session-tab-unchanged")
			headless := pushPermission("session-headless-unchanged")

			resp := get(buildPage())
			Expect(resp.Code).To(Equal(http.StatusOK))
			body := resp.Body.String()
			tabRow := rowOf(body, tab.ItemID)

			// Positive control: the card rendered.
			Expect(tabRow).To(ContainSubstring(tab.Payload.String()))

			// The jump corner, the corner X and the provenance line — with its task
			// span — are unchanged on a tab worker's row. The X is a CLEARING control,
			// not an answering one, so the gate does not reach it.
			Expect(tabRow).To(ContainSubstring(`class="jump-corner"`))
			Expect(tabRow).To(ContainSubstring("data-corner-x"))
			Expect(tabRow).To(ContainSubstring(`class="provenance"`))
			Expect(tabRow).To(ContainSubstring(`<span class="task">`))
			Expect(tabRow).NotTo(ContainSubstring("<form"))
			Expect(tabRow).NotTo(ContainSubstring("<input"))
			// And no answering control: the pair is withheld from the tab worker.
			Expect(strings.Count(tabRow, `data-decision=`)).To(Equal(0))

			// The headless row carries the pair's markup exactly, unchanged from the
			// pre-change revision.
			headlessRow := rowOf(body, headless.ItemID)
			Expect(headlessRow).To(ContainSubstring(headless.Payload.String()))
			Expect(headlessRow).To(ContainSubstring(allowDenyPair))
		},
	)

	// ⚠️ A SOURCE-PRESENCE guard, like its neighbours above: the inline script has
	// no unit harness, so this reads the served page rather than clicking it. The
	// operator-visible half is the browser observation recorded on the task.
	It(
		"hands the decision path the failure's message, never the answerFailure object",
		func() {
			body := get(buildPage()).Body.String()

			// ⚠️ Asserted as an ABSENCE, and that absence is the whole guard. The
			// defect is a WRONG ARGUMENT, not a missing branch: answerFailure
			// returns an object, showCloseNote's contract is a string, so
			// textContent coerced it and the card rendered the literal
			// [object Object] for every code — the store's own code and message
			// dropped with it. A presence check for the corrected call proves
			// nothing here, because the form path and replayFailure already emit
			// `showCloseNote(row, failure.message, true)` elsewhere on this same
			// page: a build that added the new branch BESIDE the object-passing
			// line would satisfy it and still render [object Object] on every
			// failed Allow / Deny.
			Expect(body).NotTo(
				ContainSubstring("showCloseNote(row, answerFailure(body), true)"),
				"the decision path must not hand showCloseNote the answerFailure object",
			)

			// The two arms it carries instead. The raw-body line is unique to this
			// call site — the form path renders through showNote(form, …) — so it
			// pins the decision path's fallback rather than a call string shared
			// with the other note helpers.
			Expect(body).To(ContainSubstring(
				"showCloseNote(row, 'Answer failed - HTTP ' + response.status + ' - ' + body, true)",
			))
		},
	)
})
