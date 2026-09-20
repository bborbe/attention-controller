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
