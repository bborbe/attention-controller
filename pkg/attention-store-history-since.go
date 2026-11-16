// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
)

// HistoryCreatedSince returns every item created at or after since, newest-first.
//
// ⚠️ The bound is checked on the index KEY, before any decode. The key is the
// fixed-width UTC creation time, then `|`, then the id; `|` sorts above every
// digit, so a key compares at or above the bare time prefix exactly when its time
// is at or after since. The reverse walk therefore breaks on the first older key
// and never decodes it — the whole point of this read.
func (a *attentionStore) HistoryCreatedSince(
	ctx context.Context,
	since libtime.DateTime,
) (Items, error) {
	if err := a.ensureHistoryIndex(ctx); err != nil {
		return nil, errors.Wrap(ctx, err, "ensure history index failed")
	}
	bound := since.Time().UTC().Format(historyIndexKeyLayout)
	var items Items
	err := a.db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		var err error
		items, err = collectHistoryCreatedSince(ctx, tx, bound)
		return err
	})
	if err != nil {
		return nil, errors.Wrap(ctx, err, "history created since failed")
	}
	return items, nil
}

// collectHistoryCreatedSince walks the history index in reverse and collects
// every item whose key is at or above bound.
//
// ⚠️ It is split from HistoryCreatedSince for the same reason collectHistoryPage
// is split from History: this walks an index, the caller decides the bound.
func collectHistoryCreatedSince(
	ctx context.Context,
	tx libkv.Tx,
	bound string,
) (Items, error) {
	items := make(Items, 0)
	bucket, err := tx.Bucket(ctx, attentionHistoryIndexBucketName)
	if err != nil {
		if errors.Is(err, libkv.BucketNotFoundError) {
			return items, nil
		}
		return nil, errors.Wrap(ctx, err, "get history index bucket failed")
	}
	it := bucket.IteratorReverse()
	defer it.Close()
	for it.Rewind(); it.Valid(); it.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		key := string(it.Item().Key())
		// Skipped by NAME, never by position — same rule as collectHistoryPage.
		if key == historyIndexMarkerKey {
			continue
		}
		if key < bound {
			break
		}
		item, err := decodeHistoryIndexEntry(ctx, it.Item())
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}
