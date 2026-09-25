// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"

	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

// NewAttentionStore creates an AttentionStore backed by a libkv database.
//
// heartbeatWindow is how stale a `heartbeat:<path>` mtime may be before the
// store treats the producer as finished. ⚠️ The schema requires the check but
// declares no field carrying the window — it is a recorded silence, and this
// parameter is the store's own resolution of it rather than a schema value.
func NewAttentionStore(
	db libkv.DB,
	itemIDGenerator ItemIDGenerator,
	sessionLivenessChecker SessionLivenessChecker,
	currentDateTimeGetter libtime.CurrentDateTimeGetter,
	heartbeatWindow libtime.Duration,
) AttentionStore {
	return &attentionStore{
		store:                  libkv.NewStoreTx[string, Item](AttentionStoreBucketName),
		db:                     db,
		itemIDGenerator:        itemIDGenerator,
		sessionLivenessChecker: sessionLivenessChecker,
		currentDateTimeGetter:  currentDateTimeGetter,
		heartbeatWindow:        heartbeatWindow,
	}
}

type attentionStore struct {
	store                  libkv.StoreTx[string, Item]
	db                     libkv.DB
	itemIDGenerator        ItemIDGenerator
	sessionLivenessChecker SessionLivenessChecker
	currentDateTimeGetter  libtime.CurrentDateTimeGetter
	heartbeatWindow        libtime.Duration
}

func (a *attentionStore) Push(ctx context.Context, request PushRequest) (*Item, error) {
	var result *Item
	err := a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		updated, err := a.updateExistingIfLive(ctx, tx, request)
		if err != nil {
			return err
		}
		if updated != nil {
			result = updated
			return nil
		}
		item, err := a.newItem(ctx, request)
		if err != nil {
			return err
		}
		if err := a.store.Add(ctx, tx, item.ItemID.String(), *item); err != nil {
			return errors.Wrap(ctx, err, "add item failed")
		}
		result = item
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "push failed")
	}
	return result, nil
}

// newItem builds a fresh item in the open state. The store owns ItemID, State,
// CreatedAt and the answer fields; the producer owns everything else.
func (a *attentionStore) newItem(ctx context.Context, request PushRequest) (*Item, error) {
	itemID, err := a.itemIDGenerator.Generate(ctx)
	if err != nil {
		return nil, errors.Wrap(ctx, err, "generate item id failed")
	}
	item := &Item{
		ItemID:          itemID,
		ProducerID:      request.ProducerID,
		ProducerKind:    request.ProducerKind,
		ProvenanceClass: request.ProvenanceClass,
		LivenessRef:     request.LivenessRef,
		DedupKey:        request.DedupKey,
		InterruptClass:  request.InterruptClass,
		Payload:         request.Payload,
		Context:         request.Context,
		AnswerMechanism: request.AnswerMechanism,
		Options:         request.Options,
		State:           OpenState,
		CreatedAt:       a.currentDateTimeGetter.Now(),
		ExpiresAt:       request.ExpiresAt,
	}
	if err := item.Validate(ctx); err != nil {
		return nil, errors.Wrap(ctx, err, "validate item failed")
	}
	return item, nil
}

func (a *attentionStore) Get(ctx context.Context, itemID ItemID) (*Item, error) {
	var result *Item
	err := a.db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		item, err := a.store.Get(ctx, tx, itemID.String())
		if err != nil {
			if isNotFound(err) {
				return errors.Wrapf(ctx, ErrItemNotFound, "item %s not found", itemID)
			}
			return errors.Wrap(ctx, err, "get item failed")
		}
		result = item
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "get failed")
	}
	return result, nil
}

// Read returns the open items an arm should render, and removes dead askers as
// a side effect so the window between producer-exit and removal is one read at
// most.
//
// An item is removed only when all three hold: its state is open, its producer
// is not live, and it is a question the producer asked rather than a report it
// made. An `answered` or `closed` item is never pruned — it is no longer
// rendered anyway, and removing it would rewrite history the schema says is
// never rewritten.
func (a *attentionStore) Read(ctx context.Context) (Items, error) {
	items := make(Items, 0)
	dead := make([]string, 0)
	err := a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		err := a.store.Map(ctx, tx, func(ctx context.Context, key string, item Item) error {
			if item.State != OpenState {
				return nil
			}
			live, err := a.isProducerLive(ctx, &item)
			if err != nil {
				return errors.Wrap(ctx, err, "check producer liveness failed")
			}
			if live {
				items = append(items, item)
				return nil
			}
			if isAsked(&item) {
				glog.V(2).
					Infof("removing item %s: producer %s asked and is gone", item.ItemID, item.ProducerID)
				dead = append(dead, key)
				return nil
			}
			// The producer reported a condition rather than asking a question.
			// The operator can still act on it, so it stays open.
			items = append(items, item)
			return nil
		})
		if err != nil {
			return errors.Wrap(ctx, err, "map items failed")
		}
		for _, key := range dead {
			if err := a.store.Remove(ctx, tx, key); err != nil {
				return errors.Wrapf(ctx, err, "remove dead item %s failed", key)
			}
		}
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "read failed")
	}
	return items, nil
}

