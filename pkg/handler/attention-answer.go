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

// NewAttentionAnswerHandler creates an HTTP handler that applies open ->
// answered as an atomic compare-and-set.
//
// Exactly one of two concurrent answers transitions the item. The loser gets
// HTTP 409 with code ALREADY_ANSWERED and must read the item back and report it
// as already answered — it must not retry, and it must not route its own answer
// anyway, which would reinstate the duplicate the guard exists to prevent.
func NewAttentionAnswerHandler(store pkg.AttentionStore) http.Handler {
	return libhttp.NewJSONErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
				return handleAttentionAnswer(ctx, resp, req, store)
			},
		),
	)
}

func handleAttentionAnswer(
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
		AnsweredBy string `json:"answered_by"`
		// ResolvedBy is the session that resolved the item, as distinct from
		// AnsweredBy, the arm that carried the answer. Optional: an omitted
		// value stores an item with no resolver recorded, which is what every
		// item answered before this field existed reads as.
		ResolvedBy string `json:"resolved_by"`
		// Decision is what the answer decided — allow or deny — and it is
		// distinct from AnsweredBy for the same reason ResolvedBy is: an arm
		// supplies an allow and a deny alike, so the arm cannot say what was
		// decided. Optional: an omitted value stores an item with no verdict
		// recorded, which is what a message- or ack-class item reads as, and
		// what every item answered before this field existed reads as.
		Decision pkg.Decision `json:"decision"`
	}
	if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "decode request failed"),
			libhttp.ErrorCodeValidation,
			http.StatusBadRequest,
			map[string]any{"reason": "request body is not valid JSON"},
		)
	}
	item, err := store.Answer(ctx, itemID, request.AnsweredBy, request.ResolvedBy, request.Decision)
	if err != nil {
		return wrapAnswerError(ctx, err, itemID)
	}
	if err := libhttp.SendJSONResponse(ctx, resp, item, http.StatusOK); err != nil {
		return errors.Wrap(ctx, err, "send response failed")
	}
	return nil
}

// wrapAnswerError maps the store's three distinct answer failures onto three
// distinct responses, so a caller can tell a lost race from an impossible move
// from a missing item.
func wrapAnswerError(ctx context.Context, err error, itemID pkg.ItemID) error {
	switch {
	case errors.Is(err, pkg.ErrItemNotFound):
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "answer failed"),
			libhttp.ErrorCodeNotFound,
			http.StatusNotFound,
			map[string]any{"item_id": itemID.String()},
		)
	case errors.Is(err, pkg.ErrAlreadyAnswered):
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "answer failed"),
			ErrorCodeAlreadyAnswered,
			http.StatusConflict,
			map[string]any{"item_id": itemID.String()},
		)
	default:
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "answer failed"),
			libhttp.ErrorCodeValidation,
			http.StatusConflict,
			map[string]any{"item_id": itemID.String()},
		)
	}
}
