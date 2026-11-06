// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"
	"github.com/gorilla/mux"

	"github.com/bborbe/attention-controller/pkg"
)

// NewAttentionRateHandler creates an HTTP handler that records or clears the
// operator's 👍/👎 rating of an item.
//
// Body: {"rating":"up"}, {"rating":"down"} or {"rating":""} (clear). The
// rating is a measurement only — not an answer, not a transition, not a flag —
// and is accepted in every item state. Any other value, or a body that is not
// JSON, is HTTP 400 RATING_INVALID and writes nothing.
func NewAttentionRateHandler(store pkg.AttentionStore) http.Handler {
	return libhttp.NewJSONErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
				return handleAttentionRate(ctx, resp, req, store)
			},
		),
	)
}

func handleAttentionRate(
	ctx context.Context,
	resp http.ResponseWriter,
	req *http.Request,
	store pkg.AttentionStore,
) error {
	itemID := pkg.ItemID(mux.Vars(req)["itemID"])
	if itemID == "" {
		return libhttp.WrapWithCode(
			errors.New(ctx, "itemID is empty"),
			libhttp.ErrorCodeValidation,
			http.StatusBadRequest,
		)
	}
	var request struct {
		Rating pkg.Rating `json:"rating"`
	}
	if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "decode request failed"),
			pkg.ErrorCodeRatingInvalid,
			http.StatusBadRequest,
			map[string]any{"rating": "request body is not valid JSON"},
		)
	}
	item, err := store.Rate(ctx, itemID, request.Rating)
	if err != nil {
		return wrapRateError(ctx, err, itemID)
	}
	if err := libhttp.SendJSONResponse(ctx, resp, item, http.StatusOK); err != nil {
		return errors.Wrap(ctx, err, "send response failed")
	}
	return nil
}

// wrapRateError maps the store's rate failures onto responses. There is no
// ITEM_NOT_OPEN arm: a rating is accepted in every state.
func wrapRateError(ctx context.Context, err error, itemID pkg.ItemID) error {
	switch {
	case errors.Is(err, pkg.ErrRatingInvalid):
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "rate failed"),
			pkg.ErrorCodeRatingInvalid,
			http.StatusBadRequest,
			map[string]any{"item_id": itemID.String(), "rating": "must be up, down or empty"},
		)
	case errors.Is(err, pkg.ErrItemNotFound):
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "rate failed"),
			libhttp.ErrorCodeNotFound,
			http.StatusNotFound,
			map[string]any{"item_id": itemID.String()},
		)
	case errors.Is(err, pkg.ErrPeerUnavailable):
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "rate failed"),
			pkg.ErrorCodePeerUnavailable,
			http.StatusServiceUnavailable,
			map[string]any{"item_id": itemID.String()},
		)
	default:
		return errors.Wrap(ctx, err, "rate failed")
	}
}
