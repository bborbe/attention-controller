// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
)

// Transition is one legal move between two states, with the schema's trigger.
type Transition struct {
	// From is the state the item must be in.
	From State
	// To is the state the item lands in.
	To State
	// Trigger is the schema's own description of what causes the move.
	Trigger string
}

// Transitions is a collection of Transition.
type Transitions []Transition

// AvailableTransitions is the schema's transitions table and nothing else.
// Every legal move is one row; anything not in this list is not a legal
// transition.
//
// `open → gone` is deliberately absent: it is a removal, not a state change.
// The schema has no `gone` state, so it is expressed as a delete and is not a
// row here.
var AvailableTransitions = Transitions{
	{
		From:    OpenState,
		To:      AnsweredState,
		Trigger: "the answer-return path delivers an answer to producer_id",
	},
	{
		From:    OpenState,
		To:      ClosedState,
		Trigger: "the item is resolved without an answer",
	},
	{
		From:    AnsweredState,
		To:      ClosedState,
		Trigger: "acknowledgement, or the producer's next read",
	},
}

// Find returns the transition between two states, if one is legal.
func (t Transitions) Find(from State, to State) (*Transition, bool) {
	for _, transition := range t {
		if transition.From == from && transition.To == to {
			return &transition, true
		}
	}
	return nil, false
}

// ValidateTransition returns nil when moving from one state to another is a row
// of the schema's table, and a wrapped ErrIllegalTransition otherwise.
//
// The distinction this preserves: an *illegal* move is one the schema never
// allows from any state (`closed → answered`), and it is a different failure
// from a *legal* move attempted from the wrong state (`answered → answered`,
// where the caller simply lost a race). Both are rejected, but a caller can
// tell them apart and respond differently.
func ValidateTransition(ctx context.Context, from State, to State) error {
	if _, found := AvailableTransitions.Find(from, to); found {
		return nil
	}
	return errors.Wrapf(
		ctx,
		ErrIllegalTransition,
		"transition %s -> %s is not in the schema's table",
		from,
		to,
	)
}
