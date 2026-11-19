// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
)

// EvidenceNote is appended to an item whose claim was never observed, so the
// reopened card says why it is back.
const EvidenceNote = "reopened: the work this answer claimed was not observed before the evidence deadline"

// parkedItem is one parked card as the probe pass sees it.
type parkedItem struct {
	key      string
	claims   []string
	deadline *libtime.DateTime
}

// evidenceVerdict is one parked item's probe result, computed OUTSIDE the write
// transaction.
type evidenceVerdict struct {
	key      string
	observed bool
	// deadline is the deadline the item carried when it was probed. The apply
	// pass re-checks it so a RE-ANSWER of the parked item during the probe
	// window — which stamps a new deadline and possibly new claims — cannot be
	// settled on the old claim's verdict. That would reintroduce the false
	// `done` this whole path exists to prevent.
	deadline *libtime.DateTime
}

// sweepAwaitingEvidence closes the items whose claimed artifact is now visible
// and reopens the ones whose deadline passed without it.
//
// ⚠️ TWO PHASES, and the split is load-bearing: the probes shell out to `gh`
// (up to EvidenceProbeTimeout each), and bbolt admits one writer, so probing
// inside the write transaction held the store's write lock across network I/O
// and blocked every Push/Answer/Close for the duration. Phase one reads the
// parked set and probes it with no transaction open; phase two applies the
// decisions in one short write.
func (a *attentionStore) sweepAwaitingEvidence(
	ctx context.Context,
	now libtime.DateTime,
) (int, error) {
	verdicts, err := a.evaluateParkedEvidence(ctx)
	if err != nil {
		return 0, err
	}
	if len(verdicts) == 0 {
		return 0, nil
	}
	changed := 0
	err = a.db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
		moved, err := a.applyEvidenceVerdicts(ctx, tx, verdicts, now)
		changed = moved
		return err
	})
	if err != nil {
		return 0, errors.Wrap(ctx, err, "apply evidence verdicts failed")
	}
	return changed, nil
}

// evaluateParkedEvidence reads the parked set and probes each claim with no
// write transaction open.
func (a *attentionStore) evaluateParkedEvidence(ctx context.Context) ([]evidenceVerdict, error) {
	items := make([]parkedItem, 0)
	if err := a.db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		keys, err := a.awaitingEvidenceKeys(ctx, tx)
		if err != nil {
			return err
		}
		for _, key := range keys {
			item, err := a.store.Get(ctx, tx, key)
			if err != nil {
				if isNotFound(err) {
					continue
				}
				return errors.Wrap(ctx, err, "get awaiting-evidence item failed")
			}
			if item.State != AwaitingEvidenceState {
				continue
			}
			items = append(items, parkedItem{
				key:      key,
				claims:   item.EvidenceClaims,
				deadline: item.EvidenceDeadline,
			})
		}
		return nil
	}); err != nil {
		return nil, errors.Wrap(ctx, err, "read parked evidence set failed")
	}
	// ⚠️ Truncate by DEADLINE, not by key order. Live-index keys are random
	// 128-bit ids, so a key-ordered prefix is a stable arbitrary subset: the items
	// outside it would never be probed, hence never closed on an observed claim
	// and never reopened at their deadline — they would sit parked forever. The
	// soonest deadline goes first, and every probed item leaves the parked set
	// (closed, or reopened when its deadline passes), so each tick makes progress
	// on the oldest waiters.
	sort.Slice(items, func(a, b int) bool {
		if items[a].deadline == nil || items[b].deadline == nil {
			return items[a].deadline == nil
		}
		return items[a].deadline.Time().Before(items[b].deadline.Time())
	})
	return a.probeWithinBudget(ctx, items), nil
}

// applyEvidenceVerdicts closes or reopens the parked items a probe settled.
//
// ⚠️ The state is re-read inside the write: the probe ran with no transaction
// open, so an item can have been answered, closed or re-parked in between.
func (a *attentionStore) applyEvidenceVerdicts(
	ctx context.Context,
	tx libkv.Tx,
	verdicts []evidenceVerdict,
	now libtime.DateTime,
) (int, error) {
	changed := 0
	for _, verdict := range verdicts {
		moved, err := a.applyEvidenceVerdict(ctx, tx, verdict, now)
		if err != nil {
			return changed, err
		}
		if moved {
			changed++
		}
	}
	return changed, nil
}

