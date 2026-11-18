// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bborbe/errors"

	"github.com/bborbe/attention-controller/pkg"
)

// postHTTPTimeout bounds one card post.
const postHTTPTimeout = 10 * time.Second

// HTTPCardPoster posts to the board's own push route.
//
// ⚠️ It deliberately does NOT use `attention-ask.py`: that script lives only
// under a versioned plugin cache, so a path pinned here would rot on the next
// plugin update. `POST /api/1.0/attention` is the board's stable public entry
// point and is the same route every producer uses.
type HTTPCardPoster struct {
	// URL is the push route, e.g. http://localhost:18080/api/1.0/attention.
	URL string
	// Client issues the request. Defaults to a client with postHTTPTimeout.
	Client *http.Client
}

// Post pushes one card and requires the 201 the route answers a new item with.
func (p *HTTPCardPoster) Post(ctx context.Context, request pkg.PushRequest) error {
	body, err := json.Marshal(request)
	if err != nil {
		return errors.Wrap(ctx, err, "marshal push request failed")
	}
	httpRequest, err := http.NewRequestWithContext(
		ctx, http.MethodPost, p.URL, bytes.NewReader(body),
	)
	if err != nil {
		return errors.Wrap(ctx, err, "build push request failed")
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	resp, err := p.client().Do(httpRequest)
	if err != nil {
		return errors.Wrapf(ctx, err, "post to %s failed", p.URL)
	}
	defer func() { _ = resp.Body.Close() }()
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusCreated {
		return errors.Errorf(
			ctx,
			"post to %s answered %d: %s",
			p.URL, resp.StatusCode, strings.TrimSpace(string(responseBody)),
		)
	}
	return nil
}

// client returns the configured client or the default one.
func (p *HTTPCardPoster) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return &http.Client{Timeout: postHTTPTimeout}
}
