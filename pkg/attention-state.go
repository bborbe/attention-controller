// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/collection"
	"github.com/bborbe/errors"
	"github.com/bborbe/validation"
)

// State is where an item sits in its lifecycle. The three values are the
// schema's own; `open` is the only state an arm renders to the operator.
type State string

const (
	// OpenState is pushed and not yet answered.
	OpenState State = "open"
	// AwaitingEvidenceState is answered with a `done`-class claim whose effect
	// has not been observed yet.
	//
	// ⚠️ It exists because a `done` answer is a CLAIM, not a fact. Measured
	// 2026-11-13: a card was answered `done — reviewed and flipped both`, and
	// minutes later the two PRs it named were still `OPEN isDraft=true
	// reviewDecision=REVIEW_REQUIRED` with unchanged heads. The card read as
	// satisfied while the work was untouched, and nothing noticed — a false
	// `done` is invisible, which is strictly worse than an ask nobody answered.
	// An item in this state is off the open queue but NOT satisfied: the answer
	// is recorded, EvidenceClaim names what would prove it, and the sweep closes
	// it only when that claim is observed or reopens it when the deadline passes.
	AwaitingEvidenceState State = "awaiting_evidence"
	// AnsweredState is answered and routed back to producer_id.
	AnsweredState State = "answered"
	// ClosedState has left the queue. Terminal.
	ClosedState State = "closed"
)

// States is a collection of State.
type States []State

// AvailableStates holds every legal state, in lifecycle order.
var AvailableStates = States{
	OpenState,
	AwaitingEvidenceState,
	AnsweredState,
	ClosedState,
}

// String returns the state as a string.
func (s State) String() string {
	return string(s)
}

// Validate returns an error when the state is not one of AvailableStates.
func (s State) Validate(ctx context.Context) error {
	if !AvailableStates.Contains(s) {
		return errors.Wrapf(ctx, validation.Error, "unknown state '%s'", s)
	}
	return nil
}

// Contains reports whether the collection holds the given state.
func (s States) Contains(state State) bool {
	return collection.Contains(s, state)
}
