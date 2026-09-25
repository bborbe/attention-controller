// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"net/http"

	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"
	"github.com/golang/glog"
	"github.com/gorilla/mux"

	"github.com/bborbe/attention-controller/pkg"
)

// NewAttentionJumpHandler creates the endpoint the board's Jump button calls to
// hand an item back to the session that raised it.
//
// ⚠️ It performs the jump **server-side** and answers 204 with no body, rather
// than redirecting the browser at the fleet-jump server. Two reasons, and the
// second is the one the operator asked for:
//
//   - The token never reaches the browser. The fleet-jump server authenticates
//     with a shared token in a query parameter, so a redirect would put it in
//     the Location header of every click; here it stays on the server.
//   - A redirect navigates the browser away from the board. The operator's ask
//     was explicitly "so we dont switch the screen" — a click must switch
//     WezTerm and leave the board exactly where it was.
//
// The pane is re-resolved here rather than carried in the request, because a
// pane id is recycled across tab moves and WezTerm restarts: a request holding
// one would keep pointing at a pane that has since become another session's,
// which is a wrong answer wearing the appearance of a resolved one. An item
// whose pane does not resolve jumps nowhere and says so.
func NewAttentionJumpHandler(
	store pkg.AttentionStore,
	provenance pkg.ProvenanceResolver,
	jumpTokens pkg.JumpTokenReader,
	jumpCaller pkg.JumpCaller,
	jumpBaseURL string,
) http.Handler {
	return libhttp.NewJSONErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
				if err := requireSameOrigin(ctx, req); err != nil {
					return err
				}
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
				pane := provenance.Resolve(ctx, pkg.Items{*item})[item.ItemID].Pane
				if pane == "" {
					return libhttp.WrapWithCode(
						errors.New(ctx, "item has no resolvable pane"),
						libhttp.ErrorCodeNotFound,
						http.StatusNotFound,
					)
				}
				token, err := jumpTokens.Read(ctx)
				if err != nil {
					// The failure is logged; the token value never is, and this
					// branch is the one that would leak it if anything did.
					glog.V(2).Infof("jump token unavailable, refusing jump: %v", err)
					return libhttp.WrapWithCode(
						errors.New(ctx, "jump token unavailable"),
						libhttp.ErrorCodeInternal,
						http.StatusServiceUnavailable,
					)
				}
				// ⚠️ The jump is performed here rather than handed to the browser
				// as a redirect. A redirect would publish the token in the
				// Location header and navigate the board out of view — the exact
				// behaviour the operator asked to remove. The response carries no
				// body, so there is nowhere for the token to land either.
				if err := jumpCaller.Jump(ctx, jumpBaseURL, pane, token); err != nil {
					// The error is logged without the target, which carries the
					// token; the caller gets the failure, never the URL.
					glog.V(2).Infof("jump failed: %v", err)
					return libhttp.WrapWithCode(
						errors.Wrap(ctx, err, "jump failed"),
						libhttp.ErrorCodeInternal,
						http.StatusBadGateway,
					)
				}
				resp.WriteHeader(http.StatusNoContent)
				return nil
			},
		),
	)
}

// requireSameOrigin rejects a cross-site request to the redirect.
//
// ⚠️ The token's whole purpose is to defeat the cross-origin case where a
// visited page fires <img src=".../jump?pane=X">. Routing the jump through this
// origin re-opens that vector — a cross-origin <img src=".../jump/<itemID>">
// would be followed straight through the redirect — so the route checks the
// request itself rather than resting on item-id unguessability. An item id is
// 128-bit crypto-random, which makes guessing impractical rather than
// impossible, and a posture that depends on that is one refactor away from
// being no posture at all.
//
// ⚠️ Sec-Fetch-Site is the gate, not Origin. A same-origin <a href> GET
// navigation sends Sec-Fetch-Site: same-origin but no Origin header at all —
// Origin accompanies CORS requests and non-GET/HEAD same-origin requests — so
// an Origin-only check would reject the very button click this route exists to
// serve. `none` is allowed because it is a direct address-bar or bookmark
// entry, which is not a cross-site vector.
func requireSameOrigin(ctx context.Context, req *http.Request) error {
	switch req.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return nil
	default:
		return libhttp.WrapWithCode(
			errors.New(ctx, "cross-site jump refused"),
			libhttp.ErrorCodeForbidden,
			http.StatusForbidden,
		)
	}
}
