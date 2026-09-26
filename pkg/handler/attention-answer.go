// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"
	"github.com/gorilla/mux"

	"github.com/bborbe/attention-controller/pkg"
)

// NewAttentionAnswerHandler creates an HTTP handler that applies open ->
// answered as an atomic compare-and-set.
//
// Exactly one of two concurrent answers transitions the item. The loser gets
// HTTP 409 with code ALREADY_ANSWERED and must read the item back and report it
// as already answered — it must not retry, and it must not route its own answer
// anyway, which would reinstate the duplicate the guard exists to prevent.
func NewAttentionAnswerHandler(store pkg.AttentionStore) http.Handler {
	return libhttp.NewJSONErrorHandler(
		libhttp.WithErrorFunc(
			func(ctx context.Context, resp http.ResponseWriter, req *http.Request) error {
				return handleAttentionAnswer(ctx, resp, req, store)
			},
		),
	)
}

func handleAttentionAnswer(
	ctx context.Context,
	resp http.ResponseWriter,
	req *http.Request,
	store pkg.AttentionStore,
) error {
	itemID := pkg.ItemID(mux.Vars(req)["itemID"])
	if itemID == "" {
		return libhttp.WrapWithCode(
			errors.New(ctx, "itemID is empty"),
			libhttp.ErrorCodeValidation,
			http.StatusBadRequest,
		)
	}
	var request struct {
		AnsweredBy string `json:"answered_by"`
		// ResolvedBy is the session that resolved the item, as distinct from
		// AnsweredBy, the arm that carried the answer. Optional: an omitted
		// value stores an item with no resolver recorded, which is what every
		// item answered before this field existed reads as.
		ResolvedBy string `json:"resolved_by"`
		// Decision is what the answer decided — allow or deny — and it is
		// distinct from AnsweredBy for the same reason ResolvedBy is: an arm
		// supplies an allow and a deny alike, so the arm cannot say what was
		// decided. Optional: an omitted value stores an item with no verdict
		// recorded, which is what a message- or ack-class item reads as, and
		// what every item answered before this field existed reads as.
		Decision pkg.Decision `json:"decision"`
		// Answer is what the operator actually said on a message item — the
		// chosen option's label, a skip, or free text. Distinct from Decision
		// for the same reason Decision is distinct from AnsweredBy: a chosen
		// label is not a verdict. Optional: an omitted value stores an item with
		// no answer content recorded, which is what a permission- or ack-class
		// item reads as, and what every item answered before this field existed
		// reads as.
		Answer *pkg.Answer `json:"answer"`
		// Answers is the same content for an item carrying `questions` — one
		// entry per tab, each naming the question it answers. It is distinct from
		// Answer rather than a replacement for it, so a single-question item's
		// wire shape is unchanged. Optional: an omitted value stores an item with
		// no per-question content recorded. It is mutually exclusive with Answer,
		// and a call carrying both is rejected rather than resolved by convention.
		Answers pkg.Answers `json:"answers"`
		// Automation is the page's own navigator.webdriver reading, and it is the
		// only member of the answered client the body may carry: user_agent and
		// remote_addr are read from the request itself, so a body carrying either
		// has nowhere to land and is ignored rather than honoured.
		//
		// It is a pointer so an omitted hint is absent rather than a positive
		// claim that the client was not automated — the distinction the field
		// exists to keep. Optional: an omitted value stores a client record with
		// no automation reading, which is what a client that cannot read it
		// reports.
		Automation *bool `json:"automation"`
	}
	if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "decode request failed"),
			libhttp.ErrorCodeValidation,
			http.StatusBadRequest,
			map[string]any{"reason": "request body is not valid JSON"},
		)
	}
	if err := validateAnswerRequest(ctx, request.Decision, request.Answer, request.Answers); err != nil {
		return err
	}
	item, err := store.Answer(
		ctx,
		itemID,
		request.AnsweredBy,
		request.ResolvedBy,
		request.Decision,
		request.Answer,
		request.Answers,
		// Composed here rather than accepted from the body: the two derived
		// members come from the request itself, and only the automation hint
		// comes from what the page posted.
		answeredClientFromRequest(req, request.Automation),
	)
	if err != nil {
		return wrapAnswerError(ctx, err, itemID, store)
	}
	if err := libhttp.SendJSONResponse(ctx, resp, item, http.StatusOK); err != nil {
		return errors.Wrap(ctx, err, "send response failed")
	}
	return nil
}

