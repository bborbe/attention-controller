// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

// AnsweredClient is what the store can say about the client that posted an
// answer — derived from the request, never declared by it.
//
// It exists because AnsweredBy is a caller declaration: a human clicking Submit
// answer and a scripted client posting the identical body write the identical
// value there, so an arm or a sweep that reads `answered` as *the operator saw
// it* is corrupted by it. This field is store-written for the same reason
// EscalatedAt is: a fact the store already holds is not one to accept from a
// caller.
//
// The server-read members are the load-bearing ones: the store reads them from
// the HTTP request itself and never accepts them from the request body.
// RemoteAddr is the only member a caller cannot spoof. UserAgent is
// server-read but caller-set — the caller writes its own User-Agent header —
// so it sits in the same weaker class as Automation, which is the one member
// the body carries.
type AnsweredClient struct {
	// UserAgent is the request's own User-Agent header, read by the store from
	// the request. Never accepted from the request body.
	UserAgent string `json:"user_agent,omitempty"`
	// RemoteAddr is the request's own source address, read by the store from the
	// request. Never accepted from the request body.
	RemoteAddr string `json:"remote_addr,omitempty"`
	// ForwardedFor is the request's X-Forwarded-For header, recorded when the
	// header is present.
	//
	// ⚠️ It is recorded BESIDE RemoteAddr, never in place of it, and it does not
	// share RemoteAddr's trust class. A caller sets this header freely, so it is
	// a declaration like UserAgent and Automation — while RemoteAddr remains the
	// one member a caller cannot spoof. Honouring it as the client's identity
	// requires a trusted-proxy allowlist the store does not have; recording it
	// keeps what the request claimed without letting that claim overwrite a fact.
	ForwardedFor string `json:"forwarded_for,omitempty"`
	// Automation is the page's own navigator.webdriver reading — the one member
	// the body carries, because only the page's own script can read it. It sits
	// in the weaker class with UserAgent: it is a client declaration, and a
	// scripted client can lie about it.
	//
	// It is a pointer so that absent is distinguishable from false. An item
	// answered before this field existed, and a request that omits the hint, must
	// not read as a positive claim that the client was not automated.
	Automation *bool `json:"automation,omitempty"`
}
