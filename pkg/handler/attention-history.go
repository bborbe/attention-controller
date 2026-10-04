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

	// historyDefaultLimit bounds the response when the caller names no limit.
	//
	// ⚠️ It is sized from the payload rather than from taste. Rows average ~840
	// bytes, so 1000 is ~0.8 MB — comfortably under the 2 MB the criterion pins,
	// and roughly 400x below the 18.8 MB the unbounded read returned. A caller
	// that genuinely needs the whole store can still ask for it with `limit=0`;
	// what changed is that it has to ASK.
	historyDefaultLimit = 1000
)

// NewAttentionHistoryHandler creates an HTTP handler that returns items
// regardless of state, newest-first and paginated.
//
// It exists for counting, not rendering: the read path returns only open items
// and prunes dead askers, so it cannot say how many items a manager resolved
// or the operator was asked about. This handler never filters and never
// prunes, which is what makes a resolved-versus-escalated split countable.
//
// ⚠️ Pagination bounds the RESPONSE, and it does not change what the endpoint
// means: every row is still reachable, and a consumer that counts must walk the
// pages rather than read one. The response stays a bare JSON array — the REST
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
				offset, err := historyIntParam(ctx, req, historyOffsetParam, 0)
				if err != nil {
					return err
				}
				items, err := store.History(ctx, limit, offset)
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
