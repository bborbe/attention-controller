// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/bborbe/validation"
)

// ItemID is the stable unique id for an item. It survives the producer's exit,
// so an answer can name an item whose asker is gone.
type ItemID string

// String returns the item id as a string.
func (i ItemID) String() string {
	return string(i)
}

// ProducerID is who asked — the session id, agent id, or job id that made the
// claim. This is the field the answer routes back to, and the identity a
// duplicate is judged against.
type ProducerID string

// String returns the producer id as a string.
func (p ProducerID) String() string {
	return string(p)
}

// LivenessRef is how to tell whether this producer is still alive. Exactly two
// models exist, and the producer declares which one its item uses:
//
//	session:<id>    a long-lived producer whose absence is meaningful
//	heartbeat:<path> a short-lived producer that exits by design
type LivenessRef string

// String returns the liveness ref as a string.
func (l LivenessRef) String() string {
	return string(l)
}

// Validate returns an error when the ref is neither model.
func (l LivenessRef) Validate(ctx context.Context) error {
	if _, _, err := l.Parse(ctx); err != nil {
		return err
	}
	return nil
}

// DedupKey is the value two pushes must share to be the same item. Duplicate
// suppression compares this, not the prose.
type DedupKey string

// String returns the dedup key as a string.
func (d DedupKey) String() string {
	return string(d)
}

// InterruptClass is the producer's declaration of how much this deserves the
// operator — the channel ladder's rung, declared at push time. Stored, never
// derived: the store never reorders, recomputes or promotes.
type InterruptClass string

// String returns the interrupt class as a string.
func (i InterruptClass) String() string {
	return string(i)
}

// Payload is the operator-facing content: the question, the gate, the failure.
// Plain, self-explaining sentences — no pane ids, session ids or commit SHAs in
// the sentence being read.
type Payload string

// String returns the payload as a string.
func (p Payload) String() string {
	return string(p)
}

// ItemContext is the operator-facing background for an ask — what is being built
// and why, what is blocked until the answer, what each option changes.
//
// It carries Payload's contract (plain, self-explaining sentences, no machine
// handles) and is deliberately a separate field rather than more Payload: the
// question has to stay readable on its own at the top of a board row, and
// folding the background into it buries the ask.
type ItemContext string

// String returns the context as a string.
func (c ItemContext) String() string {
	return string(c)
}

