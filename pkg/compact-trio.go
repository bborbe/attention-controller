// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"strings"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

// CompactTrioCommands is the ordered sequence an approved compact card runs.
//
// ⚠️ Three commands, three separate submitted messages — never one message
// carrying three lines. Claude Code dispatches one submitted message as one
// command, so a message holding all three runs `prepare-compact` with the other
// two as its ARGUMENTS and neither later command executes. The operator ruled
// this verbatim on 2026-10-17: "we should send these three commands, each a
// separate message, so the compact is done … it's important that there's three
// separated messages."
var CompactTrioCommands = []string{
	"/vault-cli:prepare-compact",
	"/compact",
	"/vault-cli:post-compact",
}

// compactApprovalDedupPrefix is the dedup-key prefix a session-raised compact
// card carries (`attention-push.py compact-approval`).
//
// ⚠️ It is the PREFIX and not the answer shape that selects a trio item. The
// same recommended-option shape is used by every ordinary `message` card, and
// matching on it would compact a session because the operator picked the
// highlighted option on an unrelated question.
const compactApprovalDedupPrefix = "compact-approval:"

// Defaults for the trio. They are constants beside the job rather than config
// knobs: each is a measured or ruled value, and a deployment that could re-tune
// them would make the sequence's own safety a property of the host.
const (
	// CompactTrioInterval is how often a trio advances one step.
	CompactTrioInterval = 2 * time.Second

	// CompactTrioQuiet is how long a session's transcript must go unwritten
	// before the session counts as idle enough to type into.
	//
	// ⚠️ 90 s is the reference watcher's own value, and the signal is the
	// transcript's MTIME rather than the registry's `status`: the registry reads
	// `busy` for a running turn AND for a session merely holding a background
	// shell, so a shell-holder could never be compacted — the measured 2026-10-18
	// defect. A running turn writes the transcript continuously; a background
	// shell writes nothing at all.
	CompactTrioQuiet = 90 * time.Second

	// CompactTrioIdleCeiling bounds how long one step may wait for an idle
	// session before the sequence gives up. It is the reference watcher's
	// TRIO_TURN_TIMEOUT_S.
	CompactTrioIdleCeiling = 180 * time.Second

	// CompactTrioBoundaryCeiling bounds how long `post-compact` waits for a new
	// compaction boundary. It is the reference watcher's TRIO_COMPACT_TIMEOUT_S,
	// and it is the long one because `/compact` summarises the whole history.
	CompactTrioBoundaryCeiling = 600 * time.Second

	// CompactTrioRestarts bounds how many times a sequence that has sent NOTHING
	// (or only `prepare-compact`) may restart after a wait ceiling. A sequence
	// that has already compacted never restarts — see `overdue`.
	CompactTrioRestarts = 3
)

// CompactTrioJob runs the three-command compact sequence for an approved card.
//
// ⚠️ It is a SEPARATE job rather than a branch inside the delivery arm, and that
// is what makes the sequence safe. One step waits up to 180 s for an idle
// session and the next up to 600 s for a compaction boundary, so running it
// inside `DeliverOnce` would stall every other answer delivery — including the
// permission gates that keep parked workers moving — for up to thirteen minutes
// per compact card.
type CompactTrioJob interface {
	// Run advances the sequence on every tick until the context is cancelled.
	Run(ctx context.Context) error

	// AdvanceOnce advances every live sequence by at most one step. It is a seam
	// so a spec can drive a tick deterministically instead of racing a ticker.
	AdvanceOnce(ctx context.Context) error
}

// NewCompactTrioJob creates the trio job.
//
// currentDateTimeGetter is injected rather than reached for, because every wait
// here is a decision about elapsed time and a spec has to be able to move the
// clock instead of sleeping through a quiet window.
func NewCompactTrioJob(
	store AttentionStore,
	carrier PaneCarrierAndLocator,
	writer PaneTextWriter,
	transcripts TranscriptReader,
	interval time.Duration,
	currentDateTimeGetter libtime.CurrentDateTimeGetter,
) CompactTrioJob {
	return &compactTrioJob{
		store:                 store,
		carrier:               carrier,
		writer:                writer,
		transcripts:           transcripts,
		interval:              interval,
		currentDateTimeGetter: currentDateTimeGetter,
		progress:              map[ItemID]*compactTrioProgress{},
		finished:              map[ItemID]struct{}{},
	}
}

