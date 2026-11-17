// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"net/http"
)

// NewShutdownAwareHandler serves next under a request context that also ends
// when done closes, so a long-lived response ends when the process shuts down.
//
// ⚠️ It exists for the board's live channel. http.Server.Shutdown closes the
// listener and waits for in-flight requests to finish, but it never cancels
// their contexts — so a `text/event-stream` response, which ends only when its
// request context does, outlives every graceful drain: Shutdown ran into its
// timeout with the streams still attached, and the process hung on SIGTERM.
// Wrapping the stream route with the process lifecycle's done channel ends each
// stream as soon as shutdown begins, which lets the drain finish at once.
//
// It is applied to the stream route only, never to the whole router: an
// ordinary request in flight at SIGTERM — an answer being recorded — must be
// allowed to finish under the drain, not be cancelled by it.
func NewShutdownAwareHandler(done <-chan struct{}, next http.Handler) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithCancel(req.Context())
		defer cancel()
		go func() {
			select {
			case <-done:
				cancel()
			case <-ctx.Done():
			}
		}()
		next.ServeHTTP(resp, req.WithContext(ctx))
	})
}
