// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/golang/glog"
)

// wrapperRequestTimeout bounds one wrapper request. It is deliberately short:
// the wrapper owns a child's stdin, so an unresponsive wrapper must degrade to
// "leave the gate for the operator" rather than park the delivery sweep.
const wrapperRequestTimeout = 10 * time.Second

// WrapperProbeState is what a wrapper probe learned about a session.
//
// ⚠️ The three values are NOT interchangeable, and collapsing them is how an
// operator's answer gets thrown away. `none` means the wrapper answered and
// holds no question for this session — terminal, the gate is gone, nothing will
// ever carry the answer. `unreachable` means nothing was learned at all, and
// treating that as terminal records a failure and drops the card over a
// transient outage.
type WrapperProbeState string

const (
	// WrapperProbeOK means the wrapper answered and holds a session record.
	WrapperProbeOK WrapperProbeState = "ok"
	// WrapperProbeNone means the wrapper answered and holds no record.
	WrapperProbeNone WrapperProbeState = "none"
	// WrapperProbeUnreachable means nothing was learned.
	WrapperProbeUnreachable WrapperProbeState = "unreachable"
)

// WrapperQuestion is the wrapper's pending question for a session.
type WrapperQuestion struct {
	// RequestID is the id the answer must name. It is how a check-then-act
	// race is closed: the caller decided against one gate, and the answer must
	// land on that same gate.
	RequestID string `json:"request_id"`
	// Input is the question payload, which carries the tool name and its
	// arguments the routine policy evaluates.
	Input json.RawMessage `json:"input"`
	// Raw is the whole question object as the wrapper sent it, kept so the
	// routine policy sees the envelope it was written for
	// (`question.input.arguments`) rather than a re-marshalled subset.
	Raw json.RawMessage `json:"-"`
}

// WrapperSession is the wrapper's record for one session.
type WrapperSession struct {
	// State is the record's own state, e.g. `exited`.
	State string `json:"state"`
	// Question is the pending question, or nil when none is pending.
	Question *WrapperQuestion `json:"question"`
}

// ClaudeWrapperClient talks to the claude-wrapper that owns a headless child's
// stdin.
//
// ⚠️ It exists because a wrapper-spawned child has no pane: the wrapper owns
// its stdin and the child's pid resolves to no tty, so the pane writer is not
// merely failing on those sessions — it has nothing to type into. The wrapper
// is the only process that can move them, and it already exposes the route.
type ClaudeWrapperClient interface {
	// Probe asks the wrapper what it holds for the session.
	Probe(ctx context.Context, sessionID string) (WrapperProbeState, *WrapperSession)

	// AnswerViaWrapper delivers an EXISTING answer.
	//
	// expectRequestID, when set, makes the delivery fail-safe against a
	// check-then-act race: the caller decided against one gate, and if the
	// wrapper now holds a different one the delivery is retried rather than
	// landed on a gate nothing evaluated.
	AnswerViaWrapper(
		ctx context.Context,
		sessionID string,
		decision string,
		expectRequestID *string,
	) (DeliveryVerdict, string)
}

// NewClaudeWrapperClient creates the client.
//
// ⚠️ The URL is accepted only when it is a LOOPBACK http(s) URL. The wrapper
// owns the child's stdin and its response's `cwd` decides the routine workspace
// boundary, so a value pointing anywhere else is not a wrapper this arm should
// trust. Anything else disables the arm, which is the documented safe default.
func NewClaudeWrapperClient(rawURL string) ClaudeWrapperClient {
	base := normaliseWrapperURL(rawURL)
	return &claudeWrapperClient{
		baseURL: base,
		client:  &http.Client{Timeout: wrapperRequestTimeout},
	}
}

// normaliseWrapperURL returns the loopback base URL, or "" when the value is
// not one.
func normaliseWrapperURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ""
	}
	host := parsed.Hostname()
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() {
		return ""
	}
	return raw
}

type claudeWrapperClient struct {
	baseURL string
	client  *http.Client
}

