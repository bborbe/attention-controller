// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"sort"
	"time"

	libtime "github.com/bborbe/time"
)

// ManagerParkedReasons is the closed set of reasons that mean "parked on the
// operator". Only these may age. Mirrors the source script's PARKED_REASONS.
//
// ⚠️ A `stuck:<cause>` gate is deliberately absent: a stall is a duration with
// no answer path, not a park, and ageing it would escalate a worker nobody can
// unblock by answering.
var ManagerParkedReasons = []string{
	"registry:waiting", "busy+parked-modal", "headless+closer", "idle+closer",
}

// ManagerParkAgeThresholds are the escalation thresholds, in minutes. Each fires
// ONCE per continuous park.
var ManagerParkAgeThresholds = []int{15, 60}

// managerIsParkedReason reports whether a gate reason may age.
func managerIsParkedReason(reason string) bool {
	for _, parked := range ManagerParkedReasons {
		if parked == reason {
			return true
		}
	}
	return false
}

// managerParkAge is one session's continuous-park clock.
type managerParkAge struct {
	// Since is when this arm first observed the park in progress. It survives a
	// restart, so a park open across one keeps its age.
	Since libtime.DateTime `json:"since"`
	// Aged holds the thresholds already fired for this park, so each fires once.
	Aged []int `json:"aged"`
}

// managerAgedEvent is one threshold crossing to escalate.
type managerAgedEvent struct {
	// Session is the session the park belongs to.
	Session string
	// Minutes is the threshold that was crossed.
	Minutes int
}

// managerAdvanceParkAges drops every session no longer parked and starts a
// clock for every session newly parked. Mutates and returns ages.
//
// ⚠️ Keyed on CONTINUOUS PARK, never on gated-set membership: a worker that
// answers one question and asks the next stays gated throughout, so a
// membership-keyed clock would never restart. A non-parked pass ends the park;
// the next parked pass starts a new one.
func managerAdvanceParkAges(
	ages map[string]managerParkAge,
	parked []string,
	now libtime.DateTime,
) map[string]managerParkAge {
	if ages == nil {
		ages = make(map[string]managerParkAge, len(parked))
	}
	parkedSet := make(map[string]struct{}, len(parked))
	for _, session := range parked {
		parkedSet[session] = struct{}{}
	}
	for session := range ages {
		if _, ok := parkedSet[session]; ok {
			continue
		}
		delete(ages, session)
	}
	for session := range parkedSet {
		record, ok := ages[session]
		if !ok {
			ages[session] = managerParkAge{Since: now, Aged: []int{}}
			continue
		}
		if record.Since.IsZero() {
			// A clock with no start (a hand-written or corrupt file) starts now
			// rather than reading as an unbounded age.
			record.Since = now
			ages[session] = record
		}
	}
	return ages
}

// managerAgedEvents returns (session, minutes) for each threshold crossed since
// the last call, ONCE each. A backwards clock step yields a non-negative age.
func managerAgedEvents(
	ages map[string]managerParkAge,
	now libtime.DateTime,
	thresholds []int,
) []managerAgedEvent {
	sessions := make([]string, 0, len(ages))
	for session := range ages {
		sessions = append(sessions, session)
	}
	sort.Strings(sessions)
	events := make([]managerAgedEvent, 0)
	for _, session := range sessions {
		record := ages[session]
		minutes := managerParkAgeMinutes(record.Since, now)
		for _, threshold := range thresholds {
			if minutes < threshold || managerThresholdAged(record.Aged, threshold) {
				continue
			}
			record.Aged = append(record.Aged, threshold)
			events = append(events, managerAgedEvent{Session: session, Minutes: threshold})
		}
		ages[session] = record
	}
	return events
}

// managerParkAgeMinutes is the whole minutes elapsed since a park started.
//
// ⚠️ `max(0, …)` rather than a raw subtraction: a backwards clock step (NTP, a
// manual set) would otherwise yield a negative age. That is benign — it simply
// does not fire — but the direction is stated rather than left incidental.
func managerParkAgeMinutes(since, now libtime.DateTime) int {
	elapsed := now.Sub(since)
	if elapsed < 0 {
		return 0
	}
	return int(elapsed.Duration() / time.Minute)
}

// managerThresholdAged reports whether a threshold already fired for this park.
func managerThresholdAged(aged []int, threshold int) bool {
	for _, fired := range aged {
		if fired == threshold {
			return true
		}
	}
	return false
}
