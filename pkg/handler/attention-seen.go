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

// NewAttentionSeenHandler creates the HTTP handler the board calls once it has
// painted a card. It records the moment from the STORE's own clock: the
// request carries no body and no time, and the store's first write wins.
//
// It answers 204 with no body, diverging from the sibling writes that answer
// 200 with the item: the call is fire-and-forget and the board does nothing
// with a body.
func NewAttentionSeenHandler(store pkg.AttentionStore) http.Handler {
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
				// The body is never read: there is nothing to decode, and a
				// caller-supplied time is a claim rather than a fact.
				if _, err := store.MarkSeen(ctx, itemID); err != nil {
					return wrapSeenError(ctx, err, itemID)
				}
				resp.WriteHeader(http.StatusNoContent)
				return nil
			},
		),
	)
}

// wrapSeenError maps the store's seen-write failures onto responses. A missing
// item — including a federated one this store does not own — is a 404; anything
// else is an internal failure.
func wrapSeenError(ctx context.Context, err error, itemID pkg.ItemID) error {
	if errors.Is(err, pkg.ErrItemNotFound) {
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "mark seen failed"),
			libhttp.ErrorCodeNotFound,
			http.StatusNotFound,
			map[string]any{"item_id": itemID.String()},
		)
	}
	return libhttp.WrapWithDetails(
		errors.Wrap(ctx, err, "mark seen failed"),
		libhttp.ErrorCodeInternal,
		http.StatusInternalServerError,
		map[string]any{"item_id": itemID.String()},
	)
}
