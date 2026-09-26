// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"net/http"

	"github.com/bborbe/attention-controller/pkg"
)

// answeredClientFromRequest composes the client record the store writes onto an
// answered item: the members the server reads from the request itself, plus the
// page's own automation hint.
//
// ⚠️ user_agent and remote_addr are read from the request and never from the
// request body. remote_addr is the only member a caller cannot spoof, so
// accepting it from a body would make the field a declaration like answered_by
// rather than a fact the store holds. user_agent is server-read but
// caller-set — the caller writes its own User-Agent header — so it sits in the
// same weaker class as automation, which is the one member the body carries
// because only the page's own script can read navigator.webdriver.
//
// It returns nil when the request carries nothing to record at all: no user
// agent, no source address and no automation hint. The store then leaves the
// field off the record rather than storing an empty object, which is the
// distinction the pointer exists for — a reader must be able to tell "the store
// recorded nothing about the client" from "the store recorded a client that
// said nothing".
//
// Both routes that set the field — the answer route and the close route —
// compose it here rather than each deriving it, so the two cannot drift.
func answeredClientFromRequest(req *http.Request, automation *bool) *pkg.AnsweredClient {
	userAgent := req.UserAgent()
	remoteAddr := req.RemoteAddr
	if userAgent == "" && remoteAddr == "" && automation == nil {
		return nil
	}
	return &pkg.AnsweredClient{
		UserAgent:  userAgent,
		RemoteAddr: remoteAddr,
		Automation: automation,
	}
}
