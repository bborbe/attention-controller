// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	stdtime "time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
)

// misrouteWeekLength is the exact length of the `YYYYWww` form, e.g. "2026W44".
const misrouteWeekLength = 7

// MisrouteWeek is an ISO-8601 calendar week — the window a misroute count is
// taken over.
//
// It is a pair of numbers rather than a start instant because the two are not
// interchangeable: a week is a named range, and its instants depend on the
// location the flags were stamped in. Keeping the name separate is what lets the
// same week be resolved against the caller's clock rather than against whichever
// zone happened to build the request.
type MisrouteWeek struct {
	Year int
	Week int
}

// ParseMisrouteWeek parses the `YYYYWww` form the endpoint's `week` parameter
// carries, e.g. "2026W44".
//
// ⚠️ The value is round-tripped rather than merely range-checked. A year has 53
// ISO weeks only sometimes, so `2026W53` names a week that does not exist;
// checking only `1 <= week <= 53` would accept it and then quietly answer with
// the dates of week 1 of the following year. Recomputing the week from the
// derived Monday is what turns "this week does not exist" into a refusal instead
// of a wrong window.
func ParseMisrouteWeek(ctx context.Context, raw string) (*MisrouteWeek, error) {
	if len(raw) != misrouteWeekLength || (raw[4] != 'W' && raw[4] != 'w') {
		return nil, errors.Wrapf(
			ctx,
			ErrMisrouteWeekInvalid,
			"week %q is not in the YYYYWww form (e.g. 2026W44)",
			raw,
		)
	}
	year, err := strconv.Atoi(raw[:4])
	if err != nil {
		return nil, errors.Wrapf(
			ctx,
			ErrMisrouteWeekInvalid,
			"week %q: year is not a number",
			raw,
		)
	}
	week, err := strconv.Atoi(raw[5:])
	if err != nil {
		return nil, errors.Wrapf(
			ctx,
			ErrMisrouteWeekInvalid,
			"week %q: week is not a number",
			raw,
		)
	}
	if week < 1 || week > 53 {
		return nil, errors.Wrapf(
			ctx,
			ErrMisrouteWeekInvalid,
			"week %q: week must be between 1 and 53",
			raw,
		)
	}
	candidate := MisrouteWeek{Year: year, Week: week}
	if resolvedYear, resolvedWeek := candidate.Monday(stdtime.Local).ISOWeek(); resolvedYear != year ||
		resolvedWeek != week {
		return nil, errors.Wrapf(
			ctx,
			ErrMisrouteWeekInvalid,
			"week %q does not exist in %d",
			raw,
			year,
		)
	}
	return &candidate, nil
}

// Monday returns the instant the week starts on, at midnight in loc.
//
// ⚠️ 4 January is always in ISO week 1 — that is the definition, not a
// coincidence — which is what makes this derivation correct for every year
// without a lookup table. Go's Weekday counts Sunday as 0, so the days elapsed
// since Monday are `(weekday + 6) % 7`.
func (w MisrouteWeek) Monday(loc *stdtime.Location) stdtime.Time {
	jan4 := stdtime.Date(w.Year, stdtime.January, 4, 0, 0, 0, 0, loc)
	daysSinceMonday := (int(jan4.Weekday()) + 6) % 7
	return jan4.AddDate(0, 0, -daysSinceMonday+(w.Week-1)*7)
}

// Window returns the half-open instant range the week covers in loc: from the
// Monday it starts on, up to but not including the following Monday.
//
// ⚠️ Half-open is load-bearing. A card flagged at exactly midnight on the
// following Monday belongs to the next week; an inclusive upper bound would
// count it in both weeks.
func (w MisrouteWeek) Window(loc *stdtime.Location) (stdtime.Time, stdtime.Time) {
	start := w.Monday(loc)
	return start, start.AddDate(0, 0, 7)
}

// String renders the week back into the `YYYYWww` form it was parsed from.
func (w MisrouteWeek) String() string {
	return fmt.Sprintf("%04dW%02d", w.Year, w.Week)
}