type compactTrioJob struct {
	store                 AttentionStore
	carrier               PaneCarrierAndLocator
	writer                PaneTextWriter
	transcripts           TranscriptReader
	interval              time.Duration
	currentDateTimeGetter libtime.CurrentDateTimeGetter

	// progress is per-card state. Run drives AdvanceOnce serially, so it needs
	// no lock; a spec that calls AdvanceOnce directly is single-goroutine too.
	progress map[ItemID]*compactTrioProgress
	// finished records cards whose sequence is over — delivered, refused, or
	// abandoned at a ceiling. Without it, forgetting the progress record would
	// make the NEXT tick create a fresh one and re-run the sequence from command
	// one, which after a compaction means compacting the session a second time.
	// A card that leaves the board is dropped from both maps.
	finished map[ItemID]struct{}
}

// compactTrioProgress is one card's position in the sequence.
type compactTrioProgress struct {
	// step is the index into CompactTrioCommands of the NEXT command to send.
	step int
	// pane is the pane the sequence resolved to, or 0 before the first step.
	pane int
	// waitingSince is when the current step began waiting.
	waitingSince libtime.DateTime
	// restarts counts how many times a wait ceiling reset the sequence.
	restarts int
	// boundaryBefore is the compaction count read immediately before
	// `/compact` was sent. Only meaningful from step 2 on.
	boundaryBefore int
}

// Run advances the sequence on every tick until cancellation.
func (j *compactTrioJob) Run(ctx context.Context) error {
	if j.interval <= 0 {
		return errors.Errorf(ctx, "compact trio interval must be positive")
	}
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			glog.V(2).Infof("compact trio job stopped")
			return nil
		case <-ticker.C:
			if err := j.AdvanceOnce(ctx); err != nil {
				// A failed tick costs one interval and loses nothing: the card is
				// still answered and the progress map still holds its position.
				glog.Warningf("job=compact-trio action=cycle_error error=%v", err)
			}
		}
	}
}

// AdvanceOnce advances every live sequence by at most one step.
func (j *compactTrioJob) AdvanceOnce(ctx context.Context) error {
	// ⚠️ Bounded like the delivery arm's own board read: this runs on a 2 s tick
	// and a store that stalls would otherwise hold the whole trio behind it.
	readCtx, cancel := context.WithTimeout(ctx, storeReadTimeout)
	defer cancel()
	items, err := j.store.ReadBoard(readCtx)
	if err != nil {
		return errors.Wrap(ctx, err, "read board failed")
	}
	seen := make(map[ItemID]struct{}, len(items))
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return nil
		}
		if !compactTrioOwns(item) {
			continue
		}
		seen[item.ItemID] = struct{}{}
		if _, done := j.finished[item.ItemID]; done {
			continue
		}
		j.advance(ctx, item)
	}
	// A card that left the board is finished, closed elsewhere, or answered by
	// another arm: its sequence is over and its state must not outlive it.
	for id := range j.progress {
		if _, ok := seen[id]; !ok {
			delete(j.progress, id)
		}
	}
	for id := range j.finished {
		if _, ok := seen[id]; !ok {
			delete(j.finished, id)
		}
	}
	return nil
}

// compactTrioOwns reports whether an approved compact card's answer is the
// trio's to execute rather than the delivery arm's to type as prose.
//
// ⚠️ The two arms read ONE answer, so exactly one of them must own it. This
// predicate is shared by the trio's selection and by the delivery arm's
// `deliverable` guard: if the delivery arm kept the card it would type
// "Operator answer, via attention board: Compact now [id]" — prose that starts
// with "Operator", not "/", so the session would read it as a message and never
// dispatch anything — while the trio compacted the session underneath it.
func compactTrioOwns(item Item) bool {
	if item.State != AnsweredState || item.AnsweredAt == nil {
		return false
	}
	if !strings.HasPrefix(item.DedupKey.String(), compactApprovalDedupPrefix) {
		return false
	}
	return compactApproved(item)
}

// compactApproved reports whether the operator's answer APPROVED the compaction.
//
// A card carrying NO options is the ack shape: there is nothing to choose
// between, so acknowledging IS the approval and it passes. A `message` card
// approves only when the answer names one of its `recommended` labels.
//
// ⚠️ It fails CLOSED. A false approval compacts a session the operator declined
// and cannot be undone; a false decline only leaves the session as it was, and
// the card may be raised again. Measured 2026-10-17: an implementation that read
// the empty string as the approve signal ran the trio on "Not now" too, turning
// a no-op into an unwanted compaction.
func compactApproved(item Item) bool {
	if len(item.Options) == 0 {
		return true
	}
	approved := make(map[string]struct{}, len(item.Options))
	for _, option := range item.Options {
		if option.Recommended && strings.TrimSpace(option.Label) != "" {
			approved[strings.TrimSpace(option.Label)] = struct{}{}
		}
	}
	if len(approved) == 0 {
		return false
	}
	if item.Answer == nil || item.Answer.Kind != OptionAnswerKind {
		return false
	}
	_, ok := approved[strings.TrimSpace(item.Answer.Value)]
	return ok
}

