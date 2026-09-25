// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
	"github.com/bborbe/validation"
)

// QuestionAnswer is what the operator said on one question of a
// multi-question item: the tab it answers, and the answer itself.
//
// Answer is embedded rather than nested so the wire shape is the flat
// `{question, kind, value}` the schema declares, and so the kind/value rules
// are Answer's own rather than a second copy of them.
type QuestionAnswer struct {
	// Question is the Tab of the Questions entry this answers.
	Question string `json:"question"`
	Answer
	// Values carries the chosen labels of a `multiple` question, and is mutually
	// exclusive with the embedded Answer's Value.
	//
	// The choice between them is fixed by the question's declared cardinality,
	// not by the caller: a `single` question's answer carries Value and no
	// Values, a `multiple` question's carries Values and no Value. That pairing
	// is what keeps the encoding reversible — a two-pick answer and a one-pick
	// answer whose label happens to contain ", " are different shapes rather
	// than one string read two ways.
	//
	// It is `option`-kind only: a `skip` has nothing to list, and a `text`
	// answer is already the operator's own words.
	Values []string `json:"values,omitempty"`
}

// Validate returns an error when the question is unnamed, when the kind and the
// value disagree, or when `values` breaks its own rules.
//
// It does not delegate wholesale to the embedded Answer, because Answer's
// kind/value rule requires an `option` answer to carry a label and a multi-pick
// answer carries none — the labels are in `values`. Only that one rule is
// relaxed, and only when `values` is actually present.
func (a QuestionAnswer) Validate(ctx context.Context) error {
	return validation.All{
		validation.Name("Question", validation.NotEmptyString(a.Question)),
		validation.Name("Kind", a.Kind),
		validation.Name("Value", validation.HasValidationFunc(a.validateValue)),
		validation.Name("Values", validation.HasValidationFunc(a.validateValues)),
	}.Validate(ctx)
}

// validateValue applies Answer's kind/value rule, except that an `option` answer
// carrying `values` is legal.
//
// Requiring a single label there would reject exactly the shape `values` exists
// to carry, and `validateValues` already rejects an entry carrying both fields —
// so this relaxation cannot be used to smuggle a value past the rule.
func (a QuestionAnswer) validateValue(ctx context.Context) error {
	if a.Kind == OptionAnswerKind && len(a.Values) > 0 {
		return nil
	}
	return a.Answer.validateValue(ctx)
}

// validateValues enforces the rules `values` carries on its own: `option`-kind
// only, mutually exclusive with `value`, and no empty label in the list.
//
// An empty list validates — `values` is optional, and an absent value is what
// every `single` question's answer and every `skip` carries. Whether the field
// is the right one for its question is not decidable here, because this type
// does not hold the question's cardinality; Item.validateAnswersMatchQuestions
// enforces that pairing, where both sides are in hand.
func (a QuestionAnswer) validateValues(ctx context.Context) error {
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

// Answers is an ordered list of QuestionAnswer — the operator's answer to each
// question of a multi-question item.
//
// It is distinct from Answer rather than a replacement for it, so a
// single-question item's wire shape is unchanged from the one already shipped.
// The two are mutually exclusive by construction: an item carrying both would
// have two places the operator's content could be and no rule for which wins.
type Answers []QuestionAnswer

// Validate returns an error when any entry is invalid, or when two entries
// answer the same question.
//
// An empty list validates — `answers` is optional, and an absent value is what
// every item without `questions` carries.
func (a Answers) Validate(ctx context.Context) error {
	return validation.All{
		validation.Name("Answers", validation.HasValidationFunc(a.validateAnswers)),
		validation.Name("Questions", validation.HasValidationFunc(a.validateUniqueQuestions)),
	}.Validate(ctx)
}

// validateAnswers returns an error when any entry in the list is invalid.
func (a Answers) validateAnswers(ctx context.Context) error {
	for _, answer := range a {
		if err := answer.Validate(ctx); err != nil {
			return errors.Wrapf(
				ctx,
				err,
				"validate answer for question '%s' failed",
				answer.Question,
			)
		}
	}
	return nil
}

// validateUniqueQuestions returns an error when two entries answer the same
// question.
//
// One question has one answer. Two entries for one tab would leave the store
// holding a set where a value was asked for, and no rule for which the producer
// should read.
func (a Answers) validateUniqueQuestions(ctx context.Context) error {
	seen := make(map[string]struct{}, len(a))
	for _, answer := range a {
		if _, ok := seen[answer.Question]; ok {
			return errors.Wrapf(
				ctx,
				validation.Error,
				"question '%s' is answered more than once, one question has one answer",
				answer.Question,
			)
		}
		seen[answer.Question] = struct{}{}
	}
	return nil
}
