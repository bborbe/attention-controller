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

	"github.com/bborbe/attention-controller/pkg"
)

// NewAttentionPushHandler creates an HTTP handler that accepts a producer's
// declaration and stores it.
//
// The store owns the transaction, so this is deliberately NOT a
// `NewJSONUpdateErrorHandlerTx` — the compare-and-set and the dedup suppression
// both happen inside the store's own write transaction, and nesting a second
// one here would fail with ErrTransactionAlreadyOpen.
func NewAttentionPushHandler(store pkg.AttentionStore) http.Handler {
	return libhttp.NewJSONErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
				return handleAttentionPush(ctx, resp, req, store)
			},
		),
	)
}

func handleAttentionPush(
	ctx context.Context,
	resp http.ResponseWriter,
	req *http.Request,
	store pkg.AttentionStore,
) error {
	var request pkg.PushRequest
	if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "decode request failed"),
			libhttp.ErrorCodeValidation,
			http.StatusBadRequest,
			map[string]any{"reason": "request body is not valid JSON"},
		)
	}
	if err := validatePushRequest(ctx, request); err != nil {
		return err
	}
	item, err := store.Push(ctx, request)
	if err != nil {
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "push failed"),
			libhttp.ErrorCodeValidation,
			http.StatusBadRequest,
			map[string]any{
				"producer_id": request.ProducerID.String(),
				"dedup_key":   request.DedupKey.String(),
			},
		)
	}
	if err := libhttp.SendJSONResponse(ctx, resp, item, http.StatusCreated); err != nil {
		return errors.Wrap(ctx, err, "send response failed")
	}
	return nil
}

// validatePushRequest rejects a declaration the schema's field rules do not
// allow, before the store is touched.
func validatePushRequest(ctx context.Context, request pkg.PushRequest) error {
	item := pkg.Item{
		ProducerID:      request.ProducerID,
		ProducerKind:    request.ProducerKind,
		LivenessRef:     request.LivenessRef,
		DedupKey:        request.DedupKey,
		InterruptClass:  request.InterruptClass,
		Payload:         request.Payload,
		AnswerMechanism: request.AnswerMechanism,
		State:           pkg.OpenState,
	}
	if err := item.Validate(ctx); err != nil {
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "validate push request failed"),
			libhttp.ErrorCodeValidation,
			http.StatusBadRequest,
			map[string]any{
				"liveness_ref":     request.LivenessRef.String(),
				"producer_kind":    request.ProducerKind.String(),
				"answer_mechanism": request.AnswerMechanism.String(),
			},
		)
	}
	return nil
}
