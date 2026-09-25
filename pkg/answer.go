// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
	"github.com/bborbe/validation"
)

// Answer is what the operator actually said on a `message` item.
//
// It is store-written on the existing open -> answered transition, exactly as
// AnsweredAt, AnsweredBy and Decision are. Decision carries a `permission`
// item's verdict and Answer carries a `message` item's content; neither
// substitutes for the other, because an allow/deny verdict and a chosen label
// are different kinds of thing.
type Answer struct {
	// Kind is the shape the answer took. Empty means no answer content was
	// recorded, which is what a `permission`- or `ack`-class item reads as, and
	// what every item answered before this field existed reads as.
	Kind AnswerKind `json:"kind,omitempty"`
	// Value carries the chosen option's label for `option`, the operator's words
	// for `text`, and nothing for `skip`.
	Value string `json:"value,omitempty"`
	// Values carries the chosen labels of a `multiple` question, and is mutually
	// exclusive with Value.
	//
	// The choice between them is fixed by the question's declared cardinality,
	// not by the caller: a `single` question's answer carries Value and no
	// Values, a `multiple` question's carries Values and no Value. That pairing
	// is what keeps the encoding reversible — a two-pick answer and a one-pick
	// answer whose label happens to contain ", " are different shapes rather
	// than one string read two ways.
	//
	// It lives here rather than on QuestionAnswer so that a single-question item
	// declared `multiple` has somewhere to put its picks. Without it that card
	// renders checkboxes and can never submit them: the item-level `answer` field
	// would carry one label and drop the rest. QuestionAnswer embeds Answer, so
	// it inherits this rather than declaring a second copy.
	//
	// It is `option`-kind only: a `skip` has nothing to list, and a `text`
	// answer is already the operator's own words.
	Values []string `json:"values,omitempty"`
}

// Validate returns an error when the kind and the value disagree with the shape
// the kind describes.
//
// The three rules are the schema's own descriptions of the kinds: an `option`
// carries the label of the entry that was chosen, a `text` carries the
// operator's words, and a `skip` carries nothing. An empty Kind validates — the
// schema adds no write-time rejection for an omitted answer, so an absent value
// reads as a pre-change item rather than as an error.
func (a Answer) Validate(ctx context.Context) error {
	return validation.All{
		validation.Name("Kind", a.Kind),
		validation.Name("Value", validation.HasValidationFunc(a.validateValue)),
		validation.Name("Values", validation.HasValidationFunc(a.validateValues)),
	}.Validate(ctx)
}

// validateValue returns an error when the value does not match the shape the
// declared kind describes.
//
// An `option` answer carrying `values` is legal and needs no `value`: a
// multi-pick answer has no single label, and requiring one would reject exactly
// the shape `values` exists to carry. validateValues rejects an entry carrying
// both, so this relaxation cannot smuggle a value past the rule.
func (a Answer) validateValue(ctx context.Context) error {
	switch a.Kind {
	case OptionAnswerKind:
		if len(a.Values) > 0 {
			return nil
		}
		if err := validation.NotEmptyString(a.Value).Validate(ctx); err != nil {
			return errors.Wrapf(ctx, err, "answerKind '%s' requires a value", a.Kind)
		}
	case TextAnswerKind:
		if err := validation.NotEmptyString(a.Value).Validate(ctx); err != nil {
			return errors.Wrapf(ctx, err, "answerKind '%s' requires a value", a.Kind)
		}
	case SkipAnswerKind:
		if a.Value != "" {
			return errors.Wrapf(
				ctx,
				validation.Error,
				"answerKind 'skip' carries no value, got '%s'",
				a.Value,
			)
		}
	case "":
		// Absent, not unknown: the schema declines to reject an omitted answer.
	}
	return nil
}

// validateValues enforces the rules `values` carries on its own: `option`-kind
// only, mutually exclusive with `value`, and no empty label in the list.
//
// An empty list validates — `values` is optional, and an absent value is what
// every `single` question's answer and every `skip` carries. Whether the field
// is the right one for its question is not decidable here, because this type
// does not hold the question's cardinality; Item.validateAnswer and
// Item.validateAnswersMatchQuestions enforce that pairing, where both sides are
// in hand.
func (a Answer) validateValues(ctx context.Context) error {
	if len(a.Values) == 0 {
		return nil
	}
	if a.Kind != OptionAnswerKind {
		return errors.Wrapf(
			ctx,
			validation.Error,
			"values are only allowed on an option answer, got answerKind '%s'",
			a.Kind,
		)
	}
	if a.Value != "" {
		return errors.Wrap(
			ctx,
			validation.Error,
			"value and values are mutually exclusive, got both",
		)
	}
	for _, value := range a.Values {
		if err := validation.NotEmptyString(value).Validate(ctx); err != nil {
			return errors.Wrap(ctx, err, "values carry an empty label")
		}
	}
	return nil
}
