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

// The whole chain, end to end, for the production-touching exclusion: fixture
// task files on disk, read by the real task index, resolved by the real
// provenance resolver and rendered by the real page handler, with every
// assertion made on the served row. This is the file that carries this
// feature's acceptance criteria that observe the served page, because the
// resolution and the template are each only half of what the operator sees —
// neither a resolver-level nor a template-only spec can observe the rendered
// HTML that half produces.
//
// ⚠️ The gate this file pins is fail-OPEN, the opposite polarity to the
// sibling headless file: every uncertainty about the marker renders the pair.
// The no-task case below is the evidence for that invariant, and every
// absence-shaped case is preceded by a positive control so it cannot pass on a
// page that dropped the row.
var _ = Describe("the production-touching marker on the served page", func() {
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

	// writeTask writes one task file under <vault>/25 Tasks/, as raw text rather
	// than through a parser: the fixture is the *file shape* the vault holds, so a
	// rename in the reader cannot make a fixture agree with itself.
	writeTask := func(name, content string) {
		path := filepath.Join(vault, "25 Tasks")
		Expect(os.MkdirAll(path, 0o750)).To(BeNil())
		Expect(os.WriteFile(filepath.Join(path, name), []byte(content), 0o600)).To(BeNil())
	}

	// taskContent is a task file recording sessionID, with body as its text.
	taskContent := func(sessionID, body string) string {
		return "---\nclaude_session_id: " + sessionID + "\nstatus: active\n---\n\n# Steps\n\n" + body + "\n"
	}

	// writeSpawn writes one spawn-ledger record as `<spawnDir>/<sessionID>.json`,
	// as raw JSON text — never marshalled from the resolver's own struct.
	writeSpawn := func(sessionID, mode string) {
		Expect(os.WriteFile(
			filepath.Join(spawnDir, sessionID+".json"),
			[]byte(`{"session_id":"`+sessionID+`","mode":"`+mode+`"}`),
			0o600,
		)).To(BeNil())
	}

	// pushPark pushes one `permission` item whose session id is recoverable by
	// pkg.sessionIDFromItem from the `session:<id>` marker on the LivenessRef. The
	// dedup key is a parameter because a single session raises several distinct
	// parks, and the payload carries it so a row's positive control names its own
	// park.
	pushPark := func(sessionID, dedupKey string) *pkg.Item {
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      pkg.ProducerID("producer-" + sessionID),
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:" + sessionID),
			DedupKey:        pkg.DedupKey(dedupKey),
			InterruptClass:  "approve",
			Payload:         pkg.Payload("Write: /tmp/" + dedupKey),
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

	// buildPage assembles the real chain over the vault as it stands *now*: the
	// real index, the real resolver reading the fixture state, session and spawn
	// directories, and the real page handler over the real store.
	//
	// ⚠️ It must be called per case and never in the BeforeEach ahead of the
	// fixtures: the index reads the vault once, at construction, so a chain built
	// before the case wrote its files would see an empty vault.
	buildPage := func() http.Handler {
		return handler.NewAttentionPageHandler(
			store,
			pkg.NewProvenanceResolver(
				pkg.NewEventLogReader(stateDir),
				sessionsDir,
				spawnDir,
				panes,
				pkg.NewTaskIndex(ctx, vault, clock),
				clock,
			),
			false,
			vault,
			testBuildIdentity,
		)
	}

	It("withholds the pair when the task declares a production-touching step", func() {
		// Three tasks, two of them marked. ⚠️ The paired probe: `Marked A` raises
		// TWO parks, so a task-level reading withholds the pair from both — a
		// command-level reading would withhold it from neither, since the payloads
		// differ. `Marked B` carries the same marker in a *differently named* file,
		// so the reading follows the marker's content rather than the task's name.
		writeTask("Marked A.md", taskContent("session-marked-a",
			"- [ ] ⚠️ production-touching — `make install`"))
		writeTask("Marked B.md", taskContent("session-marked-b",
			"- [ ] ⚠️ production-touching — `make install`"))
		writeTask("Plain C.md", taskContent("session-plain-c", "- [ ] ordinary step"))
		writeSpawn("session-marked-a", "headless")
		writeSpawn("session-marked-b", "headless")
		writeSpawn("session-plain-c", "headless")

		parkA1 := pushPark("session-marked-a", "park-a-1")
		parkA2 := pushPark("session-marked-a", "park-a-2")
		parkB1 := pushPark("session-marked-b", "park-b-1")
		parkC1 := pushPark("session-plain-c", "park-c-1")

		resp := get(buildPage())
		Expect(resp.Code).To(Equal(http.StatusOK))
		body := resp.Body.String()

		// Each marked row carries its own payload — the positive control that the
		// card rendered — and carries no answering control at all.
		for _, item := range []*pkg.Item{parkA1, parkA2, parkB1} {
			row := rowOf(body, item.ItemID)
			Expect(row).To(ContainSubstring(item.Payload.String()))
			Expect(strings.Count(row, "data-decision=")).To(Equal(0))
		}

		// ⚠️ The unmarked row is what stops an absence-shaped pass: a build that
		// renders no control anywhere would satisfy the three zeros above by doing
		// nothing. This row must carry the pair.
		plainRow := rowOf(body, parkC1.ItemID)
		Expect(plainRow).To(ContainSubstring(parkC1.Payload.String()))
		Expect(strings.Count(plainRow, `data-decision="allow"`)).To(Equal(1))
		Expect(strings.Count(plainRow, `data-decision="deny"`)).To(Equal(1))
	})

	It("renders the pair when the phrase appears only in prose", func() {
		// The precision case that stops a substring matcher: the phrase appears in
		// a Success Criteria line, a Progress paragraph and a non-checkbox bullet —
		// and in none of them in the marker form.
		writeTask("Prose.md", taskContent("session-prose",
			"# Success Criteria\n\n"+
				"- [ ] SC2: a production-touching park renders the Allow / Deny pair\n\n"+
				"# Progress\n\n"+
				"The production-touching marker is read from the task file the resolver already opens.\n\n"+
				"- The production-touching marker is authored by the operator"))
		writeSpawn("session-prose", "headless")
		item := pushPark("session-prose", "park-prose")

		resp := get(buildPage())
		Expect(resp.Code).To(Equal(http.StatusOK))
		row := rowOf(resp.Body.String(), item.ItemID)

		// ⚠️ A build that matches the bare phrase suppresses the pair here and fails.
		Expect(row).To(ContainSubstring(item.Payload.String()))
		Expect(strings.Count(row, `data-decision="allow"`)).To(Equal(1))
		Expect(strings.Count(row, `data-decision="deny"`)).To(Equal(1))
	})

	// ⚠️ The pinned acceptance-criteria substrings for this case and its two
	// neighbours below name the PAIR-RENDERS half, while the behaviour requires the
	// pair to be WITHHELD on the marked row. The leading `guards against: ` clause
	// is the defect the case exists to catch; the trailing clause states the
	// assertion beside it. Each case renders both the marked row and an unmarked
	// sibling, so the pair-renders half is observable on the sibling rather than
	// asserted nowhere.
	It(
		"guards against: renders the pair when the marker sits on a checked box — the pair is withheld on the marked row",
		func() {
			// The `[x]` state-class probe: the sibling proves the state alone does not
			// withhold the pair; only the marker does.
			writeTask("Checked.md", taskContent("session-checked",
				"- [x] ⚠️ production-touching — `make install`"))
			writeTask("Unmarked Checked.md", taskContent("session-unmarked-checked",
				"- [x] ordinary step"))
			writeSpawn("session-checked", "headless")
			writeSpawn("session-unmarked-checked", "headless")
			checked := pushPark("session-checked", "park-checked")
			unmarked := pushPark("session-unmarked-checked", "park-unmarked-checked")

			resp := get(buildPage())
			Expect(resp.Code).To(Equal(http.StatusOK))
			body := resp.Body.String()

			checkedRow := rowOf(body, checked.ItemID)
			Expect(checkedRow).To(ContainSubstring(checked.Payload.String()))
			Expect(strings.Count(checkedRow, "data-decision=")).To(Equal(0))

			unmarkedRow := rowOf(body, unmarked.ItemID)
			Expect(unmarkedRow).To(ContainSubstring(unmarked.Payload.String()))
			Expect(strings.Count(unmarkedRow, `data-decision="allow"`)).To(Equal(1))
			Expect(strings.Count(unmarkedRow, `data-decision="deny"`)).To(Equal(1))
		},
	)

	It(
		"guards against: renders the pair when the marker omits the warning glyph — the pair is withheld on the marked row",
		func() {
			// The glyph-absent probe: the warning glyph is optional as a whole, so a
			// marker written without it still withholds the pair.
			writeTask("Glyphless.md", taskContent("session-glyphless",
				"- [ ] production-touching — `make install`"))
			writeTask("Unmarked Glyphless.md", taskContent("session-unmarked-glyphless",
				"- [ ] ordinary step"))
			writeSpawn("session-glyphless", "headless")
			writeSpawn("session-unmarked-glyphless", "headless")
			glyphless := pushPark("session-glyphless", "park-glyphless")
			unmarked := pushPark("session-unmarked-glyphless", "park-unmarked-glyphless")

			resp := get(buildPage())
			Expect(resp.Code).To(Equal(http.StatusOK))
			body := resp.Body.String()

			glyphlessRow := rowOf(body, glyphless.ItemID)
			Expect(glyphlessRow).To(ContainSubstring(glyphless.Payload.String()))
			Expect(strings.Count(glyphlessRow, "data-decision=")).To(Equal(0))

			unmarkedRow := rowOf(body, unmarked.ItemID)
			Expect(unmarkedRow).To(ContainSubstring(unmarked.Payload.String()))
			Expect(strings.Count(unmarkedRow, `data-decision="allow"`)).To(Equal(1))
			Expect(strings.Count(unmarkedRow, `data-decision="deny"`)).To(Equal(1))
		},
	)

	It(
		"guards against: renders the pair when the marker sits on a slash box — the pair is withheld on the marked row",
		func() {
			// The `[/]` state-class probe: the state the vault writes for in-progress
			// subtasks. The marker is recognised on it exactly as on an open box.
			writeTask("Slash.md", taskContent("session-slash",
				"- [/] ⚠️ production-touching — `make install`"))
			writeTask("Unmarked Slash.md", taskContent("session-unmarked-slash",
				"- [/] ordinary step"))
			writeSpawn("session-slash", "headless")
			writeSpawn("session-unmarked-slash", "headless")
			slash := pushPark("session-slash", "park-slash")
			unmarked := pushPark("session-unmarked-slash", "park-unmarked-slash")

			resp := get(buildPage())
			Expect(resp.Code).To(Equal(http.StatusOK))
			body := resp.Body.String()

			slashRow := rowOf(body, slash.ItemID)
			Expect(slashRow).To(ContainSubstring(slash.Payload.String()))
			Expect(strings.Count(slashRow, "data-decision=")).To(Equal(0))

			unmarkedRow := rowOf(body, unmarked.ItemID)
			Expect(unmarkedRow).To(ContainSubstring(unmarked.Payload.String()))
			Expect(strings.Count(unmarkedRow, `data-decision="allow"`)).To(Equal(1))
			Expect(strings.Count(unmarkedRow, `data-decision="deny"`)).To(Equal(1))
		},
	)

	It("renders the pair when the session resolves no task", func() {
		// The fail-open case: no task file at all for this session. Every
		// uncertainty about the marker leaves it false, which renders the pair.
		writeSpawn("session-no-task", "headless")
		item := pushPark("session-no-task", "park-no-task")

		resp := get(buildPage())
		Expect(resp.Code).To(Equal(http.StatusOK))
		row := rowOf(resp.Body.String(), item.ItemID)

		Expect(row).To(ContainSubstring(item.Payload.String()))
		Expect(strings.Count(row, `data-decision="allow"`)).To(Equal(1))
		Expect(strings.Count(row, `data-decision="deny"`)).To(Equal(1))
	})

	It(
		"keeps the jump corner, the corner X and the provenance line on a marked row, and renders the frozen pair on an unmarked row",
		func() {
			// The unchanged-elements guard: withholding the pair must not remove the
			// clearing control, the jump corner or the provenance line — with its task
			// span — from a marked row.
			writeTask("Marked Elements.md", taskContent("session-elements-marked",
				"- [ ] ⚠️ production-touching — `make install`"))
			writeTask("Plain Elements.md", taskContent("session-elements-plain",
				"- [ ] ordinary step"))
			writeSpawn("session-elements-marked", "headless")
			writeSpawn("session-elements-plain", "headless")
			marked := pushPark("session-elements-marked", "park-elements-marked")
			plain := pushPark("session-elements-plain", "park-elements-plain")

			resp := get(buildPage())
			Expect(resp.Code).To(Equal(http.StatusOK))
			body := resp.Body.String()

			markedRow := rowOf(body, marked.ItemID)
			Expect(markedRow).To(ContainSubstring(marked.Payload.String()))
			Expect(markedRow).To(ContainSubstring(`class="jump-corner"`))
			Expect(markedRow).To(ContainSubstring("data-corner-x"))
			Expect(markedRow).To(ContainSubstring(`class="provenance"`))
			Expect(markedRow).To(ContainSubstring(`<span class="task">`))
			Expect(markedRow).NotTo(ContainSubstring("<form"))
			Expect(markedRow).NotTo(ContainSubstring("<input"))
			Expect(strings.Count(markedRow, "data-decision=")).To(Equal(0))

			plainRow := rowOf(body, plain.ItemID)
			Expect(plainRow).To(ContainSubstring(plain.Payload.String()))
			Expect(plainRow).To(ContainSubstring(allowDenyPair))
		},
	)
})
