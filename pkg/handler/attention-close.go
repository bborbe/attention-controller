// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"encoding/json"
	"io"
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
				// The body is optional, and an empty one is the shape every
				// caller of this route used before the field existed: a close
				// with no arm — a producer withdrawing its own item, or the
				// store on a producer-exit sweep — carries none. A body that is
				// present but malformed is still rejected.
				var request struct {
					// AnsweredBy names the arm that caused the close. It is
					// recorded on the open -> closed row only, so an `ack`
					// item acknowledged from the board reads back with
					// answered_by set and answered_at unset.
					AnsweredBy string `json:"answered_by"`
					// Automation is the page's own navigator.webdriver reading,
					// and it is the only member of the answered client the body
					// may carry: user_agent and remote_addr are read from the
					// request itself, so a body carrying either has nowhere to
					// land and is ignored rather than honoured.
					//
					// It is a pointer so an omitted hint is absent rather than a
					// positive claim that the client was not automated. Optional,
					// exactly as AnsweredBy is.
					Automation *bool `json:"automation"`
				}
				if err := json.NewDecoder(req.Body).Decode(&request); err != nil &&
					!errors.Is(err, io.EOF) {
					return libhttp.WrapWithDetails(
						errors.Wrap(ctx, err, "decode request failed"),
						libhttp.ErrorCodeValidation,
						http.StatusBadRequest,
						map[string]any{"reason": "request body is not valid JSON"},
					)
				}
				// Composed here rather than accepted from the body, for the same
				// reason the answer route composes it: the two derived members
				// come from the request itself. The store writes it on the
				// arm-caused open -> closed row only.
				item, err := store.Close(
					ctx,
					itemID,
					request.AnsweredBy,
					answeredClientFromRequest(req, request.Automation),
				)
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

// wrapTransitionError maps the store's failures onto responses, so an illegal
// move, a missing item and an unavailable peer are distinguishable by the
// caller.
func wrapTransitionError(ctx context.Context, err error, itemID pkg.ItemID) error {
	switch {
	case errors.Is(err, pkg.ErrItemNotFound):
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "close failed"),
			libhttp.ErrorCodeNotFound,
			http.StatusNotFound,
			map[string]any{"item_id": itemID.String()},
		)
	case errors.Is(err, pkg.ErrPeerUnavailable):
		// ⚠️ Checked BEFORE the catch-all, and the order is load-bearing: this
		// mapping is otherwise binary, so without this branch a failed peer call
		// arrives as 409 VALIDATION_ERROR and reads as a malformed request —
		// when in fact it is the one failure here that is NOT the caller's fault
		// and IS worth retrying.
		//
		// ⚠️ It is reached from Get as well as Close (attention-get.go,
		// attention-jump.go), which is why the status alone cannot be read as
		// "the close failed".
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "close failed"),
			pkg.ErrorCodePeerUnavailable,
			http.StatusServiceUnavailable,
			map[string]any{"item_id": itemID.String()},
		)
	default:
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "close failed"),
			libhttp.ErrorCodeValidation,
			http.StatusConflict,
			map[string]any{"item_id": itemID.String()},
		)
	}
}
