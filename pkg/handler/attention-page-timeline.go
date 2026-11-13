// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package-level note: this file holds the card's info-panel timeline — the
// seven-stage list and the delivery-outcome line — computed in Go from the
// item's stored timestamps and the store's own delivery report. It lives in its
// own file rather than in attention-page-helpers.go, which is already over
// 1,100 lines.
package handler

import (
	"fmt"
	"math"

	libtime "github.com/bborbe/time"

	"github.com/bborbe/attention-controller/pkg"
)

// absentStageMark is what the panel renders for a stage that has no
// timestamp.
//
// ⚠️ It is an em dash, and the choice is the contract's rather than a
// cosmetic one: a delta computed from a missing stage asserts that the stage
// took no time, which is the opposite of what an absent stage means. A blank
// would read as a rendering fault rather than as a fact.
const absentStageMark = "—"

// Timeline returns the panel's stage lines, in stage order.
func (r attentionPageRow) Timeline() []string {
	return attentionTimeline(r.Item, r.DeliveryStatus, r.DeliveryAttemptedAt)
}

// DeliveryOutcome returns the reader-visible delivery-outcome name, or "" when
// the panel must render no line for it.
//
// ⚠️ Two states render nothing, and each for its own reason. The empty status
// is a read that failed, and naming any outcome for it would assert something
// about the item that this render did not learn. HeldStatus is a deliberate
// hold: an attempt and its timestamp exist, so rendering "—" would be false,
// and rendering the literal "held" would name a fifth outcome the contract
// does not list. Silence is the interim rendering until that gap is resolved.
func (r attentionPageRow) DeliveryOutcome() string {
	if r.DeliveryStatus == "" || r.DeliveryStatus == pkg.HeldStatus {
		return ""
	}
	return string(r.DeliveryStatus)
}

func attentionTimeline(
	item pkg.Item,
	status pkg.DeliveryStatus,
	attemptedAt *libtime.DateTime,
) []string {
	// The delivery stage exists only when the answer was delivered: a failed
	// or held attempt has a timestamp of its own, but it is not the delivery.
	var deliveredAt *libtime.DateTime
	if status == pkg.DeliveredStatus {
		deliveredAt = attemptedAt
	}
	lines := []string{
		"asked " + stageTimestamp(item.AskedAt),
		"created " + stageTimestamp(&item.CreatedAt),
		"displayed " + stageTimestamp(item.DisplayedAt),
		"asked→created " + stageDelta(item.AskedAt, &item.CreatedAt),
		"created→displayed " + stageDelta(&item.CreatedAt, item.DisplayedAt),
		"answered " + stageTimestamp(item.AnsweredAt),
		"displayed→answered " + stageDelta(item.DisplayedAt, item.AnsweredAt),
	}
	if item.AnsweredBy != "" {
		lines = append(lines, "answered by "+item.AnsweredBy)
	}
	lines = append(lines,
		"delivered "+stageTimestamp(deliveredAt),
		"answered→delivered "+stageDelta(item.AnsweredAt, deliveredAt),
		"released "+stageTimestamp(deliveredAt),
	)
	if item.ClosedAt != nil {
		close := "closed " + item.ClosedAt.String() + " " + item.AnswerMechanism.String()
		// The close reason is derived, never stored: the mechanism says how
		// the card was answered, and the recorded client says by what. The
		// client half reuses recordClient so the panel and the dimmed record
		// cannot compose the same evidence two different ways.
		if client := recordClient(item); client != "" {
			close += " via " + client
		}
		return append(lines, close)
	}
	return append(lines, "closed "+absentStageMark)
}

// stageTimestamp renders a stage's instant, or the absence mark when the stage
// did not happen. It never substitutes a zero or another field's value.
func stageTimestamp(at *libtime.DateTime) string {
	if at == nil {
		return absentStageMark
	}
	return at.String()
}

// stageDelta renders the whole seconds between two stages, floored.
//
// ⚠️ Floor rather than truncate, and signed rather than clamped: a producer
// whose clock runs ahead of the store's can make a delta negative, and the
// honest reading of skew is the signed value. Flooring keeps a negative
// fractional delta from rounding toward zero and reading as smaller than it
// is.
func stageDelta(from, to *libtime.DateTime) string {
	if from == nil || to == nil {
		return absentStageMark
	}
	return fmt.Sprintf("%ds", int64(math.Floor(to.Sub(*from).Duration().Seconds())))
}
