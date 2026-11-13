// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	stdtime "time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/boardmetrics"
	"github.com/bborbe/attention-controller/pkg/handler"
)

// The card's timeline lives in its info panel: every stage that happened, its
// raw instant, and the four deltas the contract names — computed in Go from the
// item's stored timestamps and the store's own delivery report.
//
// ⚠️ These specs drive the row through a FAKE store rather than a real one.
// Two reasons, both structural: a `closed` item is never served on the board
// (ReadBoard returns only open and answered), so a fake is the only reachable
// construction for that stage; and a fake gives each fixture exact instants,
// which a real store's movable clock would make fiddly to pin. The fail-soft
// case already uses this seam for the same reason — see
// attention-undeliverable-page_test.go.
//
// ⚠️ Every fixture item carries ProducerKind: pkg.ManagerProducerKind and the
// request asks for `?producer_kind=all`. The board's kind filter runs
// server-side and defaults to manager-only, so a fixture with an empty kind
// would be dropped before it rendered and every assertion would fail on an
// empty row.
var _ = Describe("the card's timeline in the info panel", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	// parse turns a literal instant into the type the item stores, so a fixture
	// reads as the timestamp the assertion expects.
	parse := func(s string) libtime.DateTime {
		t, err := stdtime.Parse(stdtime.RFC3339Nano, s)
		Expect(err).To(BeNil())
		return libtime.DateTime(t)
	}
	// ptr returns a pointer to its argument, for the optional timestamp fields.
	ptr := func(t libtime.DateTime) *libtime.DateTime { return &t }

	// rowOf returns the rendered HTML for one item's row, so an assertion is
	// scoped to that item rather than to the whole page.
	rowOf := func(body string, itemID pkg.ItemID) string {
		start := strings.Index(body, `data-item-id="`+itemID.String()+`"`)
		Expect(start).To(BeNumerically(">=", 0), "row for %s not found", itemID)
		rest := body[start:]
		end := strings.Index(rest, "</li>")
		Expect(end).To(BeNumerically(">=", 0))
		return rest[:end]
	}

	// panelOf slices the row at the info panel, so a stage line asserted here
	// cannot be satisfied by a class name in the stylesheet or by anything on
	// the card face.
	panelOf := func(row string) string {
		at := strings.Index(row, `<div class="info-panel"`)
		Expect(at).To(BeNumerically(">=", 0), "the panel is not on the row")
		return row[at:]
	}

	// renderOne serves the board once for one item and its delivery report, and
	// returns the document.
	renderOne := func(item pkg.Item, report *pkg.DeliveryReport, deliveryErr error) string {
		fake := &mocks.AttentionStore{}
		fake.ReadBoardReturns(pkg.Items{item}, nil)
		fake.DeliveryReturns(report, deliveryErr)
		provenance := &mocks.ProvenanceResolver{}
		provenance.ResolveReturns(pkg.Provenances{})
		httpHandler := handler.NewAttentionPageHandler(
			fake,
			provenance,
			false,
			GinkgoT().TempDir(),
			testBuildIdentity,
			boardmetrics.NewMetrics(prometheus.NewRegistry()),
		)
		req := httptest.NewRequest(http.MethodGet, "/?producer_kind=all", nil)
		resp := httptest.NewRecorder()
		httpHandler.ServeHTTP(resp, req)
		Expect(resp.Code).To(Equal(http.StatusOK))
		return resp.Body.String()
	}

	It("renders every stage and the three deltas on the card's panel", func() {
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

		row := rowOf(renderOne(item, report, nil), itemID)
		Expect(row).To(ContainSubstring(item.Payload.String()))

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
		panel := panelOf(row)
		for _, literal := range literals {
			Expect(row).To(ContainSubstring(literal))
			Expect(panel).To(ContainSubstring(literal))
		}

		// The card face gains no timeline line: everything renders behind the
		// existing `i` toggle.
		face := row[:strings.Index(row, `<div class="info-panel"`)]
		for _, literal := range literals {
			Expect(face).NotTo(ContainSubstring(literal))
		}
	})

	It("renders an absent ask stage as an em dash and never as a zero", func() {
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

		row := rowOf(renderOne(item, report, nil), itemID)
		Expect(row).To(ContainSubstring("asked —"))
		Expect(row).To(ContainSubstring("asked→created —"))
		// A delta whose inputs both exist still renders: suppressing it would
		// drop a measurement that does not touch the missing stage.
		Expect(row).To(ContainSubstring("created→displayed 1s"))

		zero := regexp.MustCompile(`(?:^|[^0-9])0s`)
		Expect(zero.FindString(row)).To(BeEmpty(), "a zero duration was rendered")
	})

	It("renders the gate-released stage only for a delivered attempt", func() {
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
		delivered := rowOf(renderOne(base(deliveredID), &pkg.DeliveryReport{
			ItemID:      deliveredID,
			Status:      pkg.DeliveredStatus,
			AttemptedAt: ptr(attemptedAt),
		}, nil), deliveredID)
		Expect(delivered).To(ContainSubstring("released 2026-11-11T10:00:06Z"))

		failedID := pkg.ItemID("timeline-released-failed")
		failed := rowOf(renderOne(base(failedID), &pkg.DeliveryReport{
			ItemID:      failedID,
			Status:      pkg.FailedStatus,
			AttemptedAt: ptr(attemptedAt),
		}, nil), failedID)
		Expect(failed).To(ContainSubstring("released —"))
		Expect(failed).NotTo(ContainSubstring("released 2026-11-11T10:00:06Z"))

		noneID := pkg.ItemID("timeline-released-none")
		none := rowOf(renderOne(base(noneID), &pkg.DeliveryReport{
			ItemID: noneID,
			Status: pkg.NeverAttemptedStatus,
		}, nil), noneID)
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
		row := rowOf(renderOne(item, nil, nil), itemID)
		Expect(row).To(ContainSubstring("closed 2026-11-11T10:00:09Z permission"))
		Expect(row).NotTo(ContainSubstring(" via "))

		itemID = pkg.ItemID("timeline-close-reason-client")
		item.ItemID = itemID
		item.AnsweredClient = &pkg.AnsweredClient{
			UserAgent:  "e2e-agent",
			RemoteAddr: "127.0.0.1",
		}
		row = rowOf(renderOne(item, nil, nil), itemID)
		Expect(
			row,
		).To(ContainSubstring("closed 2026-11-11T10:00:09Z permission via e2e-agent @ 127.0.0.1"))
	})

	It("names the four delivery outcomes and renders no outcome line for a held card", func() {
		cases := []struct {
			status pkg.DeliveryStatus
			want   string
		}{
			{pkg.DeliveredStatus, `<div class="delivery-outcome">delivered</div>`},
			{pkg.FailedStatus, `<div class="delivery-outcome">failed</div>`},
			{pkg.NeverAttemptedStatus, `<div class="delivery-outcome">never_attempted</div>`},
			{pkg.PreTrailUnknownStatus, `<div class="delivery-outcome">pre_trail_unknown</div>`},
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
			panel := panelOf(rowOf(renderOne(item, &pkg.DeliveryReport{
				ItemID: itemID,
				Status: testCase.status,
			}, nil), itemID))
			Expect(panel).To(ContainSubstring(testCase.want))
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
		heldPanel := panelOf(rowOf(renderOne(held, &pkg.DeliveryReport{
			ItemID:      heldID,
			Status:      pkg.HeldStatus,
			AttemptedAt: ptr(parse("2026-11-11T10:00:06Z")),
		}, nil), heldID))
		Expect(heldPanel).NotTo(ContainSubstring("delivery-outcome"))
		Expect(strings.ToLower(heldPanel)).NotTo(ContainSubstring("held"))
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
		body := renderOne(item, nil, errors.New(ctx, "delivery read failed"))
		row := rowOf(body, itemID)
		Expect(row).To(ContainSubstring(item.Payload.String()))
		Expect(row).To(ContainSubstring("delivered —"))
		Expect(row).To(ContainSubstring("answered→delivered —"))
		Expect(row).To(ContainSubstring("released —"))
		Expect(row).NotTo(ContainSubstring("delivery-outcome"))
	})

	It("renders a negative delta as a signed value rather than clamping it", func() {
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
		firstRow := rowOf(renderOne(first, nil, nil), firstID)
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
		secondRow := rowOf(renderOne(second, nil, nil), secondID)
		Expect(secondRow).To(ContainSubstring("asked→created -3s"))

		for _, row := range []string{firstRow, secondRow} {
			Expect(row).NotTo(ContainSubstring("asked→created 0s"))
			Expect(row).NotTo(ContainSubstring("asked→created 2s"))
		}
	})
})