// History returns every item regardless of state.
//
// It is deliberately not Read. Read answers "what should an arm render now": it
// filters to open items and removes dead askers as a side effect, which makes
// it blind to everything that has left the queue — and it cannot report a
// history at all, because the act of reading would delete part of what it
// reports. History filters on nothing and prunes nothing. An answered or closed
// item is exactly what a caller counting resolutions needs, and removing it
// would rewrite the history the schema says is never rewritten.
//
// A read transaction, not a write one: this path mutates nothing, so it takes
// no writer lock and cannot interleave with the pruning Read does.
func (a *attentionStore) History(ctx context.Context) (Items, error) {
	items := make(Items, 0)
	err := a.db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		return a.store.Map(ctx, tx, func(ctx context.Context, key string, item Item) error {
			items = append(items, item)
			return nil
		})
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "history failed")
	}
	return items, nil
}

// Answer applies open -> answered as an atomic compare-and-set. The read, the
// compare and the write all happen inside one write transaction, so exactly one
// of two concurrent answers transitions the item.
//
// answeredBy and resolvedBy are stamped together and mean different things:
// answeredBy is the arm that carried the answer, resolvedBy is the session that
// resolved the item. Resolution rides this transition rather than introducing
// one — the schema's § Resolution is explicit that resolution is not a fourth
// state, so ValidateTransition is called exactly as it was before.
func (a *attentionStore) Answer(
	ctx context.Context,
	itemID ItemID,
	answeredBy string,
	resolvedBy string,
	decision Decision,
	answer *Answer,
) (*Item, error) {
	var result *Item
	err := a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		item, err := a.store.Get(ctx, tx, itemID.String())
		if err != nil {
			if isNotFound(err) {
				return errors.Wrapf(ctx, ErrItemNotFound, "item %s not found", itemID)
			}
			return errors.Wrap(ctx, err, "get item failed")
		}
		// The compare-and-set itself: succeed only from open. The three failure
		// modes are distinct and a caller can tell them apart.
		switch item.State {
		case OpenState:
			// proceed
		case AnsweredState:
			// Lost the race. The schema says the loser reads back and reports;
			// it must not retry and must not route its own answer anyway.
			return errors.Wrapf(
				ctx,
				ErrAlreadyAnswered,
				"item %s was already answered by %s",
				itemID,
				item.AnsweredBy,
			)
		default:
			// `closed → answered` is not a row of the schema's table — illegal
			// from every state, not merely from this one.
			return ValidateTransition(ctx, item.State, AnsweredState)
		}
		now := a.currentDateTimeGetter.Now()
		item.State = AnsweredState
		item.AnsweredAt = &now
		item.AnsweredBy = answeredBy
		// Stamped from the caller's declaration, never derived and never
		// authenticated. An empty value is written as empty rather than
		// backfilled from answeredBy: the arm is not an identity, so copying it
		// here would record a value that looks like a resolver and is not one.
		item.ResolvedBy = resolvedBy
		// Stamped from the caller's declaration for the same reason, and never
		// derived either: the arm is not a decision, so an empty decision stays
		// empty rather than being inferred from the arm that supplied it.
		item.Decision = decision
		// The operator's actual answer, stamped from the caller for the same
		// reason again: the arm is not an answer either, so a nil answer stays
		// nil rather than being backfilled from the arm or the decision. It rides
		// this same transition — the schema is explicit that answer content is
		// not a fourth state, so ValidateTransition is called exactly as before.
		item.Answer = answer
		if err := a.store.Add(ctx, tx, item.ItemID.String(), *item); err != nil {
			return errors.Wrap(ctx, err, "update item failed")
		}
		result = item
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "answer failed")
	}
	return result, nil
}

// Escalate records which session is carrying this item, as an atomic
// compare-and-set. The read, the compare and the write all happen inside one
// write transaction, so exactly one of two concurrent escalations stamps it.
//
// The state is deliberately left alone. Escalation changes who is being asked,
// not where the item is in its lifecycle, so no row of the schema's transitions
// table is involved and ValidateTransition is never called — calling it would
// be the state-machine detour the schema's § Escalation rules out.
func (a *attentionStore) Escalate(
	ctx context.Context,
	itemID ItemID,
	escalatedBy string,
) (*Item, error) {
	var result *Item
	err := a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		item, err := a.store.Get(ctx, tx, itemID.String())
		if err != nil {
			if isNotFound(err) {
				return errors.Wrapf(ctx, ErrItemNotFound, "item %s not found", itemID)
			}
			return errors.Wrap(ctx, err, "get item failed")
		}
		// Only an open item is rendered by an arm, so only an open item has a
		// stamp anyone would read. Refused rather than stamped-and-ignored:
		// the schema says a stamp on a closed item is unreachable.
		if item.State != OpenState {
			return errors.Wrapf(
				ctx,
				ErrItemNotOpen,
				"item %s is %s, not open",
				itemID,
				item.State,
			)
		}
		// The compare-and-set itself, against the caller's identity rather than
		// the field's presence. A manager re-running its own sweep over an item
		// it already escalated must proceed, not skip itself.
		if item.EscalatedBy != "" {
			if item.EscalatedBy == escalatedBy {
				result = item
				return nil
			}
			return errors.Wrapf(
				ctx,
				ErrAlreadyEscalated,
				"item %s was already escalated by %s",
				itemID,
				item.EscalatedBy,
			)
		}
		item.EscalatedBy = escalatedBy
		if err := a.store.Add(ctx, tx, item.ItemID.String(), *item); err != nil {
			return errors.Wrap(ctx, err, "update item failed")
		}
		result = item
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "escalate failed")
	}
	return result, nil
}

