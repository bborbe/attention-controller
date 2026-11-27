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

// NewAttentionReadHandler creates an HTTP handler that returns the items an arm
// should render.
//
// ⚠️ The read removes only EXPIRED items, and only as a side effect of the read.
// It used to be the read path's liveness half — an item whose session producer
// had exited and that the producer *asked* was omitted and removed — and that is
// gone: a dead liveness subject no longer removes anything, so a reaped session's
// question and its answer survive until `expires_at` passes. See [[Attention Item
// Schema]] § Expiry.
func NewAttentionReadHandler(store pkg.AttentionStore) http.Handler {
	return libhttp.NewJSONErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
				items, err := store.Read(ctx)
				if err != nil {
					return libhttp.WrapWithCode(
						errors.Wrap(ctx, err, "read failed"),
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

// NewAttentionBoardReadHandler creates the handler the board page reads: the
// open items plus the answered ones it renders as dimmed records. It is
// distinct from NewAttentionReadHandler, which returns only the open items an
// arm should act on — the board must show what was answered, an arm must not.
func NewAttentionBoardReadHandler(store pkg.AttentionStore) http.Handler {
	return libhttp.NewJSONErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
				items, err := store.ReadBoard(ctx)
				if err != nil {
					return libhttp.WrapWithCode(
						errors.Wrap(ctx, err, "read board failed"),
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
