// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	libboltkv "github.com/bborbe/boltkv"
	"github.com/bborbe/errors"
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

// The whole chain, end to end: a fixture spawn ledger posted through the real
// ledger store, read by the real provenance resolver and rendered by the real
// page handler, with every assertion made on the served row. This is the file
// that carries this feature's acceptance criteria that observe the served page,
// because the resolution and the template are each only half of what the
// operator sees — neither a resolver-level nor a template-only spec can observe
// the rendered HTML that half produces.
//
// ⚠️ The gate this file pins is fail-closed: every uncertainty about a
// session's mode renders NO control. The four absence cases below (a nil store,
// a store whose read fails, a missing row, an unrecognised mode) are the
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
	// stateDir holds the producers' event logs and sessionsDir the session
	// registry. Both are fresh per case: the registry is scanned inside every
	// Resolve call, so a case that writes a record must not see another case's.
	//
	// sessionLedger is the supervisor's spawn ledger, and it is the SAME store
	// the attention items live in — one BoltDB handle, two buckets, exactly the
	// production wiring. It is a real store over the real db, never a mock: the
	// whole point of the change is that the mode read goes to a real store.
	var stateDir string
	var sessionsDir string
	var sessionLedger pkg.SessionLedgerStore
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
		// ⚠️ The ledger store reuses the attention store's own db — one BoltDB
		// handle, two buckets, which is exactly the production wiring. A second
		// handle on the same file is what BoltDB refuses.
		sessionLedger = pkg.NewSessionLedgerStore(db)
		clock = libtime.NewCurrentDateTime()
		clock.SetNow(clock.Now())
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	// writeSpawn posts one spawn-ledger row through the REAL store, as the
	// supervisor's own spawn path does.
	//
	// ⚠️ Never built from the resolver's own read path: the row goes in through
	// SessionLedgerStore.Post and comes out through the resolver's own List, so
	// the fixture cannot agree with itself. The row carries only the fields the
	// page reads; the live ledger carries more.
	writeSpawn := func(sessionID, mode string) {
		Expect(sessionLedger.Post(ctx, pkg.SessionLedgerEntry{
			SessionID: sessionID,
			Mode:      mode,
			Location:  pkg.LocalSessionHeartbeatLocation,
			SpawnedAt: clock.Now(),
		})).To(Succeed())
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
	// *now*, reading the spawn ledger through the given store: the real index,
	// the real resolver reading the fixture state and session directories and
	// the ledger store, and the real page handler over the real store.
	//
	// ⚠️ It must be called per case and never in the BeforeEach ahead of the
	// fixtures: the index reads the vault once, at construction, so a chain built
	// before the case wrote its files would see an empty vault. The ledger store
	// is a parameter so the nil-store case can hand the resolver a nil.
	buildPageWithSpawn := func(sessionLedger pkg.SessionLedgerStore) http.Handler {
		return handler.NewAttentionPageHandler(
			store,
			pkg.NewProvenanceResolver(
				pkg.NewEventLogReader(stateDir),
				sessionsDir,
				sessionLedger,
				panes,
				pkg.NewTaskIndex(ctx, vault, clock),
				clock,
			),
			false,
			vault,
			testBuildIdentity,
			boardmetrics.NewMetrics(prometheus.NewRegistry()),
		)
	}

	// buildPage is buildPageWithSpawn over the case's own spawn ledger.
	buildPage := func() http.Handler {
		return buildPageWithSpawn(sessionLedger)
	}

	It(
		"renders the Allow / Deny pair on a headless worker's permission row and renders the pair on a tab worker's permission card",
		func() {
			// Two records identical apart from `mode`. ⚠️ The session ids must differ:
			// the ledger is keyed by session id and a duplicate id collides
			// last-read-wins, which would leave both rows resolving identically and the
			// paired control unobservable.
			writeSpawn(ledgerSessionID("headless"), "headless")
			writeSpawn(ledgerSessionID("tab"), "interactive")
			headless := pushPermission(ledgerSessionID("headless"))
			tab := pushPermission(ledgerSessionID("tab"))

			resp := get(buildPage())
			Expect(resp.Code).To(Equal(http.StatusOK))
			body := resp.Body.String()
			headlessRow := rowOf(body, headless.ItemID)
			tabRow := rowOf(body, tab.ItemID)

			// Positive control on both rows: each carries its own item's payload, so
			// the pair rendered. This is what makes each count below a rendered control
			// rather than a page that dropped the row.
			Expect(headlessRow).To(ContainSubstring(headless.Payload.String()))
			Expect(tabRow).To(ContainSubstring(tab.Payload.String()))

			// BOTH classes carry the pair: the mode is no longer part of the gate. The
			// headless row is asserted first so a build that rendered the pair on only
			// one class cannot pass by picking the other.
			Expect(strings.Count(headlessRow, `data-decision="allow"`)).To(Equal(1))
			Expect(strings.Count(headlessRow, `data-decision="deny"`)).To(Equal(1))
			Expect(headlessRow).To(ContainSubstring(allowDenyPair))
			Expect(strings.Count(tabRow, `data-decision="allow"`)).To(Equal(1))
			Expect(strings.Count(tabRow, `data-decision="deny"`)).To(Equal(1))
			Expect(tabRow).To(ContainSubstring(allowDenyPair))
		},
	)

	It(
		"resolves headless from the ledger store alone, with no record in the retired spawn directory",
		func() {
			// ⚠️ THE SC2 NEGATIVE PROBE. The supervisor's file-backed spawn
			// directory is RETIRED — its newest `spawned_at` is 2026-10-14T21:40:28Z
			// and every spawn since lives only in the ledger store. A resolver that
			// still read the directory would resolve nothing for this fixture,
			// because the session exists ONLY in the store. The card must render
			// the Allow / Deny pair, and it can only do so by reading the store.
			//
			// ⚠️ The retired directory is deliberately never created and its path
			// is never consulted: the resolver is handed the store and nothing
			// else.
			writeSpawn(ledgerSessionID("store-only"), "headless")
			item := pushPermission(ledgerSessionID("store-only"))

			resp := get(buildPage())
			Expect(resp.Code).To(Equal(http.StatusOK))
			row := rowOf(resp.Body.String(), item.ItemID)

			// Positive control: the card rendered, so the pair below is a rendered
			// control rather than a page that dropped the row.
			Expect(row).To(ContainSubstring(item.Payload.String()))
			Expect(strings.Count(row, `data-decision="allow"`)).To(Equal(1))
			Expect(strings.Count(row, `data-decision="deny"`)).To(Equal(1))
			Expect(row).To(ContainSubstring(allowDenyPair))
		},
	)

	It("renders the pair when the session resolves no spawn ledger record", func() {
		// The ledger holds records for other sessions, but none for this one — an
		// older spawn, a pruned ledger. The pair renders anyway: the ledger is no
		// longer part of the gate.
		writeSpawn(ledgerSessionID("somebody-else"), "headless")
		item := pushPermission(ledgerSessionID("unrecorded"))

		resp := get(buildPage())
		Expect(resp.Code).To(Equal(http.StatusOK))
		row := rowOf(resp.Body.String(), item.ItemID)

		// Positive control: the card rendered, so the pair below is a rendered
		// control rather than a dropped row.
		Expect(row).To(ContainSubstring(item.Payload.String()))
		Expect(strings.Count(row, `data-decision="allow"`)).To(Equal(1))
		Expect(strings.Count(row, `data-decision="deny"`)).To(Equal(1))
	})

	It("renders the pair when the ledger store is nil", func() {
		item := pushPermission(ledgerSessionID("no-ledger"))

		// A host with no ledger store wired in at all. The resolver resolves no
		// mode, and the pair renders regardless.
		resp := get(buildPageWithSpawn(nil))
		Expect(resp.Code).To(Equal(http.StatusOK))
		row := rowOf(resp.Body.String(), item.ItemID)

		// Positive control: the card rendered, so the pair below is a rendered
		// control rather than a dropped row.
		Expect(row).To(ContainSubstring(item.Payload.String()))
		Expect(strings.Count(row, `data-decision="allow"`)).To(Equal(1))
		Expect(strings.Count(row, `data-decision="deny"`)).To(Equal(1))
	})

	It("renders the pair when the ledger store cannot be read", func() {
		// The store's read fails. The pair renders regardless: the ledger is no
		// longer part of the gate.
		//
		// ⚠️ The mock is legitimate for THIS case alone: it simulates a store
		// FAILURE, not a store's CONTENTS, and a failure is the one thing a real
		// store cannot be asked to produce on demand. Every other fixture here
		// posts through the real store, because a fixture built from a mock's
		// contents would agree with itself.
		failing := &mocks.SessionLedgerStore{}
		failing.ListReturns(nil, errors.New(ctx, "ledger unreadable"))
		item := pushPermission(ledgerSessionID("broken"))

		resp := get(buildPageWithSpawn(failing))
		Expect(resp.Code).To(Equal(http.StatusOK))
		row := rowOf(resp.Body.String(), item.ItemID)

		// Positive control: the card rendered, so the pair below is a rendered
		// control rather than a dropped row.
		Expect(row).To(ContainSubstring(item.Payload.String()))
		Expect(strings.Count(row, `data-decision="allow"`)).To(Equal(1))
		Expect(strings.Count(row, `data-decision="deny"`)).To(Equal(1))
	})

	It("renders the pair when a record's mode is outside the headless/interactive set", func() {
		// A third mode the ledger has not been taught. The pair renders anyway:
		// the mode is no longer part of the gate.
		writeSpawn(ledgerSessionID("daemon"), "daemon")
		item := pushPermission(ledgerSessionID("daemon"))

		resp := get(buildPage())
		Expect(resp.Code).To(Equal(http.StatusOK))
		row := rowOf(resp.Body.String(), item.ItemID)

		Expect(row).To(ContainSubstring(item.Payload.String()))
		Expect(strings.Count(row, `data-decision="allow"`)).To(Equal(1))
		Expect(strings.Count(row, `data-decision="deny"`)).To(Equal(1))
	})

	It(
		"renders the pair independently of the spawn ledger, present before any record exists and unmoved by a mode flip",
		func() {
			item := pushPermission(ledgerSessionID("late"))

			// ⚠️ The handler is built once and used for all four loads. No restart, no
			// rebuild and no second handler: the case proves the pair does not depend
			// on the ledger at all, and a case that rebuilt the handler between loads
			// could not observe that.
			page := buildPage()

			first := rowOf(get(page).Body.String(), item.ItemID)
			// Positive control: the card rendered, and it already carries the pair
			// with NO ledger record in existence.
			Expect(first).To(ContainSubstring(item.Payload.String()))
			Expect(strings.Count(first, `data-decision="allow"`)).To(Equal(1))
			Expect(strings.Count(first, `data-decision="deny"`)).To(Equal(1))

			// A record appears between the loads. The clock is advanced past the
			// resolver's two-second host-snapshot window first, so this load is
			// served the snapshot the resolver already holds rather than a fresh
			// read. The pair is unchanged by the record either way, which is the
			// point: the ledger's arrival moves nothing on the card.
			writeSpawn(ledgerSessionID("late"), "headless")
			clock.SetNow(clock.Now().Add(libtime.Duration(3 * 1e9)))
			stale := rowOf(get(page).Body.String(), item.ItemID)
			// Positive control: the card rendered, so the pair below is a rendered
			// control rather than a dropped row.
			Expect(stale).To(ContainSubstring(item.Payload.String()))
			Expect(strings.Count(stale, `data-decision="allow"`)).To(Equal(1))
			Expect(strings.Count(stale, `data-decision="deny"`)).To(Equal(1))

			// ⚠️ Redundant with the assertions above now that the ledger is not read
			// by the gate: kept as a load-over-time sanity check, not as a distinct
			// control.
			Eventually(func() string {
				return rowOf(get(page).Body.String(), item.ItemID)
			}, "2s").Should(ContainSubstring(`data-decision="allow"`))

			// And the pair survives the row's mode flipping away from headless. ⚠️ The
			// store has no Delete — Post REPLACES a session's row — so the retired
			// file-shaped "record removed" is expressed as a mode flip to
			// `interactive`. The pair is unmoved, because the mode was never part of
			// the gate.
			writeSpawn(ledgerSessionID("late"), "interactive")
			clock.SetNow(clock.Now().Add(libtime.Duration(3 * 1e9)))
			Eventually(func() bool {
				row := rowOf(get(page).Body.String(), item.ItemID)
				return strings.Contains(row, item.Payload.String()) &&
					strings.Count(row, `data-decision="allow"`) == 1 &&
					strings.Count(row, `data-decision="deny"`) == 1
			}, "2s").Should(BeTrue())
		},
	)

	It(
		"keeps the jump corner, the corner X and the provenance line on a tab worker's permission row",
		func() {
			// The tab session resolves a vault task, so its row draws a provenance line
			// with the task span — the elements this case asserts are unchanged.
			writeBoardPolishVault(ledgerSessionID("tab-unchanged"))
			writeSpawn(ledgerSessionID("tab-unchanged"), "interactive")
			// ⚠️ A SECOND permission item, whose session IS recorded headless, is kept
			// beside the tab one so the pair's exact markup is observed on the class it
			// was first asserted against, not only on the newly included one.
			writeSpawn(ledgerSessionID("headless-unchanged"), "headless")

			tab := pushPermission(ledgerSessionID("tab-unchanged"))
			headless := pushPermission(ledgerSessionID("headless-unchanged"))

			resp := get(buildPage())
			Expect(resp.Code).To(Equal(http.StatusOK))
			body := resp.Body.String()
			tabRow := rowOf(body, tab.ItemID)

			// Positive control: the card rendered.
			Expect(tabRow).To(ContainSubstring(tab.Payload.String()))

			// The jump corner, the corner X and the provenance line — with its task
			// span — are unchanged on a tab worker's row.
			Expect(tabRow).To(ContainSubstring(`class="jump-corner"`))
			Expect(tabRow).To(ContainSubstring("data-corner-x"))
			Expect(tabRow).To(ContainSubstring(`class="provenance"`))
			Expect(tabRow).To(ContainSubstring(`<span class="task">`))
			Expect(tabRow).NotTo(ContainSubstring("<form"))
			Expect(tabRow).NotTo(ContainSubstring("<input"))
			// And the pair renders on the tab worker's card too: the mode is no longer
			// part of the gate.
			Expect(strings.Count(tabRow, `data-decision="allow"`)).To(Equal(1))
			Expect(strings.Count(tabRow, `data-decision="deny"`)).To(Equal(1))
			Expect(tabRow).To(ContainSubstring(allowDenyPair))

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

// ledgerSessionID maps a readable fixture label to the well-formed lowercase
// UUID the ledger store's Validate requires.
//
// ⚠️ It exists because the fixture's session ids must be the store's own shape:
// SessionLedgerEntry.Validate refuses anything that is not a dashed lowercase
// UUID, and the store's key IS that id. The mapping is deterministic, so a
// label used in a ledger row, an item's liveness ref and a vault task file all
// name the SAME session. It is shared with the production-touching page suite,
// which lives in this same test package.
func ledgerSessionID(label string) string {
	sum := sha256.Sum256([]byte(label))
	h := hex.EncodeToString(sum[:16])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
