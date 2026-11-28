// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"regexp"
	"strings"
	stdtime "time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/handler"
)

// The info panel's timeline, carried in the board JSON as BoardView.Timeline
// and DeliveryOutcome. Built through a FAKE store: it is the only reachable
// construction for a `closed` stage and it gives the fixtures exact instants.
// Every expected line is a hand-written literal.
var _ = Describe("the card's timeline in the board JSON", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	parse := func(s string) libtime.DateTime {
		t, err := stdtime.Parse(stdtime.RFC3339Nano, s)
		Expect(err).To(BeNil())
		return libtime.DateTime(t)
	}
	ptr := func(t libtime.DateTime) *libtime.DateTime { return &t }

	renderOne := func(item pkg.Item, report *pkg.DeliveryReport, deliveryErr error) pkg.BoardItem {
		fake := &mocks.AttentionStore{}
		fake.ReadBoardReturns(pkg.Items{item}, nil)
		reports := map[pkg.ItemID]*pkg.DeliveryReport{}
		if report != nil {
			reports[item.ItemID] = report
		}
		fake.DeliveryReportsReturns(reports, deliveryErr)
		provenance := &mocks.ProvenanceResolver{}
		provenance.ResolveReturns(pkg.Provenances{})
		return boardEntryOf(
			handler.NewAttentionBoardReadHandler(fake, provenance, false, GinkgoT().TempDir()),
			item.ItemID,
		)
	}

	timelineOf := func(entry pkg.BoardItem) string {
		return strings.Join(entry.Board.Timeline, "\n")
	}

	It("carries every stage and the three deltas in the card's timeline", func() {
		itemID := pkg.ItemID("timeline-full")
		item := pkg.Item{
			ItemID:          itemID,
			ProducerID:      pkg.ProducerID("producer-a"),
			ProducerKind:    pkg.ManagerProducerKind,
			Payload:         "Which surface?",
			State:           pkg.ClosedState,
			AnswerMechanism: pkg.MessageAnswerMechanism,
			AskedAt:         ptr(parse("2026-11-11T10:00:00Z")),
			CreatedAt:       parse("2026-11-11T10:00:01Z"),
			DisplayedAt:     ptr(parse("2026-11-11T10:00:02Z")),
			AnsweredAt:      ptr(parse("2026-11-11T10:00:05Z")),
			AnsweredBy:      "attention-board",
			ClosedAt:        ptr(parse("2026-11-11T10:00:09Z")),
		}
		report := &pkg.DeliveryReport{
			ItemID:      itemID,
			Status:      pkg.DeliveredStatus,
			AttemptedAt: ptr(parse("2026-11-11T10:00:06Z")),
		}

		entry := renderOne(item, report, nil)
		Expect(entry.Payload).To(Equal(item.Payload))

		literals := []string{
			"asked 2026-11-11T10:00:00Z",
			"created 2026-11-11T10:00:01Z",
			"displayed 2026-11-11T10:00:02Z",
			"asked→created 1s",
			"created→displayed 1s",
			"answered 2026-11-11T10:00:05Z",
			"displayed→answered 3s",
			"answered by attention-board",
			"delivered 2026-11-11T10:00:06Z",
			"answered→delivered 1s",
			"released 2026-11-11T10:00:06Z",
			"closed 2026-11-11T10:00:09Z message",
		}
		Expect(entry.Board.Timeline).To(Equal(literals))
	})

	It("carries an absent ask stage as an em dash and never as a zero", func() {
		itemID := pkg.ItemID("timeline-absent-ask")
		item := pkg.Item{
			ItemID:          itemID,
			ProducerID:      pkg.ProducerID("producer-a"),
			ProducerKind:    pkg.ManagerProducerKind,
			Payload:         "Which surface?",
			State:           pkg.AnsweredState,
			AnswerMechanism: pkg.MessageAnswerMechanism,
			CreatedAt:       parse("2026-11-11T10:00:00Z"),
			DisplayedAt:     ptr(parse("2026-11-11T10:00:01Z")),
			AnsweredAt:      ptr(parse("2026-11-11T10:00:03Z")),
		}
		report := &pkg.DeliveryReport{ItemID: itemID, Status: pkg.NeverAttemptedStatus}

		row := timelineOf(renderOne(item, report, nil))
		Expect(row).To(ContainSubstring("asked —"))
		Expect(row).To(ContainSubstring("asked→created —"))
		Expect(row).To(ContainSubstring("created→displayed 1s"))

		zero := regexp.MustCompile(`(?:^|[^0-9])0s`)
		Expect(zero.FindString(row)).To(BeEmpty(), "a zero duration was rendered")
	})

	It("carries the gate-released stage only for a delivered attempt", func() {
		base := func(itemID pkg.ItemID) pkg.Item {
			return pkg.Item{
				ItemID:          itemID,
				ProducerID:      pkg.ProducerID("producer-a"),
				ProducerKind:    pkg.ManagerProducerKind,
				Payload:         "Which surface?",
				State:           pkg.AnsweredState,
				AnswerMechanism: pkg.MessageAnswerMechanism,
				CreatedAt:       parse("2026-11-11T10:00:00Z"),
				AnsweredAt:      ptr(parse("2026-11-11T10:00:03Z")),
			}
		}
		attemptedAt := parse("2026-11-11T10:00:06Z")

		deliveredID := pkg.ItemID("timeline-released-delivered")
		delivered := timelineOf(renderOne(base(deliveredID), &pkg.DeliveryReport{
			ItemID:      deliveredID,
			Status:      pkg.DeliveredStatus,
			AttemptedAt: ptr(attemptedAt),
		}, nil))
		Expect(delivered).To(ContainSubstring("released 2026-11-11T10:00:06Z"))

		failedID := pkg.ItemID("timeline-released-failed")
		failed := timelineOf(renderOne(base(failedID), &pkg.DeliveryReport{
			ItemID:      failedID,
			Status:      pkg.FailedStatus,
			AttemptedAt: ptr(attemptedAt),
		}, nil))
		Expect(failed).To(ContainSubstring("released —"))
		Expect(failed).NotTo(ContainSubstring("released 2026-11-11T10:00:06Z"))

		noneID := pkg.ItemID("timeline-released-none")
		none := timelineOf(renderOne(base(noneID), &pkg.DeliveryReport{
			ItemID: noneID,
			Status: pkg.NeverAttemptedStatus,
		}, nil))
		Expect(none).To(ContainSubstring("released —"))
	})

	It("derives the close reason from the mechanism and the recorded client", func() {
		itemID := pkg.ItemID("timeline-close-reason")
		item := pkg.Item{
			ItemID:          itemID,
			ProducerID:      pkg.ProducerID("producer-a"),
			ProducerKind:    pkg.ManagerProducerKind,
			Payload:         "Deploy prod?",
			State:           pkg.ClosedState,
			AnswerMechanism: pkg.PermissionAnswerMechanism,
			CreatedAt:       parse("2026-11-11T10:00:00Z"),
			ClosedAt:        ptr(parse("2026-11-11T10:00:09Z")),
		}
		row := timelineOf(renderOne(item, nil, nil))
		Expect(row).To(ContainSubstring("closed 2026-11-11T10:00:09Z permission"))
		Expect(row).NotTo(ContainSubstring(" via "))

		itemID = pkg.ItemID("timeline-close-reason-client")
		item.ItemID = itemID
		item.AnsweredClient = &pkg.AnsweredClient{
			UserAgent:  "e2e-agent",
			RemoteAddr: "127.0.0.1",
		}
		row = timelineOf(renderOne(item, nil, nil))
		Expect(
			row,
		).To(ContainSubstring("closed 2026-11-11T10:00:09Z permission via e2e-agent @ 127.0.0.1"))
	})

	It("names the four delivery outcomes and carries no outcome line for a held card", func() {
		cases := []struct {
			status pkg.DeliveryStatus
			want   string
		}{
			{pkg.DeliveredStatus, "delivered"},
			{pkg.FailedStatus, "failed"},
			{pkg.NeverAttemptedStatus, "never_attempted"},
			{pkg.PreTrailUnknownStatus, "pre_trail_unknown"},
		}
		for _, testCase := range cases {
			itemID := pkg.ItemID("timeline-outcome-" + string(testCase.status))
			item := pkg.Item{
				ItemID:          itemID,
				ProducerID:      pkg.ProducerID("producer-a"),
				ProducerKind:    pkg.ManagerProducerKind,
				Payload:         "Which surface?",
				State:           pkg.AnsweredState,
				AnswerMechanism: pkg.MessageAnswerMechanism,
				CreatedAt:       parse("2026-11-11T10:00:00Z"),
				AnsweredAt:      ptr(parse("2026-11-11T10:00:03Z")),
			}
			entry := renderOne(item, &pkg.DeliveryReport{
				ItemID: itemID,
				Status: testCase.status,
			}, nil)
			Expect(entry.Board.DeliveryOutcome).To(Equal(testCase.want))
		}

		heldID := pkg.ItemID("timeline-outcome-held")
		held := pkg.Item{
			ItemID:          heldID,
			ProducerID:      pkg.ProducerID("producer-a"),
			ProducerKind:    pkg.ManagerProducerKind,
			Payload:         "Which surface?",
			State:           pkg.AnsweredState,
			AnswerMechanism: pkg.MessageAnswerMechanism,
			CreatedAt:       parse("2026-11-11T10:00:00Z"),
			AnsweredAt:      ptr(parse("2026-11-11T10:00:03Z")),
		}
		heldEntry := renderOne(held, &pkg.DeliveryReport{
			ItemID:      heldID,
			Status:      pkg.HeldStatus,
			AttemptedAt: ptr(parse("2026-11-11T10:00:06Z")),
		}, nil)
		Expect(heldEntry.Board.DeliveryOutcome).To(BeEmpty())
		Expect(strings.ToLower(strings.Join(heldEntry.Board.Timeline, "\n"))).NotTo(ContainSubstring("held"))
	})

	It("keeps the whole timeline absent when the delivery read fails", func() {
		itemID := pkg.ItemID("timeline-read-fails")
		item := pkg.Item{
			ItemID:          itemID,
			ProducerID:      pkg.ProducerID("producer-a"),
			ProducerKind:    pkg.ManagerProducerKind,
			Payload:         "Which surface?",
			State:           pkg.AnsweredState,
			AnswerMechanism: pkg.MessageAnswerMechanism,
			CreatedAt:       parse("2026-11-11T10:00:00Z"),
			AnsweredAt:      ptr(parse("2026-11-11T10:00:03Z")),
		}
		entry := renderOne(item, nil, errors.New(ctx, "delivery read failed"))
		Expect(entry.Payload).To(Equal(item.Payload))
		Expect(entry.Board.DeliveryOutcome).To(BeEmpty())
		row := strings.Join(entry.Board.Timeline, "\n")
		Expect(row).To(ContainSubstring("delivered —"))
		Expect(row).To(ContainSubstring("answered→delivered —"))
		Expect(row).To(ContainSubstring("released —"))
	})

	It("carries a negative delta as a signed value rather than clamping it", func() {
		firstID := pkg.ItemID("timeline-negative-whole")
		first := pkg.Item{
			ItemID:          firstID,
			ProducerID:      pkg.ProducerID("producer-a"),
			ProducerKind:    pkg.ManagerProducerKind,
			Payload:         "Which surface?",
			State:           pkg.OpenState,
			AnswerMechanism: pkg.MessageAnswerMechanism,
			AskedAt:         ptr(parse("2026-11-11T10:00:02Z")),
			CreatedAt:       parse("2026-11-11T10:00:00Z"),
		}
		firstRow := timelineOf(renderOne(first, nil, nil))
		Expect(firstRow).To(ContainSubstring("asked→created -2s"))

		secondID := pkg.ItemID("timeline-negative-fraction")
		second := pkg.Item{
			ItemID:          secondID,
			ProducerID:      pkg.ProducerID("producer-a"),
			ProducerKind:    pkg.ManagerProducerKind,
			Payload:         "Which surface?",
			State:           pkg.OpenState,
			AnswerMechanism: pkg.MessageAnswerMechanism,
			AskedAt:         ptr(parse("2026-11-11T10:00:02.5Z")),
			CreatedAt:       parse("2026-11-11T10:00:00Z"),
		}
		secondRow := timelineOf(renderOne(second, nil, nil))
		Expect(secondRow).To(ContainSubstring("asked→created -3s"))

		for _, row := range []string{firstRow, secondRow} {
			Expect(row).NotTo(ContainSubstring("asked→created 0s"))
			Expect(row).NotTo(ContainSubstring("asked→created 2s"))
		}
	})
})
