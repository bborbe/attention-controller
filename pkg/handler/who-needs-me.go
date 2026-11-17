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

// NewWhoNeedsMeHandler creates the handler that returns who-needs-me's answer as
// joined JSON data: the open items with their provenance, the registry sessions
// with ages, and the live pane listing.
//
// ⚠️ It returns DATA, not a rendering. Every field is a structured value the
// caller formats itself — the "store stays a store" ruling. It sets
// Content-Type: application/json and writes nothing else.
func NewWhoNeedsMeHandler(reader pkg.WhoNeedsMeReader) http.Handler {
	return libhttp.NewJSONErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, _ *http.Request) error {
				feed, err := reader.Read(ctx)
				if err != nil {
					return libhttp.WrapWithCode(
						errors.Wrap(ctx, err, "read who-needs-me feed failed"),
						libhttp.ErrorCodeInternal,
						http.StatusInternalServerError,
					)
				}
				if err := libhttp.SendJSONResponse(ctx, resp, feed, http.StatusOK); err != nil {
					return errors.Wrap(ctx, err, "send response failed")
				}
				return nil
			},
		),
	)
}
