// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"

	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
)

// collectHistoryPage walks the history index in reverse and collects one page.
//
// ⚠️ It is a separate function because the cursor loop, the marker skip and the
// two bound checks together exceed the complexity budget the linter allows the
// read — the same reason `dueAnsweredKeys` and `closeAnsweredItem` were split out
// of `SweepAnswered`. The split is by responsibility rather than by line count:
// this walks an index, `History` decides the bounds.
//
// The order is newest-first because the index is keyed by creation time and the
// cursor is REVERSE — `Rewind` positions at the highest key and `Next` descends.
func (a *attentionStore) collectHistoryPage(
	ctx context.Context,
	tx libkv.Tx,
	limit int,
	offset int,
	filter historyMatcher,
) (Items, error) {
	bucket, err := tx.Bucket(ctx, attentionHistoryIndexBucketName)
	if err != nil {
		if errors.Is(err, libkv.BucketNotFoundError) {
			return Items{}, nil
		}
		return nil, errors.Wrap(ctx, err, "get history index bucket failed")
	}
	it := bucket.IteratorReverse()
	defer it.Close()
	// ⚠️ One `now` for the whole walk, matching `read()`. History is a scan over
	// the same field with the same straddle hazard, and it is the endpoint whose
	// stated purpose is a stable count — a deadline crossed mid-walk would drop a
	// row from a set the caller is counting.
	now := a.currentDateTimeGetter.Now()
	items := make(Items, 0)
	skipped := 0
	for it.Rewind(); it.Valid(); it.Next() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		// ⚠️ Skipped by NAME, never by position. The marker sorts below every real
		// key, so the reverse cursor reaches it last — but a read that relied on
		// that would put the marker in the result the moment the key layout
		// changed, and the marker's value is an empty Item, which decodes cleanly
		// and would look like a real row.
		if string(it.Item().Key()) == historyIndexMarkerKey {
			continue
		}
		item, keep, err := filter.decode(ctx, it.Item())
		if err != nil {
			return nil, err
		}
		if !keep {
			continue
		}
		// ⚠️ An expired OPEN item is skipped, and it is filtered BEFORE the offset
		// so it never occupies a page slot. This is History's one narrowing:
		// "filters on nothing" holds for answered and closed items — their history
		// is never rewritten — while a moot open item is neither what resolved nor
		// what escalated, the population this endpoint exists to count.
		//
		// ⚠️ It also means the page is decoded before the offset is applied, where
		// the offset used to skip without decoding at all. That cost is what buys
		// the filter: expiry cannot be read off the index key.
		if item.State == OpenState && isExpired(&item, now) {
			continue
		}
		if skipped < offset {
			skipped++
			continue
		}
		if limit > 0 && len(items) >= limit {
			break
		}
		items = append(items, item)
	}
	return items, nil
}

// decodeHistoryIndexEntry unmarshals one history index entry into an Item.
//
// ⚠️ It exists because History reads the bucket directly instead of going through
// `storeTx.Map`, and the reason is the cursor rather than the decode: Map always
// rewinds FORWARD and always visits every entry, while this read needs reverse
// order and an early stop. Keeping the unmarshal here — rather than inlining it —
// is what stops the two decoders disagreeing about the encoding later.
func decodeHistoryIndexEntry(ctx context.Context, it libkv.Item) (Item, error) {
	var item Item
	err := it.Value(func(v []byte) error {
		if err := json.Unmarshal(v, &item); err != nil {
			return errors.Wrapf(ctx, err, "unmarshal history index entry failed")
		}
		return nil
	})
	if err != nil {
		return Item{}, errors.Wrap(ctx, err, "read history index entry failed")
	}
	return item, nil
}
