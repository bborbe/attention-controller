// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package deploy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// probeHTTPTimeout bounds one probe request. It is far below the poll interval
// so a hung connection costs one probe rather than the whole readiness window.
const probeHTTPTimeout = 5 * time.Second

// footerCommitPattern reads the commit the SERVING process reports in the
// board's own footer.
//
// ⚠️ This is the whole point of the probe. `go version -m <path>` reads a file;
// this reads the process that is answering, so a stale process left running
// behind a replaced binary cannot satisfy it.
var footerCommitPattern = regexp.MustCompile(`commit <span class="bi-value">([0-9a-f]+)`)

// HTTPProber proves the board serves the build that was installed.
type HTTPProber struct {
	// ReadinessURL must answer 200 with the body "OK".
	ReadinessURL string
	// HealthURL must answer 200.
	HealthURL string
	// BoardURL is the page whose footer carries the serving commit.
	BoardURL string
	// Client issues the requests. Defaults to a client with probeHTTPTimeout.
	Client *http.Client
}

// Probe observes the board once.
//
// ⚠️ All three checks are on the BOARD's listener. The store-api listener
// (18081) serves only the bearer-gated `/api/1.0/...` routes and answers 404 on
// `/healthz` — measured on the live host, where `ATTENTION_STORE_LISTEN` is set
// — so a health probe aimed there could never pass and would roll back every
// healthy deploy.
func (p *HTTPProber) Probe(ctx context.Context, expectedCommit string) ProbeResult {
	if detail := p.get(ctx, p.ReadinessURL, "OK"); detail != "" {
		return ProbeResult{Detail: "readiness " + detail}
	}
	if detail := p.get(ctx, p.HealthURL, ""); detail != "" {
		return ProbeResult{Detail: "healthz " + detail}
	}
	serving, detail := p.servingCommit(ctx)
	if detail != "" {
		return ProbeResult{Detail: detail}
	}
	if expectedCommit != "" && serving != expectedCommit {
		return ProbeResult{
			ServingCommit: serving,
			Detail:        fmt.Sprintf("serving commit %s, expected %s", serving, expectedCommit),
		}
	}
	return ProbeResult{Ready: true, ServingCommit: serving}
}

// get issues one GET and returns an empty detail when it answered as required.
func (p *HTTPProber) get(ctx context.Context, url string, wantBody string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "request failed: " + err.Error()
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return "request failed: " + err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "read failed: " + err.Error()
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("status %d", resp.StatusCode)
	}
	if wantBody != "" && strings.TrimSpace(string(body)) != wantBody {
		return fmt.Sprintf("body %q, want %q", strings.TrimSpace(string(body)), wantBody)
	}
	return ""
}

// servingCommit reads the commit out of the board's footer.
func (p *HTTPProber) servingCommit(ctx context.Context) (string, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.BoardURL, nil)
	if err != nil {
		return "", "footer request failed: " + err.Error()
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return "", "footer request failed: " + err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Sprintf("footer status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", "footer read failed: " + err.Error()
	}
	match := footerCommitPattern.FindSubmatch(body)
	if match == nil {
		return "", "footer carries no commit"
	}
	return string(match[1]), ""
}

// client returns the configured client or the default one.
func (p *HTTPProber) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return &http.Client{Timeout: probeHTTPTimeout}
}
