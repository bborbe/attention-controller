// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"regexp"
	"strings"

	"github.com/bborbe/collection"
	"github.com/bborbe/errors"
	"github.com/bborbe/validation"
)

// Risk is the producer's declaration of how much care an item deserves.
//
// It is producer-declared and stored unchanged — the discipline InterruptClass
// and ProvenanceClass already carry. The store records what the producer
// declared and reads it back unchanged, so it neither recomputes nor promotes,
// and [[Attention Item Schema]] § The stack does not rank holds.
//
// What the board RENDERS is a separate act, performed at render time and
// deliberately not stored. It may escalate above the declared level but never
// below it, so a producer that under-declares cannot make a destructive card
// render green. See RiskOf in pkg/handler.
type Risk string

const (
	// RoutineRisk is a card the operator may answer without care — a compact
	// nudge, a reload, an ack notice.
	RoutineRisk Risk = "routine"
	// DecisionRisk is a reversible choice — a pick, a task approval. It is also
	// what an absent declaration reads as.
	DecisionRisk Risk = "decision"
	// ElevatedRisk is an irreversible or production-touching act — a delete, a
	// drop, a merge, a deploy, a live trade, an operator-only gate.
	ElevatedRisk Risk = "risk"
)

// Risks is a collection of Risk.
type Risks []Risk

// AvailableRisks holds every legal risk level.
var AvailableRisks = Risks{
	RoutineRisk,
	DecisionRisk,
	ElevatedRisk,
}

// String returns the risk as a string.
func (r Risk) String() string {
	return string(r)
}

// Validate returns an error when the risk is not one of the declared levels.
//
// An empty value validates. The schema adds no write-time rejection for an
// omitted level, so an absent value reads as a pre-change item and renders as
// DecisionRisk — the same choice silences 9, 11 and 12 made, and for the same
// reason.
func (r Risk) Validate(ctx context.Context) error {
	if r == "" {
		return nil
	}
	if !AvailableRisks.Contains(r) {
		return errors.Wrapf(ctx, validation.Error, "unknown risk '%s'", r)
	}
	return nil
}

// OrDefault returns the declared risk, or DecisionRisk when none was declared.
//
// It is what makes "absent means decision" a single expression rather than a
// rule every reader restates.
func (r Risk) OrDefault() Risk {
	if r == "" {
		return DecisionRisk
	}
	return r
}

// Contains reports whether the collection holds the given risk.
func (r Risks) Contains(risk Risk) bool {
	return collection.Contains(r, risk)
}

// irreversibleRiskVerbs are the verbs whose presence in an item's own payload
// or context escalates it to ElevatedRisk whatever the producer declared.
//
// The list is deliberately shallow, on [[Action Risk Classification]]'s own
// instruction to over-classify rather than under: a card that mentions a
// deploy and turns out to be harmless costs the operator a moment's care,
// while one that hides a deploy costs them the thing the care was for.
var irreversibleRiskVerbs = []string{
	"delete", "drop", "rm", "merge", "deploy", "release", "trade", "force",
}

// irreversibleRiskVerbPattern matches any of irreversibleRiskVerbs at the
// START of a word, case-insensitively, and deliberately carries no closing
// boundary.
//
// The asymmetry is the point, and both halves were measured. The leading
// boundary is what stops "enforce" matching "force" and "former" matching
// "force" — a substring match without it would escalate a card about enforcing
// an existing limit. The absent closing boundary is what lets the stems work:
// `\bdelete\b` does not match "deleted", `\bdeploy\b` does not match
// "deploying", and those are precisely the phrasings a real card uses, so a
// closed match would leave the commonest destructive wording rendering
// routine — the exact failure this rule exists to prevent. Matching a prefix
// over-classifies on words like "droplet"; that is the direction to err in.
var irreversibleRiskVerbPattern = regexp.MustCompile(
	`(?i)\b(?:` + strings.Join(irreversibleRiskVerbs, "|") + `)`,
)

// RiskOf returns the risk the board renders for an item: the higher of the
// producer's declaration and the rule the item's own content matches.
//
// It is a RENDER-TIME act, never a store write. The store keeps the declared
// value untouched, so it neither recomputes nor promotes, per
// [[Attention Item Schema]] § The stack does not rank — this is what the board
// calls on the way out, and it reorders nothing.
//
// The escalation is one-directional. A producer may declare above the rule and
// keep that; it can never render below it. That is the whole point: a card
// asking about a delete must not be able to look routine because its producer
// said so.
//
// productionTouching is the resolved provenance fact — whether the vault task
// the producing session is anchored to declares the operator's
// production-touching marker. ⚠️ That value was parsed and resolved on every
// page load while nothing read it; this function is its first consumer, which
// is what makes the marker a live rule again rather than a dead read.
func RiskOf(item Item, productionTouching bool) Risk {
	declared := item.Risk.OrDefault()
	if declared == ElevatedRisk {
		return ElevatedRisk
	}
	if productionTouching {
		return ElevatedRisk
	}
	if irreversibleRiskVerbPattern.MatchString(item.Payload.String()) {
		return ElevatedRisk
	}
	if irreversibleRiskVerbPattern.MatchString(item.Context.String()) {
		return ElevatedRisk
	}
	return declared
}