// advance moves one card at most one step.
func (j *compactTrioJob) advance(ctx context.Context, item Item) {
	sessionID, ok := itemSessionID(ctx, item)
	if !ok {
		glog.V(2).Infof(
			"job=compact-trio action=no_session item=%s", item.ItemID,
		)
		j.finish(item.ItemID)
		return
	}
	progress := j.progressOf(item.ItemID)
	if progress.step >= len(CompactTrioCommands) {
		j.finish(item.ItemID)
		return
	}
	// Pane 0 makes the carrier resolve the pane from the session's own tty; the
	// resolved id is kept so the gates can read the composer without resolving
	// again on every tick.
	delivery := &PaneDelivery{
		SessionID:   sessionID,
		StoreItemID: string(item.ItemID),
	}
	pane, ok := j.resolvePane(ctx, item, delivery)
	if !ok {
		return
	}
	if progress.pane != pane {
		progress.pane = pane
		progress.waitingSince = j.currentDateTimeGetter.Now()
	}
	j.step(ctx, item, sessionID, pane, delivery, progress)
}

// resolvePane resolves the card's pane, reporting whether the sequence may
// proceed this tick. It also finishes the card on a terminal verdict, so the
// caller never has to.
func (j *compactTrioJob) resolvePane(
	ctx context.Context,
	item Item,
	delivery *PaneDelivery,
) (int, bool) {
	verdict, pane, reason := j.carrier.Locate(ctx, delivery)
	switch verdict {
	case DeliveryOK:
		return pane, true
	case DeliveryRetry:
		// A transient read failure: keep the position and try again next tick.
		return 0, false
	case DeliveryNoPane:
		glog.V(2).Infof(
			"job=compact-trio action=no-pane item=%s reason=%q", item.ItemID, reason,
		)
	default:
		glog.V(2).Infof(
			"job=compact-trio action=refuse item=%s verdict=%s reason=%q",
			item.ItemID,
			verdict,
			reason,
		)
	}
	j.finish(item.ItemID)
	return 0, false
}

// step sends the next command when its gate is open, or records why it waited.
func (j *compactTrioJob) step(
	ctx context.Context,
	item Item,
	sessionID string,
	pane int,
	delivery *PaneDelivery,
	progress *compactTrioProgress,
) {
	command := CompactTrioCommands[progress.step]
	open, why := j.gateOpen(ctx, sessionID, pane, progress)
	if !open {
		if j.overdue(progress) {
			j.giveUp(item, progress, why)
		}
		return
	}
	// ⚠️ The boundary count is read immediately BEFORE `/compact` is sent, so
	// the wait after it counts only the boundary that command produces.
	if progress.step == 1 {
		boundaries, ok := j.transcripts.CompactionBoundaries(sessionID)
		if !ok {
			return
		}
		progress.boundaryBefore = boundaries
	}
	verdict, why := j.carrier.TypeIntoPane(ctx, delivery, command, true)
	switch verdict {
	case DeliveryOK:
		// One record PER SEND, naming the command and the pane. This is the
		// artifact that tells an automated run from a hand-typed one.
		glog.V(2).Infof(
			"job=compact-trio action=send item=%s command=%s pane=%d step=%d/%d",
			item.ItemID,
			command,
			pane,
			progress.step+1,
			len(CompactTrioCommands),
		)
		progress.step++
		progress.waitingSince = j.currentDateTimeGetter.Now()
		if progress.step >= len(CompactTrioCommands) {
			glog.Infof(
				"job=compact-trio action=delivered item=%s pane=%d steps=%d",
				item.ItemID,
				pane,
				len(CompactTrioCommands),
			)
			j.finish(item.ItemID)
		}
	case DeliveryRetry:
		// Nothing was sent, so the position is unchanged and the ceiling applies
		// on a later tick.
	default:
		glog.V(2).Infof(
			"job=compact-trio action=stop item=%s command=%s step=%d/%d verdict=%s reason=%q",
			item.ItemID,
			command,
			progress.step+1,
			len(CompactTrioCommands),
			verdict,
			why,
		)
		j.finish(item.ItemID)
	}
}