// applyEvidenceVerdict settles one parked item.
func (a *attentionStore) applyEvidenceVerdict(
	ctx context.Context,
	tx libkv.Tx,
	verdict evidenceVerdict,
	now libtime.DateTime,
) (bool, error) {
	item, err := a.store.Get(ctx, tx, verdict.key)
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, errors.Wrap(ctx, err, "get awaiting-evidence item failed")
	}
	if item.State != AwaitingEvidenceState {
		return false, nil
	}
	// ⚠️ The item must be the one that was probed. A re-answer during the probe
	// window stamps a new deadline (and possibly new claims), so settling it on
	// the old verdict would close a card nobody verified — or reopen one the
	// operator has just settled.
	if !sameDeadline(item.EvidenceDeadline, verdict.deadline) {
		return false, nil
	}
	if verdict.observed {
		item.State = AnsweredState
		item.EvidenceDeadline = nil
		if err := a.putItem(ctx, tx, *item); err != nil {
			return false, errors.Wrap(ctx, err, "close verified item failed")
		}
		return true, nil
	}
	if item.EvidenceDeadline == nil || now.Before(*item.EvidenceDeadline) {
		return false, nil
	}
	// ⚠️ A card whose target pane AND session are both provably gone is retired,
	// not reopened. Reopening it puts a question on the board whose only action
	// is "type into a pane that no longer exists" — the operator's "more useless
	// attention cards". Unprovable is NOT gone: see targetGone.
	if a.targetGone(ctx, item) {
		return a.retireDeadTarget(ctx, tx, item, now)
	}
	// The claim was never observed in time. The card goes back on the board with
	// the claim intact and a note saying why, because an answer nobody can
	// verify is a question the operator still has to settle.
	//
	// ⚠️ The durable answered-index entry MUST go too, and before the stamps are
	// cleared: `reconcileAnsweredIndex` is ADD-ONLY (by design — a re-raise shares
	// the record), so clearing `AnsweredAt` alone leaves the ask recorded as
	// answered. `ReadBoard` then drops the now-open card through
	// `suppressAnsweredTwins` and `closeRedundantReraises` closes it as an
	// answered twin — the reopen would be invisible, which is precisely the
	// failure this state exists to make visible.
	if item.AnsweredAt != nil {
		if err := a.removeIndexKey(
			ctx, tx, a.answeredIndex, askIdentity(*item), "answered",
		); err != nil {
			return false, errors.Wrap(ctx, err, "remove answered index entry failed")
		}
	}
	// ⚠️ The expiry must be re-armed. A parked item keeps the producer's original
	// ExpiresAt, and only OpenState items are renewed by the live sweep — so the
	// clock kept ticking while the item waited and a reopen could land already
	// expired, closing the card the same minute it came back.
	expiresAt := now.Add(DefaultItemExpiry)
	item.ExpiresAt = &expiresAt
	item.State = OpenState
	item.EvidenceDeadline = nil
	item.AnsweredAt = nil
	item.AnsweredBy = ""
	item.AnsweredClient = nil
	item.ResolvedBy = ""
	item.Decision = ""
	item.Answer = nil
	item.Answers = nil
	item.Context = ItemContext(appendEvidenceNote(string(item.Context), now))
	if err := a.putItem(ctx, tx, *item); err != nil {
		return false, errors.Wrap(ctx, err, "reopen unverified item failed")
	}
	return true, nil
}

// awaitingEvidenceKeys lists the live-index keys currently parked.
func (a *attentionStore) awaitingEvidenceKeys(
	ctx context.Context,
	tx libkv.Tx,
) ([]string, error) {
	keys := make([]string, 0)
	if err := a.liveIndex.Map(
		ctx,
		tx,
		func(ctx context.Context, key string, item Item) error {
			if key == liveIndexMarkerKey || item.State != AwaitingEvidenceState {
				return nil
			}
			keys = append(keys, key)
			return nil
		},
	); err != nil {
		return nil, errors.Wrap(ctx, err, "scan live index for awaiting evidence failed")
	}
	return keys, nil
}

// RetiredNote is appended to a card closed because its target is gone, so the
// board's history says why it will not come back.
const RetiredNote = "retired: the pane and session this card was waiting on are both gone"

// targetGone reports whether the item's target pane and session are BOTH
// provably gone.
//
// ⚠️ Every read must SUCCEED for this to answer true, and that asymmetry is the
// whole rule. The pane lister's own contract draws it: an empty listing read
// successfully proves a recorded pane is gone, while a failed read proves
// nothing. So a nil lister, a listing error, an unresolvable session name, or a
// non-session liveness model all answer "unprovable" — and unprovable reopens
// exactly as before. Retiring on a failed read would delete a live question.
func (a *attentionStore) targetGone(ctx context.Context, item *Item) bool {
	if a.paneLister == nil || a.sessionsDir == "" {
		return false
	}
	model, sessionID, err := item.LivenessRef.Parse(ctx)
	if err != nil || model != SessionLivenessModel {
		// `owner:` is deliberately always-live, and `heartbeat:` has its own
		// clock — neither is a pane target.
		return false
	}
	if a.sessionLivenessChecker.IsLive(ctx, sessionID) {
		return false
	}
	names, err := readSessionNamesWithError(ctx, a.sessionsDir)
	if err != nil {
		return false
	}
	record, ok := names[sessionID]
	if !ok || record.Name == "" {
		return false
	}
	panes, err := a.paneLister.List(ctx)
	if err != nil {
		return false
	}
	for paneID := range panes {
		if OwnsPane(panes, paneID, record.Name) {
			return false
		}
	}
	return true
}

