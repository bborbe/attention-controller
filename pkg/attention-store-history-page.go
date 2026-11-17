// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

// History returns items NEWEST-FIRST, up to limit of them, skipping the first
// offset.
//
// It is deliberately not Read. Read answers "what should an arm render now": it
// filters to open items and removes expired ones as a side effect, which makes
// it blind to everything that has left the queue — and it cannot report a
// history at all, because the act of reading would delete part of what it
// reports. History prunes nothing, and narrows on exactly one thing: an expired
// OPEN item, which is neither what resolved nor what escalated. An answered or closed
// item is exactly what a caller counting resolutions needs, and removing it
// would rewrite the history the schema says is never rewritten.
//
// ⚠️ It walks the HISTORY INDEX in reverse rather than scanning the items
// bucket, and both halves of that are load-bearing. The scan decoded all ~22,000
// rows (~18.8 MB) on every request; the index costs the page. And the reverse
// cursor is what makes the page MEANINGFUL — item ids are random 128-bit hex, so
// items-bucket order carries no time meaning and "the first 1000" would have been
// an arbitrary 1000 that shifted as the store grew.
//
// ⚠️ limit <= 0 means UNBOUNDED, and that is the compatibility path rather than a
// convenience. The endpoint's consumers count resolutions across the whole store,
// so a caller that needs the old behaviour can still ask for it; what changes is
// that the DEFAULT is now a page rather than everything. offset < 0 is treated as
// 0, so a malformed caller pages from the start instead of failing.
//
// A read transaction, not a write one: this path mutates nothing, so it takes
// no writer lock and cannot interleave with the pruning Read does.
//
// ⚠️ It decodes the page it was given, so a caller that only forwards the page
// should call HistoryJSON instead — that path writes the cached body with no
// decode at all. This form exists for callers that need the structs.
func (a *attentionStore) History(
	ctx context.Context,
	limit int,
	offset int,
	filters ...HistoryFilter,
) (Items, error) {
	body, err := a.HistoryJSON(ctx, limit, offset, filters...)
	if err != nil {
		return nil, err
	}
	var items Items
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, errors.Wrap(ctx, err, "unmarshal history page failed")
	}
	return items, nil
}

// HistoryJSON returns one History page as the exact JSON body the history
// endpoint serves.
//
// ⚠️ It is the endpoint's read, and it exists so the cache can hold the ENCODED
// page. Caching the decoded Items is what put 156 MB of live heap on the process
// for one 10,000-row page — see historyPageCache. A cache hit here writes the
// bytes it already holds; a miss walks the index once, encodes once, and stores
// the body.
//
// ⚠️ A miss does not materialise the page as `Items`: the walk encodes each row
// straight into the response buffer. That is what keeps a cold burst bounded —
// the decoded slice of one 10,000-row page is 156 MB, and it would exist for
// every concurrent miss even though only the encoded body is retained.
//
// ⚠️ Concurrent misses for the SAME page share one walk (historyPageCache.flights),
// and concurrent misses for DISTINCT pages are capped (historyPageDecodeLimit), so
// a burst cannot multiply the walk cost by the number of callers.
//
// ⚠️ The body carries no trailing newline: the endpoint serves it through
// `json.RawMessage`, and `json.Encoder` supplies the newline the endpoint has
// always carried.
func (a *attentionStore) HistoryJSON(
	ctx context.Context,
	limit int,
	offset int,
	filters ...HistoryFilter,
) ([]byte, error) {
	if err := a.ensureHistoryIndex(ctx); err != nil {
		return nil, errors.Wrap(ctx, err, "ensure history index failed")
	}
	if offset < 0 {
		offset = 0
	}
	key := newHistoryPageKey(limit, offset, filters)
	if cached, _ := a.historyPage.load(key, a.currentDateTimeGetter.Now()); cached != nil {
		return cached, nil
	}
	flight, joined := a.historyPage.begin(key)
	if joined {
		<-flight.done
		return flight.body, flight.err
	}
	body, validUntil, err := a.buildHistoryPage(ctx, limit, offset, filters)
	a.historyPage.complete(key, flight, body, validUntil, err)
	if err != nil {
		return nil, err
	}
	return body, nil
}

// buildHistoryPage walks one page under a decode slot.
//
// ⚠️ The slot is taken around the walk AND the encode: the buffer is the thing a
// burst would multiply, so releasing it before the body exists would defeat the
// cap.
func (a *attentionStore) buildHistoryPage(
	ctx context.Context,
	limit int,
	offset int,
	filters []HistoryFilter,
) ([]byte, libtime.DateTime, error) {
	if !a.historyPage.acquireDecode(ctx) {
		return nil, libtime.DateTime{}, ctx.Err()
	}
	defer a.historyPage.releaseDecode()
	body, validUntil, err := historyPageCollector(
		a,
		ctx,
		limit,
		offset,
		newHistoryMatcher(filters),
	)
	if err != nil {
		return nil, libtime.DateTime{}, errors.Wrap(ctx, err, "history failed")
	}
	// ⚠️ One line per BUILD, never per read: a cache hit returns before this
	// point, so counting these lines counts index walks. That is the observable
	// the single-flight and cache-hit claims are measured against — a burst of
	// concurrent requests for one page must add exactly one.
	key := newHistoryPageKey(limit, offset, filters)
	glog.V(2).Infof(
		"history page built: limit=%d offset=%d producers=%q dedup_prefix=%q bytes=%d",
		key.limit,
		key.offset,
		key.producers,
		key.dedupPrefix,
		len(body),
	)
	return body, validUntil, nil
}

