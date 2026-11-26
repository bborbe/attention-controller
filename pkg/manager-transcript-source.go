// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"time"

	libtime "github.com/bborbe/time"
)

// managerHeartbeatTTL is how long a session heartbeat reads live. A headless
// worker re-stamps every 30 s and reads live until 60 s; the TTL is a constant,
// never a config knob.
const managerHeartbeatTTL = 60 * time.Second

// managerGateDetailMax bounds the detail a gate carries, in runes, so one long
// closer body cannot paste a page into a manager's composer.
const managerGateDetailMax = 150

// ManagerTranscriptSource is the wake job's second source: the registry, the
// heartbeat store and the transcripts, read once per pass for a manager's
// tracked set.
//
// ⚠️ It is a SECOND SOURCE on the one wake job, not a second job: a mid-turn
// `👤 You:` closer never reaches the store, so the store source cannot carry
// it, and two writers typing into the same manager pane is the dual-writer
// failure the port exists to avoid.
type ManagerTranscriptSource interface {
	// GateStates returns one reading per session, in the order given.
	GateStates(ctx context.Context, sessionIDs []string) (ManagerGateStates, error)
}

// NewManagerTranscriptSource creates the source over the registry directory, the
// heartbeat store and the transcript reader.
func NewManagerTranscriptSource(
	registryDir string,
	heartbeats SessionHeartbeatStore,
	transcripts TranscriptReader,
	currentDateTimeGetter libtime.CurrentDateTimeGetter,
) ManagerTranscriptSource {
	return &managerTranscriptSource{
		registryDir:           registryDir,
		heartbeats:            heartbeats,
		transcripts:           transcripts,
		currentDateTimeGetter: currentDateTimeGetter,
	}
}

type managerTranscriptSource struct {
	registryDir           string
	heartbeats            SessionHeartbeatStore
	transcripts           TranscriptReader
	currentDateTimeGetter libtime.CurrentDateTimeGetter
}

// GateStates runs one pass over the given session ids.
//
// ⚠️ Fail-open is split by input, and the split falls out of managerGateVerdict's
// order rather than a special case here: an unreadable TRANSCRIPT leaves
// CloserKnown false, Stuck "", PendingTool "" and Frozen false, so a `waiting`
// session is still gated by the registry half and a session whose only evidence
// was the transcript contributes no gate. An unreadable REGISTRY leaves Status
// nil, so a session WITHOUT a fresh heartbeat reads `unregistered` (held) while a
// headless-live session reads the transcript half alone. The registry error is
// therefore consumed here, never returned: returning it would skip the whole arm
// and reverse the split.
func (s *managerTranscriptSource) GateStates(
	ctx context.Context,
	sessionIDs []string,
) (ManagerGateStates, error) {
	statuses, registryErr := readSessionStatuses(ctx, s.registryDir)
	registryReadable := registryErr == nil
	now := s.currentDateTimeGetter.Now()
	// ⚠️ ONE scan for every session, resolved before the loop. Resolving a path
	// inside gateState cost four whole-tree filepath.Glob walks per session per
	// pass (LastAssistantText, PendingToolCall, ModifiedAt and transcriptTails
	// each resolved their own), which measured 63.3 % of process CPU on the live
	// daemon at the 10 s wake interval (2026-11-26). Paths() answers every session
	// from a single walk — the same reason the interface documents it.
	paths := s.transcripts.Paths()
	states := make(ManagerGateStates, 0, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		states = append(
			states,
			s.gateState(ctx, sessionID, paths[sessionID], statuses, registryReadable, now),
		)
	}
	return states, nil
}

// gateState reads one session's two halves and computes its verdict.
func (s *managerTranscriptSource) gateState(
	ctx context.Context,
	sessionID string,
	path string,
	statuses map[string]string,
	registryReadable bool,
	now libtime.DateTime,
) ManagerGateState {
	status := managerRegisteredStatus(statuses, sessionID, registryReadable)
	headlessLive := managerHeartbeatLive(ctx, s.heartbeats, sessionID, now)
	// ⚠️ One evidence read for all four facts, from the path already resolved.
	evidence := s.transcripts.GateEvidence(path)
	closerBody, _ := managerCloserBody(evidence.LastAssistantText)
	frozen := managerTranscriptFrozen(
		evidence.ModifiedAt,
		evidence.ModifiedAtKnown,
		now.Time(),
		managerLiveWindow,
	)
	stuck := managerStuckReason(
		evidence.LastAssistantText,
		evidence.CloserTail,
		evidence.ChannelTail,
	)
	decision, reason := managerGateVerdict(managerGateInput{
		Status:       status,
		HeadlessLive: headlessLive,
		Stuck:        stuck,
		CloserKnown:  evidence.AssistantTextKnown,
		CloserBody:   closerBody,
		PendingTool:  evidence.PendingToolCall,
		Frozen:       frozen,
	})
	return ManagerGateState{
		SessionID: sessionID,
		Label:     sessionID,
		Decision:  decision,
		Reason:    reason,
		Detail:    managerGateDetail(reason, closerBody),
	}
}

// managerRegisteredStatus returns the registry status for a session, or nil when
// the registry was unreadable or does not list the session. The nil is
// load-bearing: an unlisted session and an idle one must stay different facts.
func managerRegisteredStatus(
	statuses map[string]string,
	sessionID string,
	registryReadable bool,
) *string {
	if !registryReadable {
		return nil
	}
	value, ok := statuses[sessionID]
	if !ok {
		return nil
	}
	return &value
}

// managerHeartbeatLive reports whether a session has a fresh heartbeat. Absent,
// error or not-fresh all read false, which is the safe direction: a stuck
// headless worker then reads `unregistered` rather than `stuck:`.
func managerHeartbeatLive(
	ctx context.Context,
	store SessionHeartbeatStore,
	sessionID string,
	now libtime.DateTime,
) bool {
	if store == nil {
		return false
	}
	heartbeat, ok, err := store.Get(ctx, sessionID)
	if err != nil || !ok {
		return false
	}
	return heartbeat.IsFresh(now, libtime.Duration(managerHeartbeatTTL))
}

// managerGateDetail renders the detail a gate carries: the reason, plus the
// closer body when there is one, bounded to managerGateDetailMax runes.
func managerGateDetail(reason, closerBody string) string {
	detail := reason
	if closerBody != "" {
		detail = reason + " — " + closerBody
	}
	runes := []rune(detail)
	if len(runes) > managerGateDetailMax {
		return string(runes[:managerGateDetailMax])
	}
	return detail
}

// Compile-time guard: the source must satisfy its own interface.
var _ ManagerTranscriptSource = (*managerTranscriptSource)(nil)
