// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"
)

// remoteAttentionTimeout bounds one call to the peer store.
//
// It is deliberately short. The peer is read on the board's render path, so a
// slow peer must degrade the board to local-only quickly rather than hold the
// operator's page open. The cache window in the decorator bounds how OFTEN the
// call is made; this bounds how long one of them can take.
const remoteAttentionTimeout = 3 * time.Second

//counterfeiter:generate -o ../mocks/remote-attention-store.go --fake-name RemoteAttentionStore . RemoteAttentionStore

// RemoteAttentionStore is the peer attention store a federating store reads
// from and proxies its items' writes to.
//
// ⚠️ It is a narrow interface rather than the full AttentionStore, and the
// omissions are the design. The peer is authoritative for its own items, so
// only the two operations that READ them and the three that are addressed by an
// item id cross the wire. ReadBoard, History, Push and SweepAnswered are absent
// because the peer answers those for its own store and nothing federates in the
// reverse direction; RecordAttempt and Delivery are absent because they are the
// LOCAL arm's bookkeeping about deliveries it performed, not state the peer
// holds about the item.
type RemoteAttentionStore interface {
	// Read returns the peer's open items.
	Read(ctx context.Context) (Items, error)

	// Get returns one of the peer's items whatever state it is in.
	Get(ctx context.Context, itemID ItemID) (*Item, error)

	// Answer applies open -> answered on the peer, as an atomic
	// compare-and-set, and returns the peer's item.
	Answer(
		ctx context.Context,
		itemID ItemID,
		answeredBy string,
		resolvedBy string,
		decision Decision,
		answer *Answer,
		answers Answers,
		answeredClient *AnsweredClient,
	) (*Item, error)

	// Close marks one of the peer's answered items closed.
	Close(
		ctx context.Context,
		itemID ItemID,
		answeredBy string,
		answeredClient *AnsweredClient,
	) (*Item, error)

	// Escalate records on the peer which session is carrying one of its items.
	Escalate(ctx context.Context, itemID ItemID, escalatedBy string) (*Item, error)
}

// NewHTTPRemoteAttentionStore returns a RemoteAttentionStore backed by a peer
// attention-controller's bearer-gated business API.
//
// ⚠️ baseURL and token are the peer's second, cluster-reachable listener — the
// one behind the bearer gate — not its board. The board listener renders HTML
// and is deliberately not exposed; the business API is the surface that can be
// gated by a token, which is why the federation reads through it.
func NewHTTPRemoteAttentionStore(baseURL string, token string) RemoteAttentionStore {
	return &httpRemoteAttentionStore{
		client:  &http.Client{Timeout: remoteAttentionTimeout},
		baseURL: strings.TrimSuffix(baseURL, "/"),
		token:   token,
	}
}

type httpRemoteAttentionStore struct {
	client  *http.Client
	baseURL string
	token   string
}

// remoteItemPath is the peer's single-item route. ⚠️ The id is appended raw
// rather than path-escaped, because an ItemID is a hex digest by construction
// (see the id generator) and escaping it would be a second, silently divergent
// spelling of the same route. A caller passing something else is a bug in the
// caller, not a case to paper over here.
func remoteItemPath(itemID ItemID) string {
	return "/api/1.0/attention/" + itemID.String()
}

// Read fetches the peer's open items.
//
// ⚠️ The response decodes straight into Items with no conversion step: the peer
// serialises []pkg.Item (`pkg/handler/attention-read.go`), and Item's own JSON
// tags ARE the wire shape, so the two ends share one definition by construction
// rather than by a mapping that can drift.
func (h *httpRemoteAttentionStore) Read(ctx context.Context) (Items, error) {
	var items Items
	if err := h.do(ctx, http.MethodGet, "/api/1.0/attention", nil, &items); err != nil {
		return nil, err
	}
	return items, nil
}

// Get fetches one of the peer's items whatever state it is in.
func (h *httpRemoteAttentionStore) Get(ctx context.Context, itemID ItemID) (*Item, error) {
	var item Item
	if err := h.do(ctx, http.MethodGet, remoteItemPath(itemID), nil, &item); err != nil {
		return nil, err
	}
	return &item, nil
}

// remoteAnswerRequest is the peer's answer body, mirrored field for field.
//
// ⚠️ Only `automation` is carried from the caller's AnsweredClient, and that is
// the whole of what can be carried: the peer composes user_agent and remote_addr
// from ITS OWN request, and deliberately never accepts either from a body (see
// answeredClientFromRequest). The consequence is recorded on the federating
// store: a federated answer reads back at the peer with the local board as the
// client, because the board process is the caller there.
type remoteAnswerRequest struct {
	AnsweredBy string   `json:"answered_by"`
	ResolvedBy string   `json:"resolved_by"`
	Decision   Decision `json:"decision"`
	Answer     *Answer  `json:"answer"`
	Answers    Answers  `json:"answers"`
	Automation *bool    `json:"automation"`
}