// retireDeadTarget closes a card whose target is gone, instead of reopening it.
func (a *attentionStore) retireDeadTarget(
	ctx context.Context,
	tx libkv.Tx,
	item *Item,
	now libtime.DateTime,
) (bool, error) {
	closedAt := now
	item.State = ClosedState
	item.ClosedAt = &closedAt
	item.EvidenceDeadline = nil
	item.ResolvedBy = "retired: target pane and session are gone"
	item.Context = ItemContext(appendNote(string(item.Context), RetiredNote))
	if err := a.putItem(ctx, tx, *item); err != nil {
		return false, errors.Wrap(ctx, err, "retire dead-target item failed")
	}
	return true, nil
}

// appendNote appends one line to an item's context, keeping an empty note empty
// rather than starting it with a newline.
func appendNote(note string, line string) string {
	if note == "" {
		return line
	}
	return note + "\n" + line
}

// evidenceNotePrefix opens the one line a reopen leaves behind.
//
// ⚠️ The line is REPLACED on each reopen, never appended. An item that bounced
// N times used to carry N identical `reopened: …` lines, which is what the
// operator saw as "some cards in the subtitle multiple times the same line".
const evidenceNotePrefix = "reopened ×"

// appendEvidenceNote records one more reopen on an item's context, keeping a
// single line that carries the count and the time of the last reopen.
//
// An item written before this change carries the bare EvidenceNote; it counts
// as one reopen, so its next one renders `reopened ×2, …`.
func appendEvidenceNote(note string, now libtime.DateTime) string {
	if strings.TrimSpace(note) == "" {
		return evidenceReopenLine(1, now)
	}
	count := 0
	kept := make([]string, 0, 1)
	for _, line := range strings.Split(note, "\n") {
		if n, ok := evidenceReopenCount(line); ok {
			// ⚠️ ACCUMULATE, never assign. The defect this replaces left N
			// identical bare notes on one item; assigning would collapse 34 of
			// them to `×2` and throw away the very count the operator wanted.
			count += n
			continue
		}
		kept = append(kept, line)
	}
	count++
	kept = append(kept, evidenceReopenLine(count, now))
	return strings.Join(kept, "\n")
}

// evidenceReopenCount reads the reopen count out of a context line. It returns
// true for both the counted line and the legacy bare EvidenceNote, which counts
// as one reopen.
func evidenceReopenCount(line string) (int, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == EvidenceNote {
		return 1, true
	}
	if !strings.HasPrefix(trimmed, evidenceNotePrefix) {
		return 0, false
	}
	rest := strings.TrimPrefix(trimmed, evidenceNotePrefix)
	countText, _, found := strings.Cut(rest, ",")
	if !found {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(countText))
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// evidenceReopenLine renders the one counted reopen line.
func evidenceReopenLine(count int, now libtime.DateTime) string {
	return evidenceNotePrefix + strconv.Itoa(count) + ", last " + now.Time().Format("15:04")
}

// sameDeadline reports whether two optional deadlines are the same instant,
// treating both-nil as equal.
func sameDeadline(a, b *libtime.DateTime) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Time().Equal(b.Time())
}

// probeWithinBudget probes parked items until the sweep's probe budget is
// spent, returning one verdict per probed item.
//
// ⚠️ The budget bounds PROBE CALLS, not items. One item can carry several claims
// (a two-tab answer names two artifacts) and each claim is its own `gh` call, so
// an item-count bound would let two multi-claim items spend four probes' worth
// of the sweep budget.
func (a *attentionStore) probeWithinBudget(
	ctx context.Context,
	items []parkedItem,
) []evidenceVerdict {
	verdicts := make([]evidenceVerdict, 0, len(items))
	budget := MaxEvidenceProbesPerSweep
	for _, item := range items {
		cost := len(item.claims)
		if cost == 0 {
			cost = 1
		}
		// ⚠️ Always probe at least ONE item, even when its claim count exceeds the
		// whole budget. Breaking here stranded any item with three or more claims
		// — it could never be probed, so it was never closed on an observed claim
		// and never reopened at its deadline. One item per tick is progress; the
		// sweep's own context still bounds the time.
		if len(verdicts) > 0 && cost > budget {
			break
		}
		budget -= cost
		if budget < 0 {
			budget = 0
		}
		verdicts = append(verdicts, evidenceVerdict{
			key:      item.key,
			observed: EvidenceAllObserved(ctx, item.claims, a.evidenceRunner),
			deadline: item.deadline,
		})
	}
	return verdicts
}
