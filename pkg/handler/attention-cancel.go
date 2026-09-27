// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

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
	"github.com/gorilla/mux"

	"github.com/bborbe/attention-controller/pkg"
)

// cancelTimeout bounds the upstream call. The tts server marks the message and
// returns rather than waiting for playback to stop, so a call that takes this
// long is a dead or unreachable server rather than a long utterance.
const cancelTimeout = 10 * time.Second

// NewAttentionCancelHandler creates an HTTP handler that stops a reading this
// board started.
//
// It is the mirror of NewAttentionSpeakHandler and exists for the same measured
// reason: the tts server is a FastAPI app with no CORS middleware, so a page
// served from this origin cannot POST to it. Forwarding here keeps the browser
// talking only to its own origin.
//
// ⚠️ It cancels by message id and NEVER by draining the queue. The tts server
// is a single shared queue every session speaks through, so a body-less or
// `{all: true}` cancel would cut off another session's narration — a worse
// defect than the one-way control this endpoint fixes. That is enforced by the
// request struct rather than by convention: `cancelRequest` has no `all` field,
// so no code path here can send one.
//
// ttsURL is the tts server's base URL. An empty value is not fatal at
// construction — main simply does not route this endpoint, on the same
// condition that stops the page rendering the control.
func NewAttentionCancelHandler(ttsURL string) http.Handler {
	client := &http.Client{Timeout: cancelTimeout}
	cancelURL := strings.TrimSuffix(ttsURL, "/") + "/cancel"
	return libhttp.NewJSONErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
				return handleAttentionCancel(ctx, resp, req, client, cancelURL)
			},
		),
	)
}

// cancelRequest is what this endpoint forwards.
//
// ⚠️ `all` is deliberately absent rather than present-and-false. The tts
// server treats a body-less cancel as "stop whatever is playing", which on a
// shared queue means another session's utterance, so the field this struct can
// express is the only one the board is allowed to send.
type cancelRequest struct {
	MessageID string `json:"message_id"`
}

// cancelResponse is the tts server's answer, narrowed to what a caller needs.
// `queued` is the server's queue depth after the call, not a count of what this
// cancel removed.
type cancelResponse struct {
	Cancelled []string `json:"cancelled"`
	Queued    int      `json:"queued"`
}

func handleAttentionCancel(
	ctx context.Context,
	resp http.ResponseWriter,
	req *http.Request,
	client *http.Client,
	cancelURL string,
) error {
	itemID := pkg.ItemID(mux.Vars(req)["itemID"])
	if itemID == "" {
		return libhttp.WrapWithCode(
			errors.New(ctx, "itemID is empty"),
			libhttp.ErrorCodeValidation,
			http.StatusBadRequest,
		)
	}
	var body cancelRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return libhttp.WrapWithCode(
			errors.Wrap(ctx, err, "decode cancel request failed"),
			libhttp.ErrorCodeValidation,
			http.StatusBadRequest,
		)
	}
	// A missing id is refused rather than defaulted, because the only default
	// the tts server offers is "stop whatever is playing" — the queue drain
	// this endpoint exists to avoid.
	if body.MessageID == "" {
		return libhttp.WrapWithCode(
			errors.New(ctx, "message_id is empty"),
			libhttp.ErrorCodeValidation,
			http.StatusBadRequest,
		)
	}
	stopped, status, err := callCancel(ctx, client, cancelURL, body.MessageID)
	if err != nil {
		// The upstream's own explanation rides in `reason`, because "unknown
		// message ID" is actionable and "502" is not. A 404 is reported as a
		// not-found rather than a gateway failure: the tts server has no record
		// of the id because it aged out of its TTL-pruned status table, so the
		// reading is not playing and there is nothing left to stop.
		code := libhttp.ErrorCodeInternal
		httpStatus := http.StatusBadGateway
		if status == http.StatusNotFound {
			code = libhttp.ErrorCodeNotFound
			httpStatus = http.StatusNotFound
		}
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "cancel failed"),
			code,
			httpStatus,
			map[string]any{
				"item_id":    itemID.String(),
				"message_id": body.MessageID,
				"reason":     err.Error(),
			},
		)
	}
	if err := libhttp.SendJSONResponse(ctx, resp, stopped, http.StatusOK); err != nil {
		return errors.Wrap(ctx, err, "send response failed")
	}
	return nil
}

// callCancel posts one cancel to the tts server and returns its response
// alongside the upstream status.
//
// The status is returned rather than folded into the error because the tts
// server answers 404 for an id it has no record of, and that is an outcome the
// caller reports differently from a failure. Any other non-2xx is returned as
// an error, carrying the upstream's body so the caller has something to act on.
func callCancel(
	ctx context.Context,
	client *http.Client,
	cancelURL string,
	messageID string,
) (cancelResponse, int, error) {
	body, err := json.Marshal(cancelRequest{MessageID: messageID})
	if err != nil {
		return cancelResponse{}, 0, errors.Wrap(ctx, err, "marshal cancel request failed")
	}
	upstream, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		cancelURL,
		bytes.NewReader(body),
	)
	if err != nil {
		return cancelResponse{}, 0, errors.Wrap(ctx, err, "build cancel request failed")
	}
	upstream.Header.Set(libhttp.ContentTypeHeaderName, libhttp.ApplicationJSONContentType)

	upstreamResp, err := client.Do(upstream)
	if err != nil {
		return cancelResponse{}, 0, errors.Wrap(ctx, err, "call tts server failed")
	}
	defer upstreamResp.Body.Close()

	raw, err := io.ReadAll(upstreamResp.Body)
	if err != nil {
		return cancelResponse{}, 0, errors.Wrap(ctx, err, "read tts response failed")
	}
	if upstreamResp.StatusCode < 200 || upstreamResp.StatusCode > 299 {
		return cancelResponse{}, upstreamResp.StatusCode, errors.Errorf(
			ctx,
			"tts server returned %d: %s",
			upstreamResp.StatusCode,
			string(raw),
		)
	}
	var stopped cancelResponse
	if err := json.Unmarshal(raw, &stopped); err != nil {
		return cancelResponse{}, 0, errors.Wrap(ctx, err, "decode tts response failed")
	}
	return stopped, upstreamResp.StatusCode, nil
}
