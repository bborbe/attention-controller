// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

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

	// Answer applies open -> answered as an atomic compare-and-set. Exactly one
	// of two concurrent answers transitions the item; the loser receives
	// ErrAlreadyAnswered and must read back and report rather than retry.
	Answer(ctx context.Context, itemID ItemID, answeredBy string) (*Item, error)

	// Close applies open -> closed or answered -> closed. Closing an item that
	// is already closed is rejected with ErrIllegalTransition.
	Close(ctx context.Context, itemID ItemID) (*Item, error)
}

// Items is a collection of Item.
type Items []Item

// PushRequest carries a producer's declaration. The store owns ItemID, State,
// CreatedAt and the answer fields; the producer owns everything else.
type PushRequest struct {
	ProducerID      ProducerID        `json:"producer_id"`
	ProducerKind    ProducerKind      `json:"producer_kind"`
	LivenessRef     LivenessRef       `json:"liveness_ref"`
	DedupKey        DedupKey          `json:"dedup_key"`
	InterruptClass  InterruptClass    `json:"interrupt_class"`
	Payload         Payload           `json:"payload"`
	AnswerMechanism AnswerMechanism   `json:"answer_mechanism"`
	ExpiresAt       *libtime.DateTime `json:"expires_at,omitempty"`
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
		validation.Name("LivenessRef", p.LivenessRef),
		validation.Name("DedupKey", validation.NotEmptyString(p.DedupKey)),
		validation.Name("InterruptClass", validation.NotEmptyString(p.InterruptClass)),
		validation.Name("Payload", validation.NotEmptyString(p.Payload)),
		validation.Name("AnswerMechanism", p.AnswerMechanism),
	}.Validate(ctx)
}

// AttentionStoreBucketName is the bucket every item lives in.
var AttentionStoreBucketName = libkv.NewBucketName("attention-items")
