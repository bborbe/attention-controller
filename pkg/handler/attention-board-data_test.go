// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"

	libboltkv "github.com/bborbe/boltkv"
	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/handler"
)

// The data the server-rendered board page computed per row, asserted on the
// board JSON (`GET /api/1.0/attention/board`) the React board renders from. A
// real boltkv DB and a real store, never a fake of either: ReadBoard prunes open
// items whose producer is not live and omits closed items and probes, so a faked
// store would let the board serve fixtures the production read path drops.
//
// Every href is a hand-written literal: a helper shared with the production code
// would agree with itself whatever it produced.
var _ = Describe("the board JSON's per-card data", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var provenance *mocks.ProvenanceResolver
	var httpHandler http.Handler
	// vaultDir ends in a known name so the expected hrefs can be literals. The
	// directory itself need not exist: only its base name is read.
	var vaultDir string

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

		// The mock defaults to false, which would prune every fixture during the
		// read and make every absence assertion below pass vacuously.
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
		vaultDir = filepath.Join(GinkgoT().TempDir(), "Personal")
		httpHandler = handler.NewAttentionBoardReadHandler(store, provenance, false, vaultDir)
	})

	AfterEach(func() {
		// bbolt's Close is idempotent, so the read-failure case closing the db
		// first does not turn this into a second-close failure.
		Expect(db.Close()).To(BeNil())
	})

	pushRequest := func(
		producerID pkg.ProducerID,
		dedupKey pkg.DedupKey,
		payload pkg.Payload,
	) pkg.PushRequest {
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

	push := func(producerID pkg.ProducerID, dedupKey pkg.DedupKey, payload pkg.Payload) *pkg.Item {
		item, err := store.Push(ctx, pushRequest(producerID, dedupKey, payload))
		Expect(err).To(BeNil())
		return item
	}

	viewOf := func(itemID pkg.ItemID) pkg.BoardView {
		return boardEntryOf(httpHandler, itemID).Board
	}

	Describe("which cards the board serves", func() {
		It("serves one entry per open item, carrying the item's own fields", func() {
			first := push("producer-a", "gate-a", "deploy prod?")
			second := push("producer-b", "gate-b", "approve the migration")

			board := readBoardByID(httpHandler)

			Expect(board).To(HaveLen(2))
			for _, item := range []*pkg.Item{first, second} {
				Expect(board).To(HaveKey(item.ItemID))
				entry := board[item.ItemID]
				Expect(entry.ProducerID).To(Equal(item.ProducerID))
				Expect(entry.ProducerKind).To(Equal(item.ProducerKind))
				Expect(entry.Payload).To(Equal(item.Payload))
				Expect(entry.State).To(Equal(item.State))
				Expect(entry.CreatedAt.Time().Equal(item.CreatedAt.Time())).To(BeTrue())
			}
		})

		It("serves no entry for a synthetic probe, and an entry for the card beside it", func() {
			probe := push(
				"producer-probe",
				"probe-1",
				pkg.Payload(pkg.ProbeTagPrefix+" #218: synthetic dismissal"),
			)
			ordinary := push("producer-b", "gate-b", "approve the migration")

			board := readBoardByID(httpHandler)

			Expect(board).NotTo(HaveKey(probe.ItemID))
			Expect(board).To(HaveKey(ordinary.ItemID))
			Expect(board).To(HaveLen(1))
		})

		It("omits closed items while keeping open ones", func() {
			open := push("producer-open", "gate-open", "still open")
			closed := push("producer-closed", "gate-closed", "already done")
			_, err := store.Close(ctx, closed.ItemID, "", nil)
			Expect(err).To(BeNil())

			board := readBoardByID(httpHandler)

			Expect(board).To(HaveKey(open.ItemID))
			Expect(board).NotTo(HaveKey(closed.ItemID))
		})

		It("keeps serving an answered item, carrying the recorded answer and client", func() {
			// The dimmed record: an answered card stays on the board until it is
			// closed, so the operator can see the answer standing in their name.
			item := push("producer-answered", "gate-answered", "Which surface?")
			answer := pkg.Answer{Kind: pkg.TextAnswerKind, Value: "the board"}
			_, err := store.Answer(
				ctx,
				item.ItemID,
				"attention-board",
				"",
				"",
				&answer,
				nil,
				&pkg.AnsweredClient{
					UserAgent:    "Mozilla/5.0 (Macintosh)",
					RemoteAddr:   "127.0.0.1:51422",
					ForwardedFor: "10.0.0.7",
				},
			)
			Expect(err).To(BeNil())
			beside := push("producer-beside", "gate-beside", "still open")

			board := readBoardByID(httpHandler)

			entry := board[item.ItemID]
			Expect(entry.State).To(Equal(pkg.AnsweredState))
			Expect(entry.AnsweredBy).To(Equal("attention-board"))
			Expect(entry.Answer).NotTo(BeNil())
			Expect(entry.Answer.Value).To(Equal("the board"))
			Expect(entry.AnsweredClient).NotTo(BeNil())
			Expect(entry.AnsweredClient.UserAgent).To(Equal("Mozilla/5.0 (Macintosh)"))
			Expect(entry.AnsweredClient.RemoteAddr).To(Equal("127.0.0.1:51422"))
			Expect(entry.AnsweredClient.ForwardedFor).To(Equal("10.0.0.7"))
			// An open item in the same read stays open and unanswered.
			Expect(board[beside.ItemID].State).To(Equal(pkg.OpenState))
			Expect(board[beside.ItemID].Answer).To(BeNil())
		})

		It("serves every producer kind, leaving the kind filter to the client", func() {
			// The managers-only default view is the React board's filter; the
			// read serves all kinds and the uncovered marker the filter reads.
			manager, err := store.Push(ctx, pkg.PushRequest{
				ProducerID:      "producer-manager",
				ProducerKind:    pkg.ManagerProducerKind,
				LivenessRef:     pkg.LivenessRef("session:producer-manager"),
				DedupKey:        "kind-manager",
				InterruptClass:  "approve",
				Payload:         "manager ask",
				AnswerMechanism: pkg.MessageAnswerMechanism,
			})
			Expect(err).To(BeNil())
			session := push("producer-session", "kind-session", "session ask")
			provenance.ResolveReturns(pkg.Provenances{
				session.ItemID: pkg.Provenance{Uncovered: true},
			})

			board := readBoardByID(httpHandler)

			Expect(board[manager.ItemID].ProducerKind).To(Equal(pkg.ManagerProducerKind))
			Expect(board[manager.ItemID].Board.Uncovered).To(BeFalse())
			Expect(board[session.ItemID].ProducerKind).To(Equal(pkg.SessionProducerKind))
			Expect(board[session.ItemID].Board.Uncovered).To(BeTrue())
		})

		It("answers an empty list for an empty store", func() {
			Expect(readBoardByID(httpHandler)).To(BeEmpty())
		})

		It("returns the standard JSON error body when the read fails", func() {
			Expect(db.Close()).To(BeNil())

			req := httptest.NewRequest(http.MethodGet, "/api/1.0/attention/board", nil)
			resp := httptest.NewRecorder()
			httpHandler.ServeHTTP(resp, req)

			Expect(resp.Code).To(Equal(http.StatusInternalServerError))
			var errorResponse libhttp.ErrorResponse
			Expect(json.NewDecoder(resp.Body).Decode(&errorResponse)).To(BeNil())
			Expect(errorResponse.Error.Code).To(Equal(libhttp.ErrorCodeInternal))
			Expect(errorResponse.Error.Message).To(ContainSubstring("read failed"))
		})
	})

	Describe("the machine identity", func() {
		It("carries host, cwd, tool and pane on a resolving card", func() {
			item := push("producer-prov", "gate-prov", "deploy prod?")
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

			prov := viewOf(item.ItemID).Provenance

			Expect(prov.Host).To(Equal("burn"))
			Expect(prov.Cwd).To(Equal("/Users/bborbe/Documents/workspaces/attention-controller"))
			Expect(prov.Tool).To(Equal("AskUserQuestion"))
			Expect(prov.Pane).To(Equal("1140"))
			Expect(prov.Unroutable).To(BeFalse())
		})

		It("carries each card's own values, never a neighbour's", func() {
			first := push("producer-info-a", "gate-info-a", "first ask")
			second := push("producer-info-b", "gate-info-b", "second ask")
			provenance.ResolveReturns(pkg.Provenances{
				first.ItemID:  pkg.Provenance{Host: "burn", Cwd: "/w/a", Tool: "Bash"},
				second.ItemID: pkg.Provenance{Host: "hell", Cwd: "/w/b", Tool: "Edit"},
			})

			board := readBoardByID(httpHandler)

			Expect(board[first.ItemID].Board.Provenance.Host).To(Equal("burn"))
			Expect(board[first.ItemID].Board.Provenance.Cwd).To(Equal("/w/a"))
			Expect(board[first.ItemID].Board.Provenance.Tool).To(Equal("Bash"))
			Expect(board[second.ItemID].Board.Provenance.Host).To(Equal("hell"))
			Expect(board[second.ItemID].Board.Provenance.Cwd).To(Equal("/w/b"))
			Expect(board[second.ItemID].Board.Provenance.Tool).To(Equal("Edit"))
		})

		It("marks an unvalidated pane unroutable, carries no pane id, and invents nothing", func() {
			item := push("producer-unroutable", "gate-unroutable", "who owns this?")
			// A recorded pane that did not validate against the session — a
			// recycled id resolving to another session's pane (silence 7).
			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{
					Host:         "burn",
					Cwd:          "/tmp",
					PaneRecorded: true,
					Routable:     false,
				},
			})

			prov := viewOf(item.ItemID).Provenance

			Expect(prov.Unroutable).To(BeTrue())
			// Withheld entirely: showing it invites routing to somebody else's pane.
			Expect(prov.Pane).To(BeEmpty())
			Expect(prov.PaneUnvalidated).To(BeFalse())
			// An absent field is absent, never a placeholder.
			Expect(prov.Tool).To(BeEmpty())
		})

		It("carries no provenance at all when nothing resolves", func() {
			first := push("producer-noprov-a", "gate-noprov-a", "first ask")
			second := push("producer-noprov-b", "gate-noprov-b", "second ask")
			provenance.ResolveReturns(pkg.Provenances{})

			board := readBoardByID(httpHandler)

			// No provenance source is not an error and does not blank the board.
			Expect(board).To(HaveLen(2))
			for _, item := range []*pkg.Item{first, second} {
				Expect(board[item.ItemID].Payload).To(Equal(item.Payload))
				Expect(board[item.ItemID].Board.Provenance).To(Equal(pkg.BoardProvenance{}))
			}
		})
	})

	Describe("the vault task link", func() {
		taskHref := "obsidian://open?vault=Personal&file=25%20Tasks%2FFix%20the%20board"

		It("carries the resolved task as a link to the vault's own URL", func() {
			item := push("producer-task", "gate-task", "which task is this?")
			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{
					Host:     "burn",
					TaskName: "Fix the board",
					TaskPath: "25 Tasks/Fix the board.md",
				},
			})

			prov := viewOf(item.ItemID).Provenance

			Expect(prov.TaskName).To(Equal("Fix the board"))
			Expect(prov.TaskHref).To(Equal(taskHref))
		})

		It("carries no link for an item whose session anchors no task", func() {
			item := push("producer-notask", "gate-notask", "no task here")
			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{Host: "burn", Cwd: "/tmp"},
			})

			prov := viewOf(item.ItemID).Provenance

			// Positive control: the card's other provenance did resolve.
			Expect(prov.Host).To(Equal("burn"))
			Expect(prov.TaskName).To(BeEmpty())
			Expect(prov.TaskHref).To(BeEmpty())
		})

		It("carries the task link for a task-only provenance", func() {
			item := push("producer-taskonly", "gate-taskonly", "task and nothing else")
			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{
					TaskName: "Fix the board",
					TaskPath: "25 Tasks/Fix the board.md",
				},
			})

			Expect(viewOf(item.ItemID).Provenance.TaskHref).To(Equal(taskHref))
		})

		It("escapes a task name the query grammar would otherwise split on", func() {
			item := push("producer-amp", "gate-amp", "which task is this?")
			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{
					Host:     "burn",
					TaskName: "R&D notes",
					TaskPath: "25 Tasks/R&D notes.md",
				},
			})

			prov := viewOf(item.ItemID).Provenance

			Expect(prov.TaskHref).To(Equal(
				"obsidian://open?vault=Personal&file=25%20Tasks%2FR%26D%20notes",
			))
			Expect(prov.TaskName).To(Equal("R&D notes"))
		})
	})

	Describe("the session name", func() {
		It("carries the resolved session name", func() {
			item := push("producer-name", "gate-name", "which session is this?")
			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{SessionName: "Board Polish Session"},
			})

			Expect(viewOf(item.ItemID).Provenance.SessionName).To(Equal("Board Polish Session"))
		})

		It("carries the session name beside the task, goal and topic links", func() {
			item := push("producer-four", "gate-four", "which session is this?")
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

			prov := viewOf(item.ItemID).Provenance

			Expect(prov.TaskHref).NotTo(BeEmpty())
			Expect(prov.GoalHref).NotTo(BeEmpty())
			Expect(prov.TopicHref).NotTo(BeEmpty())
			Expect(prov.SessionName).To(Equal("Board Polish Session"))
		})

		It("carries no session name when the name is absent", func() {
			item := push("producer-noname", "gate-noname", "which session is this?")
			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{Host: "burn", Cwd: "/tmp"},
			})

			prov := viewOf(item.ItemID).Provenance

			Expect(prov.Host).To(Equal("burn"))
			Expect(prov.SessionName).To(BeEmpty())
		})

		It("carries each distinct navigation value once", func() {
			equal := push("producer-nav-equal", "nav-equal", "which task is this?")
			glyph := push("producer-nav-glyph", "nav-glyph", "which task is this?")
			differing := push("producer-nav-differing", "nav-differing", "what next?")
			provenance.ResolveReturns(pkg.Provenances{
				equal.ItemID: {
					TaskName:    "Fix the board",
					TaskPath:    "25 Tasks/Fix the board.md",
					SessionName: "Fix the board",
				},
				glyph.ItemID: {
					TaskName:    "Fix the board",
					TaskPath:    "25 Tasks/Fix the board.md",
					SessionName: "⚙ Fix the board",
				},
				differing.ItemID: {
					TaskName:    "Fix the board",
					TaskPath:    "25 Tasks/Fix the board.md",
					SessionName: "Board Polish Session",
				},
			})

			board := readBoardByID(httpHandler)

			// A session named after its task — with or without Claude Code's
			// status glyph — is carried once, as the task link.
			Expect(board[equal.ItemID].Board.Provenance.TaskName).To(Equal("Fix the board"))
			Expect(board[equal.ItemID].Board.Provenance.SessionName).To(BeEmpty())
			Expect(board[glyph.ItemID].Board.Provenance.SessionName).To(BeEmpty())
			Expect(board[differing.ItemID].Board.Provenance.TaskName).To(Equal("Fix the board"))
			Expect(board[differing.ItemID].Board.Provenance.SessionName).
				To(Equal("Board Polish Session"))
		})
	})

	Describe("the goal and topic links", func() {
		goalHref := "obsidian://open?vault=Personal&file=24%20Goals%2FFix%20the%20board"
		topicHref := "obsidian://open?vault=Personal&file=23%20Topics%2FAttention%20Board%20Polish"

		It("carries task, goal and topic as three links", func() {
			item := push("producer-goaltopic", "gate-goaltopic", "which goal is this?")
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

			prov := viewOf(item.ItemID).Provenance

			Expect(prov.GoalName).To(Equal("Fix the board"))
			Expect(prov.GoalHref).To(Equal(goalHref))
			Expect(prov.TopicName).To(Equal("Attention Board Polish"))
			Expect(prov.TopicHref).To(Equal(topicHref))
		})

		It("carries the goal and no topic when no topic lists that goal", func() {
			item := push("producer-goalonly", "gate-goalonly", "which goal is this?")
			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{
					Host:     "burn",
					TaskName: "Fix the board",
					TaskPath: "25 Tasks/Fix the board.md",
					GoalName: "Fix the board",
					GoalPath: "24 Goals/Fix the board.md",
				},
			})

			prov := viewOf(item.ItemID).Provenance

			Expect(prov.GoalHref).To(Equal(goalHref))
			Expect(prov.TopicHref).To(BeEmpty())
			Expect(prov.TopicName).To(BeEmpty())
		})

		It("carries no goal and no topic for a task whose goals list is empty", func() {
			item := push("producer-nogoal", "gate-nogoal", "no goal here")
			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{
					Host:     "burn",
					TaskName: "Fix the board",
					TaskPath: "25 Tasks/Fix the board.md",
				},
			})

			prov := viewOf(item.ItemID).Provenance

			Expect(prov.TaskHref).NotTo(BeEmpty())
			Expect(prov.GoalHref).To(BeEmpty())
			Expect(prov.TopicHref).To(BeEmpty())
		})

		It("escapes a goal name the query grammar would otherwise split on", func() {
			item := push("producer-goalamp", "gate-goalamp", "which goal is this?")
			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{
					Host:     "burn",
					TaskName: "Fix the board",
					TaskPath: "25 Tasks/Fix the board.md",
					GoalName: "R&D notes",
					GoalPath: "24 Goals/R&D notes.md",
				},
			})

			Expect(viewOf(item.ItemID).Provenance.GoalHref).To(Equal(
				"obsidian://open?vault=Personal&file=24%20Goals%2FR%26D%20notes",
			))
		})
	})

	Describe("the jump target", func() {
		It("explains a card that carries no jump target, without naming a value", func() {
			item := push("producer-nojump", "gate-nojump", "why is there no button?")
			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{Host: "burn", Cwd: "/tmp"},
			})

			view := viewOf(item.ItemID)

			Expect(view.JumpURL).To(BeEmpty())
			Expect(view.JumpCommand).To(BeEmpty())
			Expect(view.NoJump).To(Equal(
				"No pane was recorded for this item, so there is no session to jump to.",
			))
		})

		It("gives the unroutable card its own sentence and keeps silence 7's marker", func() {
			item := push("producer-nojump-unroutable", "gate-nojump-unroutable", "who owns this?")
			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{
					Host:         "burn",
					Cwd:          "/tmp",
					PaneRecorded: true,
					Routable:     false,
				},
			})

			view := viewOf(item.ItemID)

			Expect(view.Provenance.Unroutable).To(BeTrue())
			Expect(view.NoJump).To(Equal(
				"The pane recorded for this item does not resolve to this session.",
			))
		})

		It("carries a working target on a resolved pane, with no explanation", func() {
			item := push("producer-nojump-token", "gate-nojump-token", "token is gone?")
			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{
					Host:         "burn",
					Cwd:          "/tmp",
					Pane:         "1140",
					PaneRecorded: true,
					Routable:     true,
				},
			})

			view := viewOf(item.ItemID)

			Expect(view.JumpURL).To(Equal("/jump/" + item.ItemID.String()))
			Expect(view.NoJump).To(BeEmpty())
		})

		It("derives the explanation per read, so a changed reason changes it with no store write", func() {
			item := push("producer-nojump-render", "gate-nojump-render", "does this change?")
			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{Host: "burn", Cwd: "/tmp"},
			})
			Expect(viewOf(item.ItemID).NoJump).To(ContainSubstring("No pane was recorded"))

			provenance.ResolveReturns(pkg.Provenances{
				item.ItemID: pkg.Provenance{
					Host:         "burn",
					Cwd:          "/tmp",
					PaneRecorded: true,
					Routable:     false,
				},
			})

			Expect(viewOf(item.ItemID).NoJump).To(ContainSubstring("does not resolve to this session"))
		})

		It("keeps its jump target, marked unvalidated, when only the pane listing was unreadable", func() {
			// The real resolver over a real event log and session registry, with a
			// pane listing that fails — the wedged-mux case.
			stateDir := GinkgoT().TempDir()
			sessionsDir := GinkgoT().TempDir()
			Expect(os.WriteFile(
				filepath.Join(stateDir, "producer-nopane.events.jsonl"),
				[]byte(`{"item_id":"gate-nopane","session_id":"producer-nopane",`+
					`"host":"burn","cwd":"/tmp","tool_name":"","pane":"58"}`+"\n"),
				0o600,
			)).To(BeNil())
			Expect(os.WriteFile(
				filepath.Join(sessionsDir, "12345.json"),
				[]byte(`{"sessionId":"producer-nopane","name":"⚙ Go Mod Update 2026-10"}`),
				0o600,
			)).To(BeNil())

			paneLister := &mocks.PaneLister{}
			paneLister.ListReturns(nil, errors.New(ctx, "wezterm not found"))

			httpHandler = handler.NewAttentionBoardReadHandler(
				store,
				pkg.NewProvenanceResolver(
					pkg.NewEventLogReader(stateDir),
					sessionsDir,
					pkg.NewSessionLedgerStore(db),
					paneLister,
					nil,
					libtime.NewCurrentDateTime(),
				),
				false,
				vaultDir,
			)

			item := push("producer-nopane", "gate-nopane", "who owns this?")

			view := viewOf(item.ItemID)

			Expect(view.JumpURL).To(Equal("/jump/" + item.ItemID.String()))
			Expect(view.Provenance.Pane).To(Equal("58"))
			Expect(view.Provenance.PaneUnvalidated).To(BeTrue())
			Expect(view.NoJump).To(BeEmpty())
		})
	})

	Describe("the read-aloud flag", func() {
		It("carries speak_enabled on every card when read-aloud is enabled, and on none when not", func() {
			first := push("producer-speak-a", "gate-speak-a", "first ask")
			second := push("producer-speak-b", "gate-speak-b", "second ask")

			for _, entry := range readBoardByID(httpHandler) {
				Expect(entry.Board.SpeakEnabled).To(BeFalse())
			}

			enabled := readBoardByID(
				handler.NewAttentionBoardReadHandler(store, provenance, true, vaultDir),
			)
			Expect(enabled[first.ItemID].Board.SpeakEnabled).To(BeTrue())
			Expect(enabled[second.ItemID].Board.SpeakEnabled).To(BeTrue())
		})
	})

	Describe("the flag", func() {
		It("carries the stored flag reason on a flagged card and none on an unflagged one", func() {
			flagged := push("producer-flagged", "gate-flagged", "misrouted?")
			plain := push("producer-plain", "gate-plain", "routed fine")
			_, err := store.Flag(ctx, flagged.ItemID, "asked the wrong team")
			Expect(err).To(BeNil())

			board := readBoardByID(httpHandler)

			Expect(board[flagged.ItemID].FlagReason).To(Equal("asked the wrong team"))
			Expect(board[flagged.ItemID].FlaggedAt).NotTo(BeNil())
			Expect(board[plain.ItemID].FlagReason).To(BeEmpty())
			Expect(board[plain.ItemID].FlaggedAt).To(BeNil())
		})
	})
})
