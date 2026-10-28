// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"strings"

	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
)

// Flag records that the operator believes this item should never have reached
// them, as a field write on an open item.
//
// It is deliberately NOT a transition: ValidateTransition is never called, and
// State, AnsweredAt, AnsweredBy, Decision, Answer and Answers are all left
// exactly as they were. A flag records that the question should not have been
// asked; it is not an answer to it, so the item stays answerable.
//
// Two fields are written, not one: FlagReason carries the caller's text and
// FlaggedAt stamps when, from the store's own clock rather than the caller's, so
// the pair is always present together — exactly as EscalatedBy pairs with
// EscalatedAt.
//
// ⚠️ There is no compare against an existing flag, unlike Escalate: a flag is
// not a claim of ownership, so a re-flag is a valid flag and the later reason
// wins.
func (a *attentionStore) Flag(
	ctx context.Context,
	itemID ItemID,
	reason string,
) (*Item, error) {
	// Refused before the transaction opens: a flag with no reason is a caller
	// bug and no write should be attempted for one. TrimSpace is the check
	// only — the stored reason is the caller's text verbatim, because the
	// contract declares no length cap and no normalization.
	if strings.TrimSpace(reason) == "" {
		return nil, errors.Wrapf(
			ctx,
			ErrFlagReasonRequired,
			"item %s: flag reason is empty",
			itemID,
		)
	}
	var result *Item
	err := a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		item, err := a.store.Get(ctx, tx, itemID.String())
		if err != nil {
			if isNotFound(err) {
				return errors.Wrapf(ctx, ErrItemNotFound, "item %s not found", itemID)
			}
			return errors.Wrap(ctx, err, "get item failed")
		}
		// A flag is a field write on an OPEN item. Only an open item is a
		// card the operator is being asked; an answered or closed item's
		// misroute signal is out of scope, so the write is refused rather
		// than applied to a record nothing would render.
		if item.State != OpenState {
			return errors.Wrapf(
				ctx,
				ErrItemNotOpen,
				"item %s is %s, not open",
				itemID,
				item.State,
			)
		}
		// Last writer wins: a re-flag is a valid flag, and the later reason
		// replaces the earlier one. There is no compare against the existing
		// value — unlike Escalate, a flag is not a claim of ownership.
		item.FlagReason = reason
		now := a.currentDateTimeGetter.Now()
		item.FlaggedAt = &now
		if err := a.putItem(ctx, tx, *item); err != nil {
			return errors.Wrap(ctx, err, "update item failed")
		}
		result = item
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "flag failed")
	}
	return result, nil
}

// Unflag clears FlagReason and FlaggedAt and returns the item.
//
// ⚠️ It is deliberately NOT state-gated. A flag survives an answer — the two
// records coexist, per the contract's failure-mode row 10 — so a gate on `open`
// would make a flagged-then-answered item impossible to unflag, making the flag
// permanent, which is the opposite of "unlike closed, a flag is not terminal".
// The state gate belongs to Flag only.
func (a *attentionStore) Unflag(ctx context.Context, itemID ItemID) (*Item, error) {
	var result *Item
	err := a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		item, err := a.store.Get(ctx, tx, itemID.String())
		if err != nil {
			if isNotFound(err) {
				return errors.Wrapf(ctx, ErrItemNotFound, "item %s not found", itemID)
			}
			return errors.Wrap(ctx, err, "get item failed")
		}
		// Both fields are cleared together, so the pair cannot survive
		// half-written. No state gate and no presence gate: unflagging is
		// reversible and idempotent — an item carrying no flag is already in
		// the requested state, so the call succeeds and the item reads back
		// unchanged.
		item.FlagReason = ""
		item.FlaggedAt = nil
		if err := a.putItem(ctx, tx, *item); err != nil {
			return errors.Wrap(ctx, err, "update item failed")
		}
		result = item
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "unflag failed")
	}
	return result, nil
}
