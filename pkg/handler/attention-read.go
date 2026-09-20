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
// This is the read path's liveness half: an item whose session producer has
// exited and that the producer *asked* is omitted — and removed from the store
// as a side effect of this read — while an item it merely *reported* survives
// its producer's exit.
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