// Probe asks the wrapper what it holds for the session.
//
// ⚠️ ONLY 404 means "the wrapper holds no question". Every other non-2xx — a
// restart's 502, a 503, a 429 — is a wrapper that could not answer the
// question, not a gate that is gone. Reading those as `none` records a failure
// and drops the operator's answer, which is the exact loss this split exists to
// prevent.
func (c *claudeWrapperClient) Probe(
	ctx context.Context,
	sessionID string,
) (WrapperProbeState, *WrapperSession) {
	if !isUUID(sessionID) {
		return WrapperProbeNone, nil
	}
	status, body, ok := c.request(ctx, "/api/1.0/sessions/"+sessionID, nil)
	if !ok {
		return WrapperProbeUnreachable, nil
	}
	switch {
	case status == http.StatusNotFound:
		return WrapperProbeNone, nil
	case status < 200 || status >= 300:
		return WrapperProbeUnreachable, nil
	case len(body) == 0:
		return WrapperProbeNone, nil
	}
	var envelope struct {
		State    string          `json:"state"`
		Question json.RawMessage `json:"question"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return WrapperProbeNone, nil
	}
	session := &WrapperSession{State: envelope.State}
	if len(envelope.Question) > 0 && string(envelope.Question) != "null" {
		var question WrapperQuestion
		if err := json.Unmarshal(envelope.Question, &question); err == nil {
			question.Raw = envelope.Question
			session.Question = &question
		}
	}
	return WrapperProbeOK, session
}

// AnswerViaWrapper delivers an existing answer.
//
// ⚠️ This is the delivery arm and it does NOT consult the routine policy. The
// policy decides whether the watcher may ORIGINATE an answer; once an answer
// exists — the operator answered the card, or a CLI did — carrying it is not a
// decision and must not be second-guessed. A policy check here would let the
// watcher refuse to deliver the very answer the operator just gave on a `Bash`
// gate.
func (c *claudeWrapperClient) AnswerViaWrapper(
	ctx context.Context,
	sessionID string,
	decision string,
	expectRequestID *string,
) (DeliveryVerdict, string) {
	if c.baseURL == "" {
		return DeliveryRefuse, "no wrapper configured (ATTENTION_WRAPPER_URL unset)"
	}
	state, session := c.Probe(ctx, sessionID)
	if state == WrapperProbeUnreachable {
		// NOT a refusal: nothing was learned, so the answer is still
		// deliverable once the wrapper is back. A refusal here would record a
		// failure and drop the operator's answer over a transient outage.
		return DeliveryRetry, "wrapper unreachable"
	}
	if session == nil || session.Question == nil || session.Question.RequestID == "" {
		if state == WrapperProbeNone {
			return DeliveryRefuse, "wrapper has no record (session gone) for session " + shortID(
				sessionID,
			)
		}
		if session != nil && session.State == "exited" {
			return DeliveryRefuse,
				"wrapper record exited (session gone, not superseded) for session " + shortID(
					sessionID,
				)
		}
		// ⚠️ NOT `refuse`. The wrapper is reachable and holds a live record but
		// reports NO pending question — the gate was answered before this arm
		// got to it. There is nothing to deliver to and nothing went wrong.
		return DeliveryResolved,
			"wrapper holds no question (gate already resolved) for session " + shortID(sessionID)
	}
	requestID := session.Question.RequestID
	if expectRequestID != nil && *expectRequestID != "" && requestID != *expectRequestID {
		return DeliveryRetry, "gate changed under the decision (" +
			shortID(*expectRequestID) + " -> " + shortID(requestID) + ")"
	}
	payload, err := json.Marshal(map[string]string{
		"request_id": requestID,
		"behavior":   decision,
	})
	if err != nil {
		return DeliveryRefuse, "encode wrapper answer failed"
	}
	status, _, ok := c.request(
		ctx,
		"/api/1.0/sessions/"+sessionID+"/answer",
		payload,
	)
	if !ok {
		return DeliveryRetry, "wrapper unreachable"
	}
	switch {
	case status >= 200 && status < 300:
		// Any 2xx is a success — the route's own success code is not pinned
		// here, and reading a 201/202/204 as retry would re-answer a gate
		// already moved.
		return DeliveryOK, "answered " + decision + " for request " + shortID(requestID)
	case status == http.StatusConflict:
		return DeliveryRefuse, "wrapper refused the answer (unsupported)"
	default:
		return DeliveryRetry, "wrapper answered " + itoa(status)
	}
}

// request performs one wrapper call and returns the status, body and whether
// the call completed.
//
// ⚠️ It never returns an error. An unreachable wrapper must degrade to "leave
// the gate for the operator"; failing here would take the whole delivery sweep
// down for every other card, which is a much worse failure than one unanswered
// gate. A POST's body is discarded on purpose: parsing it once turned a
// perfectly good 200 into a decode error the arm read as "unreachable" AFTER
// the child had already been answered.
func (c *claudeWrapperClient) request(
	ctx context.Context,
	path string,
	body []byte,
) (int, []byte, bool) {
	if c.baseURL == "" {
		return 0, nil, false
	}
	method := http.MethodGet
	var reader io.Reader
	if body != nil {
		method = http.MethodPost
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return 0, nil, false
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		glog.V(2).Infof("wrapper %s %s failed: %v", method, path, err)
		return 0, nil, false
	}
	defer resp.Body.Close()
	if body != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, nil, true
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, false
	}
	return resp.StatusCode, raw, true
}

// isUUID reports whether the value has the canonical 8-4-4-4-12 shape.
//
// ⚠️ A session id that is not a UUID cannot be a wrapper key, so the probe
// reports `none` without a request rather than asking the wrapper about a value
// it will never hold.
func isUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, r := range value {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !isHexRune(r) {
				return false
			}
		}
	}
	return true
}

func isHexRune(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

// shortID renders the first eight characters of an id for a log line.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// itoa renders an int without pulling strconv into the call sites.
func itoa(value int) string {
	return strconv.Itoa(value)
}

// Compile-time guard: the client must satisfy its own interface.
var _ ClaudeWrapperClient = (*claudeWrapperClient)(nil)
