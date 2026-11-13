// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
)

// MarkSeen records that a board rendered this item, stamping DisplayedAt from
// the store's own clock. See the interface comment for the contract.
//
// ⚠️ No state gate, unlike Flag (open-only) and Escalate (open-only): a dimmed
// answered record is a card the board paints, so refusing it would leave
// displayed_at absent on exactly the cards the operator reads.
func (a *attentionStore) MarkSeen(ctx context.Context, itemID ItemID) (*Item, error) {
	// Every write drops the cached history page: the answer it would
	// otherwise serve predates this change.
	defer a.historyPage.invalidate()
	var result *Item
	err := a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		item, err := a.store.Get(ctx, tx, itemID.String())
		if err != nil {
			if isNotFound(err) {
				return errors.Wrapf(ctx, ErrItemNotFound, "item %s not found", itemID)
			}
			return errors.Wrap(ctx, err, "get item failed")
		}
		// First write wins, and the check is the compare half of the
		// compare-and-set: an item that already carries a stamp reads back
		// unchanged and no write is attempted.
		if item.DisplayedAt != nil {
			result = item
			return nil
		}
		now := a.currentDateTimeGetter.Now()
		item.DisplayedAt = &now
		if err := a.putItem(ctx, tx, *item); err != nil {
			return errors.Wrap(ctx, err, "update item failed")
		}
		result = item
		return nil
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "mark seen failed")
	}
	return result, nil
}
