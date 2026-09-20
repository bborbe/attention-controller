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

// NewAttentionGetHandler creates an HTTP handler that returns a single item by
// id, whatever its state.
//
// This is deliberately not the read path. `Read` answers "what should an arm
// render", which is open items only; this answers "what is this item's state",
// which is what a caller needs after a rejected transition and what the race
// loser needs when it reads back to report already-answered. Collapsing the two
// would either leak answered items into the render path or leave a transition's
// outcome unobservable.
func NewAttentionGetHandler(store pkg.AttentionStore) http.Handler {
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
				item, err := store.Get(ctx, itemID)
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
