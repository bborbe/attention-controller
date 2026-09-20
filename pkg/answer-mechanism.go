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

// AnswerMechanism is how an item is answered. All three values are required
// vocabulary, not a menu to pick one from — the goal's third success criterion
// names all three.
type AnswerMechanism string

const (
	// MessageAnswerMechanism is a question with options, answered by a
	// cross-session message to producer_id.
	MessageAnswerMechanism AnswerMechanism = "message"
	// PermissionAnswerMechanism is a gate on an action, answered by a
	// permission answer carrying the item_id as its requestId.
	PermissionAnswerMechanism AnswerMechanism = "permission"
	// AckAnswerMechanism is a condition report: acknowledgement only, no
	// reply routed. Its item reaches closed with answered_at unset.
	AckAnswerMechanism AnswerMechanism = "ack"
)

// AnswerMechanisms is a collection of AnswerMechanism.
type AnswerMechanisms []AnswerMechanism

// AvailableAnswerMechanisms holds every legal answer mechanism.
var AvailableAnswerMechanisms = AnswerMechanisms{
	MessageAnswerMechanism,
	PermissionAnswerMechanism,
	AckAnswerMechanism,
}

// String returns the answer mechanism as a string.
func (a AnswerMechanism) String() string {
	return string(a)
}

// Validate returns an error when the mechanism is not one of AvailableAnswerMechanisms.
func (a AnswerMechanism) Validate(ctx context.Context) error {
	if !AvailableAnswerMechanisms.Contains(a) {
		return errors.Wrapf(ctx, validation.Error, "unknown answerMechanism '%s'", a)
	}
	return nil
}

// Contains reports whether the collection holds the given answer mechanism.
func (a AnswerMechanisms) Contains(answerMechanism AnswerMechanism) bool {
	return collection.Contains(a, answerMechanism)
}
