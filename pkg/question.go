// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
	"github.com/bborbe/validation"
)

// Question is one question unit of an item that carries more than one. Each is
// rendered as a tab across the top of the board's card, with its own payload,
// its own control and its own options.
//
// It exists because an item otherwise holds exactly one question, so a card
// asking several things at once had no unit for a tab to be and no place to put
// a second question's options. It carries Payload's contract — plain
// operator-facing sentences, no machine handles — because it is the question
// the operator reads.
type Question struct {
	// Tab is the label the board renders on this question's tab, and the value
	// a QuestionAnswer names in its Question field.
	Tab string `json:"tab"`
	// Payload is the question itself.
	Payload Payload `json:"payload"`
	// Cardinality is whether this question takes one pick or many. Absent means
	// single, exactly as the item-level field does.
	Cardinality AnswerCardinality `json:"cardinality,omitempty"`
	// Options are this question's choices.
	Options AnswerOptions `json:"options,omitempty"`
}

// Validate returns an error when the question carries no tab or no payload, or
// when either of its own fields is invalid.
func (q Question) Validate(ctx context.Context) error {
	return validation.All{
		validation.Name("Tab", validation.NotEmptyString(q.Tab)),
		validation.Name("Payload", validation.NotEmptyString(q.Payload)),
		validation.Name("Cardinality", q.Cardinality),
		validation.Name("Options", q.Options),
	}.Validate(ctx)
}

// Questions is an ordered list of Question — the question units of a
// multi-question item, one per rendered tab.
type Questions []Question

// Validate returns an error when any question is itself invalid, or when two
// questions carry the same tab.
//
// The unique-tab rule is the schema's, and it is load-bearing rather than
// cosmetic: an answer names the question it answers by tab, so two questions
// sharing one would make an answer ambiguous between them and the store would
// have no way to tell which was meant.
//
// An empty list validates — `questions` is optional, and an absent value is
// what every single-question item carries.
func (q Questions) Validate(ctx context.Context) error {
	return validation.All{
		validation.Name("Questions", validation.HasValidationFunc(q.validateQuestions)),
		validation.Name("Tabs", validation.HasValidationFunc(q.validateUniqueTabs)),
	}.Validate(ctx)
}

// validateQuestions returns an error when any question in the list is invalid.
func (q Questions) validateQuestions(ctx context.Context) error {
	for _, question := range q {
		if err := question.Validate(ctx); err != nil {
			return errors.Wrapf(ctx, err, "validate question '%s' failed", question.Tab)
		}
	}
	return nil
}

// validateUniqueTabs returns an error when two questions carry the same tab.
func (q Questions) validateUniqueTabs(ctx context.Context) error {
	seen := make(map[string]struct{}, len(q))
	for _, question := range q {
		if _, ok := seen[question.Tab]; ok {
			return errors.Wrapf(
				ctx,
				validation.Error,
				"tab '%s' is carried by more than one question, so an answer naming it is ambiguous",
				question.Tab,
			)
		}
		seen[question.Tab] = struct{}{}
	}
	return nil
}
