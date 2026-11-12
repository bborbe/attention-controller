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

// NewAttentionWatcherCycleHandler creates the HTTP handler the attention
// watcher POSTs one cycle duration to.
//
// The watcher is a separate process that exports nothing, so this endpoint is
// how its cycle duration reaches the same registry the board already serves on
// :18080. The alternative — a second exporter on a second port — would need a
// second scrape config, and the watcher already speaks HTTP to this one to post
// cards, so the client side of this endpoint already exists.
//
// `seconds` is a *float64 rather than a float64 on purpose: a request that
// omits the field must be rejected, not observed as 0. A missing field means
// the caller sent the wrong shape, and recording that as a zero-duration cycle
// would draw a real, fast point on a chart whose whole purpose is to show a
// slow one.
func NewAttentionWatcherCycleHandler(metrics pkg.Metrics) http.Handler {
	return libhttp.NewJSONErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
				var request struct {
					Seconds *float64 `json:"seconds"`
				}
				if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
					return libhttp.WrapWithDetails(
						errors.Wrap(ctx, err, "decode request failed"),
						libhttp.ErrorCodeValidation,
						http.StatusBadRequest,
						map[string]any{"reason": "request body is not valid JSON"},
					)
				}
				if request.Seconds == nil {
					return libhttp.WrapWithDetails(
						errors.New(ctx, "seconds is missing"),
						libhttp.ErrorCodeValidation,
						http.StatusBadRequest,
						map[string]any{
							"reason": "seconds is required and must be a number",
						},
					)
				}
				if *request.Seconds < 0 {
					return libhttp.WrapWithDetails(
						errors.New(ctx, "seconds is negative"),
						libhttp.ErrorCodeValidation,
						http.StatusBadRequest,
						map[string]any{
							"reason": "seconds must not be negative",
						},
					)
				}
				metrics.WatcherCycleSecondsHistogramObserve(*request.Seconds)
				resp.WriteHeader(http.StatusNoContent)
				return nil
			},
		),
	)
}
