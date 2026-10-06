// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"crypto/subtle"
	"net/http"
	"strings"

	libhttp "github.com/bborbe/http"
	"github.com/golang/glog"
)

// bearerTokenPrefix is the authorization scheme an accepted request carries.
// The match is case-sensitive: the clients this listener serves send the
// exact `Bearer ` scheme, and accepting other spellings would widen the
// accepted surface for no caller's benefit.
const bearerTokenPrefix = "Bearer "

// unauthorizedBody is the ONE body this handler writes on every refusal.
//
// ⚠️ It is a constant, and every refusal path writes it through one helper,
// because the three refusal shapes — missing header, malformed header and
// wrong token — MUST be byte-identical. A body that varied by shape would
// make the endpoint an oracle a caller could probe for token validity.
//
// ⚠️ The shape is libhttp's canonical `{error: {code, message, details}}`
// envelope — the same shape every other handler in this service emits through
// libhttp.WrapWithCode — not a bare string. A client must not need one parse
// path for this refusal and a different one for every other error the service
// returns. `code` is libhttp.ErrorCodeUnauthorized, concatenated from the
// constant so the two cannot drift; `message` is the fixed word below and
// names no detail about the expected value, so the body stays uniform across
// all three refusal shapes.
const unauthorizedBody = `{"error":{"code":"` + libhttp.ErrorCodeUnauthorized + `","message":"unauthorized"}}`

// NewBearerTokenHandler gates a listener's routes behind a shared bearer
// token.
//
// It wraps next, so the routes behind it are unchanged: a request that
// presents the configured token in `Authorization: Bearer <token>` is passed
// straight through, and every other request is refused before next is called.
//
// ⚠️ **A missing header, a malformed header and a wrong token are refused
// identically** — same status, same body — so the endpoint cannot be probed to
// learn whether a presented token is close to correct. The refusal names no
// detail about the expected value: no length, no hint, no reason.
//
// ⚠️ **The comparison is constant time.** A byte-by-byte comparison leaks the
// token's prefix through timing, which turns an unguessable token into a
// guessable one for an attacker who can measure — and a caller on the same
// cluster can. See requireJumpToken for the same discipline on the jump route.
//
// ⚠️ **It fails closed.** A handler built with an empty token refuses every
// request rather than admitting everyone: the comparison alone would treat an
// empty presented value as a match for an empty configured token, so the empty
// case is refused before it is reached. The wired service never builds one —
// the listener is not started when the token is empty — but this unit is safe
// on its own.
//
// ⚠️ The token is a credential. It is never logged, never carried in an error
// and never rendered; a refusal is logged by shape only.
func NewBearerTokenHandler(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		presented, wellFormed := bearerToken(req)
		if token == "" || !wellFormed ||
			subtle.ConstantTimeCompare([]byte(presented), []byte(token)) != 1 {
			// ⚠️ Logged by shape only. Whether a well-formed credential was
			// present is a fact about the request; its value is a credential and
			// never reaches the log.
			glog.V(2).Infof("attention store api refused: token_present=%t", wellFormed)
			writeUnauthorized(resp)
			return
		}
		// ⚠️ Nothing is written on the admitted path. A header, status or body
		// written here would be sent before the endpoint's own response and
		// corrupt it.
		next.ServeHTTP(resp, req)
	})
}

// bearerToken extracts the presented bearer token from the request, reporting
// whether the Authorization header is well formed.
//
// A missing header, a header carrying another scheme (`Basic ...`, a bare
// token) and the scheme with no value all report false. ⚠️ The remainder is
// NOT trimmed: `Authorization: Bearer  abc` presents `" abc"`, which does not
// match `"abc"` and is refused — normalising it would silently widen what the
// check accepts.
func bearerToken(req *http.Request) (string, bool) {
	authorization := req.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, bearerTokenPrefix) {
		return "", false
	}
	presented := strings.TrimPrefix(authorization, bearerTokenPrefix)
	if presented == "" {
		return "", false
	}
	return presented, true
}

// writeUnauthorized writes the one refusal response every rejection shares.
//
// ⚠️ It is the single place a refusal is written, so the three refusal shapes
// cannot drift into distinguishable bodies. Header before WriteHeader,
// WriteHeader before Write — after WriteHeader the header map is already sent
// and a later Set has no effect.
func writeUnauthorized(resp http.ResponseWriter) {
	resp.Header().Add(libhttp.ContentTypeHeaderName, libhttp.ApplicationJSONContentType)
	resp.WriteHeader(http.StatusUnauthorized)
	_, _ = resp.Write([]byte(unauthorizedBody))
}
