// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
)

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
	existing.Payload = request.Payload
	existing.Context = request.Context
	existing.Options = request.Options
	existing.InterruptClass = request.InterruptClass
	existing.ProvenanceClass = request.ProvenanceClass
	// ⚠️ These five complete the re-push, and their absence was a real half-
	// conversion: a dedup re-push that the Decision Record rule rewrites from a
	// pick to an ack set InterruptClass=ack and Options=nil but left the stored
	// item's AnswerMechanism=message, Questions and Risk behind — an ack card
	// still answerable through the old routing. A re-push reflects the latest
	// declaration, so every mutable field the create path sets from the request
	// is refreshed here too. See applyDecisionRecordRule.
	existing.ProducerKind = request.ProducerKind
	existing.Risk = request.Risk
	existing.AnswerMechanism = request.AnswerMechanism
	existing.AnswerCardinality = request.AnswerCardinality
	existing.Questions = request.Questions
	// A suppressed re-push is a re-ask of the same question, and this block
	// already resets CreatedAt to now for exactly that reason — so the ask time
	// follows the same rule, including the case where the re-push carries none
	// (the field returns to absent rather than keeping a stale instant from the
	// previous push).
	existing.AskedAt = askedAtOrNil(request.AskedAt)
	existing.CreatedAt = a.currentDateTimeGetter.Now()
	// ⚠️ After CreatedAt, never before: a re-push without a deadline re-arms the
	// default from the NEW created_at. Copying the request's nil straight in would
	// clear the stored default and leave the row with no expiry at all.
	expiresAt, defaulted := expiresAtOrDefault(request.ExpiresAt, existing.CreatedAt)
	existing.ExpiresAt = expiresAt
	// The provenance of the deadline follows the push that last wrote it, exactly
	// as the deadline itself does: a producer that switches to declaring its own
	// TTL stops being renewable, and one that drops back to the default starts.
	existing.ExpiresAtDefaulted = defaulted
	if err := a.putItem(ctx, tx, *existing); err != nil {
		return nil, errors.Wrap(ctx, err, "update existing item failed")
	}
	return existing, nil
}

// findOpenByDedupKey returns the open item sharing this producer and dedup
// key, or nil when none exists.
//
// ⚠️ It is a POINT READ of the dedup index, and the walk it replaces was the
// dominant cost of a push: it decoded every stored item on every push,
// including the create path's, because `updateExistingIfLive` runs before the
// row is created. Its early-out never worked either — the guard returned nil,
// and a nil return from a `Map` callback CONTINUES the walk, so the scan ran
// to the end of the bucket whether or not it matched on the first row.
//
// ⚠️ The state guard is gone, and its absence is safe rather than lost: the
// index admits open items only (`openIndexWorthy`), so every entry it can
// return is one suppression may legitimately collapse onto. The producer and
// dedup-key guards are gone for the same reason — they are the index's key.
//
// ⚠️ `nil, nil` on absence is the contract `updateExistingIfLive` already
// relies on ("no suppression applied, create a new row"), and `isNotFound` is
// what turns both the absent-key and the never-created-bucket case into it.
func (a *attentionStore) findOpenByDedupKey(
	ctx context.Context,
	tx libkv.Tx,
	request PushRequest,
) (*Item, error) {
	item, err := a.dedupIndex.Get(
		ctx,
		tx,
		dedupIndexKeyOf(request.ProducerID, request.DedupKey),
	)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, errors.Wrap(ctx, err, "get dedup index entry failed")
	}
	return item, nil
}