// collectHistoryPageJSON walks one page and encodes it as it goes.
//
// ⚠️ It encodes each row into the buffer instead of collecting `Items` first.
// The decoded slice of a 10,000-row page is 156 MB of live heap (measured
// 2026-11-16) and it would exist for every concurrent miss even though only the
// encoded body is retained — the streaming form is what makes the memory cost of a
// miss the body alone.
//
// It also returns the instant the page stops matching a fresh walk: the earliest
// deadline among its OPEN items, or the zero instant when the page holds none.
// See historyPageCache for why the cache needs it.
func (a *attentionStore) collectHistoryPageJSON(
	ctx context.Context,
	limit int,
	offset int,
	filter historyMatcher,
) ([]byte, libtime.DateTime, error) {
	var body bytes.Buffer
	body.WriteByte('[')
	var validUntil libtime.DateTime
	wrote := 0
	err := a.db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		return a.walkHistoryPage(ctx, tx, limit, offset, filter, func(item Item) error {
			if wrote > 0 {
				body.WriteByte(',')
			}
			encoded, err := json.Marshal(item)
			if err != nil {
				return errors.Wrap(ctx, err, "marshal history item failed")
			}
			body.Write(encoded)
			wrote++
			if item.State == OpenState {
				deadline := effectiveExpiresAt(&item)
				if validUntil.Time().IsZero() || deadline.Time().Before(validUntil.Time()) {
					validUntil = deadline
				}
			}
			return nil
		})
	})
	if err != nil {
		return nil, libtime.DateTime{}, err
	}
	// ⚠️ No trailing newline: the body is served through json.RawMessage, and
	// json.Encoder appends the newline the endpoint has always carried.
	body.WriteString("]")
	return body.Bytes(), validUntil, nil
}

// walkHistoryPage walks the history index in reverse and hands each page row to
// visit.
//
// ⚠️ It is a separate function because the cursor loop, the marker skip and the
// two bound checks together exceed the complexity budget the linter allows the
// read — the same reason `dueAnsweredKeys` and `closeAnsweredItem` were split out
// of `SweepAnswered`. The split is by responsibility rather than by line count:
// this walks an index, `History` decides the bounds, and the visitor decides what
// the page becomes.
//
// The order is newest-first because the index is keyed by creation time and the
// cursor is REVERSE — `Rewind` positions at the highest key and `Next` descends.
func (a *attentionStore) walkHistoryPage(
	ctx context.Context,
	tx libkv.Tx,
	limit int,
	offset int,
	filter historyMatcher,
	visit func(Item) error,
) error {
	bucket, err := tx.Bucket(ctx, attentionHistoryIndexBucketName)
	if err != nil {
		if errors.Is(err, libkv.BucketNotFoundError) {
			return nil
		}
		return errors.Wrap(ctx, err, "get history index bucket failed")
	}
	it := bucket.IteratorReverse()
	defer it.Close()
	// ⚠️ One `now` for the whole walk, matching `read()`. History is a scan over
	// the same field with the same straddle hazard, and it is the endpoint whose
	// stated purpose is a stable count — a deadline crossed mid-walk would drop a
	// row from a set the caller is counting.
	now := a.currentDateTimeGetter.Now()
	skipped := 0
	kept := 0
	for it.Rewind(); it.Valid(); it.Next() {
		select {
		case <-ctx.Done():
			return ctx.Err()
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
			return err
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
		if historyPageRowExpired(&item, now) {
			continue
		}
		if skipped < offset {
			skipped++
			continue
		}
		if historyPageFull(limit, kept) {
			break
		}
		if err := visit(item); err != nil {
			return err
		}
		kept++
	}
	return nil
}

// historyPageRowExpired reports whether the row is History's one narrowing: an
// OPEN item past its deadline.
//
// ⚠️ It is a function rather than an inline `state && isExpired` so the walk's
// cognitive complexity stays under the linter's budget; the two conditions are
// one rule, not two branches.
func historyPageRowExpired(item *Item, now libtime.DateTime) bool {
	return item.State == OpenState && isExpired(item, now)
}

// historyPageFull reports whether the page has reached its limit. A limit of zero
// or less is unbounded, which is the compatibility path.
func historyPageFull(limit int, kept int) bool {
	return limit > 0 && kept >= limit
}
