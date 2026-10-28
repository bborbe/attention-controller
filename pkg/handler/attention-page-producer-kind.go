// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

// The board's producer-kind view filter: the query parameter it reads, the URL
// its control points at, and the split it applies to the rows the store
// returned.
//
// ⚠️ It lives in its own file rather than beside the page handler because
// attention-page.go sits at the revive file-length ceiling, and this is the one
// cohesive unit in it that can move without dragging the template or the
// handler with it. Nothing here renders: the template reads only the values the
// handler computes from these functions.

import (
	"context"
	"net/http"
	"net/url"

	"github.com/bborbe/errors"

	"github.com/bborbe/attention-controller/pkg"
)

// boardProducerKindParam is the query parameter carrying the board's
// producer-kind filter, and boardProducerKindAll is the value that turns it
// off. The parameter follows boardHideParam's shape on purpose — the same
// parse-validate-default reading, the same write-back by its control — so the
// two view controls compose instead of each owning a private convention.
//
// ⚠️ The default is `manager`, and it is the operator's decision rather than a
// guess: card `7ca5385d5d226b1463778ac2e5f18b56`, 2026-10-28 — *"filter on
// producer_kind (manager kind only)"*. The axis is a KIND, not a manager-tier
// list, because `producer_kind` is the field an item actually carries; there is
// no `roles=` vocabulary anywhere in the store.
//
// ⚠️ `all` rather than an absent parameter is the show-everything spelling, for
// the same reason boardHideNone exists: a deleted parameter is indistinguishable
// from a defaulted one, so the control needs a value it can write back into the
// URL and a reload must reproduce the view.
const (
	boardProducerKindParam = "producer_kind"
	boardProducerKindAll   = "all"
)

// boardProducerKindHref is the URL the kind link points at: the current request
// with only `producer_kind` replaced, so every other view parameter — `hide`
// above all — survives the toggle.
//
// ⚠️ This is the NO-JS value only. The page's script rebuilds the link from
// window.location after every URL write (syncKindHref), because the answered
// toggle rewrites the query with replaceState and a link left holding the
// load-time query silently resets the other filter when it is followed — the
// exact mutual reset this function exists to prevent. The server-rendered href
// is right only when nothing has rewritten the URL since the load.
func boardProducerKindHref(current *url.URL, allKinds bool) string {
	next := current.Query()
	if allKinds {
		next.Set(boardProducerKindParam, pkg.ManagerProducerKind.String())
	} else {
		next.Set(boardProducerKindParam, boardProducerKindAll)
	}
	clone := *current
	clone.RawQuery = next.Encode()
	return clone.RequestURI()
}

// boardProducerKindOf reads the board's producer-kind filter from the request.
//
// It answers allKinds=true for the explicit `all` value and the default kind
// otherwise. ⚠️ An unrecognised value is an ERROR rather than a silent
// fallback, which is the opposite of boardHideParam's rule and deliberately so:
// a typo in `hide` costs the operator a filter they did not ask for, while a
// typo here would render the wrong population and read as "no cards of that
// kind" — a claim the board never checked.
func boardProducerKindOf(
	ctx context.Context,
	req *http.Request,
) (pkg.ProducerKind, bool, error) {
	raw := req.URL.Query().Get(boardProducerKindParam)
	if raw == "" {
		return pkg.ManagerProducerKind, false, nil
	}
	if raw == boardProducerKindAll {
		return "", true, nil
	}
	kind := pkg.ProducerKind(raw)
	if err := kind.Validate(ctx); err != nil {
		return "", false, errors.Wrap(ctx, err, "validate producer_kind failed")
	}
	return kind, false, nil
}

// filterByProducerKind splits the rendered rows into the ones the board shows
// and the count it is hiding.
//
// ⚠️ It runs over the rows the store returned rather than being pushed into the
// store query: the hidden count has to describe the same population the page
// renders, and a second read would let the two disagree. boardHideParam is a
// rendering input for exactly the same reason.
//
// The rows themselves are returned untouched when the filter is off, so the
// all-kinds view allocates nothing and cannot reorder what the store gave.
//
// ⚠️ The count is "rows the kind filter removes that the operator would
// otherwise be looking at", so it depends on the OTHER filter's position rather
// than on a fixed notion of openness. ReadBoard returns answered items too, and
// the page renders them dimmed:
//
//   - with `hide` ON (the default) the answered filter already parks them, so
//     counting them would report "6 hidden" on a board whose all-kinds view
//     reveals one open card;
//   - with `hide=none` those records ARE on screen, so the kind filter really
//     is hiding them and the count must include them.
//
// A fixed "open rows only" rule is wrong in the second case, and a fixed "every
// non-matching row" rule is wrong in the first. Neither the count nor the empty
// state may contradict what the board is showing.
func filterByProducerKind(
	rows []attentionPageRow,
	kind pkg.ProducerKind,
	allKinds bool,
	hideAnswered bool,
) ([]attentionPageRow, int) {
	if allKinds {
		return rows, 0
	}
	shown := make([]attentionPageRow, 0, len(rows))
	hidden := 0
	for _, row := range rows {
		if row.Item.ProducerKind == kind {
			shown = append(shown, row)
			continue
		}
		if !row.Dimmed || !hideAnswered {
			hidden++
		}
	}
	return shown, hidden
}
