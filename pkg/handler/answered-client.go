// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"net/http"

	"github.com/bborbe/attention-controller/pkg"
)

// answeredClientFromRequest composes the client record the store writes onto an
// answered item: the two members the server derives, read from the request
// itself, plus the page's own automation hint.
//
// ⚠️ user_agent and remote_addr are read from the request and never from the
// request body. A caller can lie about its own automation, but cannot lie about
// its own source address, so those two are the load-bearing members — accepting
// either from a body would make the field a declaration like answered_by rather
// than a fact the store holds. automation is the one member the body carries,
// because only the page's own script can read navigator.webdriver, and it is
// explicitly the weaker member.
//
// Both routes that set the field — the answer route and the close route —
// compose it here rather than each deriving it, so the two cannot drift.
func answeredClientFromRequest(req *http.Request, automation *bool) *pkg.AnsweredClient {
	return &pkg.AnsweredClient{
		UserAgent:  req.UserAgent(),
		RemoteAddr: req.RemoteAddr,
		Automation: automation,
	}
}
