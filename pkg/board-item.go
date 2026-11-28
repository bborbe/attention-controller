// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

// BoardItem is one entry of the board read (GET /api/1.0/attention/board): the
// stored item, flat on the wire exactly as the arm read returns it, plus the
// render data the board derives for it at read time.
//
// The item is embedded so its fields stay top-level — a consumer that reads the
// board as a list of items keeps working — and the derived data sits under one
// `board` key, so a stored field and a render-time derivation can never be
// confused for one another.
type BoardItem struct {
	Item `tstype:",extends"`
	// Board is what the board renders for this item beyond its stored fields.
	Board BoardView `json:"board"`
}

// BoardView is the per-card render data the server-rendered board computed per
// row, carried in the board JSON so the React board renders the same card. Every
// value is derived at read time from the item, its resolved provenance and its
// delivery report; none of it is stored.
type BoardView struct {
	// JumpURL is the board-relative path (`/jump/{itemID}`) that hands the card
	// back to its session. Empty when no pane resolved.
	JumpURL string `json:"jump_url"`
	// JumpCommand is the copyable `/supervisor:jump <pane>` handover for a
	// non-`message` item. Empty when no pane resolved, and for a `message` item.
	JumpCommand string `json:"jump_command"`
	// NoJump explains why the card carries no jump target. Empty whenever
	// JumpURL or JumpCommand is set.
	NoJump string `json:"no_jump"`
	// Uncovered reports that no live manager can relay this card.
	Uncovered bool `json:"uncovered"`
	// Provenance is the navigation line and the info panel's machine identity.
	Provenance BoardProvenance `json:"provenance"`
	// Timeline is the info panel's stage lines, in stage order.
	Timeline []string `json:"timeline"`
	// DeliveryStatus is the derived delivery status of the card's answer.
	// Empty when the delivery read failed.
	DeliveryStatus string `json:"delivery_status"`
	// DeliveryOutcome is the info panel's delivery-outcome line. Empty when the
	// panel renders none (read failed, or a deliberate hold).
	DeliveryOutcome string `json:"delivery_outcome"`
	// Undeliverable reports that the card's answer failed to reach, or was never
	// attempted toward, the asking session.
	Undeliverable bool `json:"undeliverable"`
	// SpeakEnabled reports whether a tts server is configured.
	SpeakEnabled bool `json:"speak_enabled"`
}

// BoardProvenance is the subset of a resolved Provenance the board renders.
// Every value is empty when it did not resolve; an unresolvable value is absent
// rather than a stand-in.
type BoardProvenance struct {
	// TaskName and TaskHref are the vault task link leading the navigation line.
	TaskName string `json:"task_name"`
	TaskHref string `json:"task_href"`
	// GoalName and GoalHref link the goal the task names first.
	GoalName string `json:"goal_name"`
	GoalHref string `json:"goal_href"`
	// TopicName and TopicHref link the topic page that lists that goal.
	TopicName string `json:"topic_name"`
	TopicHref string `json:"topic_href"`
	// SessionName is the session's registry name, empty when it repeats the
	// task name the task link already carries.
	SessionName string `json:"session_name"`
	// Host, Cwd and Tool are the info panel's machine identity.
	Host string `json:"host"`
	Cwd  string `json:"cwd"`
	Tool string `json:"tool"`
	// Pane is the pane the info panel names: the validated pane, or the recorded
	// one when the pane listing was unreadable (then PaneUnvalidated is set).
	Pane            string `json:"pane"`
	PaneUnvalidated bool   `json:"pane_unvalidated"`
	// Unroutable reports a recorded pane that does not resolve to this session.
	Unroutable bool `json:"unroutable"`
}