// gateOpen reports whether the next command may be typed, and why not.
//
// Two halves, and both are required because they fail differently. The composer
// half catches the operator's own half-typed input, which Enter would submit.
// The transcript half catches a running turn: its input box is drawn and EMPTY,
// so the composer alone reads "empty" for a session that will swallow the text
// without ever submitting it — the measured 2026-10-17 defect, where `/compact`
// was typed while `prepare-compact` still ran, every Enter was spent, and the
// text left in the composer made every later retry read "busy".
func (j *compactTrioJob) gateOpen(
	ctx context.Context,
	sessionID string,
	pane int,
	progress *compactTrioProgress,
) (bool, string) {
	// `post-compact` is gated on a NEW compaction boundary. An emptiness-only
	// gate would fire it mid-compaction, losing exactly what the sequence exists
	// to preserve.
	if progress.step == len(CompactTrioCommands)-1 {
		boundaries, ok := j.transcripts.CompactionBoundaries(sessionID)
		if !ok {
			return false, "transcript unreadable"
		}
		if boundaries <= progress.boundaryBefore {
			return false, "no new compaction boundary"
		}
	}
	if j.writer.ComposerState(ctx, pane) != ComposerEmpty {
		return false, "composer not empty"
	}
	modified, ok := j.transcripts.ModifiedAt(sessionID)
	if !ok {
		return false, "transcript unreadable"
	}
	if j.currentDateTimeGetter.Now().Time().Sub(modified) < CompactTrioQuiet {
		return false, "transcript still being written"
	}
	return true, "idle"
}

// overdue reports whether the current step has waited past its ceiling.
func (j *compactTrioJob) overdue(progress *compactTrioProgress) bool {
	ceiling := CompactTrioIdleCeiling
	if progress.step == len(CompactTrioCommands)-1 {
		ceiling = CompactTrioBoundaryCeiling
	}
	return j.currentDateTimeGetter.Now().Sub(progress.waitingSince) >
		libtime.Duration(ceiling)
}

// giveUp handles a step that waited past its ceiling.
//
// ⚠️ A sequence that has already sent `/compact` NEVER restarts. Restarting
// would re-run `prepare-compact` and `/compact`, i.e. compact the session a
// second time — and the runbook's own rule is the opposite: "No drop → report
// COMPACT FAILED and stop. Never retry: a retry loop against a wedged session is
// worse than a full context." A sequence that has sent nothing, or only
// `prepare-compact`, may restart, bounded — that is the reference watcher's
// documented cooldown cost (one extra prepare turn), and it is the only way a
// session that was busy at the moment the card was approved ever makes progress.
func (j *compactTrioJob) giveUp(
	item Item,
	progress *compactTrioProgress,
	why string,
) {
	if progress.step > 1 {
		glog.Warningf(
			"job=compact-trio action=compact_failed item=%s step=%d/%d reason=%q",
			item.ItemID,
			progress.step+1,
			len(CompactTrioCommands),
			why,
		)
		j.finish(item.ItemID)
		return
	}
	progress.restarts++
	if progress.restarts > CompactTrioRestarts {
		glog.Warningf(
			"job=compact-trio action=abandon item=%s step=%d/%d restarts=%d reason=%q",
			item.ItemID,
			progress.step+1,
			len(CompactTrioCommands),
			progress.restarts-1,
			why,
		)
		j.finish(item.ItemID)
		return
	}
	glog.V(2).Infof(
		"job=compact-trio action=restart item=%s step=%d/%d restarts=%d reason=%q",
		item.ItemID,
		progress.step+1,
		len(CompactTrioCommands),
		progress.restarts,
		why,
	)
	progress.step = 0
	progress.boundaryBefore = 0
	progress.waitingSince = j.currentDateTimeGetter.Now()
}

// progressOf returns the card's progress record, creating it on first sighting.
func (j *compactTrioJob) progressOf(id ItemID) *compactTrioProgress {
	if progress, ok := j.progress[id]; ok {
		return progress
	}
	progress := &compactTrioProgress{waitingSince: j.currentDateTimeGetter.Now()}
	j.progress[id] = progress
	return progress
}

// finish ends a card's sequence for good.
//
// ⚠️ It records the card in `finished` rather than only forgetting its progress,
// because a forgotten progress record is indistinguishable from a card never
// seen — and the next tick would create a fresh one and re-run the sequence from
// `prepare-compact`. After a compaction that means compacting the session a
// second time, which is the one outcome the ceiling exists to prevent.
func (j *compactTrioJob) finish(id ItemID) {
	delete(j.progress, id)
	j.finished[id] = struct{}{}
}

// Compile-time guard: the job must satisfy its own interface.
var _ CompactTrioJob = (*compactTrioJob)(nil)
