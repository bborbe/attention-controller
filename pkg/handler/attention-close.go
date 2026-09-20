// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"net/http"

	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"
	"github.com/gorilla/mux"

	"github.com/bborbe/attention-controller/pkg"
)

// NewAttentionCloseHandler creates an HTTP handler that applies open -> closed
// or answered -> closed.
//
// `closed` is terminal: reopening is a new item with a new item_id, not a state
// change, so an item's history is never rewritten. Closing an item that is
// already closed is rejected as an illegal transition rather than treated as a
// no-op, because the schema's table has no `closed -> closed` row.
func NewAttentionCloseHandler(store pkg.AttentionStore) http.Handler {
	return libhttp.NewJSONErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
				itemID := pkg.ItemID(mux.Vars(req)["itemID"])
				if itemID == "" {
					return libhttp.WrapWithCode(
						errors.New(ctx, "itemID is empty"),
						libhttp.ErrorCodeValidation,
						http.StatusBadRequest,
					)
				}
				item, err := store.Close(ctx, itemID)
				if err != nil {
					return wrapTransitionError(ctx, err, itemID)
				}
				if err := libhttp.SendJSONResponse(ctx, resp, item, http.StatusOK); err != nil {
					return errors.Wrap(ctx, err, "send response failed")
				}
				return nil
			},
		),
	)
}

// wrapTransitionError maps the store's close failures onto responses, so an
// illegal move and a missing item are distinguishable by the caller.
func wrapTransitionError(ctx context.Context, err error, itemID pkg.ItemID) error {
	if errors.Is(err, pkg.ErrItemNotFound) {
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "close failed"),
			libhttp.ErrorCodeNotFound,
			http.StatusNotFound,
			map[string]any{"item_id": itemID.String()},
		)
	}
	return libhttp.WrapWithDetails(
		errors.Wrap(ctx, err, "close failed"),
		libhttp.ErrorCodeValidation,
		http.StatusConflict,
		map[string]any{"item_id": itemID.String()},
	)
}