// Answer proxies an answer to the peer and returns the peer's item.
func (h *httpRemoteAttentionStore) Answer(
	ctx context.Context,
	itemID ItemID,
	answeredBy string,
	resolvedBy string,
	decision Decision,
	answer *Answer,
	answers Answers,
	answeredClient *AnsweredClient,
) (*Item, error) {
	var automation *bool
	if answeredClient != nil {
		automation = answeredClient.Automation
	}
	var item Item
	err := h.do(ctx, http.MethodPost, remoteItemPath(itemID)+"/answer", remoteAnswerRequest{
		AnsweredBy: answeredBy,
		ResolvedBy: resolvedBy,
		Decision:   decision,
		Answer:     answer,
		Answers:    answers,
		Automation: automation,
	}, &item)
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// remoteCloseRequest is the peer's close body, mirrored field for field, and
// narrowed exactly as remoteAnswerRequest is.
type remoteCloseRequest struct {
	AnsweredBy string `json:"answered_by"`
	Automation *bool  `json:"automation"`
}

// Close proxies a close to the peer and returns the peer's item.
func (h *httpRemoteAttentionStore) Close(
	ctx context.Context,
	itemID ItemID,
	answeredBy string,
	answeredClient *AnsweredClient,
) (*Item, error) {
	var automation *bool
	if answeredClient != nil {
		automation = answeredClient.Automation
	}
	var item Item
	err := h.do(ctx, http.MethodPost, remoteItemPath(itemID)+"/close", remoteCloseRequest{
		AnsweredBy: answeredBy,
		Automation: automation,
	}, &item)
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// remoteEscalateRequest is the peer's escalate body.
type remoteEscalateRequest struct {
	EscalatedBy string `json:"escalated_by"`
}

// Escalate proxies an escalation to the peer and returns the peer's item.
func (h *httpRemoteAttentionStore) Escalate(
	ctx context.Context,
	itemID ItemID,
	escalatedBy string,
) (*Item, error) {
	var item Item
	err := h.do(
		ctx,
		http.MethodPost,
		remoteItemPath(itemID)+"/escalate",
		remoteEscalateRequest{EscalatedBy: escalatedBy},
		&item,
	)
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// do performs one call against the peer and decodes a successful response into
// result. A nil result means the caller wants only the failure signal.
func (h *httpRemoteAttentionStore) do(
	ctx context.Context,
	method string,
	path string,
	requestBody any,
	result any,
) error {
	var body io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return errors.Wrap(ctx, err, "marshal peer request failed")
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.baseURL+path, body)
	if err != nil {
		return errors.Wrap(ctx, err, "build peer request failed")
	}
	// ⚠️ The token rides the Authorization header and nothing else — never a
	// query parameter, which would land in the peer's access log and in every
	// proxy between the two hosts, and never a log line here.
	req.Header.Set("Authorization", "Bearer "+h.token)
	if requestBody != nil {
		req.Header.Set("Content-Type", libhttp.ApplicationJSONContentType)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return errors.Wrap(ctx, err, "call peer attention store failed")
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return remoteResponseError(ctx, resp)
	}
	if result == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
		return errors.Wrap(ctx, err, "decode peer response failed")
	}
	return nil
}

// remoteResponseError turns the peer's error response back into this store's
// own sentinel error.
//
// ⚠️ The body's code is read, not just the status, and that is the point of the
// function. A lost answer race and an item that left the queue are BOTH HTTP
// 409, so a status-only mapping cannot tell them apart and would report "no
// longer exists" for a race another arm won — erasing exactly the distinction
// ErrAlreadyAnswered and ErrIllegalTransition exist to keep. The status is the
// fallback for a peer that answers with a body this shape cannot read.
func remoteResponseError(ctx context.Context, resp *http.Response) error {
	var errorResponse libhttp.ErrorResponse
	// A body that is absent, empty or not the error shape leaves Code empty and
	// the status then decides. A decode failure is deliberately not fatal: the
	// status is still a real signal, and turning a readable 404 into an opaque
	// decode error would lose it.
	_ = json.NewDecoder(resp.Body).Decode(&errorResponse)
	code := errorResponse.Error.Code
	switch {
	case code == ErrorCodeAlreadyAnswered:
		return errors.Wrapf(
			ctx,
			ErrAlreadyAnswered,
			"peer attention store: item was already answered (%s)",
			errorResponse.Error.Message,
		)
	case code == ErrorCodeItemClosed:
		return errors.Wrapf(
			ctx,
			ErrIllegalTransition,
			"peer attention store: item left the queue (%s)",
			errorResponse.Error.Message,
		)
	case code == ErrorCodeAlreadyEscalated:
		return errors.Wrapf(
			ctx,
			ErrAlreadyEscalated,
			"peer attention store: item was already escalated (%s)",
			errorResponse.Error.Message,
		)
	case code == ErrorCodeItemNotOpen:
		return errors.Wrapf(
			ctx,
			ErrItemNotOpen,
			"peer attention store: item is not open (%s)",
			errorResponse.Error.Message,
		)
	case code == libhttp.ErrorCodeNotFound || resp.StatusCode == http.StatusNotFound:
		return errors.Wrapf(
			ctx,
			ErrItemNotFound,
			"peer attention store: item not found (%s)",
			errorResponse.Error.Message,
		)
	default:
		// ⚠️ Includes the peer's 401: an unreachable-but-answering peer and a
		// rejected token are different operator problems, so the status and the
		// peer's own code ride the message rather than being flattened into a
		// generic failure.
		return errors.Wrapf(
			ctx,
			errors.New(ctx, "peer attention store call failed"),
			"status %d code %q: %s",
			resp.StatusCode,
			code,
			errorResponse.Error.Message,
		)
	}
}