// Item is the one record every producer writes and every arm reads. Its fields,
// states and legal transitions come from the attention item schema; this type
// implements that schema and redefines nothing in it.
type Item struct {
	// ItemID is the stable unique id. Written by the store, not the producer.
	ItemID ItemID `json:"item_id"`
	// ProducerID is who asked.
	ProducerID ProducerID `json:"producer_id"`
	// ProducerKind is what kind of thing the producer is.
	ProducerKind ProducerKind `json:"producer_kind"`
	// ProvenanceClass is where the claim came from. Declared by the
	// producer, stored, never derived. Empty marks a pre-change item.
	ProvenanceClass ProvenanceClass `json:"provenance_class,omitempty"`
	// LivenessRef is how to tell whether the producer is still alive.
	LivenessRef LivenessRef `json:"liveness_ref"`
	// DedupKey is what two pushes must share to be the same item.
	DedupKey DedupKey `json:"dedup_key"`
	// InterruptClass is the producer's declaration of how much this deserves
	// the operator. Stored, never derived.
	InterruptClass InterruptClass `json:"interrupt_class"`
	// Payload is the operator-facing content.
	Payload Payload `json:"payload"`
	// Context is the operator-facing background for the ask. Optional, and the
	// same plain-sentence contract as Payload.
	Context ItemContext `json:"context,omitempty"`
	// AnswerMechanism is how this item is answered.
	AnswerMechanism AnswerMechanism `json:"answer_mechanism"`
	// Options are the choices a `message` item offers the operator.
	// Producer-declared, and rejected on a `permission` or `ack` item: a
	// `permission` item is approve-shaped and its verdict is Decision, so
	// options there would describe a choice the mechanism does not offer.
	Options AnswerOptions `json:"options,omitempty"`
	// AnswerCardinality is whether this item's question takes one pick or many.
	// Producer-declared, never derived, and rejected on a `permission` or `ack`
	// item exactly as Options is. Absent means single, which is what every item
	// pushed before this field existed reads as.
	AnswerCardinality AnswerCardinality `json:"answer_cardinality,omitempty"`
	// Questions are the question units of an item that carries more than one —
	// one per tab the board renders. Absent on a single-question item, which
	// uses Payload, Options and AnswerCardinality as its one unit; when present
	// it supersedes those three as the question units, and Payload becomes the
	// card's title rather than a question.
	Questions Questions `json:"questions,omitempty"`
	// State is where the item is in its lifecycle. Written by the store.
	State State `json:"state"`
	// CreatedAt is when the item entered the stack. Written by the store.
	CreatedAt libtime.DateTime `json:"created_at"`

	// AnsweredAt is when an answer arrived. Absent while unanswered.
	AnsweredAt *libtime.DateTime `json:"answered_at,omitempty"`
	// AnsweredBy is which arm supplied the answer. Recorded so the
	// one-item-many-arms property is auditable rather than merely asserted.
	//
	// ⚠️ It is a caller declaration, so it cannot tell a human's click from a
	// scripted client posting the same body. AnsweredClient carries what the
	// store itself can say about the client instead.
	AnsweredBy string `json:"answered_by,omitempty"`
	// AnsweredClient is what the store can say about the client that posted the
	// answer — derived from the request, never declared by it. RemoteAddr is the
	// only member a caller cannot spoof; UserAgent is server-read but
	// caller-set — the caller writes its own User-Agent header — so it sits in
	// the weaker class with Automation, which carries the page's own
	// navigator.webdriver reading.
	//
	// It is store-written inside the same compare-and-set that writes AnsweredAt
	// and AnsweredBy, so a rejected transition records nothing: set on the
	// `open` -> `answered` transition and on an arm-caused `open` -> `closed`.
	// Absent on every item answered before this field existed, and its pointer
	// with omitempty is what keeps such an item reading exactly as it did.
	AnsweredClient *AnsweredClient `json:"answered_client,omitempty"`
	// EscalatedBy is which session escalated this item to the operator — a
	// session id, never a pane id and never a boolean. A pane id is recycled
	// across tab moves and WezTerm restarts, so a stale one returns another
	// session's pane rather than failing; a boolean cannot answer "is it me",
	// which the self-stamp rule requires. Absent until a manager escalates.
	//
	// The store rejects a value that is not a well-formed session id rather than
	// normalizing it: a placeholder has no UUID to normalize to, so "repair"
	// could only mean inventing an identity. See the schema's § Escalation.
	EscalatedBy string `json:"escalated_by,omitempty"`
	// EscalatedAt is when the item was escalated to the operator, stamped by the
	// store from its own clock inside the same compare-and-set that writes
	// EscalatedBy — never supplied by the caller, so the two are always present
	// together. Its one reader is the latency measure: the operator rung's
	// time-to-answer is AnsweredAt minus EscalatedAt.
	//
	// This comment previously said the opposite — that there was deliberately no
	// EscalatedAt companion because nothing would read it. That held while the
	// field had no reader; the latency measure is one. See the schema page's
	// silence 13, resolved 2026-09-25.
	EscalatedAt *libtime.DateTime `json:"escalated_at,omitempty"`
	// ResolvedBy is which session resolved this item — a session id, never a
	// pane id and never a boolean, supplied by the caller of the answer path
	// exactly as AnsweredBy is. It is distinct from AnsweredBy, which names the
	// arm that supplied the answer rather than the identity that gave it, and
	// from EscalatedBy, which names the session that carried the item to the
	// operator.
	//
	// It is a declaration, not a proof. The store records the value the caller
	// sent and does not authenticate it or derive it from the request, so two
	// callers claiming one session id are indistinguishable here — the same
	// trust model InterruptClass and ProvenanceClass already carry. Empty means
	// absent, which is what an item resolved before this field existed reads as.
	ResolvedBy string `json:"resolved_by,omitempty"`
	// Decision is what the answer decided — allow or deny. It is distinct from
	// AnsweredBy, which names the arm that supplied the answer: an arm is not a
	// decision, so before this field an item answered allow and one answered
	// deny were indistinguishable on the record.
	//
	// It is a caller's declaration, exactly as AnsweredBy and ResolvedBy are,
	// and it is optional. Empty means no verdict was recorded, which is what a
	// message- or ack-class item reads as and what every item answered before
	// this field existed reads as.
	Decision Decision `json:"decision,omitempty"`
	// Answer is what the operator actually said on a `message` item. Written by
	// the store on the open -> answered transition, exactly as AnsweredAt,
	// AnsweredBy and Decision are.
	//
	// It is a pointer so an absent answer is absent on the wire rather than an
	// empty object: a struct field carrying omitempty still serialises, which
	// would make every unanswered item look like one holding a blank answer.
	Answer *Answer `json:"answer,omitempty"`
	// Answers is what the operator said on each question of a multi-question
	// item — an ordered list of {question, kind, value}, where question is the
	// Tab of the Questions entry it answers.
	//
	// It is distinct from Answer rather than a replacement for it, so a
	// single-question item's wire shape is unchanged from the one already
	// shipped. The two are mutually exclusive: an item carrying both would have
	// two places the operator's content could be and no rule for which wins,
	// which validateAnswers rejects rather than leaving to convention.
	Answers Answers `json:"answers,omitempty"`
	// ClosedAt is when the item left the queue. Absent while open or answered.
	ClosedAt *libtime.DateTime `json:"closed_at,omitempty"`
	// ExpiresAt is the producer's own deadline, if it has one. Absent means the
	// item does not expire on a timer.
	ExpiresAt *libtime.DateTime `json:"expires_at,omitempty"`
}

