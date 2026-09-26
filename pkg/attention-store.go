// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	"github.com/bborbe/validation"
)

//counterfeiter:generate -o ../mocks/attention-store.go --fake-name AttentionStore . AttentionStore

// AttentionStore is the durable home for attention items. It implements the
// attention item schema's lifecycle and redefines nothing in it.
//
// It never ranks: InterruptClass is stored and read back unchanged, and
// CreatedAt is a timestamp rather than an ordering.
type AttentionStore interface {
	// Push stores a producer's claim that a human is needed and returns the
	// stored item. A push whose DedupKey matches an item already open for the
	// same live ProducerID updates that item rather than adding a row; a push
	// from a producer whose liveness has already failed creates a new row
	// rather than reviving the dead one.
	Push(ctx context.Context, request PushRequest) (*Item, error)

	// Get returns a single item by id.
	Get(ctx context.Context, itemID ItemID) (*Item, error)

	// Read returns the items an arm should render: the open items whose
	// producer is still live, or whose producer reported rather than asked.
	// Dead askers are removed from the store as a side effect of the read, so
	// the window between producer-exit and removal is one read at most.
	Read(ctx context.Context) (Items, error)

	// History returns every item regardless of state, for counting what
	// resolved and what escalated rather than for rendering.
	//
	// It is deliberately not Read: Read answers "what should an arm show now"
	// and prunes dead askers as a side effect, which makes it blind to anything
	// that has left the queue — and a pruning read cannot report a history,
	// because the act of reading would delete part of what it reports. History
	// never prunes, never filters on liveness and never filters on state.
	History(ctx context.Context) (Items, error)

	// Answer applies open -> answered as an atomic compare-and-set. Exactly one
	// of two concurrent answers transitions the item; the loser receives
	// ErrAlreadyAnswered and must read back and report rather than retry.
	//
	// answeredBy names the *arm* that supplied the answer and resolvedBy names
	// the *session* that resolved it. They are separate fields because they
	// answer separate questions: the arm is the same value whether a manager or
	// the operator used it, so the arm alone cannot say who settled the item.
	// resolvedBy may be empty — the schema does not reject an omitted value, and
	// an item answered before the field existed reads back without one.
	//
	// decision is what the answer decided, and it is separate from answeredBy
	// for the same reason: an arm supplies an allow and a deny alike, so the arm
	// cannot say what was decided. It may be empty — the schema adds no
	// write-time rejection, and an empty value stores an item with no verdict
	// recorded, which is what a message- or ack-class item reads as.
	//
	// answer is what the operator actually said, and it is separate from both
	// for the same reason again: decision carries a `permission` item's verdict
	// and answer carries a `message` item's content, and a chosen label is not a
	// verdict. It may be nil — the schema adds no write-time rejection for an
	// omitted answer either, so a nil value stores an item with no answer
	// content recorded.
	//
	// answers is the same content for an item carrying `questions`: one entry
	// per tab. It may be empty, and it is mutually exclusive with answer — an
	// item holding both would have two places the operator's content could be
	// and no rule for which wins, so a call carrying both is rejected rather
	// than resolved by convention.
	//
	// answeredClient is what the store can say about the client that posted the
	// answer, composed by the caller from the HTTP request and the page's own
	// automation hint. It is separate from answeredBy for the reason the field
	// exists: answeredBy is a caller declaration, so a human's click and a
	// scripted client posting the same body write the identical value there.
	// Unlike every other argument above it is not a declaration the caller is
	// trusted for — the two server-derived members come from the request itself.
	// It may be nil, and a nil value stores an item with no client recorded,
	// which is what every item answered before this field existed reads as.
	Answer(
		ctx context.Context,
		itemID ItemID,
		answeredBy string,
		resolvedBy string,
		decision Decision,
		answer *Answer,
		answers Answers,
		answeredClient *AnsweredClient,
	) (*Item, error)

	// Escalate records which session is carrying this item to the operator, as
	// an atomic compare-and-set. Exactly one of two concurrent escalations
	// stamps the item; the loser receives ErrAlreadyEscalated and must read
	// back EscalatedBy and report who holds it rather than stamping over it.
	//
	// Escalation is NOT a transition. The item stays in whatever state it was
	// in and no row of the schema's transitions table is involved — escalating
	// changes who is being asked, not where the item is in its lifecycle. An
	// implementation that models it as a state redefines the schema.
	//
	// Escalating an item that is not open is rejected with ErrItemNotOpen: the
	// item has left the queue, so nothing would render the stamp.
	//
	// Re-escalating by the session that already stamped the item succeeds and
	// returns the item unchanged. A manager re-running its own sweep must never
	// be blocked by its own stamp — the comparison is against the caller's
	// identity, not merely against the field's presence.
	Escalate(ctx context.Context, itemID ItemID, escalatedBy string) (*Item, error)

	// Close applies open -> closed or answered -> closed. Closing an item that
	// is already closed is rejected with ErrIllegalTransition.
	//
	// answeredBy names the arm that caused the close, and it is recorded **only
	// on the open -> closed row** — the acknowledgement of an `ack` item, which
	// the schema routes to `answered_by` with `answered_at` left unset because
	// nothing is routed back. It may be empty: the row's other causers (the
	// producer withdrawing its own item, the store on a producer-exit sweep) are
	// not arms, and an empty value stores a close with no arm recorded, which is
	// what every item closed before this parameter existed reads as.
	//
	// On the answered -> closed row the value is deliberately NOT written: that
	// row is the store's own step after an answer, and overwriting answeredBy
	// there would replace the arm that *answered* with the one that closed.
	//
	// answeredClient is what the store can say about the client that caused the
	// close, composed by the caller from the HTTP request and the page's own
	// automation hint. It rides the same rule as answeredBy rather than a rule of
	// its own: it is written on the arm-caused `open` -> `closed` row and left
	// alone on `answered` -> `closed`, where it already names the client that
	// answered. A close no arm caused — a producer withdrawing its own item —
	// records none, because the schema sets the field on an arm-caused row only.
	// It may be nil, exactly as answeredBy may be empty.
	Close(
		ctx context.Context,
		itemID ItemID,
		answeredBy string,
		answeredClient *AnsweredClient,
	) (*Item, error)
}

