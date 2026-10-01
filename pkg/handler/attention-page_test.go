// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"

	libboltkv "github.com/bborbe/boltkv"
	libhttp "github.com/bborbe/http"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/handler"
)

var _ = Describe("AttentionPageHandler", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var sessionLivenessChecker *mocks.SessionLivenessChecker
	var provenance *mocks.ProvenanceResolver
	var httpHandler http.Handler
	// vaultDir ends in a known name so the task link's expected href can be a
	// hand-written literal. The directory itself need not exist: the handler only
	// reads its base name, and the provenance is mocked.
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
		// pruned during Read and the page renders empty — the empty-store case
		// would pass vacuously and the positive assertions would fail. Pinning it
		// to true is what makes the pruning real rather than incidental.
		sessionLivenessChecker = &mocks.SessionLivenessChecker{}
		sessionLivenessChecker.IsLiveReturns(true)

		store = pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			sessionLivenessChecker,
			libtime.NewCurrentDateTime(),
			libtime.Duration(15*60*1e9),
		)

		// Left returning nil, so every existing case exercises the no-provenance
		// path — which is the degradation this page must keep: a row whose
		// provenance cannot be resolved renders no provenance line at all.
		provenance = &mocks.ProvenanceResolver{}

		// An empty token path never resolves, so the page renders no Jump
		// button — the fail-soft path, which is what a host with no fleet-jump
		// server looks like. The button's own cases live in attention-jump_test.
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
		// bbolt's Close is idempotent, so the read-failure case closing the db
		// first does not turn this into a second-close failure.
		Expect(db.Close()).To(BeNil())
	})

	// pushRequest builds a declaration whose liveness model matches its producer
	// id and whose answer mechanism makes it a question rather than a report, so
	// the pruning Read performs is exercised for every fixture.
	pushRequest := func(producerID pkg.ProducerID, dedupKey pkg.DedupKey, payload pkg.Payload) pkg.PushRequest {
		return pkg.PushRequest{
			ProducerID:      producerID,
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:" + producerID.String()),
			DedupKey:        dedupKey,
			InterruptClass:  "approve",
			Payload:         payload,
			AnswerMechanism: pkg.MessageAnswerMechanism,
		}
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
	// grep would pass on a page where the wrong item carried the controls.
	rowOf := func(body string, itemID pkg.ItemID) string {
		start := strings.Index(body, `data-item-id="`+itemID.String()+`"`)
		Expect(start).To(BeNumerically(">=", 0), "row for %s not found", itemID)
		rest := body[start:]
		end := strings.Index(rest, "</li>")
		Expect(end).To(BeNumerically(">=", 0))
		return rest[:end]
	}

	// messageRequest builds a `message` declaration whose question shape the
	// caller supplies, so the card's control can be driven from the declared
	// cardinality rather than from the option count. The fixtures above carry no
	// options at all, which is what keeps their cases about the row rather than
	// about the card.
	messageRequest := func(
		dedupKey pkg.DedupKey,
		cardinality pkg.AnswerCardinality,
	) pkg.PushRequest {
		producerID := pkg.ProducerID("producer-" + dedupKey.String())
		return pkg.PushRequest{
			ProducerID:        producerID,
			ProducerKind:      pkg.SessionProducerKind,
			LivenessRef:       pkg.LivenessRef("session:" + producerID.String()),
			DedupKey:          dedupKey,
			InterruptClass:    "pick",
			Payload:           "Which vault-cleanup chores should I queue for this week?",
			AnswerMechanism:   pkg.MessageAnswerMechanism,
			AnswerCardinality: cardinality,
			Options: pkg.AnswerOptions{
				{
					Label:       "Dead-link sweep",
					Description: "Scan the vault for broken wikilinks.",
					Recommended: true,
				},
				{Label: "Archive 2025 daily notes"},
			},
		}
	}

	It("renders one row per open item, carrying every rendered field", func() {
		first, err := store.Push(ctx, pushRequest("producer-a", "gate-a", "deploy prod?"))
		Expect(err).To(BeNil())
		second, err := store.Push(ctx, pushRequest("producer-b", "gate-b", "approve the migration"))
		Expect(err).To(BeNil())

		resp := get("GET")
		Expect(resp.Code).To(Equal(http.StatusOK))
		Expect(resp.Header().Get("Content-Type")).To(Equal("text/html; charset=utf-8"))

		body := resp.Body.String()
		// Gomega has no count matcher, so the row count is counted directly.
		Expect(strings.Count(body, `data-item-id="`)).To(Equal(2))

		for _, item := range []*pkg.Item{first, second} {
			Expect(body).To(ContainSubstring(item.ProducerID.String()))
			Expect(body).To(ContainSubstring(item.ProducerKind.String()))
			Expect(body).To(ContainSubstring(item.Payload.String()))
			Expect(body).To(ContainSubstring(item.State.String()))
			Expect(body).To(ContainSubstring(item.CreatedAt.String()))
			Expect(body).To(ContainSubstring(`data-item-id="` + item.ItemID.String() + `"`))
		}
	})

	It("renders an empty page for an empty store", func() {
		resp := get("GET")

		Expect(resp.Code).To(Equal(http.StatusOK))
		Expect(strings.Count(resp.Body.String(), "data-item-id=")).To(Equal(0))
	})

	It("escapes producer-supplied text instead of emitting it raw", func() {
		// ProducerID is a free string validated only by NotEmptyString, so this is
		// the exact value the html/template boundary exists to neutralise.
		_, err := store.Push(
			ctx,
			pushRequest(`"><script>alert(1)</script>`, "gate-escape", "escaping fixture"),
		)
		Expect(err).To(BeNil())

		body := get("GET").Body.String()

		// The page legitimately carries its own <script> for the answer controls,
		// so the assertion is that the *injected* raw script is absent rather than
		// that no script element exists at all.
		Expect(body).NotTo(ContainSubstring("<script>alert(1)"))
		Expect(body).To(ContainSubstring("&lt;script&gt;"))
	})

	It("omits closed items while keeping open ones", func() {
		open, err := store.Push(ctx, pushRequest("producer-open", "gate-open", "still open"))
		Expect(err).To(BeNil())
		closed, err := store.Push(
			ctx,
			pushRequest("producer-closed", "gate-closed", "already done"),
		)
		Expect(err).To(BeNil())
		_, err = store.Close(ctx, closed.ItemID, "", nil)
		Expect(err).To(BeNil())

		body := get("GET").Body.String()

		Expect(body).To(ContainSubstring(open.ProducerID.String()))
		Expect(body).NotTo(ContainSubstring(closed.ProducerID.String()))
	})

	// ⚠️ This spec previously asserted the page was inert — no <form>, no
	// <script>, no method="post" — which was the recorded design decision the
	// board reversal overturns. It asserts the *bounded* rule instead: answer
	// controls exist for `message` items and a `permission` item renders none,
	// because only the operator may answer a gate, and only in the session that
	// raised it. See the attention item schema § Answer routing, and the page
	// handler's own doc comment for why the reversal stops there.
	It(
		"offers answer controls for a message item and only Allow/Deny for a permission item",
		func() {
			message, err := store.Push(
				ctx,
				pushRequest("producer-readonly", "gate-readonly", "read me"),
			)
			Expect(err).To(BeNil())
			permission, err := store.Push(ctx, pkg.PushRequest{
				ProducerID:      "producer-gate",
				ProducerKind:    pkg.SessionProducerKind,
				LivenessRef:     pkg.LivenessRef("session:producer-gate"),
				DedupKey:        "gate-permission",
				InterruptClass:  "approve",
				Payload:         "deploy prod?",
				AnswerMechanism: pkg.PermissionAnswerMechanism,
			})
			Expect(err).To(BeNil())

			// A non-empty page first, so the absence assertions below are made against
			// a rendered document rather than against an empty body.
			resp := get("GET")
			Expect(resp.Body.String()).NotTo(BeEmpty())
			body := resp.Body.String()

			// Scoped per row rather than page-wide: a page-wide grep would pass on a
			// page where the wrong item carried the controls.
			Expect(rowOf(body, message.ItemID)).To(ContainSubstring("<form"))
			Expect(rowOf(body, permission.ItemID)).NotTo(ContainSubstring("<form"))
			// ⚠️ AMENDED 2026-09-27: a permission row renders the jump corner
			// (disabled when it has no target) and, from this change, the corner X.
			// Neither is an ANSWER control, which is the property this spec exists
			// for — so the assertions below are on forms and inputs, not buttons.
			Expect(rowOf(body, permission.ItemID)).NotTo(ContainSubstring("<input"))
			Expect(rowOf(body, permission.ItemID)).To(ContainSubstring(`class="jump-corner"`))
			// ⚠️ AMENDED 2026-09-29 (operator decision): a permission row carries
			// exactly the two verdict buttons — still no form and no free input.
			Expect(rowOf(body, permission.ItemID)).To(ContainSubstring(`data-decision="allow"`))
			Expect(rowOf(body, permission.ItemID)).To(ContainSubstring(`data-decision="deny"`))
			Expect(rowOf(body, message.ItemID)).NotTo(ContainSubstring(`data-decision=`))

			// HEAD is routed to this handler too; it is read-only and a link checker
			// or browser may issue it, so it is asserted rather than merely declared.
			Expect(get("HEAD").Code).To(Equal(http.StatusOK))
		},
	)

	It("renders host, cwd, tool and pane as four required values on a resolving row", func() {
		item, err := store.Push(ctx, pushRequest("producer-prov", "gate-prov", "deploy prod?"))
		Expect(err).To(BeNil())
		provenance.ResolveReturns(pkg.Provenances{
			item.ItemID: pkg.Provenance{
				Host:         "burn",
				Cwd:          "/Users/bborbe/Documents/workspaces/attention-controller",
				Tool:         "AskUserQuestion",
				Pane:         "1140",
				PaneRecorded: true,
				Routable:     true,
			},
		})

		body := get("GET").Body.String()

		// Positive control first: a row that failed to render at all must not be
		// able to satisfy the assertions below.
		Expect(body).To(ContainSubstring(item.Payload.String()))
		Expect(body).To(ContainSubstring(item.ProducerID.String()))

		// All four, each in its own element. The pane is asserted as a required
		// value rather than a conditional one: an implementation that resolves
		// host, cwd and tool but never a pane fails here.
		Expect(body).To(ContainSubstring(`<span class="host">burn</span>`))
		Expect(
			body,
		).To(ContainSubstring(`<span class="cwd">/Users/bborbe/Documents/workspaces/attention-controller</span>`))
		Expect(body).To(ContainSubstring(`<span class="tool">AskUserQuestion</span>`))
		Expect(body).To(ContainSubstring(`<span class="pane">pane 1140</span>`))
	})

	It("marks an unvalidated pane unroutable, shows no pane id, and invents nothing", func() {
		item, err := store.Push(
			ctx,
			pushRequest("producer-unroutable", "gate-unroutable", "who owns this?"),
		)
		Expect(err).To(BeNil())
		// A pane was recorded but did not validate against the session, which is
		// § Silence 7's exact case: a recycled id that resolves to another
		// session's pane. Tool is absent, which is the common case for idle items.
		provenance.ResolveReturns(pkg.Provenances{
			item.ItemID: pkg.Provenance{
				Host:         "burn",
				Cwd:          "/tmp",
				PaneRecorded: true,
				Routable:     false,
			},
		})

		body := get("GET").Body.String()

		Expect(body).To(ContainSubstring(item.Payload.String()))
		Expect(body).To(ContainSubstring(`<span class="unroutable">unroutable</span>`))

		// The pane id is withheld entirely — not shown struck through, not shown
		// with a warning, not shown at all. Showing it invites the operator to
		// route to a pane that belongs to somebody else.
		Expect(body).NotTo(ContainSubstring(`class="pane"`))

		// An absent field renders blank, never a placeholder. A stand-in would be
		// an unresolvable value presented as resolved, which is the one failure
		// this task exists to avoid.
		Expect(body).NotTo(ContainSubstring(`class="tool"`))
		for _, placeholder := range []string{"unknown", "n/a", "N/A", "—", "??"} {
			Expect(body).NotTo(ContainSubstring(placeholder))
		}
	})

	It("renders every item with no provenance line when nothing resolves", func() {
		first, err := store.Push(
			ctx,
			pushRequest("producer-noprov-a", "gate-noprov-a", "first ask"),
		)
		Expect(err).To(BeNil())
		second, err := store.Push(
			ctx,
			pushRequest("producer-noprov-b", "gate-noprov-b", "second ask"),
		)
		Expect(err).To(BeNil())
		provenance.ResolveReturns(pkg.Provenances{})

		resp := get("GET")

		// The standalone claim, honoured rather than broken: no provenance source
		// is not an error and does not blank the page.
		Expect(resp.Code).To(Equal(http.StatusOK))
		body := resp.Body.String()
		Expect(strings.Count(body, `data-item-id="`)).To(Equal(2))
		for _, item := range []*pkg.Item{first, second} {
			Expect(body).To(ContainSubstring(item.ProducerID.String()))
			Expect(body).To(ContainSubstring(item.Payload.String()))
		}
		Expect(body).NotTo(ContainSubstring(`class="provenance"`))
	})

	// The vault task link: the one fact the card had always lacked. Host, cwd,
	// tool and pane all say *where* a session ran and none says *what* it was
	// working on, so judging a card meant leaving the board and opening the
	// session it named. These specs drive the page handler and assert on the
	// RENDERED ROW, scoped with rowOf, because a page-wide check cannot fail —
	// other rows legitimately carry a link.
	Describe("the vault task link", func() {
		// taskAnchor is the served markup the link is asserted against, written
		// as html/template actually emits it. ⚠️ A hand-written literal, never one
		// built with the same helper the code uses: a shared helper would agree
		// with itself whatever it produced, so neither the `%20`/`%2F` escaping
		// nor the dropped `.md` would be asserted at all. The `&amp;` is the
		// template's own HTML-escaping of the `&` in the attribute, which is why
		// the assertion is a raw-string match over the served HTML.
		taskAnchor := `<span class="task"><a href="obsidian://open?vault=Personal&amp;file=25%20Tasks%2FFix%20the%20board">Fix the board</a></span>`

		It("draws the resolved task as an anchor to the vault's own URL", func() {
			item, err := store.Push(
				ctx,
				pushRequest("producer-task", "gate-task", "which task is this?"),
			)
			Expect(err).To(BeNil())
			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{
					Host:     "burn",
					TaskName: "Fix the board",
					TaskPath: "25 Tasks/Fix the board.md",
				},
			})

			row := rowOf(get("GET").Body.String(), item.ItemID)

			Expect(row).To(ContainSubstring(taskAnchor))
			// It leads the line: the task is the first span in the provenance div,
			// which is what the wrapper span's adjacency to the host span buys.
			Expect(row).To(ContainSubstring(`<div class="provenance"><span class="task">`))
		})

		It("renders no anchor for an item whose session anchors no task", func() {
			item, err := store.Push(
				ctx,
				pushRequest("producer-notask", "gate-notask", "no task here"),
			)
			Expect(err).To(BeNil())
			// ⚠️ Another resolved value is required rather than incidental: with
			// nothing but the absent task, the provenance div never renders at all
			// and the absence assertions below would pass vacuously.
			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{Host: "burn", Cwd: "/tmp"},
			})

			row := rowOf(get("GET").Body.String(), item.ItemID)

			// Positive control: the line rendered, so the absences below are a
			// withheld link rather than an absent line.
			Expect(row).To(ContainSubstring(`class="provenance"`))
			Expect(row).To(ContainSubstring(`<span class="host">burn</span>`))
			Expect(row).NotTo(ContainSubstring(`class="task"`))
			Expect(row).NotTo(ContainSubstring("<a href="))
		})

		It("renders the provenance line for a task-only provenance", func() {
			item, err := store.Push(
				ctx,
				pushRequest("producer-taskonly", "gate-taskonly", "task and nothing else"),
			)
			Expect(err).To(BeNil())
			// Nothing but the task: no event-log line resolved a host or cwd and no
			// pane validated. This is the shape the `Resolved` conjunct exists for —
			// without it the line never renders, so the name is resolved correctly,
			// drawn correctly, and never appears.
			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{
					TaskName: "Fix the board",
					TaskPath: "25 Tasks/Fix the board.md",
				},
			})

			row := rowOf(get("GET").Body.String(), item.ItemID)

			Expect(row).To(ContainSubstring(`class="provenance"`))
			Expect(row).To(ContainSubstring(taskAnchor))
		})

		// ⚠️ The `&` case, and it is the one that distinguishes the escaper the
		// helper uses from url.PathEscape. PathEscape leaves `&` alone — it is
		// legal inside a path segment — so a task named `R&D notes` would emit
		// `…&file=25%20Tasks%2FR&D%20notes`, which Obsidian reads as a `file` of
		// `25 Tasks/R` plus a stray `D notes` parameter: the link resolves, and
		// resolves to the wrong file. Every other assertion in this block passes
		// under either escaper, so without this case the regression is invisible.
		It("escapes a task name the query grammar would otherwise split on", func() {
			item, err := store.Push(
				ctx,
				pushRequest("producer-amp", "gate-amp", "which task is this?"),
			)
			Expect(err).To(BeNil())
			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{
					Host:     "burn",
					TaskName: "R&D notes",
					TaskPath: "25 Tasks/R&D notes.md",
				},
			})

			row := rowOf(get("GET").Body.String(), item.ItemID)

			Expect(row).To(ContainSubstring(
				`<span class="task"><a href="obsidian://open?vault=Personal&amp;file=25%20Tasks%2FR%26D%20notes">R&amp;D notes</a></span>`,
			))
		})
	})

	// The name the session registry resolved for the session that raised the
	// card, drawn as its own span at the end of the provenance line. It is the
	// template half of the feature — the resolution half lives in the resolver —
	// so these cases feed hand-built pkg.Provenance values straight through the
	// mock and assert the served markup. They exercise the template and nothing
	// else, and are deliberately not acceptance criteria: every criterion that
	// observes the served page is asserted against a real fixture registry read by
	// the real resolver elsewhere.
	Describe("the session name span", func() {
		It("draws the resolved session name as its own span", func() {
			item, err := store.Push(
				ctx,
				pushRequest("producer-name", "gate-name", "which session is this?"),
			)
			Expect(err).To(BeNil())
			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{SessionName: "Board Polish Session"},
			})

			row := rowOf(get("GET").Body.String(), item.ItemID)

			// Positive control: the row rendered its payload, so a row that failed
			// to render cannot satisfy the assertions below.
			Expect(row).To(ContainSubstring(item.Payload.String()))
			Expect(strings.Count(row, `class="provenance"`)).To(Equal(1))
			// The expected markup is a hand-written literal, written as
			// html/template emits it, never one built with the same helper the code
			// uses: a shared helper would agree with itself whatever it produced.
			Expect(row).To(ContainSubstring(
				`<span class="session-name">Board Polish Session</span>`,
			))
			Expect(strings.Count(row, `class="session-name"`)).To(Equal(1))
		})

		It("draws the session name beside the task, goal and topic spans", func() {
			item, err := store.Push(
				ctx,
				pushRequest("producer-four", "gate-four", "which session is this?"),
			)
			Expect(err).To(BeNil())
			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{
					SessionName: "Board Polish Session",
					TaskName:    "Fix the board",
					TaskPath:    "25 Tasks/Fix the board.md",
					GoalName:    "First Goal",
					GoalPath:    "24 Goals/First Goal.md",
					TopicName:   "Attention Board Polish",
					TopicPath:   "23 Topics/Attention Board Polish.md",
				},
			})

			row := rowOf(get("GET").Body.String(), item.ItemID)

			// The four spans are independent and coexist: each is gated on its own
			// resolved value, none gating another.
			Expect(strings.Count(row, `class="task"`)).To(Equal(1))
			Expect(strings.Count(row, `class="goal"`)).To(Equal(1))
			Expect(strings.Count(row, `class="topic"`)).To(Equal(1))
			Expect(strings.Count(row, `class="session-name"`)).To(Equal(1))
			// The new span is appended last, so the task span still leads the line.
			Expect(row).To(ContainSubstring(`<div class="provenance"><span class="task">`))
		})

		It("draws no session-name span when the name is absent", func() {
			item, err := store.Push(
				ctx,
				pushRequest("producer-noname", "gate-noname", "which session is this?"),
			)
			Expect(err).To(BeNil())
			// ⚠️ Another resolved value is required rather than incidental: with
			// nothing resolved at all the provenance div never renders and the
			// absence assertion below would pass vacuously.
			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{Host: "burn", Cwd: "/tmp"},
			})

			row := rowOf(get("GET").Body.String(), item.ItemID)

			Expect(strings.Count(row, `class="provenance"`)).To(Equal(1))
			Expect(strings.Count(row, `class="host"`)).To(Equal(1))
			Expect(strings.Count(row, `class="session-name"`)).To(Equal(0))
			// An unresolved value renders absent, never as a stand-in presented as
			// resolved — [[Attention Item Schema]] § Silence 7.
			Expect(row).NotTo(ContainSubstring("unknown"))
			Expect(row).NotTo(ContainSubstring("n/a"))
		})

		It("renders the provenance line for a name-only provenance", func() {
			item, err := store.Push(
				ctx,
				pushRequest("producer-nameonly", "gate-nameonly", "name and nothing else"),
			)
			Expect(err).To(BeNil())
			// Nothing but the name resolved. This is the shape the SessionName
			// conjunct in Provenance.Resolved() exists for: without it the line
			// never renders, so the name is resolved correctly, drawn correctly, and
			// never appears.
			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{SessionName: "Lone Name"},
			})

			row := rowOf(get("GET").Body.String(), item.ItemID)

			Expect(strings.Count(row, `class="provenance"`)).To(Equal(1))
			Expect(row).To(ContainSubstring(`<span class="session-name">Lone Name</span>`))
		})
	})

	// The goal a card's task advances, and the topic page that lists that goal,
	// drawn beside the task on the same provenance line. Each resolves through the
	// provenance the resolver hands the row, and each draws independently: a goal
	// no topic lists still renders its goal link, which is the dominant live case.
	Describe("the goal and topic links", func() {
		// The served markup each link is asserted against, written as html/template
		// actually emits it. ⚠️ Hand-written literals, never ones built with the
		// same helper the code uses: a shared helper would agree with itself
		// whatever it produced, so neither the `%20`/`%2F` escaping nor the dropped
		// `.md` would be asserted at all. The `&amp;` is the template's own
		// HTML-escaping of the `&` in the attribute.
		goalAnchor := `<span class="goal"><a href="obsidian://open?vault=Personal&amp;file=24%20Goals%2FFix%20the%20board">Fix the board</a></span>`
		topicAnchor := `<span class="topic"><a href="obsidian://open?vault=Personal&amp;file=23%20Topics%2FAttention%20Board%20Polish">Attention Board Polish</a></span>`

		It("draws task, goal and topic as three spans, in that order", func() {
			item, err := store.Push(
				ctx,
				pushRequest("producer-goaltopic", "gate-goaltopic", "which goal is this?"),
			)
			Expect(err).To(BeNil())
			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{
					Host:      "burn",
					TaskName:  "Fix the board",
					TaskPath:  "25 Tasks/Fix the board.md",
					GoalName:  "Fix the board",
					GoalPath:  "24 Goals/Fix the board.md",
					TopicName: "Attention Board Polish",
					TopicPath: "23 Topics/Attention Board Polish.md",
				},
			})

			row := rowOf(get("GET").Body.String(), item.ItemID)

			Expect(row).To(ContainSubstring(goalAnchor))
			Expect(row).To(ContainSubstring(topicAnchor))
			// Each leads its own span: the line is task, then goal, then topic, and
			// the wrappers are what keep the separators beside them.
			Expect(strings.Index(row, `class="task"`)).To(BeNumerically("<",
				strings.Index(row, `class="goal"`)))
			Expect(strings.Index(row, `class="goal"`)).To(BeNumerically("<",
				strings.Index(row, `class="topic"`)))
		})

		It("draws the goal and no topic when no topic lists that goal", func() {
			item, err := store.Push(
				ctx,
				pushRequest("producer-goalonly", "gate-goalonly", "which goal is this?"),
			)
			Expect(err).To(BeNil())
			// ⚠️ The dominant live case, and the one the independent gates exist
			// for: a single gate over both links would drop this goal link too.
			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{
					Host:     "burn",
					TaskName: "Fix the board",
					TaskPath: "25 Tasks/Fix the board.md",
					GoalName: "Fix the board",
					GoalPath: "24 Goals/Fix the board.md",
				},
			})

			row := rowOf(get("GET").Body.String(), item.ItemID)

			// Positive control: the line rendered, so the absence below is a
			// withheld link rather than an absent line.
			Expect(row).To(ContainSubstring(`class="provenance"`))
			Expect(row).To(ContainSubstring(goalAnchor))
			Expect(row).NotTo(ContainSubstring(`class="topic"`))
		})

		It("draws no goal and no topic for a task whose goals list is empty", func() {
			item, err := store.Push(
				ctx,
				pushRequest("producer-nogoal", "gate-nogoal", "no goal here"),
			)
			Expect(err).To(BeNil())
			// ⚠️ Another resolved value is required rather than incidental: with
			// nothing but the task, the provenance div could still render, but the
			// host is what makes the positive control below about a drawn line.
			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{
					Host:     "burn",
					TaskName: "Fix the board",
					TaskPath: "25 Tasks/Fix the board.md",
				},
			})

			row := rowOf(get("GET").Body.String(), item.ItemID)

			// Positive control: the line rendered, so the absences below cannot
			// pass on a row that was dropped.
			Expect(row).To(ContainSubstring(`class="provenance"`))
			Expect(row).To(ContainSubstring(`class="task"`))
			Expect(row).NotTo(ContainSubstring(`class="goal"`))
			Expect(row).NotTo(ContainSubstring(`class="topic"`))
		})

		// ⚠️ The template.URL regression guard at the template seam. A plain-string
		// GoalURL or TopicURL satisfies every field-level assertion above while the
		// href renders `#ZgotmplZ`, so this is the one assertion that catches it.
		It("emits a live href rather than the URL filter's sentinel", func() {
			item, err := store.Push(
				ctx,
				pushRequest("producer-live", "gate-live", "which goal is this?"),
			)
			Expect(err).To(BeNil())
			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{
					TaskName:  "Fix the board",
					TaskPath:  "25 Tasks/Fix the board.md",
					GoalName:  "Fix the board",
					GoalPath:  "24 Goals/Fix the board.md",
					TopicName: "Attention Board Polish",
					TopicPath: "23 Topics/Attention Board Polish.md",
				},
			})

			row := rowOf(get("GET").Body.String(), item.ItemID)

			Expect(row).To(ContainSubstring(goalAnchor))
			Expect(row).NotTo(ContainSubstring("#ZgotmplZ"))
		})

		// ⚠️ The `&` case for the goal, mirroring the task's `R&D notes` case: it is
		// the one that distinguishes the escaper the helper uses from url.PathEscape,
		// which leaves `&` alone and would split the query. Both the href and the
		// link text are asserted, so a link that resolves to the wrong file fails.
		It("escapes a goal name the query grammar would otherwise split on", func() {
			item, err := store.Push(
				ctx,
				pushRequest("producer-goalamp", "gate-goalamp", "which goal is this?"),
			)
			Expect(err).To(BeNil())
			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{
					Host:     "burn",
					TaskName: "Fix the board",
					TaskPath: "25 Tasks/Fix the board.md",
					GoalName: "R&D notes",
					GoalPath: "24 Goals/R&D notes.md",
				},
			})

			row := rowOf(get("GET").Body.String(), item.ItemID)

			Expect(row).To(ContainSubstring(
				`<span class="goal"><a href="obsidian://open?vault=Personal&amp;file=24%20Goals%2FR%26D%20notes">R&amp;D notes</a></span>`,
			))
		})
	})

	It("explains a row that carries no jump control, without naming a value", func() {
		item, err := store.Push(
			ctx,
			pushRequest("producer-nojump", "gate-nojump", "why is there no button?"),
		)
		Expect(err).To(BeNil())
		// A record exists but recorded no pane. This is the row the operator
		// reported three times in six hours: a card with no control and nothing
		// saying why, indistinguishable from a board whose button failed to
		// render.
		provenance.ResolveReturns(pkg.Provenances{
			item.ItemID: pkg.Provenance{Host: "burn", Cwd: "/tmp"},
		})

		row := rowOf(get("GET").Body.String(), item.ItemID)

		// Positive control: the row rendered at all, so the assertions below
		// cannot pass on a page that dropped it.
		Expect(row).To(ContainSubstring(item.Payload.String()))
		// ⚠️ AMENDED 2026-09-27 — this row now CARRIES the corner control,
		// present but disabled, so the corner is the same on every card
		// ([[Attention Item Schema]] silence 20's resolution). The control is a
		// real disabled attribute carrying no data-jump, and both halves matter:
		// the page's jump handler is delegated at the document, so a control that
		// still dispatched would be matched by closest('button[data-jump]'), fetch
		// a null URL, and reach showJumpNote with no .jump container to append
		// into — a throw on a row that renders no explanation div at all.
		Expect(row).To(ContainSubstring(`class="jump-corner"`))
		Expect(row).To(ContainSubstring(`disabled`))
		Expect(row).NotTo(ContainSubstring(`data-jump`))
		Expect(row).NotTo(ContainSubstring(`<code>/supervisor:jump`))
		Expect(row).To(ContainSubstring(`<span class="no-jump">`))
		Expect(row).To(ContainSubstring("No pane was recorded for this item"))
		// The explanation is its own element, never the control's. The two stay
		// distinguishable by class — and that is still load-bearing after the
		// amendment above: "does this row have a usable jump control?" is now
		// answered by a :not(:disabled) test on the corner, not by whether the
		// corner is there at all.
		Expect(row).To(ContainSubstring(`class="jump-reason"`))
		Expect(row).NotTo(ContainSubstring(`class="jump"`))
	})

	It("gives the unroutable row its own sentence and keeps silence 7's marker", func() {
		item, err := store.Push(
			ctx,
			pushRequest("producer-nojump-unroutable", "gate-nojump-unroutable", "who owns this?"),
		)
		Expect(err).To(BeNil())
		provenance.ResolveReturns(pkg.Provenances{
			item.ItemID: pkg.Provenance{
				Host:         "burn",
				Cwd:          "/tmp",
				PaneRecorded: true,
				Routable:     false,
			},
		})

		row := rowOf(get("GET").Body.String(), item.ItemID)

		// The marker is kept and the sentence is added beside it, never a
		// replacement — which is what the negative assertion rules out.
		Expect(row).To(ContainSubstring(`<span class="unroutable">unroutable</span>`))
		Expect(
			row,
		).To(ContainSubstring("The pane recorded for this item does not resolve to this session."))
		Expect(row).NotTo(ContainSubstring("No pane was recorded"))
	})

	// ⚠️ INVERTED by the jump fold. This spec used to require that a resolved
	// pane on a token-less host render NO control plus "Jump is unavailable on
	// this host" — the explanation existed only for the state the token gate
	// created. The board's jump is now in-process and reads no token, so that
	// state is unreachable and the sentence is deleted from the renderer rather
	// than left as a branch nothing can take. What must hold instead is the
	// positive fact: a resolved pane renders a WORKING control even on a host
	// whose token path is empty. The fixture handler below is built with exactly
	// that empty path, so this spec fails the moment a token read is
	// reintroduced as a gate.
	It("renders a working control on a resolved pane even where the token path is empty", func() {
		item, err := store.Push(
			ctx,
			pushRequest("producer-nojump-token", "gate-nojump-token", "token is gone?"),
		)
		Expect(err).To(BeNil())
		provenance.ResolveReturns(pkg.Provenances{
			item.ItemID: pkg.Provenance{
				Host:         "burn",
				Cwd:          "/tmp",
				Pane:         "1140",
				PaneRecorded: true,
				Routable:     true,
			},
		})

		row := rowOf(get("GET").Body.String(), item.ItemID)

		Expect(row).To(ContainSubstring(`data-jump="/jump/` + item.ItemID.String() + `"`))
		// ⚠️ No `/supervisor:jump` assertion here. This fixture is a `message`
		// row, and a message row deliberately renders the button WITHOUT the
		// copyable command — the board may answer it in place, so the command's
		// "approve in the session that asked" label would be false. Asserting the
		// command here would pin the wrong behaviour for this class.
		Expect(row).NotTo(ContainSubstring("Jump is unavailable on this host"))
		Expect(row).NotTo(ContainSubstring("No pane was recorded"))
		Expect(row).NotTo(ContainSubstring("does not resolve to this session"))
	})

	// ⚠️ DELETED, not amended, and the deletion is deliberate: this spec's whole
	// purpose was to be the positive control for the token-gate spec above — the
	// same row on a host whose token READS. With the gate gone there is only one
	// case left, so the spec above IS the positive control and this one asserted
	// the same thing twice. A duplicated control is worse than none: it reads as
	// independent evidence when it is the same fixture twice.

	It(
		"derives the explanation per load, so a changed reason changes the text with no store write",
		func() {
			item, err := store.Push(
				ctx,
				pushRequest("producer-nojump-render", "gate-nojump-render", "does this change?"),
			)
			Expect(err).To(BeNil())
			// A record with no pane: the absence is the item's own.
			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{Host: "burn", Cwd: "/tmp"},
			})
			first := rowOf(get("GET").Body.String(), item.ItemID)
			Expect(first).To(ContainSubstring("No pane was recorded for this item"))

			// The same item and the same store, with nothing written between the two
			// loads: only the resolution changed — a pane now exists and does not
			// belong to this session. The explanation follows the resolution rather
			// than a value stored on the item, which is what makes it impossible for
			// the explanation and the control to disagree.
			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{
					Host:         "burn",
					Cwd:          "/tmp",
					PaneRecorded: true,
					Routable:     false,
				},
			})
			second := rowOf(get("GET").Body.String(), item.ItemID)

			Expect(second).To(ContainSubstring("does not resolve to this session"))
			Expect(second).NotTo(ContainSubstring("No pane was recorded"))
		},
	)

	It("returns the standard JSON error body when the read fails", func() {
		Expect(db.Close()).To(BeNil())

		resp := get("GET")

		Expect(resp.Code).To(Equal(http.StatusInternalServerError))

		// Decoded rather than substring-matched, so a body that merely resembles
		// the standard error shape does not pass. Only code and message are
		// asserted: details is omitempty and this error carries no data.
		var errorResponse libhttp.ErrorResponse
		Expect(json.NewDecoder(resp.Body).Decode(&errorResponse)).To(BeNil())
		Expect(errorResponse.Error.Code).To(Equal(libhttp.ErrorCodeInternal))
		Expect(errorResponse.Error.Message).To(ContainSubstring("read failed"))
	})

	// The card's control is driven by the producer's declared cardinality and
	// never by the option count: a one-option question and a many-option
	// single-pick question carry lists of different lengths and ask for different
	// things, so a card that read the length would render a checkbox for a
	// question admitting one answer. See the attention item schema and the page
	// handler's own doc comment.
	Describe("the answer card", func() {
		It("renders a checkbox per option when the question takes several picks", func() {
			item, err := store.Push(
				ctx,
				messageRequest("gate-multiple", pkg.MultipleAnswerCardinality),
			)
			Expect(err).To(BeNil())

			block := rowOf(get("GET").Body.String(), item.ItemID)

			Expect(block).To(ContainSubstring(`type="checkbox"`))
			Expect(block).NotTo(ContainSubstring(`type="radio"`))
		})

		It("renders a radio button per option when the question takes one pick", func() {
			item, err := store.Push(
				ctx,
				messageRequest("gate-single", pkg.SingleAnswerCardinality),
			)
			Expect(err).To(BeNil())

			block := rowOf(get("GET").Body.String(), item.ItemID)

			Expect(block).To(ContainSubstring(`type="radio"`))
			Expect(block).NotTo(ContainSubstring(`type="checkbox"`))
		})

		// An absent cardinality is what every item pushed before the field
		// existed carries, and the schema reads it as single. Asserted rather than
		// assumed, because the opposite reading would render a checkbox for a
		// question that admits one answer.
		It("renders a radio button per option when the cardinality is absent", func() {
			item, err := store.Push(ctx, messageRequest("gate-absent", ""))
			Expect(err).To(BeNil())

			block := rowOf(get("GET").Body.String(), item.ItemID)

			Expect(block).To(ContainSubstring(`type="radio"`))
			Expect(block).NotTo(ContainSubstring(`type="checkbox"`))
		})

		It("renders the cardinality hint and marks the recommended option", func() {
			item, err := store.Push(
				ctx,
				messageRequest("gate-hint", pkg.MultipleAnswerCardinality),
			)
			Expect(err).To(BeNil())

			block := rowOf(get("GET").Body.String(), item.ItemID)

			Expect(block).To(ContainSubstring("(pick any number)"))
			Expect(block).To(ContainSubstring(`class="recommended"`))
			Expect(block).To(ContainSubstring("(Recommended)"))
		})

		It("renders an option's muted description under its label", func() {
			item, err := store.Push(
				ctx,
				messageRequest("gate-description", pkg.SingleAnswerCardinality),
			)
			Expect(err).To(BeNil())

			block := rowOf(get("GET").Body.String(), item.ItemID)

			Expect(block).To(ContainSubstring(`class="option-desc"`))
			Expect(block).To(ContainSubstring("Scan the vault for broken wikilinks."))
			// An option carrying no description renders its label alone rather
			// than an empty line, so the two options differ in this block.
			Expect(strings.Count(block, `class="option-desc"`)).To(Equal(1))
		})

		It("renders one tab and one panel per question of a multi-question item", func() {
			item, err := store.Push(ctx, pkg.PushRequest{
				ProducerID:      "producer-two-questions",
				ProducerKind:    pkg.SessionProducerKind,
				LivenessRef:     pkg.LivenessRef("session:producer-two-questions"),
				DedupKey:        "gate-two-questions",
				InterruptClass:  "pick",
				Payload:         "Vault cleanup",
				AnswerMechanism: pkg.MessageAnswerMechanism,
				Questions: pkg.Questions{
					{
						Tab:         "Chores",
						Payload:     "Which chores should I queue?",
						Cardinality: pkg.MultipleAnswerCardinality,
						Options:     pkg.AnswerOptions{{Label: "Dead-link sweep"}},
					},
					{
						Tab:         "Priority",
						Payload:     "Which one comes first?",
						Cardinality: pkg.SingleAnswerCardinality,
						Options:     pkg.AnswerOptions{{Label: "Dead-link sweep"}},
					},
				},
			})
			Expect(err).To(BeNil())

			block := rowOf(get("GET").Body.String(), item.ItemID)

			// Both tabs, both panels, and both questions' payloads — a card that
			// rendered only the first question would fail here.
			Expect(block).To(ContainSubstring(`data-tab="Chores"`))
			Expect(block).To(ContainSubstring(`data-tab="Priority"`))
			Expect(block).To(ContainSubstring(`data-question="Chores"`))
			Expect(block).To(ContainSubstring(`data-question="Priority"`))
			Expect(block).To(ContainSubstring("Which chores should I queue?"))
			Expect(block).To(ContainSubstring("Which one comes first?"))

			// The item's own payload is the card's title on a multi-question item
			// rather than a question, and it renders beside the questions rather
			// than instead of them.
			Expect(block).To(ContainSubstring("Vault cleanup"))

			// The first tab is the open one, and the second panel ships in the
			// document already hidden, so a click reveals a panel that is present
			// rather than fetching one.
			Expect(block).To(ContainSubstring(`class="tab active" data-tab="Chores"`))
			// The second panel carries its own cardinality, which is what the script
			// reads to choose between the value and values carriers, and ships in
			// the document already hidden so a click reveals a panel that is
			// present rather than fetching one.
			Expect(
				block,
			).To(ContainSubstring(`data-question="Priority" data-multi-pick="false" hidden`))

			// Each question carries its own control: the multi-pick tab renders a
			// checkbox and the single-pick tab a radio button, on one card.
			Expect(block).To(ContainSubstring(`type="checkbox"`))
			Expect(block).To(ContainSubstring(`type="radio"`))

			// The wire shape follows the tab strip. The script reads this attribute
			// to decide between `answer` and `answers`, so a card rendering tabs
			// while reporting false would post the wrong field.
			Expect(block).To(ContainSubstring(`data-multi="true"`))
		})

		It("renders no tab strip and answers by `answer` on a single-question item", func() {
			item, err := store.Push(
				ctx,
				messageRequest("gate-no-tabs", pkg.SingleAnswerCardinality),
			)
			Expect(err).To(BeNil())

			block := rowOf(get("GET").Body.String(), item.ItemID)

			Expect(block).To(ContainSubstring(`data-multi="false"`))
			Expect(block).NotTo(ContainSubstring(`data-tab=`))
			Expect(block).NotTo(ContainSubstring(`class="tabs"`))
			Expect(block).NotTo(ContainSubstring(`class="card-title"`))
		})

		It("renders Dismiss and Submit answer, and no card on a permission row", func() {
			message, err := store.Push(
				ctx,
				messageRequest("gate-buttons", pkg.SingleAnswerCardinality),
			)
			Expect(err).To(BeNil())
			permission, err := store.Push(ctx, pkg.PushRequest{
				ProducerID:      "producer-gate-buttons",
				ProducerKind:    pkg.SessionProducerKind,
				LivenessRef:     pkg.LivenessRef("session:producer-gate-buttons"),
				DedupKey:        "gate-buttons-permission",
				InterruptClass:  "approve",
				Payload:         "deploy prod?",
				AnswerMechanism: pkg.PermissionAnswerMechanism,
			})
			Expect(err).To(BeNil())

			body := get("GET").Body.String()
			block := rowOf(body, message.ItemID)

			// The Dismiss value is what the script reads as the skip, so it is
			// asserted rather than left to the label.
			Expect(block).To(ContainSubstring(`value="skip"`))
			Expect(block).To(ContainSubstring("Dismiss"))
			// The submit control is named for what it does rather than for an
			// advance: `Next` was inherited from the Paseo reference card, where
			// it means *advance to the next card*, while this control submits the
			// whole card. See [[Attention Item Schema]] § Answer routing.
			Expect(block).To(ContainSubstring("Submit answer"))

			// The card is one `if .Message` away from a permission row, so the
			// negative case is asserted beside the positive one rather than only
			// page-wide.
			permissionBlock := rowOf(body, permission.ItemID)
			Expect(permissionBlock).NotTo(ContainSubstring("<form"))
			// ⚠️ AMENDED 2026-09-27: the jump corner is not an answer control and
			// now renders here too. See the board-page spec for the reasoning.
			Expect(permissionBlock).NotTo(ContainSubstring("<input"))
			Expect(permissionBlock).To(ContainSubstring(`class="jump-corner"`))
		})

		// The answer arm's terminal-state branch. The failure it exists for is an
		// item that left the queue between the render and the answer, and the
		// operator's report was of exactly that: a raw JSON body printed into a
		// failed note, and a card left in front of them for an item that no longer
		// existed. The script is inline and has no unit harness, so the assertions
		// are on what it ships — the code it recognises, the human line it writes,
		// and the return to the queue that follows. The browser click-through in
		// the task's Definition of Done is the half this cannot supply.
		It("recognises a closed item and returns the operator to the queue", func() {
			body := get("GET").Body.String()

			// The code is read out of the store's envelope rather than matched in
			// the raw body, so the branch fires on the classification and not on a
			// substring some other failure's message happens to contain.
			Expect(body).To(ContainSubstring("failure.code !== 'ITEM_CLOSED'"))
			Expect(body).To(ContainSubstring("failure.details.closed_at"))
			Expect(body).To(ContainSubstring("left the queue before this answer arrived"))

			// The line is shown AND the queue is returned to. A branch that
			// reloaded without showing would swallow the outcome into a reload,
			// which this file's own rule forbids; one that showed without
			// reloading would leave the stale card, which is the defect.
			Expect(body).To(ContainSubstring("showNote(form, failure.message, true)"))
			Expect(body).
				To(ContainSubstring("window.setTimeout(function () { window.location.reload(); }, 2500)"))
		})
	})

	// ackRequest builds a report-only declaration. An `ack` item is a condition
	// report, so it declares no options and no cardinality, and its liveness
	// model is a session so the pruning Read keeps it while the session is live.
	ackRequest := func(dedupKey pkg.DedupKey) pkg.PushRequest {
		producerID := pkg.ProducerID("producer-" + dedupKey.String())
		return pkg.PushRequest{
			ProducerID:      producerID,
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:" + producerID.String()),
			DedupKey:        dedupKey,
			InterruptClass:  "approve",
			Payload:         "the nightly sweep failed",
			AnswerMechanism: pkg.AckAnswerMechanism,
		}
	}

	It("renders the acknowledge control on an ack row, and on no other class", func() {
		ack, err := store.Push(ctx, ackRequest("report-ack"))
		Expect(err).To(BeNil())
		message, err := store.Push(ctx, pushRequest("producer-m", "gate-m", "deploy prod?"))
		Expect(err).To(BeNil())

		body := get("GET").Body.String()

		// The positive case: a report-only item carries a control where it used
		// to carry none, which is the defect this change exists to fix.
		ackBlock := rowOf(body, ack.ItemID)
		Expect(ackBlock).To(ContainSubstring("data-ack"))
		Expect(ackBlock).To(ContainSubstring("Acknowledge"))
		// It carries no answer form, and no Other field: an ack item asks
		// nothing, so the message card's controls would describe a choice it
		// does not offer.
		Expect(ackBlock).NotTo(ContainSubstring("<form"))
		Expect(ackBlock).NotTo(ContainSubstring(`class="other"`))

		// The negative control: the control is derived from the mechanism, so a
		// message row must not gain one. Without this the positive case would
		// pass on a page that rendered one identical control for every class.
		messageBlock := rowOf(body, message.ItemID)
		Expect(messageBlock).NotTo(ContainSubstring("data-ack"))
	})

	// ⚠️ RENAMED 2026-09-27: this spec was "keeps a permission row free of every
	// control, acknowledge included". That name stopped being true when the
	// corner X landed on permission rows, and a passing spec with a false name
	// is worse than no spec — it reads as coverage of a property that no longer
	// holds. What it actually guards is narrower and still load-bearing: the
	// ACKNOWLEDGE control must not reach a permission row.
	It("keeps a permission row free of the acknowledge control and every answer control", func() {
		permission, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      "producer-perm",
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:producer-perm"),
			DedupKey:        "gate-perm",
			InterruptClass:  "approve",
			Payload:         "approve the deploy",
			AnswerMechanism: pkg.PermissionAnswerMechanism,
		})
		Expect(err).To(BeNil())

		block := rowOf(get("GET").Body.String(), permission.ItemID)

		// A permission item is approve-shaped and only the operator may ANSWER
		// it, in the session that raised it. The schema forbids an arm answering
		// it, which is why the ack branch must not reach it — an acknowledge
		// there would be the laundering. ⚠️ The corner X is not an exception to
		// this: it CLEARS the card without answering, so it is not asserted
		// against here. See the inverted spec above.
		Expect(block).NotTo(ContainSubstring("data-ack"))
		Expect(block).NotTo(ContainSubstring("<form"))
		// ⚠️ AMENDED 2026-09-27: the jump corner renders on a permission row too.
		// The ack branch must still not reach it, which is what the line above
		// asserts and is the property this spec is for.
		Expect(block).To(ContainSubstring(`class="jump-corner"`))
		Expect(block).NotTo(ContainSubstring("<input"))
	})

	// The card's control set is derived from the item's answer mechanism in one
	// place, and this table is that derivation's own coverage. It iterates the
	// enum's collection rather than listing the members by hand, so a fourth
	// mechanism fails here until its affordance is decided — which is the
	// property the single switch exists for.
	//
	// ⚠️ `affordance` and `newAttentionPageRow` are both unexported and this file
	// is `package handler_test`, so the (message, ack) pair cannot be read
	// directly. Each case drives the page handler this file already builds and
	// asserts the RENDERED MARKUP of one item's row — never the whole body,
	// because the inline <script> carries the literal `data-ack` on every page,
	// so a page-wide `data-ack` assertion cannot fail.
	Describe("the affordance an item's answer mechanism carries", func() {
		// affordances is keyed by the enum itself rather than by string, so a
		// member spelled wrong is a compile error, and the table body can assert
		// that every member the enum declares has an entry here.
		affordances := map[pkg.AnswerMechanism]struct {
			message bool
			ack     bool
		}{
			pkg.MessageAnswerMechanism:    {message: true, ack: false},
			pkg.PermissionAnswerMechanism: {message: false, ack: false},
			pkg.AckAnswerMechanism:        {message: false, ack: true},
		}

		// affordanceRequest builds a pushable declaration for one mechanism. All
		// three are real, pushable items — `permission` is what exercises the new
		// default branch rather than a synthetic stand-in for it.
		affordanceRequest := func(mechanism pkg.AnswerMechanism) pkg.PushRequest {
			producerID := pkg.ProducerID("producer-affordance-" + mechanism.String())
			return pkg.PushRequest{
				ProducerID:      producerID,
				ProducerKind:    pkg.SessionProducerKind,
				LivenessRef:     pkg.LivenessRef("session:" + producerID.String()),
				DedupKey:        pkg.DedupKey("affordance-" + mechanism.String()),
				InterruptClass:  "approve",
				Payload:         "what does this card carry?",
				AnswerMechanism: mechanism,
			}
		}

		// The entries are built by iterating the enum's own collection, never by
		// listing the members here: a table that listed them by hand would keep
		// passing when a fourth is added, which is exactly what this table must
		// not do.
		entries := make([]TableEntry, 0, len(pkg.AvailableAnswerMechanisms))
		for _, mechanism := range pkg.AvailableAnswerMechanisms {
			entries = append(entries, Entry(mechanism.String(), mechanism))
		}

		DescribeTable("renders the controls the mechanism derives",
			func(mechanism pkg.AnswerMechanism) {
				expected, decided := affordances[mechanism]
				Expect(decided).To(
					BeTrue(),
					"no affordance decided for mechanism %q — decide it in affordance()",
					mechanism,
				)

				item, err := store.Push(ctx, affordanceRequest(mechanism))
				Expect(err).To(BeNil())

				row := rowOf(get("GET").Body.String(), item.ItemID)

				// Positive control: the row rendered at all, so the absences below
				// cannot pass on a page that dropped it.
				Expect(row).To(ContainSubstring(item.Payload.String()))

				// A `message` row renders the answer form and no acknowledge; an
				// `ack` row renders the acknowledge and no form; a `permission` row
				// renders neither, which is the default branch.
				if expected.message {
					Expect(row).To(ContainSubstring("<form"))
				} else {
					Expect(row).NotTo(ContainSubstring("<form"))
				}
				if expected.ack {
					Expect(row).To(ContainSubstring("data-ack"))
				} else {
					Expect(row).NotTo(ContainSubstring("data-ack"))
				}
			},
			entries,
		)
	})

	// The corner X. It is one affordance whose act is the mechanism's own
	// dominant act — an alias of Dismiss on a message card, of Acknowledge on an
	// ack card — so it adds no transition and no field. See [[Attention Item
	// Schema]] § Answer routing, § The corner X. The browser click-through in the
	// task's Definition of Done is the half these cannot supply.
	Describe("the corner X", func() {
		It("renders on a message row and dispatches the Dismiss the card already carries", func() {
			message, err := store.Push(
				ctx,
				messageRequest("corner-x-message", pkg.SingleAnswerCardinality),
			)
			Expect(err).To(BeNil())

			body := get("GET").Body.String()
			block := rowOf(body, message.ItemID)

			Expect(block).To(ContainSubstring("data-corner-x"))
			Expect(block).To(ContainSubstring(`aria-label="Skip this item"`))

			// The X's act is the Dismiss's act, and the assertion is on the
			// mechanism rather than on the label: the card already carries the
			// skip submit, so the X dispatches it instead of posting a second
			// write that merely agrees with it.
			Expect(block).To(ContainSubstring(`value="skip"`))
			Expect(body).To(ContainSubstring("button[data-corner-x]"))
			Expect(body).To(ContainSubstring(`form.querySelector('button[value=skip]')`))
		})

		It("renders on an ack row and shares the acknowledge close", func() {
			ack, err := store.Push(ctx, ackRequest("corner-x-ack"))
			Expect(err).To(BeNil())

			body := get("GET").Body.String()
			block := rowOf(body, ack.ItemID)

			Expect(block).To(ContainSubstring("data-corner-x"))
			Expect(block).To(ContainSubstring("data-ack"))

			// The ack card carries no form, so the X reaches the close path by
			// the named function both controls share rather than by dispatching
			// a submit that does not exist here.
			Expect(block).NotTo(ContainSubstring("<form"))
			// ⚠️ RENAMED 2026-09-27: the shared function is closeCard, not
			// closeAck — it serves a permission card's X as well as an ack
			// card's Acknowledge, so the ack-flavoured name misdescribed one of
			// its two callers.
			Expect(body).To(ContainSubstring("function closeCard(row)"))
			Expect(body).To(ContainSubstring("closeCard(row);"))
		})

		// ⚠️ INVERTED 2026-09-27 — this spec previously asserted the X was
		// ABSENT on a permission row ("renders no corner X on a permission
		// row"), on the reading that a control there would launder the gate.
		// That reading conflated ANSWERING with CLEARING, and the exception it
		// produced was disowned by the operator. The assertion is inverted
		// rather than deleted: a regression that re-excludes permission rows
		// must fail here, which is what keeps the exclusion from creeping back.
		It("renders the corner X on a permission row", func() {
			permission, err := store.Push(ctx, pkg.PushRequest{
				ProducerID:      "producer-corner-x-perm",
				ProducerKind:    pkg.SessionProducerKind,
				LivenessRef:     pkg.LivenessRef("session:producer-corner-x-perm"),
				DedupKey:        "corner-x-permission",
				InterruptClass:  "approve",
				Payload:         "approve the deploy",
				AnswerMechanism: pkg.PermissionAnswerMechanism,
			})
			Expect(err).To(BeNil())

			block := rowOf(get("GET").Body.String(), permission.ItemID)

			// The permission card carries the X from 2026-09-27. It still
			// carries no ANSWER control — the card renders no form, which is
			// what keeps the gate answerable only by the operator. The X closes
			// it without answering, so both assertions hold at once.
			Expect(block).To(ContainSubstring("data-corner-x"))
			// ⚠️ `<form`, not `class="answer"`: the two are on the same element
			// and equivalent, but `<form` is one of the three ANSWER-control
			// markers this change declared as the set to assert on. Using a
			// fourth spelling here would quietly widen that set.
			Expect(block).NotTo(ContainSubstring("<form"))
			// The jump corner is navigation rather than an answer, so it
			// renders here too — disabled when the row has no target.
			Expect(block).To(ContainSubstring(`class="jump-corner"`))

			// ⚠️ The precondition for a defect found by the served-page check on
			// 2026-09-27, asserted here so it cannot regress unnoticed: a
			// permission row renders NO .actions div — that div is the answer
			// controls' container, and only message and ack rows carry one. The
			// X's note therefore has to tolerate its absence, which it did not:
			// every X-click on a permission card threw a TypeError from
			// showCloseNote. The operator never saw it, because the row leaves
			// the page via the stream rather than via that handler — the correct
			// outcome masked a real error, which is why it is pinned twice here:
			// once as the missing container, once as the fallback that handles it.
			// ⚠️ FLIPPED 2026-09-29: a permission row now carries an .actions div
			// holding its Allow / Deny verdict buttons (operator decision), so the
			// container is present. The fallback stays pinned: an ack or message
			// row the stream re-renders without controls still needs it.
			Expect(block).To(ContainSubstring(`class="actions"`))
			Expect(block).To(ContainSubstring(`data-decision="allow"`))
			Expect(get("GET").Body.String()).
				To(ContainSubstring(`row.querySelector('.actions') || row`))
		})

		// The positive control read from the other end: a regression that
		// stripped the X from the whole page must fail here rather than pass
		// silently on the permission case alone.
		It("renders the X on both message and ack rows in the same run", func() {
			message, err := store.Push(
				ctx,
				messageRequest("corner-x-both-m", pkg.SingleAnswerCardinality),
			)
			Expect(err).To(BeNil())
			ack, err := store.Push(ctx, ackRequest("corner-x-both-a"))
			Expect(err).To(BeNil())

			body := get("GET").Body.String()
			Expect(rowOf(body, message.ItemID)).To(ContainSubstring("data-corner-x"))
			Expect(rowOf(body, ack.ItemID)).To(ContainSubstring("data-corner-x"))
		})
	})

	// The Jump control is bound by delegation, not per button, and the
	// difference is a defect rather than a preference: a listener attached to a
	// button dies with the node, and upsertRow replaces a row's whole outerHTML
	// on every stream event. A per-button binding therefore leaves every
	// re-rendered card's control inert while still rendering it correctly.
	// ⚠️ Assert the delegation, not the control's presence: "the button
	// renders" is exactly the check that kept passing while the control was
	// dead, which is how this shipped unnoticed.
	It("delegates the jump handler instead of binding each button once at load", func() {
		body := get("GET").Body.String()
		Expect(body).To(ContainSubstring("closest('button[data-jump]')"))
		Expect(body).NotTo(ContainSubstring("querySelectorAll('button[data-jump]').forEach"))
	})

	// The console trail. A failed action renders a transient note beside its
	// control and nothing else, so once the note scrolls out of view or the
	// stream replaces the row there is no evidence left that the action failed.
	// These specs assert the markers a devtools session greps for, in the served
	// source — which is the boundary this change crosses. They do NOT verify
	// browser behaviour: the script is inline and has no unit harness, and the
	// operator's own click-through is the half they cannot supply.
	It("logs every fetch failure to the console, and only the failures", func() {
		body := get("GET").Body.String()

		// A floor rather than an equality, and deliberately so: the two global
		// handlers and the stream's parse guard log as well, so the true total is
		// higher than the nine action lines this change adds.
		Expect(strings.Count(body, "console.error")).To(BeNumerically(">=", 9))

		// The throws nothing catches today. Both are registered at the top level
		// of the script rather than inside a handler that may never run.
		Expect(body).To(ContainSubstring("window.onerror"))
		Expect(body).To(ContainSubstring("unhandledrejection"))

		// Each action carries its own line, asserted one by one rather than as a
		// single count, so a line that exists but sits on the wrong action is
		// caught instead of satisfied by its neighbour's.
		Expect(body).To(ContainSubstring("attention board: answer failed - HTTP "))
		Expect(body).To(ContainSubstring("attention board: read aloud failed - HTTP "))
		Expect(body).To(ContainSubstring("attention board: jump failed - HTTP "))
		Expect(body).To(ContainSubstring("attention board: close failed - HTTP "))

		// The prefix is reserved for those nine action lines — the global handlers
		// and the parse guard log without it — which is what makes this count
		// unambiguous where a bare console.error count is not.
		Expect(strings.Count(body, "attention board: ")).To(Equal(9))

		// The answer path renders TWO failure branches and each logs, so a branch
		// that was logged and a branch that was forgotten are distinguishable
		// rather than both satisfying a bare count. The trailing "HTTP " is
		// load-bearing: it excludes the catch line, which carries no status.
		Expect(strings.Count(body, "attention board: answer failed - HTTP ")).To(Equal(2))
	})

	// The reserved prefix is asserted here as well as in the spec above, so a
	// later change that introduces a tenth occurrence fails loudly instead of
	// silently widening a name another spec depends on.
	It("keeps the reserved console prefix at exactly nine lines", func() {
		Expect(strings.Count(get("GET").Body.String(), "attention board: ")).To(Equal(9))
	})

	// The stream's own health. A stream that has stopped for good — a 404 after
	// a redeploy, a persistent 500 — left the board frozen at its load-time
	// state with no signal at all, so the operator read a stale board as a
	// current one. The state is a visible element rather than a console line
	// because reading it without devtools is the whole point.
	// These specs assert presence in the served source, which is the boundary
	// this change crosses. They do NOT verify browser behaviour: the script is
	// inline and has no unit harness, and the operator's own click-through is
	// the half that runs outside this container.
	It("says when the stream has stopped, beside the board's filter switch", func() {
		body := get("GET").Body.String()

		// The handler that raises the state, and the class that styles it in
		// the palette's existing warn colour.
		Expect(body).To(ContainSubstring("source.onerror"))
		Expect(body).To(ContainSubstring("stream-stale"))

		// It sits in the board's control row, beside the filter switch — the
		// element that describes the board rather than an item. Scoped to the
		// row's own block, so a copy of the class elsewhere on the page cannot
		// satisfy this.
		controls := strings.Index(body, `<div class="board-controls">`)
		Expect(controls).To(BeNumerically(">=", 0), "no control row rendered")
		rest := body[controls:]
		end := strings.Index(rest, "</div>")
		Expect(end).To(BeNumerically(">=", 0))
		Expect(rest[:end]).To(ContainSubstring(`data-stream-stale`))
		Expect(rest[:end]).To(ContainSubstring(`data-board-filter`))

		// ⚠️ Hidden in the served markup, and that is a requirement rather than
		// a detail: a healthy load, an empty board and a board whose stream is
		// merely quiet must each render exactly what they rendered before. Only
		// the stream's own onerror can show it.
		Expect(body).To(ContainSubstring(
			`<span class="stream-stale" data-stream-stale hidden>`,
		))

		// And it clears itself on the next message, so a brief restart of the
		// store shows it only for the gap. Both directions are counted, so a
		// handler that raised the state and never cleared it fails here.
		Expect(strings.Count(body, "setTracking(false)")).To(Equal(1))
		Expect(strings.Count(body, "setTracking(true)")).To(Equal(1))
	})

	// A failure note is a child of the row it was rendered into, and the stream
	// replaces a row's whole node on every event — so the note the operator was
	// reading was destroyed mid-sentence. The most recent failure per item is
	// kept in a map keyed on the item id, mirroring the speaking map, and
	// rendered into the row BEFORE it is committed, on both the visible and the
	// parked path. ⚠️ Before, not after: the swap-then-repair order left a row
	// swapped with its note missing whenever the repair threw, which is the
	// half-update the ordering assertion below exists to prevent returning.
	It("re-renders a failure note after the stream replaces its row", func() {
		body := get("GET").Body.String()

		// The map itself, keyed the way speaking is.
		Expect(body).To(ContainSubstring("var failures = {}"))

		// Recorded when a helper renders a failure, and cleared when the same
		// action next succeeds: a stale failure note outliving its cause is a
		// worse defect than the one being fixed, because it reports a failure
		// that is no longer true.
		Expect(body).To(ContainSubstring("failures[itemID] = { kind: kind, message: message }"))
		Expect(body).To(ContainSubstring("delete failures[itemID]"))

		// Every note helper records, asserted one by one rather than as a
		// count, so a helper that stopped recording is caught rather than
		// satisfied by its neighbour's call.
		Expect(body).To(ContainSubstring("rememberFailure(form, 'form', message, isError)"))
		Expect(body).To(ContainSubstring("rememberFailure(row, 'jump', message, isError)"))
		Expect(body).To(ContainSubstring("rememberFailure(row, 'close', message, isError)"))

		// Re-rendered on BOTH paths through ONE prepare step, which renders the
		// note into a detached node before the row is committed.
		Expect(body).To(ContainSubstring("function preparedHTML(html, itemID)"))
		Expect(body).To(ContainSubstring("replayFailure(row)"))
		Expect(body).To(ContainSubstring("var prepared = preparedHTML(html, itemID)"))
		// The parked path, where the row exists only as the markup the filter
		// stored and so has to be parsed to be re-rendered into.
		Expect(body).To(ContainSubstring("parked[hidden].html = prepared"))
		// ⚠️ Asserted by POSITION, not by presence. The same two lines in the
		// old order — swap first, then replay — compile and read perfectly well
		// while restoring the half-update defect, so a presence check would pass
		// on exactly the regression this spec exists to catch.
		prepareAt := strings.Index(body, "var prepared = preparedHTML(html, itemID)")
		swapAt := strings.Index(body, "row.outerHTML = prepared")
		Expect(prepareAt).To(BeNumerically(">=", 0), "the prepare step is gone")
		Expect(swapAt).To(BeNumerically(">=", 0), "the swap is gone")
		Expect(prepareAt).To(BeNumerically("<", swapAt),
			"the row must be prepared before it is swapped, or a throw between them half-updates it")

		// Through the helper that rendered the note, never through a second
		// renderer that could drift from the three note helpers.
		Expect(body).To(ContainSubstring("showJumpNote(row, failure.message, true)"))
		Expect(body).To(ContainSubstring("showCloseNote(row, failure.message, true)"))
	})
})
