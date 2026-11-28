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
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/handler"
)

var _ = Describe("AttentionBoardReadHandler", func() {
	var store *mocks.AttentionStore
	var provenance *mocks.ProvenanceResolver
	var vaultDir string
	var speakEnabled bool

	parse := func(s string) libtime.DateTime {
		t, err := time.Parse(time.RFC3339, s)
		Expect(err).NotTo(HaveOccurred())
		return libtime.DateTime(t)
	}
	ptr := func(t libtime.DateTime) *libtime.DateTime { return &t }

	item := func(itemID pkg.ItemID, mechanism pkg.AnswerMechanism) pkg.Item {
		return pkg.Item{
			ItemID:          itemID,
			ProducerID:      pkg.ProducerID("producer-a"),
			ProducerKind:    pkg.SessionProducerKind,
			Payload:         "Which surface?",
			State:           pkg.OpenState,
			AnswerMechanism: mechanism,
			CreatedAt:       parse("2026-11-11T10:00:01Z"),
		}
	}

	BeforeEach(func() {
		store = &mocks.AttentionStore{}
		store.DeliveryReportsReturns(map[pkg.ItemID]*pkg.DeliveryReport{}, nil)
		provenance = &mocks.ProvenanceResolver{}
		provenance.ResolveReturns(pkg.Provenances{})
		vaultDir = filepath.Join(GinkgoT().TempDir(), "Personal")
		speakEnabled = false
	})

	// read serves the board once and decodes the JSON into the wire shape.
	read := func() []pkg.BoardItem {
		httpHandler := handler.NewAttentionBoardReadHandler(
			store,
			provenance,
			speakEnabled,
			vaultDir,
		)
		req := httptest.NewRequest(http.MethodGet, "/api/1.0/attention/board", nil)
		resp := httptest.NewRecorder()
		httpHandler.ServeHTTP(resp, req)
		Expect(resp.Code).To(Equal(http.StatusOK), resp.Body.String())
		var decoded []pkg.BoardItem
		Expect(json.Unmarshal(resp.Body.Bytes(), &decoded)).To(Succeed())
		return decoded
	}

	It("keeps the item's own fields flat and carries the render data under board", func() {
		store.ReadBoardReturns(pkg.Items{item("flat", pkg.MessageAnswerMechanism)}, nil)

		httpHandler := handler.NewAttentionBoardReadHandler(store, provenance, false, vaultDir)
		req := httptest.NewRequest(http.MethodGet, "/api/1.0/attention/board", nil)
		resp := httptest.NewRecorder()
		httpHandler.ServeHTTP(resp, req)
		Expect(resp.Code).To(Equal(http.StatusOK))

		var raw []map[string]any
		Expect(json.Unmarshal(resp.Body.Bytes(), &raw)).To(Succeed())
		Expect(raw).To(HaveLen(1))
		Expect(raw[0]).To(HaveKeyWithValue("item_id", "flat"))
		Expect(raw[0]).To(HaveKeyWithValue("payload", "Which surface?"))
		Expect(raw[0]).To(HaveKey("board"))
		board := raw[0]["board"].(map[string]any)
		Expect(board).To(HaveKey("jump_url"))
		Expect(board).To(HaveKey("uncovered"))
		Expect(board).To(HaveKey("provenance"))
		Expect(board).To(HaveKey("timeline"))
	})

	It("carries the jump URL when a pane validates", func() {
		itemID := pkg.ItemID("jump-pane")
		store.ReadBoardReturns(pkg.Items{item(itemID, pkg.PermissionAnswerMechanism)}, nil)
		provenance.ResolveReturns(pkg.Provenances{
			itemID: {Pane: "42", PaneRecorded: true, Routable: true},
		})

		board := read()
		Expect(board).To(HaveLen(1))
		view := board[0].Board
		Expect(view.JumpURL).To(Equal("/jump/jump-pane"))
		Expect(view.JumpCommand).To(Equal("/supervisor:jump 42"))
		Expect(view.NoJump).To(BeEmpty())
		Expect(view.Provenance.Pane).To(Equal("42"))
		Expect(view.Provenance.PaneUnvalidated).To(BeFalse())
	})

	It("carries the recorded pane marked unvalidated when the listing was unreadable", func() {
		itemID := pkg.ItemID("jump-unvalidated")
		store.ReadBoardReturns(pkg.Items{item(itemID, pkg.MessageAnswerMechanism)}, nil)
		provenance.ResolveReturns(pkg.Provenances{
			itemID: {PaneRecorded: true, PaneListingUnreadable: true, RecordedPane: "7"},
		})

		view := read()[0].Board
		Expect(view.JumpURL).To(Equal("/jump/jump-unvalidated"))
		// A message item renders the button without the copyable command.
		Expect(view.JumpCommand).To(BeEmpty())
		Expect(view.Provenance.Pane).To(Equal("7"))
		Expect(view.Provenance.PaneUnvalidated).To(BeTrue())
	})

	It("carries no jump URL and the reason when no pane validates", func() {
		recordedID := pkg.ItemID("jump-unroutable")
		noneID := pkg.ItemID("jump-none")
		store.ReadBoardReturns(pkg.Items{
			item(recordedID, pkg.MessageAnswerMechanism),
			item(noneID, pkg.MessageAnswerMechanism),
		}, nil)
		provenance.ResolveReturns(pkg.Provenances{
			recordedID: {PaneRecorded: true},
		})

		board := read()
		Expect(board).To(HaveLen(2))
		unroutable := board[0].Board
		Expect(unroutable.JumpURL).To(BeEmpty())
		Expect(unroutable.NoJump).To(Equal(
			"The pane recorded for this item does not resolve to this session."))
		Expect(unroutable.Provenance.Unroutable).To(BeTrue())
		Expect(unroutable.Provenance.Pane).To(BeEmpty())

		none := board[1].Board
		Expect(none.JumpURL).To(BeEmpty())
		Expect(none.NoJump).To(Equal(
			"No pane was recorded for this item, so there is no session to jump to."))
		Expect(none.Provenance.Unroutable).To(BeFalse())
	})

	It("carries uncovered exactly as the resolver reports it", func() {
		uncoveredID := pkg.ItemID("uncovered")
		coveredID := pkg.ItemID("covered")
		store.ReadBoardReturns(pkg.Items{
			item(uncoveredID, pkg.MessageAnswerMechanism),
			item(coveredID, pkg.MessageAnswerMechanism),
		}, nil)
		provenance.ResolveReturns(pkg.Provenances{
			uncoveredID: {Uncovered: true},
			coveredID:   {Uncovered: false},
		})

		board := read()
		Expect(board[0].Board.Uncovered).To(BeTrue())
		Expect(board[1].Board.Uncovered).To(BeFalse())
	})

	It("carries the navigation line with vault links and a de-duplicated session name", func() {
		linkedID := pkg.ItemID("linked")
		namedID := pkg.ItemID("named")
		store.ReadBoardReturns(pkg.Items{
			item(linkedID, pkg.MessageAnswerMechanism),
			item(namedID, pkg.MessageAnswerMechanism),
		}, nil)
		provenance.ResolveReturns(pkg.Provenances{
			linkedID: {
				TaskName:    "Fix the Board",
				TaskPath:    "25 Tasks/Fix the Board.md",
				GoalName:    "Ship the Board",
				GoalPath:    "24 Goals/Ship the Board.md",
				SessionName: "⚙ Fix the Board",
				Host:        "host-a",
				Cwd:         "/work",
				Tool:        "claude",
			},
			namedID: {SessionName: "e2e"},
		})

		board := read()
		linked := board[0].Board.Provenance
		Expect(linked.TaskName).To(Equal("Fix the Board"))
		Expect(linked.TaskHref).To(Equal(
			"obsidian://open?vault=Personal&file=25%20Tasks%2FFix%20the%20Board"))
		Expect(linked.GoalHref).To(Equal(
			"obsidian://open?vault=Personal&file=24%20Goals%2FShip%20the%20Board"))
		Expect(linked.TopicHref).To(BeEmpty())
		// The session name repeats the task title, so the span renders nothing.
		Expect(linked.SessionName).To(BeEmpty())
		Expect(linked.Host).To(Equal("host-a"))
		Expect(linked.Cwd).To(Equal("/work"))
		Expect(linked.Tool).To(Equal("claude"))

		Expect(board[1].Board.Provenance.SessionName).To(Equal("e2e"))
	})

	It("renders no task link when no vault is configured", func() {
		itemID := pkg.ItemID("no-vault")
		vaultDir = ""
		store.ReadBoardReturns(pkg.Items{item(itemID, pkg.MessageAnswerMechanism)}, nil)
		provenance.ResolveReturns(pkg.Provenances{
			itemID: {TaskName: "Fix the Board", TaskPath: "25 Tasks/Fix the Board.md"},
		})

		Expect(read()[0].Board.Provenance.TaskHref).To(BeEmpty())
	})

	It("carries the delivery timeline, outcome and undeliverable marker", func() {
		deliveredID := pkg.ItemID("delivered")
		failedID := pkg.ItemID("failed")
		delivered := item(deliveredID, pkg.MessageAnswerMechanism)
		delivered.State = pkg.AnsweredState
		delivered.AnsweredAt = ptr(parse("2026-11-11T10:00:05Z"))
		failed := item(failedID, pkg.MessageAnswerMechanism)
		failed.State = pkg.AnsweredState
		failed.AnsweredAt = ptr(parse("2026-11-11T10:00:05Z"))
		store.ReadBoardReturns(pkg.Items{delivered, failed}, nil)
		store.DeliveryReportsReturns(map[pkg.ItemID]*pkg.DeliveryReport{
			deliveredID: {
				ItemID:      deliveredID,
				Status:      pkg.DeliveredStatus,
				AttemptedAt: ptr(parse("2026-11-11T10:00:06Z")),
			},
			failedID: {
				ItemID:      failedID,
				Status:      pkg.FailedStatus,
				AttemptedAt: ptr(parse("2026-11-11T10:00:07Z")),
			},
		}, nil)

		board := read()
		ok := board[0].Board
		Expect(ok.DeliveryStatus).To(Equal("delivered"))
		Expect(ok.DeliveryOutcome).To(Equal("delivered"))
		Expect(ok.Undeliverable).To(BeFalse())
		Expect(ok.Timeline).To(ContainElements(
			"created 2026-11-11T10:00:01Z",
			"answered 2026-11-11T10:00:05Z",
			"delivered 2026-11-11T10:00:06Z",
			"answered→delivered 1s",
			"released 2026-11-11T10:00:06Z",
		))

		bad := board[1].Board
		Expect(bad.DeliveryStatus).To(Equal("failed"))
		Expect(bad.DeliveryOutcome).To(Equal("failed"))
		Expect(bad.Undeliverable).To(BeTrue())
		// A failed attempt is not a delivery: the stage stays absent.
		Expect(bad.Timeline).To(ContainElement("delivered —"))
	})

	It("degrades to an absent delivery stage when the delivery read fails", func() {
		itemID := pkg.ItemID("delivery-read-failed")
		store.ReadBoardReturns(pkg.Items{item(itemID, pkg.MessageAnswerMechanism)}, nil)
		store.DeliveryReportsReturns(nil, errors.New(context.Background(), "boom"))

		view := read()[0].Board
		Expect(view.DeliveryStatus).To(BeEmpty())
		Expect(view.DeliveryOutcome).To(BeEmpty())
		Expect(view.Timeline).To(ContainElement("delivered —"))
	})

	It("carries the speak flag", func() {
		store.ReadBoardReturns(pkg.Items{item("speak", pkg.MessageAnswerMechanism)}, nil)
		Expect(read()[0].Board.SpeakEnabled).To(BeFalse())
		speakEnabled = true
		Expect(read()[0].Board.SpeakEnabled).To(BeTrue())
	})

	It("answers an empty list for an empty board", func() {
		store.ReadBoardReturns(pkg.Items{}, nil)
		Expect(read()).To(BeEmpty())
	})

	It("answers 500 when the board read fails", func() {
		store.ReadBoardReturns(nil, errors.New(context.Background(), "read failed"))
		httpHandler := handler.NewAttentionBoardReadHandler(store, provenance, false, vaultDir)
		req := httptest.NewRequest(http.MethodGet, "/api/1.0/attention/board", nil)
		resp := httptest.NewRecorder()
		httpHandler.ServeHTTP(resp, req)
		Expect(resp.Code).To(Equal(http.StatusInternalServerError))
		Expect(provenance.ResolveCallCount()).To(Equal(0))
	})
})
