// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	stdtime "time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

var _ = Describe("Misroute week", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	Describe("ParseMisrouteWeek", func() {
		It("parses the YYYYWww form", func() {
			week, err := pkg.ParseMisrouteWeek(ctx, "2026W44")
			Expect(err).To(BeNil())
			Expect(week.Year).To(Equal(2026))
			Expect(week.Week).To(Equal(44))
			Expect(week.String()).To(Equal("2026W44"))
		})

		It("accepts a lowercase w", func() {
			week, err := pkg.ParseMisrouteWeek(ctx, "2026w44")
			Expect(err).To(BeNil())
			Expect(week.String()).To(Equal("2026W44"))
		})

		// ⚠️ A malformed week must be a refusal rather than an empty count. Zero
		// is a real answer for a quiet week, so a fallback would make a typo
		// indistinguishable from a week nobody flagged in.
		DescribeTable("rejects values that are not a week",
			func(raw string) {
				week, err := pkg.ParseMisrouteWeek(ctx, raw)
				Expect(err).NotTo(BeNil())
				Expect(week).To(BeNil())
			},
			Entry("empty", ""),
			Entry("dashes instead of W", "2026-44"),
			Entry("missing a digit", "2026W4"),
			Entry("week zero", "2026W00"),
			Entry("week above 53", "2026W54"),
			Entry("non-numeric year", "20x6W44"),
			Entry("non-numeric week", "2026W4x"),
			Entry("trailing text", "2026W44x"),
		)

		// ⚠️ 2025 has 52 ISO weeks, so 2025W53 names a week that does not exist.
		// A range check alone accepts it and then answers with week 1 of 2026 —
		// a wrong window that reads as a valid one. The round-trip is what makes
		// this a refusal.
		It("rejects a week 53 that the year does not have", func() {
			week, err := pkg.ParseMisrouteWeek(ctx, "2025W53")
			Expect(err).NotTo(BeNil())
			Expect(week).To(BeNil())
		})

		It("accepts week 53 in a year that has one", func() {
			week, err := pkg.ParseMisrouteWeek(ctx, "2026W53")
			Expect(err).To(BeNil())
			Expect(week.String()).To(Equal("2026W53"))
		})
	})

	Describe("Window", func() {
		It("starts on the Monday and ends before the following Monday", func() {
			week := pkg.MisrouteWeek{Year: 2026, Week: 44}
			start, end := week.Window(stdtime.Local)
			Expect(
				start,
			).To(Equal(stdtime.Date(2026, stdtime.October, 26, 0, 0, 0, 0, stdtime.Local)))
			Expect(
				end,
			).To(Equal(stdtime.Date(2026, stdtime.November, 2, 0, 0, 0, 0, stdtime.Local)))
		})
	})

	Describe("CountMisroutes", func() {
		flagged := func(
			producerID string,
			kind pkg.ProducerKind,
			reason string,
			at stdtime.Time,
		) pkg.Item {
			stamp := libtime.DateTime(at)
			return pkg.Item{
				ProducerID:   pkg.ProducerID(producerID),
				ProducerKind: kind,
				FlagReason:   reason,
				FlaggedAt:    &stamp,
			}
		}

		inside := func(day, hour int) stdtime.Time {
			return stdtime.Date(2026, stdtime.October, day, hour, 0, 0, 0, stdtime.Local)
		}

		week := pkg.MisrouteWeek{Year: 2026, Week: 44}

		It("counts a repeated (producer, reason) pair as one row of 2", func() {
			result := pkg.CountMisroutes(
				pkg.Items{
					flagged("producer-a", pkg.SessionProducerKind, "routing", inside(27, 10)),
					flagged("producer-a", pkg.SessionProducerKind, "routing", inside(28, 10)),
				},
				week,
				stdtime.Local,
			)
			Expect(result.Rows).To(HaveLen(1))
			Expect(result.Rows[0].ProducerID).To(Equal(pkg.ProducerID("producer-a")))
			Expect(result.Rows[0].Reason).To(Equal("routing"))
			Expect(result.Rows[0].Count).To(Equal(2))
		})

		It("groups the same reason per producer separately", func() {
			result := pkg.CountMisroutes(
				pkg.Items{
					flagged("producer-a", pkg.SessionProducerKind, "routing", inside(27, 10)),
					flagged("producer-b", pkg.ManagerProducerKind, "routing", inside(27, 11)),
				},
				week,
				stdtime.Local,
			)
			Expect(result.Rows).To(HaveLen(2))
			Expect(result.Rows[0].Count).To(Equal(1))
			Expect(result.Rows[1].Count).To(Equal(1))
		})

		// ⚠️ The out-of-window card is what makes the window observable: an
		// aggregation that counted every flagged item would pass a spec that only
		// asserted the rows it expected to see.
		It("excludes a card flagged outside the week", func() {
			result := pkg.CountMisroutes(
				pkg.Items{
					flagged("producer-a", pkg.SessionProducerKind, "routing", inside(27, 10)),
					flagged("producer-c", pkg.SessionProducerKind, "routing", inside(20, 10)),
				},
				week,
				stdtime.Local,
			)
			Expect(result.Rows).To(HaveLen(1))
			Expect(result.Rows[0].ProducerID).To(Equal(pkg.ProducerID("producer-a")))
		})

		It("excludes an unflagged item", func() {
			result := pkg.CountMisroutes(
				pkg.Items{
					{ProducerID: "producer-a", ProducerKind: pkg.SessionProducerKind},
					flagged("producer-a", pkg.SessionProducerKind, "routing", inside(27, 10)),
				},
				week,
				stdtime.Local,
			)
			Expect(result.Rows).To(HaveLen(1))
			Expect(result.Rows[0].Count).To(Equal(1))
		})

		// The window is half-open, so a flag stamped at exactly midnight on the
		// following Monday belongs to the next week and is counted once, not twice.
		It("excludes a flag stamped at the next week's first instant", func() {
			result := pkg.CountMisroutes(
				pkg.Items{
					flagged("producer-a", pkg.SessionProducerKind, "routing", inside(27, 10)),
					flagged(
						"producer-a",
						pkg.SessionProducerKind,
						"routing",
						stdtime.Date(2026, stdtime.November, 2, 0, 0, 0, 0, stdtime.Local),
					),
				},
				week,
				stdtime.Local,
			)
			Expect(result.Rows).To(HaveLen(1))
			Expect(result.Rows[0].Count).To(Equal(1))
		})

		// The store writes the reason verbatim, so a trailing space would split
		// one reason into two rows and undercount the pair a routing fix is
		// scoped against.
		It("groups a reason whose surrounding whitespace differs", func() {
			result := pkg.CountMisroutes(
				pkg.Items{
					flagged("producer-a", pkg.SessionProducerKind, "routing", inside(27, 10)),
					flagged("producer-a", pkg.SessionProducerKind, " routing ", inside(28, 10)),
				},
				week,
				stdtime.Local,
			)
			Expect(result.Rows).To(HaveLen(1))
			Expect(result.Rows[0].Reason).To(Equal("routing"))
			Expect(result.Rows[0].Count).To(Equal(2))
		})

		// Case is deliberately preserved: folding it would hide that the operator
		// used two spellings, which is itself a finding.
		It("keeps reasons differing only in case as separate rows", func() {
			result := pkg.CountMisroutes(
				pkg.Items{
					flagged("producer-a", pkg.SessionProducerKind, "routing", inside(27, 10)),
					flagged("producer-a", pkg.SessionProducerKind, "Routing", inside(28, 10)),
				},
				week,
				stdtime.Local,
			)
			Expect(result.Rows).To(HaveLen(2))
		})

		It("states the window it covers as calendar dates", func() {
			result := pkg.CountMisroutes(pkg.Items{}, week, stdtime.Local)
			Expect(result.Week).To(Equal("2026W44"))
			Expect(result.Start.String()).To(Equal("2026-10-26"))
			Expect(result.End.String()).To(Equal("2026-11-01"))
		})

		// ⚠️ An empty week answers with an empty array, never null: null reads as
		// "no answer", and a quiet week IS this endpoint's answer.
		It("returns a non-nil empty row list for a week nobody flagged in", func() {
			result := pkg.CountMisroutes(pkg.Items{}, week, stdtime.Local)
			Expect(result.Rows).NotTo(BeNil())
			Expect(result.Rows).To(BeEmpty())
		})

		It("orders rows by count descending", func() {
			result := pkg.CountMisroutes(
				pkg.Items{
					flagged("producer-a", pkg.SessionProducerKind, "noise", inside(27, 10)),
					flagged("producer-b", pkg.SessionProducerKind, "routing", inside(27, 11)),
					flagged("producer-b", pkg.SessionProducerKind, "routing", inside(28, 11)),
					flagged("producer-b", pkg.SessionProducerKind, "routing", inside(28, 12)),
				},
				week,
				stdtime.Local,
			)
			Expect(result.Rows).To(HaveLen(2))
			Expect(result.Rows[0].ProducerID).To(Equal(pkg.ProducerID("producer-b")))
			Expect(result.Rows[0].Count).To(Equal(3))
			Expect(result.Rows[1].ProducerID).To(Equal(pkg.ProducerID("producer-a")))
		})
	})
})
