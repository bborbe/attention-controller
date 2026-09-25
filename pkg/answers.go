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
// `{question, kind, value, values}` the schema declares, and so the kind, value
// and values rules are Answer's own rather than a second copy of them. In
// particular `Values` is Answer's field, inherited here — a single-question item
// declared `multiple` needs it just as much as a tab does.
type QuestionAnswer struct {
	// Question is the Tab of the Questions entry this answers.
	Question string `json:"question"`
	Answer
}

// Validate returns an error when the question is unnamed, or when the embedded
// answer's kind, value and values disagree with one another.
func (a QuestionAnswer) Validate(ctx context.Context) error {
	return validation.All{
		validation.Name("Question", validation.NotEmptyString(a.Question)),
		validation.Name("Answer", a.Answer),
	}.Validate(ctx)
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