// Close applies open -> closed or answered -> closed.
func (a *attentionStore) Close(ctx context.Context, itemID ItemID) (*Item, error) {
	var result *Item
	err := a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		item, err := a.store.Get(ctx, tx, itemID.String())
		if err != nil {
			if isNotFound(err) {
				return errors.Wrapf(ctx, ErrItemNotFound, "item %s not found", itemID)
			}
			return errors.Wrap(ctx, err, "get item failed")
		}
		if err := ValidateTransition(ctx, item.State, ClosedState); err != nil {
			return err
		}
		now := a.currentDateTimeGetter.Now()
		item.State = ClosedState
		item.ClosedAt = &now
		if err := a.store.Add(ctx, tx, item.ItemID.String(), *item); err != nil {
			return errors.Wrap(ctx, err, "update item failed")
		}
		result = item
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "close failed")
	}
	return result, nil
}

// updateExistingIfLive applies duplicate suppression. Suppression compares
// dedup_key *within a live producer_id*: when an open item already carries this
// producer and dedup key and its producer is still live, the push updates that
// item rather than adding a row.
//
// It returns nil — meaning "no suppression applied, create a new row" — in two
// distinct cases, and both are deliberate: no open item matches, or one matches
// but its producer's liveness has already failed. Reviving the dead producer's
// row would let a gone asker's item reappear under a new claim.
func (a *attentionStore) updateExistingIfLive(
	ctx context.Context,
	tx libkv.Tx,
	request PushRequest,
) (*Item, error) {
	existing, err := a.findOpenByDedupKey(ctx, tx, request)
	if err != nil {
		return nil, errors.Wrap(ctx, err, "find by dedup key failed")
	}
	if existing == nil {
		return nil, nil
	}
	live, err := a.isProducerLive(ctx, existing)
	if err != nil {
		return nil, errors.Wrap(ctx, err, "check producer liveness failed")
	}
	if !live {
		return nil, nil
	}
	existing.Payload = request.Payload
	existing.Context = request.Context
	existing.Options = request.Options
	existing.InterruptClass = request.InterruptClass
	existing.ProvenanceClass = request.ProvenanceClass
	existing.ExpiresAt = request.ExpiresAt
	existing.CreatedAt = a.currentDateTimeGetter.Now()
	if err := a.store.Add(ctx, tx, existing.ItemID.String(), *existing); err != nil {
		return nil, errors.Wrap(ctx, err, "update existing item failed")
	}
	return existing, nil
}

// findOpenByDedupKey returns the open item sharing this producer and dedup key,
// or nil when none exists.
func (a *attentionStore) findOpenByDedupKey(
	ctx context.Context,
	tx libkv.Tx,
	request PushRequest,
) (*Item, error) {
	var found *Item
	err := a.store.Map(ctx, tx, func(ctx context.Context, key string, item Item) error {
		if found != nil {
			return nil
		}
		if item.State != OpenState {
			return nil
		}
		if item.ProducerID != request.ProducerID {
			return nil
		}
		if item.DedupKey != request.DedupKey {
			return nil
		}
		copy := item
		found = &copy
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "map items failed")
	}
	return found, nil
}

// isProducerLive resolves the item's declared liveness model. The producer
// knows which model it is, so the store never guesses.
func (a *attentionStore) isProducerLive(ctx context.Context, item *Item) (bool, error) {
	model, value, err := item.LivenessRef.Parse(ctx)
	if err != nil {
		return false, errors.Wrap(ctx, err, "parse liveness ref failed")
	}
	switch model {
	case SessionLivenessModel:
		return a.sessionLivenessChecker.IsLive(ctx, value), nil
	case HeartbeatLivenessModel:
		return a.isHeartbeatFresh(value), nil
	default:
		return false, errors.Wrapf(ctx, ErrInvalidLivenessRef, "unknown model '%s'", model)
	}
}

// isHeartbeatFresh reports whether the heartbeat file's mtime is within the
// declared window. A missing file is not fresh — the producer is gone.
func (a *attentionStore) isHeartbeatFresh(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	now := a.currentDateTimeGetter.Now().Time()
	return now.Sub(info.ModTime()) <= a.heartbeatWindow.Duration()
}

// isAsked reports whether the item is a question the producer asked of someone
// else, as opposed to a condition it reported. The schema draws the
// distinction but declares no field carrying it, so it is read from
// answer_mechanism: `ack` is defined as a condition report, and the other two
// are questions.
func isAsked(item *Item) bool {
	return item.AnswerMechanism != AckAnswerMechanism
}
