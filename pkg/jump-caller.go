// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	liberrors "github.com/bborbe/errors"
	"github.com/golang/glog"
)

// ErrJumpRefused is returned when the fleet-jump server answers a jump with a
// non-success status.
var ErrJumpRefused = errors.New("jump server refused the request")

//counterfeiter:generate -o ../mocks/jump-caller.go --fake-name JumpCaller . JumpCaller

// JumpCaller asks the fleet-jump server to activate a pane.
//
// ⚠️ The token is a credential. An implementation must never log it, never put
// it in an error message, and never write it anywhere but the request it is
// building. It is passed in rather than read here so the caller that already
// holds it stays the only thing that does.
//
// It exists as an interface so the board's handler is testable without a live
// fleet-jump server, and so the jump stays one injectable capability rather
// than an http call buried in the handler.
type JumpCaller interface {
	// Jump activates pane on the fleet-jump server at baseURL, authenticating
	// with token. It returns an error when the server does not accept the jump,
	// so the caller can report a failed jump rather than a silent no-op.
	Jump(ctx context.Context, baseURL, pane, token string) error
}

// NewJumpCaller creates the HTTP caller the board's Jump button goes through.
//
// The client is injected so the caller inherits its timeout: a jump that hangs
// must not hold the request open, and the board must not appear to succeed on a
// request that never completed.
func NewJumpCaller(client *http.Client) JumpCaller {
	return &jumpCaller{client: client}
}

type jumpCaller struct {
	client *http.Client
}

func (c *jumpCaller) Jump(ctx context.Context, baseURL, pane, token string) error {
	// Encoded rather than interpolated: a token containing '&' or '#' would
	// otherwise inject a parameter or truncate itself.
	target := baseURL + "/jump?" + url.Values{
		"pane": {pane},
		"t":    {token},
	}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return liberrors.Wrap(ctx, err, "build jump request failed")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		// The transport error carries the URL, which carries the token — so it
		// is wrapped without it and the caller reports the failure, never the
		// target.
		glog.V(2).Infof("jump pane %s failed: %v", pane, err)
		return liberrors.Wrap(ctx, err, "jump request failed")
	}
	defer resp.Body.Close()
	// The boundary call's audit line: which pane, and what the server said.
	// ⚠️ Never the target — it carries the token. Latency is deliberately absent:
	// measuring it needs time.Now(), which this repo forbids in favour of an
	// injected clock, and a clock dependency is not worth carrying into a
	// one-request caller for a number nobody reads.
	glog.V(2).Infof("jump pane %s: status=%d", pane, resp.StatusCode)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return liberrors.Wrapf(ctx, ErrJumpRefused, "jump server returned %d", resp.StatusCode)
	}
	return nil
}
