// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	libtime "github.com/bborbe/time"
)

// ManagerOpenView is the store's OPEN items, projected to the two fields the
// re-post decision reads. A nil view means the store read FAILED.
type ManagerOpenView struct {
	// Producers holds every open item's producer_id (the full session id).
	Producers map[string]struct{}
	// DedupKeys holds every open item's dedup key.
	DedupKeys map[string]struct{}
}

// ManagerParkAnswer is one answered `park:<sid8>` history row.
type ManagerParkAnswer struct {
	ItemID    ItemID
	CreatedAt libtime.DateTime
	Answer    string
}

// ManagerAnswerView is the answered park rows, keyed by the 8-char session
// prefix. A nil view means the store read FAILED.
type ManagerAnswerView struct {
	BySession map[string]ManagerParkAnswer
}

// managerTrackedEntry is one tracked session reduced to the two facts a re-post
// needs: the full id that becomes the producer, and the label the card shows.
type managerTrackedEntry struct {
	FullID string
	Label  string
}

// managerRepostInput is one pass's re-post decision input.
type managerRepostInput struct {
	// Parked is the 8-char prefixes of the sessions parked THIS pass.
	Parked []string
	// Entries is the tracked set keyed by the 8-char prefix.
	Entries map[string]managerTrackedEntry
	// Open is the store's open items; nil means the read FAILED.
	Open *ManagerOpenView
	// Answered is the answered park rows; nil means the read FAILED.
	Answered *ManagerAnswerView
	// ParkSince is when each park in progress started, keyed by prefix.
	ParkSince map[string]libtime.DateTime
	// Reasons is the gate reason observed this pass, keyed by prefix.
	Reasons map[string]string
}

// ManagerRepostRequest is one card to re-post for a parked worker whose card the
// store lost.
type ManagerRepostRequest struct {
	ProducerID      string
	DedupKey        DedupKey
	Payload         Payload
	AnswerMechanism AnswerMechanism
	Options         AnswerOptions // always empty for an ack card
}

// managerRepostCandidate is one parked session that survived the open-card test.
type managerRepostCandidate struct {
	session string
	entry   managerTrackedEntry
}

// managerRepostPlan returns the cards to re-post. An empty slice with a non-nil
// reason to re-post nothing is the normal suppressed case; a failed store read
// returns an empty slice and never fires.
//
// ⚠️ A failed read is NOT "the store holds nothing". `Open == nil` and
// `Answered == nil` both suppress the re-post rather than firing it for every
// parked worker — an empty result is a positive claim the re-post acts on.
func managerRepostPlan(in managerRepostInput) []ManagerRepostRequest {
	// ⚠️ The store is not read at all when nothing is parked: an idle arm must
	// not depend on the store being up.
	if len(in.Parked) == 0 || in.Open == nil {
		return nil
	}
	candidates := managerRepostCandidates(in)
	if len(candidates) == 0 || in.Answered == nil {
		return nil
	}
	requests := make([]ManagerRepostRequest, 0, len(candidates))
	for _, candidate := range candidates {
		if managerAnswerSuppresses(in.Answered, candidate.session, in.ParkSince) {
			continue
		}
		requests = append(requests, managerRepostRequest(candidate, in.Reasons))
	}
	return requests
}

// managerRepostCandidates keeps the parked sessions with no open card of their
// own: no `park:<sid8>` dedup key and no item already produced by the worker.
func managerRepostCandidates(in managerRepostInput) []managerRepostCandidate {
	candidates := make([]managerRepostCandidate, 0, len(in.Parked))
	for _, session := range in.Parked {
		if _, open := in.Open.DedupKeys["park:"+session]; open {
			continue
		}
		entry, tracked := in.Entries[session]
		if !tracked || entry.FullID == "" {
			continue
		}
		if _, open := in.Open.Producers[entry.FullID]; open {
			continue
		}
		candidates = append(candidates, managerRepostCandidate{session: session, entry: entry})
	}
	return candidates
}

// managerAnswerSuppresses reports whether an answered row silences this
// candidate. A row that is not there suppresses nothing.
func managerAnswerSuppresses(
	answered *ManagerAnswerView,
	session string,
	parkSince map[string]libtime.DateTime,
) bool {
	row, found := answered.BySession[session]
	if !found {
		return false
	}
	var since *libtime.DateTime
	if value, ok := parkSince[session]; ok {
		since = &value
	}
	return managerAnswerCoversCurrentPark(row, since)
}

// managerRepostRequest renders one card. The card's SHAPE follows the park.
func managerRepostRequest(
	candidate managerRepostCandidate,
	reasons map[string]string,
) ManagerRepostRequest {
	reason := reasons[candidate.session]
	return ManagerRepostRequest{
		ProducerID:      candidate.entry.FullID,
		DedupKey:        DedupKey("park:" + candidate.session),
		Payload:         managerRepostPayload(candidate, reason),
		AnswerMechanism: managerRepostMechanism(reason),
	}
}

// managerRepostPayload renders the card's operator-facing text.
//
// ⚠️ A permission-class park posts an `ack` card with NO input field and no
// options, because a free-text field on a native permission modal is a lie: the
// modal is answered by a keystroke in its own pane, so the text says where the
// gate actually lives.
func managerRepostPayload(candidate managerRepostCandidate, reason string) Payload {
	label := candidate.entry.Label
	if label == "" {
		label = candidate.session
	}
	if managerRepostMechanism(reason) == AckAnswerMechanism {
		return Payload(label + ": this session is parked on a prompt in its own pane. " +
			"Answer it in the pane — a board answer cannot release a prompt raised " +
			"in another session.")
	}
	return Payload(label + ": this session is parked on the operator and the attention " +
		"store holds no open item for it. Re-registered by the tracked-set watcher — " +
		"answer it and the session can continue.")
}

// managerAnswerCoversCurrentPark reports whether row's answer is about the park
// IN PROGRESS rather than an earlier one.
//
// ⚠️ This is the predicate that keeps the answered read from silencing a FAULT.
// `park:<sid8>` rows have exactly one producer — the re-post — so an answered
// row is the newest row for its key forever, and a naive read would suppress a
// LATER lost park on the same session. An answered row PREDATING `park_since`
// therefore belongs to a previous park and must not suppress a later one.
//
// ⚠️ A `since` the caller does not have means DO NOT SUPPRESS: an answer that
// cannot be dated against the park is not evidence the park is answered, and the
// safe side is the noisy one — re-post.
func managerAnswerCoversCurrentPark(row ManagerParkAnswer, since *libtime.DateTime) bool {
	if since == nil {
		return false
	}
	return row.CreatedAt.Time().After(since.Time())
}

// managerRepostMechanism is the card shape a park reason gets.
//
// `registry:waiting` and `busy+parked-modal` are permission-class: the worker is
// parked on a native modal, so the card is an `ack` with no answer control.
// Every other reason is a question whose free-text field IS the answer path.
func managerRepostMechanism(reason string) AnswerMechanism {
	if reason == "registry:waiting" || reason == "busy+parked-modal" {
		return AckAnswerMechanism
	}
	return MessageAnswerMechanism
}
