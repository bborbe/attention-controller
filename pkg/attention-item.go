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
	// State is where the item is in its lifecycle. Written by the store.
	State State `json:"state"`
	// CreatedAt is when the item entered the stack. Written by the store.
	CreatedAt libtime.DateTime `json:"created_at"`

	// AnsweredAt is when an answer arrived. Absent while unanswered.
	AnsweredAt *libtime.DateTime `json:"answered_at,omitempty"`
	// AnsweredBy is which arm supplied the answer. Recorded so the
	// one-item-many-arms property is auditable rather than merely asserted.
	AnsweredBy string `json:"answered_by,omitempty"`
	// EscalatedBy is which session escalated this item to the operator — a
	// session id, never a pane id and never a boolean. A pane id is recycled
	// across tab moves and WezTerm restarts, so a stale one returns another
	// session's pane rather than failing; a boolean cannot answer "is it me",
	// which the self-stamp rule requires. Absent until a manager escalates.
	//
	// There is deliberately no EscalatedAt companion: the schema declares no
	// such field, and with no TTL and no clearing sweep nothing would read it.
	// Adding one would be a gap filled silently in code rather than a finding
	// reported on the schema page.
	EscalatedBy string `json:"escalated_by,omitempty"`
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
		validation.Name("AnswerMechanism", i.AnswerMechanism),
		validation.Name("Decision", i.Decision),
		validation.Name("Answer", validation.HasValidationFunc(i.validateAnswer)),
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
	return i.Answer.Validate(ctx)
}