// validateAnswerRequest rejects a decision or an answer the schema's rules do
// not allow, before the store is touched — the same shape validatePushRequest
// uses.
//
// It checks the decision and the answer alone. answered_by and resolved_by are
// free-form declarations with no value domain, while decision and answer are
// closed vocabularies whose whole purpose is membership, so they are the fields
// here that can be wrong in a way the store could not notice. An OMITTED
// decision or answer is deliberately not rejected: the schema declines to add
// those rules, so an empty value stores an item with nothing recorded. Only a
// present-and-wrong value fails.
func validateAnswerRequest(
	ctx context.Context,
	decision pkg.Decision,
	answer *pkg.Answer,
	answers pkg.Answers,
) error {
	if err := decision.Validate(ctx); err != nil {
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "validate answer request failed"),
			libhttp.ErrorCodeValidation,
			http.StatusBadRequest,
			map[string]any{"decision": decision.String()},
		)
	}
	if answer != nil {
		if err := answer.Validate(ctx); err != nil {
			return libhttp.WrapWithDetails(
				errors.Wrap(ctx, err, "validate answer request failed"),
				libhttp.ErrorCodeValidation,
				http.StatusBadRequest,
				map[string]any{"answer_kind": answer.Kind.String()},
			)
		}
	}
	if len(answers) > 0 {
		if err := answers.Validate(ctx); err != nil {
			return libhttp.WrapWithDetails(
				errors.Wrap(ctx, err, "validate answer request failed"),
				libhttp.ErrorCodeValidation,
				http.StatusBadRequest,
				map[string]any{"answers": len(answers)},
			)
		}
	}
	return nil
}

// wrapAnswerError maps the store's four distinct answer failures onto four
// distinct responses, so a caller can tell a lost race from an item that left
// the queue from a missing item from a request the store refused.
func wrapAnswerError(
	ctx context.Context,
	err error,
	itemID pkg.ItemID,
	store pkg.AttentionStore,
) error {
	switch {
	case errors.Is(err, pkg.ErrItemNotFound):
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "answer failed"),
			libhttp.ErrorCodeNotFound,
			http.StatusNotFound,
			map[string]any{"item_id": itemID.String()},
		)
	case errors.Is(err, pkg.ErrAlreadyAnswered):
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "answer failed"),
			ErrorCodeAlreadyAnswered,
			http.StatusConflict,
			map[string]any{"item_id": itemID.String()},
		)
	case errors.Is(err, pkg.ErrIllegalTransition):
		return wrapItemClosedError(ctx, err, itemID, store)
	default:
		return libhttp.WrapWithDetails(
			errors.Wrap(ctx, err, "answer failed"),
			libhttp.ErrorCodeValidation,
			http.StatusConflict,
			map[string]any{"item_id": itemID.String()},
		)
	}
}

// wrapItemClosedError reports an answer against an item that had already left
// the queue, naming the terminal state and when it happened.
//
// The store's own error carries neither. ErrIllegalTransition says the move is
// not a row of the schema's table — a fact about the table, not about this item
// — so the item is read back for its state and its closed_at. The read cannot
// race the answer that just failed: `closed` is terminal, so nothing can move
// the item again, and the store's Read prunes only open items, so a closed one
// is never removed.
//
// The item id is the only thing read back that the caller already had, and it
// is carried anyway so the details shape matches every other answer failure.
//
// A failed read degrades to the code without a timestamp rather than turning a
// terminal item into a 500. The caller still learns the item has left the
// queue, which is the part it acts on; only the "when" is lost.
func wrapItemClosedError(
	ctx context.Context,
	err error,
	itemID pkg.ItemID,
	store pkg.AttentionStore,
) error {
	details := map[string]any{"item_id": itemID.String()}
	if item, getErr := store.Get(ctx, itemID); getErr == nil && item.ClosedAt != nil {
		// ClosedAt is written by the same transition that makes the item
		// unanswerable, so an item that failed this way carries it. It is
		// guarded rather than assumed: an item closed by a path that left it
		// unset would otherwise report a zero time as the moment it left.
		details["state"] = item.State.String()
		details["closed_at"] = item.ClosedAt.String()
	}
	return libhttp.WrapWithDetails(
		errors.Wrapf(ctx, err, "item %s left the queue before the answer arrived", itemID),
		ErrorCodeItemClosed,
		http.StatusConflict,
		details,
	)
}
