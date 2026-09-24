// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"net/http"

	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"

	"github.com/bborbe/attention-controller/pkg"
)

// NewAttentionHistoryHandler creates an HTTP handler that returns every item
// regardless of state.
//
// It exists for counting, not rendering: the read path returns only open items
// and prunes dead askers, so it cannot say how many items a manager resolved
// or the operator was asked about. This handler never filters and never
// prunes, which is what makes a resolved-versus-escalated split countable.
func NewAttentionHistoryHandler(store pkg.AttentionStore) http.Handler {
	return libhttp.NewJSONErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
				items, err := store.History(ctx)
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
