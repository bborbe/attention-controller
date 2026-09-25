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

// speakSender is the sender label the tts server records for a read-aloud
// requested from this board. It names the surface that asked, exactly as
// `answered_by: attention-board` names the surface that answered.
const speakSender = "attention-board"

// speakTimeout bounds the upstream call. The tts server queues and returns
// immediately rather than waiting for playback, so a call that takes this long
// is a dead or unreachable server rather than a long utterance.
const speakTimeout = 10 * time.Second

// NewAttentionSpeakHandler creates an HTTP handler that reads an item aloud
// through the tts server.
//
// It is a server-side proxy rather than a direct browser-to-tts call, and the
// reason is measured rather than assumed: the tts server is a FastAPI app with
// no CORS middleware, so a page served from this origin cannot POST to it — a
// JSON content type triggers a preflight the server does not answer. Forwarding
// here keeps the browser talking only to its own origin.
//
// ttsURL is the tts server's base URL. An empty value is not fatal at
// construction — main simply does not route this endpoint, and the page renders
// no read-aloud control, rather than offering a control that always fails.
func NewAttentionSpeakHandler(store pkg.AttentionStore, ttsURL string) http.Handler {
	client := &http.Client{Timeout: speakTimeout}
	sayURL := strings.TrimSuffix(ttsURL, "/") + "/say"
	return libhttp.NewJSONErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
				return handleAttentionSpeak(ctx, resp, req, store, client, sayURL)
			},
		),
	)
}

// speakRequest is what this endpoint forwards. Only the text and the sender are
// set: the item's payload is the utterance, and voice/engine stay the tts
// server's own defaults so this proxy does not become a second place where
// speech configuration lives.
type speakRequest struct {
	Text   string `json:"text"`
	Sender string `json:"sender"`
}

// speakResponse is what the tts server returns, narrowed to the field this
// endpoint's caller needs: the message id to poll for status.
type speakResponse struct {
	MessageID string `json:"message_id"`
}

func handleAttentionSpeak(
	ctx context.Context,
	resp http.ResponseWriter,
	req *http.Request,
	store pkg.AttentionStore,
	client *http.Client,
	sayURL string,
) error {
	itemID := pkg.ItemID(mux.Vars(req)["itemID"])
	if itemID == "" {
		return libhttp.WrapWithCode(
			errors.New(ctx, "itemID is empty"),
			libhttp.ErrorCodeValidation,
			http.StatusBadRequest,
		)
	}
	item, err := store.Get(ctx, itemID)
	if err != nil {
		return wrapSpeakLookupError(ctx, err, itemID)
	}
	spoken, err := callSay(ctx, client, sayURL, item.Payload.String())
	if err != nil {
		// A failure past this point is the upstream's, not this process's:
		// reported as a gateway error so a caller can tell "the store is broken"
		// from "the tts server is". The upstream's own explanation rides in
		// `reason`, because "unknown engine" is actionable and "502" is not.
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "speak failed"),
			libhttp.ErrorCodeInternal,
			http.StatusBadGateway,
			map[string]any{
				"item_id": itemID.String(),
				"reason":  err.Error(),
			},
		)
	}
	if err := libhttp.SendJSONResponse(ctx, resp, spoken, http.StatusOK); err != nil {
		return errors.Wrap(ctx, err, "send response failed")
	}
	return nil
}

// wrapSpeakLookupError maps the store's lookup failures onto their responses,
// keeping "no such item" distinct from "the store failed".
func wrapSpeakLookupError(ctx context.Context, err error, itemID pkg.ItemID) error {
	if errors.Is(err, pkg.ErrItemNotFound) {
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "speak failed"),
			libhttp.ErrorCodeNotFound,
			http.StatusNotFound,
			map[string]any{"item_id": itemID.String()},
		)
	}
	return libhttp.WrapWithCode(
		errors.Wrap(ctx, err, "speak failed"),
		libhttp.ErrorCodeInternal,
		http.StatusInternalServerError,
	)
}

// callSay posts one utterance to the tts server and returns the message id it
// queued.
//
// The response body is read before the status is judged: the tts server
// explains a rejection in the body, and discarding it would turn "unknown
// engine" into a bare gateway error with nothing to act on. That explanation is
// carried into the returned error rather than logged here, so the caller
// decides what a reader sees.
func callSay(
	ctx context.Context,
	client *http.Client,
	sayURL string,
	text string,
) (speakResponse, error) {
	body, err := json.Marshal(speakRequest{Text: text, Sender: speakSender})
	if err != nil {
		return speakResponse{}, errors.Wrap(ctx, err, "marshal speak request failed")
	}
	upstream, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		sayURL,
		bytes.NewReader(body),
	)
	if err != nil {
		return speakResponse{}, errors.Wrap(ctx, err, "build speak request failed")
	}
	upstream.Header.Set(libhttp.ContentTypeHeaderName, libhttp.ApplicationJSONContentType)

	upstreamResp, err := client.Do(upstream)
	if err != nil {
		return speakResponse{}, errors.Wrap(ctx, err, "call tts server failed")
	}
	defer upstreamResp.Body.Close()

	raw, err := io.ReadAll(upstreamResp.Body)
	if err != nil {
		return speakResponse{}, errors.Wrap(ctx, err, "read tts response failed")
	}
	if upstreamResp.StatusCode != http.StatusOK {
		return speakResponse{}, errors.Errorf(
			ctx,
			"tts server returned %d: %s",
			upstreamResp.StatusCode,
			string(raw),
		)
	}
	var spoken speakResponse
	if err := json.Unmarshal(raw, &spoken); err != nil {
		return speakResponse{}, errors.Wrap(ctx, err, "decode tts response failed")
	}
	return spoken, nil
}
