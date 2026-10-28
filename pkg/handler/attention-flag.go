// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"
	"github.com/gorilla/mux"

	"github.com/bborbe/attention-controller/pkg"
)

// NewAttentionFlagHandler creates an HTTP handler that records that the
// operator believes an item should never have reached them.
//
// A flag is a field write on an open item, not a transition and not an answer:
// the item stays open and answerable afterwards. A flag with no usable reason —
// empty, whitespace-only or absent — is refused with HTTP 400 and writes
// nothing, because a flag without a reason is indistinguishable from a grumble
// and cannot be counted. A body that is malformed or not JSON is the same
// refusal: there is no reason in it.
func NewAttentionFlagHandler(store pkg.AttentionStore) http.Handler {
	return libhttp.NewJSONErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
				return handleAttentionFlag(ctx, resp, req, store)
			},
		),
	)
}

func handleAttentionFlag(
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
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
		// The contract's failure table makes a malformed body the same refusal
		// as a missing reason: no usable reason was carried, so the caller is
		// told the reason is what is required rather than that the JSON is bad.
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "decode request failed"),
			pkg.ErrorCodeFlagReasonRequired,
			http.StatusBadRequest,
			map[string]any{"reason": "request body is not valid JSON"},
		)
	}
	// Checked here before the store call, and the store re-checks: this arm
	// names the missing field directly, while the store's own rule covers every
	// caller that does not come through HTTP.
	if strings.TrimSpace(request.Reason) == "" {
		return libhttp.WrapWithDetails(
			errors.New(ctx, "reason is empty"),
			pkg.ErrorCodeFlagReasonRequired,
			http.StatusBadRequest,
			map[string]any{"reason": "reason is required and must not be blank"},
		)
	}
	item, err := store.Flag(ctx, itemID, request.Reason)
	if err != nil {
		return wrapFlagError(ctx, err, itemID, "flag failed")
	}
	if err := libhttp.SendJSONResponse(ctx, resp, item, http.StatusOK); err != nil {
		return errors.Wrap(ctx, err, "send response failed")
	}
	return nil
}

// wrapFlagError maps the store's flag and unflag failures onto responses, so a
// missing item, an item that left the queue and an unavailable peer are
// distinguishable by the caller.
//
// ⚠️ `what` names the operation the CALLER asked for — "flag failed" from
// handleAttentionFlag and "unflag failed" from handleAttentionUnflag — because
// this helper has two callers. Hardcoding one verb here would put "flag failed"
// into the body of a failed unflag, the same mislabel wrapItemError documents.
func wrapFlagError(ctx context.Context, err error, itemID pkg.ItemID, what string) error {
	switch {
	case errors.Is(err, pkg.ErrFlagReasonRequired):
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, what),
			pkg.ErrorCodeFlagReasonRequired,
			http.StatusBadRequest,
			map[string]any{"item_id": itemID.String()},
		)
	case errors.Is(err, pkg.ErrItemNotFound):
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, what),
			libhttp.ErrorCodeNotFound,
			http.StatusNotFound,
			map[string]any{"item_id": itemID.String()},
		)
	case errors.Is(err, pkg.ErrItemNotOpen):
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, what),
			pkg.ErrorCodeItemNotOpen,
			http.StatusConflict,
			map[string]any{"item_id": itemID.String()},
		)
	case errors.Is(err, pkg.ErrPeerUnavailable):
		// ⚠️ Before the catch-all, as in wrapItemError and wrapEscalateError: a
		// failed peer call is not a malformed request, and it is the only failure
		// here a retry can clear.
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, what),
			pkg.ErrorCodePeerUnavailable,
			http.StatusServiceUnavailable,
			map[string]any{"item_id": itemID.String()},
		)
	default:
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, what),
			libhttp.ErrorCodeValidation,
			http.StatusConflict,
			map[string]any{"item_id": itemID.String()},
		)
	}
}
