// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"strings"

	"github.com/bborbe/collection"
	"github.com/bborbe/errors"
	"github.com/bborbe/validation"
)

// Answerer is the party whose answer an Answer carries — the operator, or a
// manager session answering one of its own workers' cards.
//
// It is distinct from AnsweredBy, which names the ARM that carried the answer:
// a manager's sub-agent and the operator's board click can travel the same arm,
// so the arm cannot say whose voice the answer is in. Every carrier reads this
// field to decide that voice, because a worker treats an operator answer as an
// operator release and must never be told the operator answered when a manager
// did.
type Answerer string

const (
	// OperatorAnswerer is the operator's own answer.
	OperatorAnswerer Answerer = "operator"
	// ManagerAnswerer is a manager session's answer, in its own voice.
	ManagerAnswerer Answerer = "manager"
)

// ManagerAnswerPrefix opens every rendering of a manager's answer. It is the
// exact prefix a worker's own rule recognises as a manager decision, so a
// carrier must render it verbatim and never wrap it in an operator envelope.
const ManagerAnswerPrefix = "Manager answer, via supervisor:"

// Answerers is a collection of Answerer.
type Answerers []Answerer

// AvailableAnswerers holds every legal answerer.
var AvailableAnswerers = Answerers{
	OperatorAnswerer,
	ManagerAnswerer,
}

// String returns the answerer as a string.
func (a Answerer) String() string {
	return string(a)
}

// Validate returns an error when the answerer is not a legal value.
//
// An empty value validates: it is absent rather than unknown, and is what every
// answer recorded before this field existed reads as. A carrier renders an
// absent answerer exactly as it rendered every answer before — the operator's.
func (a Answerer) Validate(ctx context.Context) error {
	if a == "" {
		return nil
	}
	if !AvailableAnswerers.Contains(a) {
		return errors.Wrapf(ctx, validation.Error, "unknown answerer '%s'", a)
	}
	return nil
}

// Contains reports whether the collection holds the given answerer.
func (a Answerers) Contains(answerer Answerer) bool {
	return collection.Contains(a, answerer)
}

// IsManagerAnswered reports whether a manager supplied the item's answer
// content — on the single answer, or on any per-question answer.
func IsManagerAnswered(item Item) bool {
	if item.Answer != nil && item.Answer.Answerer == ManagerAnswerer {
		return true
	}
	for _, answer := range item.Answers {
		if answer.Answerer == ManagerAnswerer {
			return true
		}
	}
	return false
}

// ManagerAnswerText renders a manager's answer text with ManagerAnswerPrefix
// exactly once: a value that already opens with the prefix — which is what the
// supervisor's gate answerer writes — is not prefixed a second time.
func ManagerAnswerText(text string) string {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, ManagerAnswerPrefix) {
		return trimmed
	}
	return ManagerAnswerPrefix + " " + trimmed
}
