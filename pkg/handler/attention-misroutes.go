// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"net/http"
	stdtime "time"

	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"

	"github.com/bborbe/attention-controller/pkg"
)

// misrouteWeekParam is the query parameter naming the ISO week to count.
const misrouteWeekParam = "week"

// NewAttentionMisroutesHandler creates the weekly per-producer misroute count.
//
// It is the reader the attention item schema names for a flag: "the weekly
// per-producer misroute count, grouped by producer_id / producer_kind and by
// reason. Flags are evidence for a routing fix, never triggers."
//
// ⚠️ It reads the store through History(0, 0) — one unbounded read, not a
// paginated walk. The history handler's own note records that a paginated walk
// is not stable while an item is expiring: rows shift position and a slow walk
// can drop one at no offset at all. A single read cannot lose a row that way,
// and a weekly endpoint is called rarely enough that the unbounded decode is the
// cheaper side of that trade.
//
// ⚠️ The population is therefore exactly what History returns, which excludes an
// expired OPEN item. A flag lives only on an open card, so a card that expires
// before its week is counted is absent from the count — and absent from the
// independent cross-check too, which is what keeps the two sides comparable.
//
// ⚠️ A malformed or absent `week` is a 400, never a fallback. Zero is a real
// answer here (a week nobody flagged in), so a fallback would make a typo
// indistinguishable from a quiet week.
func NewAttentionMisroutesHandler(store pkg.AttentionStore) http.Handler {
	return libhttp.NewJSONErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
				raw := req.URL.Query().Get(misrouteWeekParam)
				if raw == "" {
					return libhttp.WrapWithCode(
						errors.Wrapf(
							ctx,
							pkg.ErrMisrouteWeekInvalid,
							"week parameter is required (e.g. ?week=2026W44)",
						),
						libhttp.ErrorCodeValidation,
						http.StatusBadRequest,
					)
				}
				week, err := pkg.ParseMisrouteWeek(ctx, raw)
				if err != nil {
					return libhttp.WrapWithCode(
						err,
						libhttp.ErrorCodeValidation,
						http.StatusBadRequest,
					)
				}
				items, err := store.History(ctx, 0, 0)
				if err != nil {
					return libhttp.WrapWithCode(
						errors.Wrap(ctx, err, "history failed"),
						libhttp.ErrorCodeInternal,
						http.StatusInternalServerError,
					)
				}
				if err := libhttp.SendJSONResponse(
					ctx,
					resp,
					pkg.CountMisroutes(items, *week, stdtime.Local),
					http.StatusOK,
				); err != nil {
					return errors.Wrap(ctx, err, "send response failed")
				}
				return nil
			},
		),
	)
}
