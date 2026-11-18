// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	libkv "github.com/bborbe/kv"
	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
)

// EvidenceNote is appended to an item whose claim was never observed, so the
// reopened card says why it is back.
const EvidenceNote = "reopened: the work this answer claimed was not observed before the evidence deadline"

// sweepAwaitingEvidence closes the items whose claimed artifact is now visible
// and reopens the ones whose deadline passed without it.
//
// ⚠️ It runs inside SweepAnswered's transaction, beside the answered and
// re-raise passes, so the three share one minute tick and one write lock. The
// verification probe (a `gh` call) is the only slow step and it runs at most
// once per parked item per tick.
func (a *attentionStore) sweepAwaitingEvidence(
	ctx context.Context,
	tx libkv.Tx,
	now libtime.DateTime,
) (int, error) {
	keys, err := a.awaitingEvidenceKeys(ctx, tx)
	if err != nil {
		return 0, err
	}
	changed := 0
	for _, key := range keys {
		moved, err := a.sweepOneAwaitingEvidence(ctx, tx, key, now)
		if err != nil {
			return changed, err
		}
		if moved {
			changed++
		}
	}
	return changed, nil
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

// sweepOneAwaitingEvidence closes or reopens one parked item, reporting whether
// it moved it.
//
// ⚠️ The state is re-read inside the write rather than trusted from the index
// snapshot: the scan and the write are two steps, and an item can have been
// closed or reopened between them.
func (a *attentionStore) sweepOneAwaitingEvidence(
	ctx context.Context,
	tx libkv.Tx,
	key string,
	now libtime.DateTime,
) (bool, error) {
	item, err := a.store.Get(ctx, tx, key)
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, errors.Wrap(ctx, err, "get awaiting-evidence item failed")
	}
	if item.State != AwaitingEvidenceState {
		return false, nil
	}
	if EvidenceAllObserved(ctx, item.EvidenceClaims, a.evidenceRunner) {
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
	// The claim was never observed in time. The card goes back on the board with
	// the claim intact and a note saying why, because an answer nobody can
	// verify is a question the operator still has to settle.
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
	item.State = OpenState
	item.EvidenceDeadline = nil
	// ⚠️ The answer stamps MUST be cleared, or the reopened card never reaches
	// the board. `answeredIndexWorthy` is `AnsweredAt != nil`, so a reopened item
	// that kept its stamp stays in the durable answered index; `suppressAnsweredTwins`
	// then drops the now-open card from the board and `closeRedundantReraises`
	// closes it as an answered twin. The reopen would be invisible — the exact
	// failure this state exists to make visible.
	item.AnsweredAt = nil
	item.AnsweredBy = ""
	item.AnsweredClient = nil
	item.ResolvedBy = ""
	item.Decision = ""
	item.Answer = nil
	item.Answers = nil
	item.Context = ItemContext(appendNote(string(item.Context), EvidenceNote))
	if err := a.putItem(ctx, tx, *item); err != nil {
		return false, errors.Wrap(ctx, err, "reopen unverified item failed")
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
