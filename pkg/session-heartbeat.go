// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/collection"
	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/bborbe/validation"
)

// SessionHeartbeatLocation is where the heartbeating session runs.
//
// ⚠️ `local` is the only value this task exercises; `pod` is the sibling task
// [[Pod Workers Send Heartbeats to attention-controller and Render Live·pod]]'s
// vocabulary. Both are declared here rather than added later so the store's
// wire contract does not change shape when that task lands — the same
// "required vocabulary, not a menu" discipline AnswerMechanism carries.
type SessionHeartbeatLocation string

const (
	// LocalSessionHeartbeatLocation is a session on this Mac — a local tab or a
	// locally spawned headless worker.
	LocalSessionHeartbeatLocation SessionHeartbeatLocation = "local"
	// PodSessionHeartbeatLocation is a session running in a k8s pod. Declared
	// for the sibling pod task; nothing in this task posts it.
	PodSessionHeartbeatLocation SessionHeartbeatLocation = "pod"
)

// SessionHeartbeatLocations is a collection of SessionHeartbeatLocation.
type SessionHeartbeatLocations []SessionHeartbeatLocation

// AvailableSessionHeartbeatLocations holds every legal location.
var AvailableSessionHeartbeatLocations = SessionHeartbeatLocations{
	LocalSessionHeartbeatLocation,
	PodSessionHeartbeatLocation,
}

// String returns the location as a string.
func (l SessionHeartbeatLocation) String() string {
	return string(l)
}

// Validate returns an error when the location is not one of
// AvailableSessionHeartbeatLocations.
func (l SessionHeartbeatLocation) Validate(ctx context.Context) error {
	if !AvailableSessionHeartbeatLocations.Contains(l) {
		return errors.Wrapf(ctx, validation.Error, "unknown location '%s'", l)
	}
	return nil
}

// Contains reports whether the collection holds the given location.
func (l SessionHeartbeatLocations) Contains(location SessionHeartbeatLocation) bool {
	return collection.Contains(l, location)
}

// SessionHeartbeatState is what the session is doing at the moment it posts.
//
// ⚠️ The timer posts this value on every tick; hooks only *change* it between
// ticks. A parked session fires no hooks at all, so the state a reader sees for
// a blocked session is whatever the last hook set — which is exactly why the
// heartbeat, not the state, is what proves the session is alive.
type SessionHeartbeatState string

const (
	// IdleSessionHeartbeatState is a turn that ended and is waiting for the next
	// prompt. It is also the state before any hook has fired.
	IdleSessionHeartbeatState SessionHeartbeatState = "idle"
	// BusySessionHeartbeatState is a turn in progress, set by UserPromptSubmit.
	BusySessionHeartbeatState SessionHeartbeatState = "busy"
	// WaitingOnOperatorSessionHeartbeatState is a permission prompt or question
	// open, set by Notification and cleared on the next UserPromptSubmit/Stop.
	WaitingOnOperatorSessionHeartbeatState SessionHeartbeatState = "waiting-on-operator"
)

// SessionHeartbeatStates is a collection of SessionHeartbeatState.
type SessionHeartbeatStates []SessionHeartbeatState

// AvailableSessionHeartbeatStates holds every legal state.
var AvailableSessionHeartbeatStates = SessionHeartbeatStates{
	IdleSessionHeartbeatState,
	BusySessionHeartbeatState,
	WaitingOnOperatorSessionHeartbeatState,
}

// String returns the state as a string.
func (s SessionHeartbeatState) String() string {
	return string(s)
}

// Validate returns an error when the state is not one of
// AvailableSessionHeartbeatStates.
func (s SessionHeartbeatState) Validate(ctx context.Context) error {
	if !AvailableSessionHeartbeatStates.Contains(s) {
		return errors.Wrapf(ctx, validation.Error, "unknown state '%s'", s)
	}
	return nil
}

// Contains reports whether the collection holds the given state.
func (s SessionHeartbeatStates) Contains(state SessionHeartbeatState) bool {
	return collection.Contains(s, state)
}

// SessionHeartbeatSource records who posted the row. It is the field that makes
// a heartbeat evidence rather than an assertion.
//
// ⚠️ `manual` exists so its absence from the live set can be counted, not so it
// can be trusted: a hand-posted curl proves a human can reach the endpoint and
// nothing about a session being alive. SC1 asserts `mcp-timer` for exactly this
// reason — a row that a script could have written proves nothing about a
// session.
type SessionHeartbeatSource string

const (
	// HookSessionHeartbeatSource is a row written from a harness hook event.
	HookSessionHeartbeatSource SessionHeartbeatSource = "hook"
	// MCPTimerSessionHeartbeatSource is a row written by the per-session
	// supervisor MCP server's own 30-second timer. This is the only source that
	// proves a session is alive while it is parked.
	MCPTimerSessionHeartbeatSource SessionHeartbeatSource = "mcp-timer"
	// ManualSessionHeartbeatSource is a hand-posted row. It is stored so the
	// count of hand-posts is visible, and it is never proof of liveness.
	ManualSessionHeartbeatSource SessionHeartbeatSource = "manual"
)

