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

// NewAttentionAttemptGetHandler creates an HTTP handler that answers "did this
// item's answer reach the session?" in one query.
//
// It returns the DERIVED status, never the raw record. `never_attempted` and
// `pre_trail_unknown` are stored nowhere, and a caller should not have to know
// the epoch rule to read an answer's fate.
func NewAttentionAttemptGetHandler(store pkg.AttentionStore) http.Handler {
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
				report, err := store.Delivery(ctx, itemID)
				if err != nil {
					return wrapAttemptError(ctx, err, itemID)
				}
				if err := libhttp.SendJSONResponse(ctx, resp, report, http.StatusOK); err != nil {
					return errors.Wrap(ctx, err, "send response failed")
				}
				return nil
			},
		),
	)
}

// NewAttentionAttemptRecordHandler creates an HTTP handler the attempting arm
// calls to record what it observed.
//
// ⚠️ The caller is the arm that ATTEMPTS delivery, never the arm that records
// the answer. The answering script resolves a target and delivers nothing, so
// a caller that posted here at answer time would make every answer read as
// attempted — which is the defect this record exists to close.
func NewAttentionAttemptRecordHandler(store pkg.AttentionStore) http.Handler {
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
				var request struct {
					Carrier string              `json:"carrier"`
					Outcome pkg.DeliveryOutcome `json:"outcome"`
				}
				if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
					return libhttp.WrapWithDetails(
						errors.Wrap(ctx, err, "decode request failed"),
						libhttp.ErrorCodeValidation,
						http.StatusBadRequest,
						map[string]any{"reason": "request body is not valid JSON"},
					)
				}
				if request.Carrier == "" {
					return libhttp.WrapWithDetails(
						errors.New(ctx, "carrier is empty"),
						libhttp.ErrorCodeValidation,
						http.StatusBadRequest,
						map[string]any{
							"reason": "carrier is required and must name the arm that attempted delivery",
						},
					)
				}
				// Validated here as well as in the store, so a caller learns its
				// outcome is rejected from the response rather than from a record
				// it cannot read back.
				if err := request.Outcome.Validate(ctx); err != nil {
					return libhttp.WrapWithDetails(
						errors.Wrap(ctx, err, "invalid outcome"),
						libhttp.ErrorCodeValidation,
						http.StatusBadRequest,
						map[string]any{
							"reason": "outcome must be one of delivered, failed",
						},
					)
				}
				attempt, err := store.RecordAttempt(ctx, itemID, request.Carrier, request.Outcome)
				if err != nil {
					return wrapAttemptError(ctx, err, itemID)
				}
				if err := libhttp.SendJSONResponse(ctx, resp, attempt, http.StatusOK); err != nil {
					return errors.Wrap(ctx, err, "send response failed")
				}
				return nil
			},
		),
	)
}

// wrapAttemptError maps the store's failures onto distinct responses, so a
// caller can tell a missing item from a rejected declaration.
func wrapAttemptError(ctx context.Context, err error, itemID pkg.ItemID) error {
	if errors.Is(err, pkg.ErrItemNotFound) {
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "attempt failed"),
			libhttp.ErrorCodeNotFound,
			http.StatusNotFound,
			map[string]any{"item_id": itemID.String()},
		)
	}
	return libhttp.WrapWithDetails(
		errors.Wrap(ctx, err, "attempt failed"),
		libhttp.ErrorCodeValidation,
		http.StatusBadRequest,
		map[string]any{"item_id": itemID.String()},
	)
}
