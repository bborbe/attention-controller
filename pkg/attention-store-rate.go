// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
)

// Rate records the operator's 👍/👎 judgement of an item, or clears it when
// rating is NoRating.
//
// It is deliberately NOT a transition, NOT an answer and NOT a flag:
// ValidateTransition is never called, and State, every answer field, the
// delivery trail and the flag pair are left exactly as they were. Only Rating
// and RatedAt are written.
//
// ⚠️ It is deliberately NOT state-gated, unlike Flag. Rating the last hour
// after the fact is the use case, so an answered or closed item is rated
// exactly as an open one is.
//
// The value is validated before the transaction opens: anything other than
// "up", "down" or "" is ErrRatingInvalid and nothing is written. Last writer
// wins; RatedAt is stamped from the store's own clock in the same
// compare-and-set, and cleared together with Rating.
func (a *attentionStore) Rate(
	ctx context.Context,
	itemID ItemID,
	rating Rating,
) (*Item, error) {
	// Every write drops the cached history page: the answer it would
	// otherwise serve predates this change.
	defer a.historyPage.invalidate()
	if err := rating.Validate(ctx); err != nil {
		return nil, errors.Wrapf(ctx, err, "item %s: rate", itemID)
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
		item.Rating = rating
		if rating == NoRating {
			item.RatedAt = nil
		} else {
			now := a.currentDateTimeGetter.Now()
			item.RatedAt = &now
		}
		if err := a.putItem(ctx, tx, *item); err != nil {
			return errors.Wrap(ctx, err, "update item failed")
		}
		result = item
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "rate failed")
	}
	return result, nil
}