// SessionHeartbeatSources is a collection of SessionHeartbeatSource.
type SessionHeartbeatSources []SessionHeartbeatSource

// AvailableSessionHeartbeatSources holds every legal source.
var AvailableSessionHeartbeatSources = SessionHeartbeatSources{
	HookSessionHeartbeatSource,
	MCPTimerSessionHeartbeatSource,
	ManualSessionHeartbeatSource,
}

// String returns the source as a string.
func (s SessionHeartbeatSource) String() string {
	return string(s)
}

// Validate returns an error when the source is not one of
// AvailableSessionHeartbeatSources.
func (s SessionHeartbeatSource) Validate(ctx context.Context) error {
	if !AvailableSessionHeartbeatSources.Contains(s) {
		return errors.Wrapf(ctx, validation.Error, "unknown source '%s'", s)
	}
	return nil
}

// Contains reports whether the collection holds the given source.
func (s SessionHeartbeatSources) Contains(source SessionHeartbeatSource) bool {
	return collection.Contains(s, source)
}

// SessionHeartbeat is one session's liveness declaration.
//
// ⚠️ The wire shape is snake_case while the legacy on-disk record at
// `~/.local/state/claude-supervisor/live` is camelCase (`sessionId`). That is
// deliberate rather than an oversight: the endpoint's contract is this struct,
// and the legacy reader's shape is the sibling scripts' concern — see the
// store's own comment for how the two relate.
type SessionHeartbeat struct {
	// SessionID is the posting session's own CLAUDE_CODE_SESSION_ID. It is the
	// row's identity and the key every read resolves against.
	SessionID string `json:"session_id"`
	// Task is the vault task the session is anchored to, empty when unanchored.
	Task string `json:"task"`
	// Vault is the vault the task lives in, empty when unanchored.
	Vault string `json:"vault"`
	// Location is where the session runs. `local` for this task.
	Location SessionHeartbeatLocation `json:"location"`
	// State is what the session was doing when it posted.
	State SessionHeartbeatState `json:"state"`
	// Source is who posted the row.
	Source SessionHeartbeatSource `json:"source"`
	// At is when the store received the post. It is stamped by the store from
	// its own clock, never taken from the caller — a caller-supplied timestamp
	// would let a session declare itself immortal, which is the liveness check
	// deleted by the party it checks. (Same rule as escalated_at.)
	At libtime.DateTime `json:"at"`
}

// Validate returns an error when the heartbeat is not a well-formed
// declaration. ⚠️ It judges the CALLER's declaration only: `At` is the store's
// to write, so validating it here would reject every post for a zero instant —
// the same split PushRequest and Item already carry.
func (h SessionHeartbeat) Validate(ctx context.Context) error {
	return validation.All{
		validation.Name("SessionID", validation.NotEmptyString(h.SessionID)),
		validation.Name("Task", validation.HasValidationFunc(h.validateOptionalTask)),
		validation.Name("Vault", validation.HasValidationFunc(h.validateOptionalVault)),
		validation.Name("Location", h.Location),
		validation.Name("State", h.State),
		validation.Name("Source", h.Source),
	}.Validate(ctx)
}

// validateOptionalTask enforces that task and vault are declared together.
//
// ⚠️ A row naming a task with no vault cannot be resolved — the vault is what
// makes a task name unambiguous, since task names collide across vaults — so a
// half-declared anchor is rejected rather than stored and later guessed at.
// Both empty is legal: a session with no task anchor is a real state.
func (h SessionHeartbeat) validateOptionalTask(ctx context.Context) error {
	if h.Task == "" && h.Vault != "" {
		return errors.Wrapf(
			ctx,
			validation.Error,
			"vault '%s' declared with no task",
			h.Vault,
		)
	}
	return nil
}

// validateOptionalVault is the other half of validateOptionalTask's pairing
// rule. See that method for why the two are declared together or not at all.
func (h SessionHeartbeat) validateOptionalVault(ctx context.Context) error {
	if h.Vault == "" && h.Task != "" {
		return errors.Wrapf(
			ctx,
			validation.Error,
			"task '%s' declared with no vault",
			h.Task,
		)
	}
	return nil
}

// IsFresh reports whether the heartbeat is inside the given window, measured
// against the supplied clock.
//
// ⚠️ The window is a parameter rather than a constant read here, so the
// boundary is testable on an injected clock (SC7 reads at 59 s and 61 s) and so
// the one place the number lives stays visible at the call site.
func (h SessionHeartbeat) IsFresh(now libtime.DateTime, window libtime.Duration) bool {
	return now.Sub(h.At) <= window
}