// MisrouteCountRow is one bucket of the weekly misroute count: how often one
// producer was flagged for one reason inside the window.
//
// ProducerKind is a display attribute of ProducerID, not part of the grouping
// key — a producer that reported as two kinds inside one week is still one row,
// because the count is about the producer and not about how it labelled itself.
type MisrouteCountRow struct {
	ProducerID   ProducerID   `json:"producer_id"`
	ProducerKind ProducerKind `json:"producer_kind"`
	Reason       string       `json:"reason"`
	Count        int          `json:"count"`
}

// MisrouteCountRows is the response's row list, ordered by count descending,
// then producer, then reason.
//
// The ordering is total, so two reads of an unchanged store are byte-identical.
// That is what makes a before-and-after comparison (pre-install against
// post-install) a real check rather than a diff of two arbitrary orders.
type MisrouteCountRows []MisrouteCountRow

// MisrouteCount is the weekly per-producer misroute count — the reader the
// attention item schema names for a flag: "evidence for a routing fix, never
// triggers".
//
// Start and End are inclusive calendar dates, not instants, because the reader
// asks which week was counted, and a date is the form that question has. The
// instants behind them are half-open and live in Window.
type MisrouteCount struct {
	Week  string            `json:"week"`
	Start libtime.Date      `json:"start"`
	End   libtime.Date      `json:"end"`
	Rows  MisrouteCountRows `json:"rows"`
}

// misrouteBucketKey is the grouping key.
//
// ⚠️ The reason is trimmed before it becomes part of the key. The store writes
// the operator's text verbatim (attention-store-flag.go: "no length cap and no
// normalization"), so a trailing space would otherwise split one reason into two
// rows and undercount the very pair the routing fix is scoped against. Case is
// deliberately preserved: two reasons differing only in case stay two rows,
// because folding them would hide that the operator used two spellings.
type misrouteBucketKey struct {
	producerID ProducerID
	reason     string
}

// CountMisroutes aggregates the flagged items whose FlaggedAt falls inside the
// week into one row per (producer, reason).
//
// It is a pure function over the items the caller read, so the population is
// exactly the caller's — which is what lets an independent read of the same
// store be compared against it. It takes no context for the same reason: it does
// no I/O and cannot fail, so a ctx parameter would be decoration.
//
// A row list is always non-nil: a week nobody flagged in answers with an empty
// array rather than a null, because null reads as "no answer" and this endpoint's
// quiet week IS the answer.
func CountMisroutes(items Items, week MisrouteWeek, loc *stdtime.Location) MisrouteCount {
	start, end := week.Window(loc)
	counts := make(map[misrouteBucketKey]int, len(items))
	kinds := make(map[misrouteBucketKey]ProducerKind, len(items))
	for _, item := range items {
		// An unset FlaggedAt means the item carries no flag at all: the schema
		// writes FlagReason and FlaggedAt together or not at all, so a nil
		// timestamp is the absence of a flag rather than a flag of unknown date.
		if item.FlaggedAt == nil {
			continue
		}
		flaggedAt := item.FlaggedAt.Time()
		if flaggedAt.Before(start) || !flaggedAt.Before(end) {
			continue
		}
		key := misrouteBucketKey{
			producerID: item.ProducerID,
			reason:     strings.TrimSpace(item.FlagReason),
		}
		counts[key]++
		if _, seen := kinds[key]; !seen {
			kinds[key] = item.ProducerKind
		}
	}
	rows := make(MisrouteCountRows, 0, len(counts))
	for key, count := range counts {
		rows = append(rows, MisrouteCountRow{
			ProducerID:   key.producerID,
			ProducerKind: kinds[key],
			Reason:       key.reason,
			Count:        count,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Count != rows[j].Count {
			return rows[i].Count > rows[j].Count
		}
		if rows[i].ProducerID != rows[j].ProducerID {
			return rows[i].ProducerID < rows[j].ProducerID
		}
		return rows[i].Reason < rows[j].Reason
	})
	return MisrouteCount{
		Week:  week.String(),
		Start: libtime.ToDate(start),
		End:   libtime.ToDate(end.Add(-stdtime.Nanosecond)),
		Rows:  rows,
	}
}