// Validate returns an error when the item violates the schema's field rules.
func (i Item) Validate(ctx context.Context) error {
	return validation.All{
		validation.Name("ItemID", validation.NotEmptyString(i.ItemID)),
		validation.Name("ProducerID", validation.NotEmptyString(i.ProducerID)),
		validation.Name("ProducerKind", i.ProducerKind),
		validation.Name("ProvenanceClass", i.ProvenanceClass),
		validation.Name("LivenessRef", i.LivenessRef),
		validation.Name("DedupKey", validation.NotEmptyString(i.DedupKey)),
		validation.Name("InterruptClass", validation.NotEmptyString(i.InterruptClass)),
		validation.Name("Payload", validation.NotEmptyString(i.Payload)),
		validation.Name("Options", validation.HasValidationFunc(i.validateOptions)),
		validation.Name(
			"AnswerCardinality",
			validation.HasValidationFunc(i.validateAnswerCardinality),
		),
		validation.Name("Questions", validation.HasValidationFunc(i.validateQuestions)),
		validation.Name("AnswerMechanism", i.AnswerMechanism),
		validation.Name("Decision", i.Decision),
		validation.Name("Answer", validation.HasValidationFunc(i.validateAnswer)),
		validation.Name("Answers", validation.HasValidationFunc(i.validateAnswers)),
		validation.Name("State", i.State),
		validation.Name("CreatedAt", i.CreatedAt),
	}.Validate(ctx)
}

// validateOptions enforces the schema's message-only rule for `options`. The
// field is absent on `permission` and `ack` alike, because neither class offers
// the operator a choice to make.
//
// An empty list is legal: `options` is optional, and absent is what every
// `permission`- and `ack`-class item carries.
func (i Item) validateOptions(ctx context.Context) error {
	if len(i.Options) == 0 {
		return nil
	}
	if i.AnswerMechanism != MessageAnswerMechanism {
		return errors.Wrapf(
			ctx,
			validation.Error,
			"options are only allowed on a message item, got answerMechanism '%s'",
			i.AnswerMechanism,
		)
	}
	return i.Options.Validate(ctx)
}

// validateAnswerCardinality enforces the schema's message-only rule for
// `answer_cardinality`, for the same reason validateOptions enforces it for
// `options`: neither a `permission` nor an `ack` item offers the operator a
// choice whose shape could be declared.
func (i Item) validateAnswerCardinality(ctx context.Context) error {
	if i.AnswerCardinality == "" {
		return nil
	}
	if i.AnswerMechanism != MessageAnswerMechanism {
		return errors.Wrapf(
			ctx,
			validation.Error,
			"answerCardinality is only allowed on a message item, got answerMechanism '%s'",
			i.AnswerMechanism,
		)
	}
	return i.AnswerCardinality.Validate(ctx)
}

// validateQuestions enforces the message-only rule for `questions` and
// validates the units when present.
func (i Item) validateQuestions(ctx context.Context) error {
	if len(i.Questions) == 0 {
		return nil
	}
	if i.AnswerMechanism != MessageAnswerMechanism {
		return errors.Wrapf(
			ctx,
			validation.Error,
			"questions are only allowed on a message item, got answerMechanism '%s'",
			i.AnswerMechanism,
		)
	}
	return i.Questions.Validate(ctx)
}