// Items is a collection of Item.
type Items []Item

// PushRequest carries a producer's declaration. The store owns ItemID, State,
// CreatedAt and the answer fields; the producer owns everything else.
type PushRequest struct {
	ProducerID        ProducerID        `json:"producer_id"`
	ProducerKind      ProducerKind      `json:"producer_kind"`
	ProvenanceClass   ProvenanceClass   `json:"provenance_class,omitempty"`
	LivenessRef       LivenessRef       `json:"liveness_ref"`
	DedupKey          DedupKey          `json:"dedup_key"`
	InterruptClass    InterruptClass    `json:"interrupt_class"`
	Payload           Payload           `json:"payload"`
	Context           ItemContext       `json:"context,omitempty"`
	AnswerMechanism   AnswerMechanism   `json:"answer_mechanism"`
	Options           AnswerOptions     `json:"options,omitempty"`
	AnswerCardinality AnswerCardinality `json:"answer_cardinality,omitempty"`
	Questions         Questions         `json:"questions,omitempty"`
	ExpiresAt         *libtime.DateTime `json:"expires_at,omitempty"`
}

// Validate returns an error when the declaration violates the schema's rules
// for the fields a producer owns.
//
// It is deliberately NOT Item.Validate. Three of the schema's fields —
// ItemID, State and CreatedAt — are written by the store, so a producer's
// request cannot carry them and must not be judged against them. Reusing
// Item.Validate here would reject every push for an empty ItemID, which is
// exactly what it did when this was first run end to end.
func (p PushRequest) Validate(ctx context.Context) error {
	return validation.All{
		validation.Name("ProducerID", validation.NotEmptyString(p.ProducerID)),
		validation.Name("ProducerKind", p.ProducerKind),
		validation.Name("ProvenanceClass", p.ProvenanceClass),
		validation.Name("LivenessRef", p.LivenessRef),
		validation.Name("DedupKey", validation.NotEmptyString(p.DedupKey)),
		validation.Name("InterruptClass", validation.NotEmptyString(p.InterruptClass)),
		validation.Name("Payload", validation.NotEmptyString(p.Payload)),
		validation.Name("Options", validation.HasValidationFunc(p.validateOptions)),
		validation.Name(
			"AnswerCardinality",
			validation.HasValidationFunc(p.validateAnswerCardinality),
		),
		validation.Name("Questions", validation.HasValidationFunc(p.validateQuestions)),
		validation.Name("AnswerMechanism", p.AnswerMechanism),
	}.Validate(ctx)
}

// validateOptions enforces the schema's message-only rule for `options`, at push
// time rather than only at store time, so a producer learns its declaration is
// rejected from the push response rather than from a stored item it cannot read
// back.
//
// The field is absent on `permission` and `ack` alike, because neither class
// offers the operator a choice to make. An empty list is legal — `options` is
// optional.
func (p PushRequest) validateOptions(ctx context.Context) error {
	if len(p.Options) == 0 {
		return nil
	}
	if p.AnswerMechanism != MessageAnswerMechanism {
		return errors.Wrapf(
			ctx,
			validation.Error,
			"options are only allowed on a message item, got answerMechanism '%s'",
			p.AnswerMechanism,
		)
	}
	return p.Options.Validate(ctx)
}

// validateAnswerCardinality enforces the message-only rule for
// `answer_cardinality` at push time rather than only at store time, so a
// producer learns its declaration is rejected from the push response rather
// than from a stored item it cannot read back.
//
// An empty value is legal — the schema declines to reject an omitted
// cardinality, and an absent value reads as `single`.
func (p PushRequest) validateAnswerCardinality(ctx context.Context) error {
	if p.AnswerCardinality == "" {
		return nil
	}
	if p.AnswerMechanism != MessageAnswerMechanism {
		return errors.Wrapf(
			ctx,
			validation.Error,
			"answerCardinality is only allowed on a message item, got answerMechanism '%s'",
			p.AnswerMechanism,
		)
	}
	return p.AnswerCardinality.Validate(ctx)
}

// validateQuestions enforces the message-only rule for `questions` at push
// time, and validates the units when present. An empty list is legal —
// `questions` is optional, and an absent value is what every single-question
// item carries.
func (p PushRequest) validateQuestions(ctx context.Context) error {
	if len(p.Questions) == 0 {
		return nil
	}
	if p.AnswerMechanism != MessageAnswerMechanism {
		return errors.Wrapf(
			ctx,
			validation.Error,
			"questions are only allowed on a message item, got answerMechanism '%s'",
			p.AnswerMechanism,
		)
	}
	return p.Questions.Validate(ctx)
}

// AttentionStoreBucketName is the bucket every item lives in.
var AttentionStoreBucketName = libkv.NewBucketName("attention-items")
