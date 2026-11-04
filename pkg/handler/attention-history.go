// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"net/http"
	"strconv"

	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"

	"github.com/bborbe/attention-controller/pkg"
)

const (
	// historyLimitParam and historyOffsetParam are the query parameters the
	// endpoint pages with. `limit` and `offset` are the names the REST guide
	// prescribes for a paginated collection, not names invented here.
	historyLimitParam  = "limit"
	historyOffsetParam = "offset"

	// historyProducerParam narrows the page to one or more producers; repeat it
	// for several (`?producer_id=a&producer_id=b`). Absent means every producer,
	// so every existing caller is unchanged.
	historyProducerParam = "producer_id"

	// historyDedupPrefixParam narrows the page to items whose dedup key starts
	// with the value (`?dedup_key_prefix=park:`).
	historyDedupPrefixParam = "dedup_key_prefix"

	// historyDefaultLimit bounds the response when the caller names no limit.
	//
	// ⚠️ It is sized from the payload rather than from taste. Rows average ~840
	// bytes, so 1000 is ~0.8 MB — comfortably under the 2 MB the criterion pins,
	// and roughly 23x below the 18.8 MB the unbounded read returned.
	//
	// `limit=0` does NOT raise this to the whole store — it resolves to
	// historyMaxLimit, the largest page one request may ask for. A caller that
	// genuinely needs more than that walks the pages, which the endpoint's own
	// doc requires in order to count at all — though that walk is not stable
	// while an item is expiring, as the doc spells out.
	historyDefaultLimit = 1000

	// historyMaxLimit is the largest page one request may ask for, and the value
	// `limit=0` now resolves to.
	//
	// ⚠️ It exists because the page is materialised in memory before it is
	// encoded, so an unbounded one sets the process footprint for hours after it
	// is served — macOS keeps released pages counted in `phys_footprint`, so the
	// high-water mark, not the live set, is what the operator sees.
	//
	// ⚠️ **2,000, not 10,000, and the first number was wrong for a measurable
	// reason.** The cap was sized against a 50,000-row request, but the store only
	// ever held a little over 10,000 rows — so `limit=50000` returned the 10,000
	// cap and the cut was 1.6x (18.8 MB → 11.7 MB), not the 5x the sizing implied.
	// A 150 s `alloc_space` delta on v0.67.4 put `collectHistoryPage` at 132 MB
	// cumulative (41 % of accounted allocation) because `answered-watch.py` polls
	// the whole history every 60 s, and the arena settled at 335 MB against a 25 MB
	// live heap. At 2,000 rows the same poll decodes a fifth as much. The page is
	// newest-first, so a consumer polling for recent items still gets them; one
	// that genuinely needs more walks the pages, which the endpoint's own doc
	// already requires in order to count at all.
	historyMaxLimit = 2000
)

// clampHistoryLimit bounds one page. `limit=0` has always meant "the whole
// store", and that is the value this exists to remove: a caller that wants more
// than historyMaxLimit walks the pages, which the endpoint's own doc already
// requires for a stable count.
func clampHistoryLimit(limit int) int {
	if limit <= 0 || limit > historyMaxLimit {
		return historyMaxLimit
	}
	return limit
}

// NewAttentionHistoryHandler creates an HTTP handler that returns items
// regardless of state, newest-first and paginated.
//
// It exists for counting, not rendering: the read path returns only open items
// and prunes dead askers, so it cannot say how many items a manager resolved
// or the operator was asked about. This handler never prunes, and narrows on
// exactly one thing — an expired OPEN item, which is neither what resolved nor
// what escalated — which is what makes a resolved-versus-escalated split
// countable.
//
// ⚠️ Pagination bounds the RESPONSE, and a consumer that counts must walk the
// pages rather than read one — but ⚠️ **the walk is not stable while an item is
// expiring.** The expiry narrowing is a wall-clock predicate, so a row can stop
// being reachable partway through a paginated walk and every later row shifts
// one position earlier; a slow multi-page walk can drop a row at no offset at
// all. A consumer counting resolutions must walk faster than the expiry horizon
// or pin a cutoff. That is the price of the narrowing, and it is stated here
// rather than left to be discovered as a wrong count. The response stays a bare JSON array — the REST
// guide records that a pagination envelope is not yet standardized and should not
// be invented silently here, and three existing consumers parse the array.
func NewAttentionHistoryHandler(store pkg.AttentionStore) http.Handler {
	return libhttp.NewJSONErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
				limit, err := historyIntParam(ctx, req, historyLimitParam, historyDefaultLimit)
				if err != nil {
					return err
				}
				limit = clampHistoryLimit(limit)
				offset, err := historyIntParam(ctx, req, historyOffsetParam, 0)
				if err != nil {
					return err
				}
				items, err := store.History(ctx, limit, offset, pkg.HistoryFilter{
					Producers:      historyProducers(req),
					DedupKeyPrefix: pkg.DedupKey(req.URL.Query().Get(historyDedupPrefixParam)),
				})
				if err != nil {
					return libhttp.WrapWithCode(
						errors.Wrap(ctx, err, "history failed"),
						libhttp.ErrorCodeInternal,
						http.StatusInternalServerError,
					)
				}
				if err := libhttp.SendJSONResponse(ctx, resp, items, http.StatusOK); err != nil {
					return errors.Wrap(ctx, err, "send response failed")
				}
				return nil
			},
		),
	)
}

// historyProducers reads every `producer_id` query value. An empty value is
// dropped here as well as in the store, so `?producer_id=` reads as "no filter"
// rather than as "items with no producer".
func historyProducers(req *http.Request) []pkg.ProducerID {
	var producers []pkg.ProducerID
	for _, raw := range req.URL.Query()[historyProducerParam] {
		if raw != "" {
			producers = append(producers, pkg.ProducerID(raw))
		}
	}
	return producers
}

// historyIntParam reads one integer query parameter, returning fallback when it
// is absent.
//
// ⚠️ A present-but-unparseable value is a 400 rather than a fallback, and the
// distinction is the whole point. Falling back would answer a malformed request
// with a perfectly plausible page, so a caller with a typo (`?limit=1o00`) would
// silently page through a truncated history and never learn why — the failure
// would surface as wrong COUNTS, which is exactly what this endpoint exists to
// produce. Absent means default; wrong means error.
func historyIntParam(
	ctx context.Context,
	req *http.Request,
	name string,
	fallback int,
) (int, error) {
	raw := req.URL.Query().Get(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, libhttp.WrapWithCode(
			errors.Wrapf(ctx, err, "parse %s failed", name),
			libhttp.ErrorCodeValidation,
			http.StatusBadRequest,
		)
	}
	if value < 0 {
		return 0, libhttp.WrapWithCode(
			errors.New(ctx, name+" must not be negative"),
			libhttp.ErrorCodeValidation,
			http.StatusBadRequest,
		)
	}
	return value, nil
}
