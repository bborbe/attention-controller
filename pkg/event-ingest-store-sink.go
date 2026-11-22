// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

// eventIngestPayloadLimit bounds the detail forwarded as the item's payload.
// The hook's detail carries prose, and the board renders it as one block; a
// multi-kilobyte detail is a display defect rather than more information.
const eventIngestPayloadLimit = 2000

// eventIngestInterruptClass is the interrupt class each event kind declares.
// The values match what the Python watcher sent, so a card's history stays
// continuous across the port rather than showing a class change at the cutover.
var eventIngestInterruptClass = map[string]InterruptClass{
	"permission": "approve",
	"question":   "pick",
}

// eventIngestAnswerMechanism is how the board collects the answer for a kind.
// `permission` is a verdict the board posts back; `question` is content the
// operator types, which is what the store's `message` mechanism means.
var eventIngestAnswerMechanism = map[string]AnswerMechanism{
	"permission": "permission",
	"question":   "message",
}

// EventIngestProducerKindResolver reports whether a session is a worker or a
// manager.
//
// ⚠️ It is a seam because the answer needs the manager registry, which the
// sink has no business reading: "a manager asked" and "a worker asked" are
// different populations on the board, and a sink that guessed `session` for
// every gate would file every manager gate under the wrong kind — the exact
// failure the Python's `_producer_kind_for` exists to prevent.
type EventIngestProducerKindResolver func(ctx context.Context, sessionID string) ProducerKind

// eventIngestStore is the slice of the store this sink needs.
//
// ⚠️ Close is on the concrete store but not on AttentionStore, so this is a
// narrow local interface rather than the full one. The alternative — widening
// AttentionStore for one caller — would regenerate every mock that fakes it.
type eventIngestStore interface {
	Push(ctx context.Context, request PushRequest) (*Item, error)
	ReadBoard(ctx context.Context) (Items, error)
	Close(
		ctx context.Context,
		itemID ItemID,
		answeredBy string,
		answeredClient *AnsweredClient,
	) (*Item, error)
}

// StoreEventSink writes the ingest's records into the attention store.
//
// It is the mapping half of the port: the ingest job owns the tailing rules,
// this owns the schema. Both are tested separately because the tailing rules
// must not change and the schema mapping changes most often.
type StoreEventSink struct {
	store       eventIngestStore
	resolveKind EventIngestProducerKindResolver
}

// NewStoreEventSink creates the sink over the store.
//
// A nil resolver files every gate as a session, which is the correct default
// for a deployment with no manager registry: it under-claims rather than
// guessing a kind the session may not have.
func NewStoreEventSink(
	store eventIngestStore,
	resolveKind EventIngestProducerKindResolver,
) EventIngestSink {
	if resolveKind == nil {
		resolveKind = func(context.Context, string) ProducerKind {
			return SessionProducerKind
		}
	}
	return &StoreEventSink{store: store, resolveKind: resolveKind}
}

// Open forwards one open record as a push.
func (s *StoreEventSink) Open(ctx context.Context, record EventIngestRecord) error {
	request := s.pushRequest(ctx, record)
	if _, err := s.store.Push(ctx, request); err != nil {
		return errors.Wrapf(ctx, err, "push %s failed", record.ItemID)
	}
	glog.V(2).Infof(
		"job=attention-watcher action=ingest item=%s kind=%s",
		record.ItemID,
		record.Kind,
	)
	return nil
}

