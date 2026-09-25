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

// NewAttentionEscalateHandler creates an HTTP handler that records which
// session is carrying an item to the operator, as an atomic compare-and-set.
//
// Exactly one of two concurrent escalations stamps the item. The loser gets
// HTTP 409 with code ALREADY_ESCALATED and must read the item back and report
// who holds it — it must not stamp over it, which would erase the only record
// of who is carrying the item.
//
// Re-escalating by the session that already stamped the item is HTTP 200, not a
// conflict: a manager re-running its own sweep must never be blocked by its own
// stamp.
//
// A value that is not a well-formed session id is HTTP 400. § Escalation
// requires a session id here, and the store refuses anything else rather than
// normalizing it — see pkg.SessionID.
func NewAttentionEscalateHandler(store pkg.AttentionStore) http.Handler {
	return libhttp.NewJSONErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
				return handleAttentionEscalate(ctx, resp, req, store)
			},
		),
	)
}

func handleAttentionEscalate(
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
		EscalatedBy string `json:"escalated_by"`
	}
	if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "decode request failed"),
			libhttp.ErrorCodeValidation,
			http.StatusBadRequest,
			map[string]any{"reason": "request body is not valid JSON"},
		)
	}
	if request.EscalatedBy == "" {
		return libhttp.WrapWithDetails(
			errors.New(ctx, "escalated_by is empty"),
			libhttp.ErrorCodeValidation,
			http.StatusBadRequest,
			map[string]any{
				"reason": "escalated_by is required and must name a session",
			},
		)
	}
	item, err := store.Escalate(ctx, itemID, request.EscalatedBy)
	if err != nil {
		return wrapEscalateError(ctx, err, itemID)
	}
	if err := libhttp.SendJSONResponse(ctx, resp, item, http.StatusOK); err != nil {
		return errors.Wrap(ctx, err, "send response failed")
	}
	return nil
}

// wrapEscalateError maps the store's escalation failures onto distinct
// responses, so a caller can tell a lost race from an item that already left
// the queue from a missing item.
func wrapEscalateError(ctx context.Context, err error, itemID pkg.ItemID) error {
	switch {
	case errors.Is(err, pkg.ErrInvalidSessionID):
		// A malformed session id is refused, not repaired. The store owns this
		// rule rather than the handler, so every caller is covered; the empty
		// check above stays only because it names the missing field more
		// directly than the pattern can.
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "escalate failed"),
			libhttp.ErrorCodeValidation,
			http.StatusBadRequest,
			map[string]any{"item_id": itemID.String()},
		)
	case errors.Is(err, pkg.ErrItemNotFound):
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "escalate failed"),
			libhttp.ErrorCodeNotFound,
			http.StatusNotFound,
			map[string]any{"item_id": itemID.String()},
		)
	case errors.Is(err, pkg.ErrAlreadyEscalated):
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "escalate failed"),
			ErrorCodeAlreadyEscalated,
			http.StatusConflict,
			map[string]any{"item_id": itemID.String()},
		)
	case errors.Is(err, pkg.ErrItemNotOpen):
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "escalate failed"),
			ErrorCodeItemNotOpen,
			http.StatusConflict,
			map[string]any{"item_id": itemID.String()},
		)
	default:
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "escalate failed"),
			libhttp.ErrorCodeValidation,
			http.StatusConflict,
			map[string]any{"item_id": itemID.String()},
		)
	}
}
