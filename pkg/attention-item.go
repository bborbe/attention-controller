// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

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
	// AnswerMechanism is how this item is answered.
	AnswerMechanism AnswerMechanism `json:"answer_mechanism"`
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
		validation.Name("AnswerMechanism", i.AnswerMechanism),
		validation.Name("State", i.State),
		validation.Name("CreatedAt", i.CreatedAt),
	}.Validate(ctx)
}