// Close closes the open item the record's dedup key names.
//
// ⚠️ The store id is resolved by reading the board and matching on the dedup
// key, not by a book this sink keeps. The Python watcher needed a book because
// it held local state; the store is the book, and a second copy of it is a
// second answer to "which item is this event" — the divergence a book makes
// possible. A close with no matching open item is a no-op: the item may already
// have been closed by its producer, and that is not an error.
func (s *StoreEventSink) Close(ctx context.Context, record EventIngestRecord) error {
	items, err := s.store.ReadBoard(ctx)
	if err != nil {
		return errors.Wrap(ctx, err, "read board failed")
	}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return nil
		}
		if item.DedupKey.String() != record.ItemID {
			continue
		}
		// ⚠️ A HELD card — open with answered_at set — is an answer the delivery
		// arm has not carried yet. This event close names no arm, so it is the
		// producer-side clear and must not retire the card: the hold owns that
		// transition, and a close here abandons the answer while the asking
		// session is still parked. The store refuses the same write; skipping
		// here keeps the refusal out of the job's error log, where it would read
		// as a store fault rather than a deliberate no-op.
		if heldCard(item) {
			glog.V(3).Infof(
				"job=attention-watcher action=close_skip_held item=%s (answer undelivered)",
				item.ItemID,
			)
			return nil
		}
		if _, err := s.store.Close(ctx, item.ItemID, "", nil); err != nil {
			return errors.Wrapf(ctx, err, "close %s failed", item.ItemID)
		}
		glog.V(2).Infof(
			"job=attention-watcher action=close item=%s",
			item.ItemID,
		)
		return nil
	}
	glog.V(3).Infof(
		"job=attention-watcher action=close_skip item=%s (no open item)",
		record.ItemID,
	)
	return nil
}

// pushRequest maps one record onto the store's schema.
func (s *StoreEventSink) pushRequest(
	ctx context.Context,
	record EventIngestRecord,
) PushRequest {
	request := PushRequest{
		ProducerID:      ProducerID(record.SessionID),
		ProducerKind:    s.resolveKind(ctx, record.SessionID),
		ProvenanceClass: "hook",
		// The store's own liveness model, not the Python's heartbeat file: the
		// Go store probes the session registry, so `session:<id>` is the ref it
		// can actually resolve.
		LivenessRef:     LivenessRef("session:" + record.SessionID),
		DedupKey:        DedupKey(record.ItemID),
		InterruptClass:  eventIngestInterruptClass[record.Kind],
		Payload:         Payload(truncateRunes(record.Detail, eventIngestPayloadLimit)),
		AnswerMechanism: eventIngestAnswerMechanism[record.Kind],
	}
	if record.TS != nil {
		// The gate's own ask time, forwarded from the hook. Omitted when the
		// line carries no usable ts — defaulting it to the store's created_at
		// would report a zero forward lag and hide the very gap it measures.
		askedAt := libtime.DateTime(time.Unix(int64(*record.TS), 0))
		request.AskedAt = &askedAt
	}
	s.applyCarriedFields(record, &request)
	return request
}

// applyCarriedFields forwards the fields the hook built and this port must not
// rebuild: a rebuilt copy can disagree with the hook's, and a disagreement is a
// 400 that drops the whole card.
func (s *StoreEventSink) applyCarriedFields(
	record EventIngestRecord,
	request *PushRequest,
) {
	var carried struct {
		Context           *string         `json:"context"`
		AnswerCardinality *string         `json:"answer_cardinality"`
		Options           json.RawMessage `json:"options"`
		Questions         json.RawMessage `json:"questions"`
	}
	if len(record.Raw) == 0 {
		return
	}
	if err := json.Unmarshal(record.Raw, &carried); err != nil {
		return
	}
	// Omitted rather than sent empty: an absent context reads as a pre-change
	// item, while "" is a present field holding nothing.
	if carried.Context != nil && *carried.Context != "" {
		request.Context = ItemContext(*carried.Context)
	}
	if carried.AnswerCardinality != nil && *carried.AnswerCardinality != "" {
		request.AnswerCardinality = AnswerCardinality(*carried.AnswerCardinality)
	}
	if len(carried.Options) > 0 {
		var options AnswerOptions
		if err := json.Unmarshal(carried.Options, &options); err == nil {
			request.Options = options
		}
	}
	if len(carried.Questions) > 0 {
		var questions Questions
		if err := json.Unmarshal(carried.Questions, &questions); err == nil {
			request.Questions = questions
		}
	}
}

// truncateRunes bounds a string to limit runes, leaving multi-byte text intact.
func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// Compile-time guard: the sink must satisfy the ingest's own interface.
var _ EventIngestSink = (*StoreEventSink)(nil)
