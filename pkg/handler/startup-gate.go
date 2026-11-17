// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"encoding/json"
	"html/template"
	"net/http"
	"sync"

	libhttp "github.com/bborbe/http"
	"github.com/golang/glog"
)

// StartupStatusStarting is the `status` value every not-ready response carries.
const StartupStatusStarting = "starting"

// startupRetryAfterSeconds is the Retry-After a not-ready response advertises,
// and the starting page's meta-refresh interval.
const startupRetryAfterSeconds = "2"

// StartupGate is the board listener's handler while the process initializes.
//
// ⚠️ It exists so the listener can bind BEFORE the slow init steps (the BoltDB
// open, which waits up to its lock timeout, and the vault walk that builds the
// task index). Until Open is called every request answers an explicit
// "starting" response naming the pending step — HTTP 503, never 200 and never a
// refused connection — so a client that reaches the port mid-init fails loudly
// rather than reading an empty board as a quiet one.
type StartupGate interface {
	http.Handler
	// Step records the init step now pending; not-ready responses name it.
	Step(name string)
	// Open routes every later request to next: the process is ready.
	Open(next http.Handler)
	// Close returns the gate to not-ready under the given step name, so a
	// shutting-down process stops routing new requests to the real handler.
	Close(name string)
	// Status reports whether the gate is open and the pending step.
	Status() (bool, string)
}

// NewStartupGate creates a gate that is not ready, with the step `boot`.
//
// whileStarting maps exact paths to handlers served even before Open — the
// liveness probe and /metrics, which must answer while init runs. ⚠️ The
// readiness probe must NOT be listed: it answers 503 until Open by design.
func NewStartupGate(whileStarting map[string]http.Handler) StartupGate {
	return &startupGate{
		step:          "boot",
		whileStarting: whileStarting,
	}
}

type startupGate struct {
	mu            sync.RWMutex
	step          string
	next          http.Handler
	whileStarting map[string]http.Handler
}

func (g *startupGate) Step(name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.step = name
}

func (g *startupGate) Open(next http.Handler) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.next = next
	g.step = ""
}

func (g *startupGate) Close(name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.next = nil
	g.step = name
}

func (g *startupGate) Status() (bool, string) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.next != nil, g.step
}

func (g *startupGate) ServeHTTP(resp http.ResponseWriter, req *http.Request) {
	g.mu.RLock()
	next, step := g.next, g.step
	g.mu.RUnlock()
	if next != nil {
		next.ServeHTTP(resp, req)
		return
	}
	if h, ok := g.whileStarting[req.URL.Path]; ok {
		h.ServeHTTP(resp, req)
		return
	}
	resp.Header().Set("Retry-After", startupRetryAfterSeconds)
	resp.Header().Set("Cache-Control", "no-store")
	if req.URL.Path == "/" && (req.Method == http.MethodGet || req.Method == http.MethodHead) {
		writeStartingPage(resp, step)
		return
	}
	writeStartingJSON(resp, step)
}

// startingJSON is the body every not-ready API response carries.
type startingJSON struct {
	Status string `json:"status"`
	Step   string `json:"step"`
}

func writeStartingJSON(resp http.ResponseWriter, step string) {
	resp.Header().Set(libhttp.ContentTypeHeaderName, libhttp.ApplicationJSONContentType)
	resp.WriteHeader(http.StatusServiceUnavailable)
	body := startingJSON{Status: StartupStatusStarting, Step: step}
	if err := json.NewEncoder(resp).Encode(body); err != nil {
		glog.Warningf("write starting response failed: %v", err)
	}
}

var startingPageTemplate = template.Must(template.New("starting").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta http-equiv="refresh" content="{{.Refresh}}">
<title>attention-controller starting</title>
<style>body{font-family:-apple-system,system-ui,sans-serif;margin:3em;color:#333}code{background:#eee;padding:0 .3em}</style>
</head>
<body>
<h1>attention-controller is starting</h1>
<p>Pending step: <code>{{.Step}}</code></p>
<p>This page reloads every {{.Refresh}} seconds and shows the board once initialization finishes.</p>
</body>
</html>
`))

func writeStartingPage(resp http.ResponseWriter, step string) {
	resp.Header().Set(libhttp.ContentTypeHeaderName, "text/html; charset=utf-8")
	resp.WriteHeader(http.StatusServiceUnavailable)
	err := startingPageTemplate.Execute(resp, struct {
		Step    string
		Refresh string
	}{Step: step, Refresh: startupRetryAfterSeconds})
	if err != nil {
		glog.Warningf("write starting page failed: %v", err)
	}
}