// validateAnswers validates the per-question answers when present, and rejects
// an item carrying both Answer and Answers.
//
// The mutual exclusion is the schema's: the two fields are two places the
// operator's content could be, and an item holding both would leave a reader no
// rule for which is authoritative. An absent value stays legal, so an item
// answered before this field existed reads as it did.
func (i Item) validateAnswers(ctx context.Context) error {
	if len(i.Answers) == 0 {
		return nil
	}
	if i.Answer != nil {
		return errors.Wrap(
			ctx,
			validation.Error,
			"answer and answers are mutually exclusive, got both",
		)
	}
	if err := i.Answers.Validate(ctx); err != nil {
		return err
	}
	return i.validateAnswersMatchQuestions(ctx)
}

// validateAnswersMatchQuestions returns an error when an answer names a tab no
// question on this item carries, or carries its content in the field its
// question's declared cardinality does not name.
//
// The membership rule is what makes an answer routable: an answer to a question
// the item does not ask would leave the producer reading back a value for a tab
// that is not on the card, with nothing saying which question it was meant for.
//
// The carrier rule is what keeps a multi-pick answer reversible. The schema
// fixes `value` for a `single` question and `values` for a `multiple` one, and
// an entry using the other field would be indistinguishable on read-back from
// the shape it is not — a two-pick answer and a one-pick answer whose label
// happens to contain ", " would be the same string.
//
// This is the only place the pairing can be checked, because it is the only
// place both sides are in hand: QuestionAnswer does not hold its question's
// cardinality.
func (i Item) validateAnswersMatchQuestions(ctx context.Context) error {
	cardinalityOf := make(map[string]AnswerCardinality, len(i.Questions))
	for _, question := range i.Questions {
		cardinalityOf[question.Tab] = question.Cardinality
	}
	for _, answer := range i.Answers {
		cardinality, ok := cardinalityOf[answer.Question]
		if !ok {
			return errors.Wrapf(
				ctx,
				validation.Error,
				"answer names question '%s', which no question on this item carries",
				answer.Question,
			)
		}
		if err := validateAnswerCarrier(ctx, answer.Answer, answer.Question, cardinality); err != nil {
			return err
		}
	}
	return nil
}

// validateAnswerCarrier returns an error when an answer carries its content in
// the field the question's cardinality does not name.
//
// A `skip` carries neither field, and a `text` answer carries `value` whatever
// the cardinality: the operator's own words are one string either way, and
// there is no set of labels to list.
func validateAnswerCarrier(
	ctx context.Context,
	answer Answer,
	question string,
	cardinality AnswerCardinality,
) error {
	if answer.Kind != OptionAnswerKind {
		return nil
	}
	if cardinality == MultipleAnswerCardinality {
		if len(answer.Values) == 0 {
			return errors.Wrapf(
				ctx,
				validation.Error,
				"%s takes several picks, so its answer carries values, not value",
				questionLabel(question),
			)
		}
		return nil
	}
	if answer.Value == "" {
		return errors.Wrapf(
			ctx,
			validation.Error,
			"%s takes one pick, so its answer carries value, not values",
			questionLabel(question),
		)
	}
	return nil
}

// questionLabel names the question a carrier error is about, so one rule reads
// correctly on both paths it serves: the per-tab path, which names a tab, and
// the single-question path, which has no tab to name.
func questionLabel(tab string) string {
	if tab == "" {
		return "the item's question"
	}
	return "question '" + tab + "'"
}

// validateAnswer validates the answer content when one was recorded, and treats
// an absent answer as legal.
//
// The schema declines to reject an omitted answer, so a nil Answer reads as a
// pre-change item or as a `permission`- or `ack`-class item rather than as an
// error.
func (i Item) validateAnswer(ctx context.Context) error {
	if i.Answer == nil {
		return nil
	}
	// An item carrying questions is answered through `answers`, one entry per
	// tab. Accepting the single field here would store an answer with no tab
	// attached, so the producer reading `answers` back would find nothing for any
	// question and the routing `questions` exist to provide would be gone. The
	// mutual exclusion is therefore enforced in both directions, not only when
	// both fields are present.
	if len(i.Questions) > 0 {
		return errors.Wrap(
			ctx,
			validation.Error,
			"an item carrying questions is answered through answers, not answer",
		)
	}
	if err := i.Answer.Validate(ctx); err != nil {
		return errors.Wrap(ctx, err, "validate answer failed")
	}
	// The single-question carrier is the item's own AnswerCardinality, so the
	// same pairing the per-tab path enforces applies here. Without it a
	// single-question item declared `multiple` could record one label in `value`
	// and silently drop the rest, and a `single` item could record a set where a
	// value was asked for.
	if err := validateAnswerCarrier(ctx, *i.Answer, "", i.AnswerCardinality); err != nil {
		return errors.Wrap(ctx, err, "validate answer failed")
	}
	return nil
}
