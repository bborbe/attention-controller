// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"net/http"
)

// NewLegacyHealthHandler creates the liveness probe for the legacy
// pane-addressed jump listener.
//
// ⚠️ It answers plain-text `ok`, which is deliberately NOT the JSON body
// NewHealthzHandler serves on the board's port. This route reproduces the
// contract of the Python server it replaces, and that server answered
// `text/plain` `ok` on `/health` — so a consumer probing it keeps seeing what it
// has always seen. Reusing the board's `/healthz` shape here would change the
// response under a path whose callers were written against the old one, which
// is the class of silent breakage this route exists to avoid.
func NewLegacyHealthHandler() http.Handler {
	return http.HandlerFunc(
		func(resp http.ResponseWriter, _ *http.Request) {
			resp.Header().Set("Content-Type", "text/plain; charset=utf-8")
			resp.WriteHeader(http.StatusOK)
			_, _ = resp.Write([]byte("ok"))
		},
	)
}
