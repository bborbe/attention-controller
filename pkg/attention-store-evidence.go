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
	due := make([]string, 0)
	if err := a.liveIndex.Map(
		ctx,
		tx,
		func(ctx context.Context, key string, item Item) error {
			if key == liveIndexMarkerKey || item.State != AwaitingEvidenceState {
				return nil
			}
			due = append(due, key)
			return nil
		},
	); err != nil {
		return 0, errors.Wrap(ctx, err, "scan live index for awaiting evidence failed")
	}
	changed := 0
	for _, key := range due {
		// ⚠️ The state is re-read inside the write rather than trusted from the
		// index snapshot: the scan and the write are two steps, and an item can
		// have been closed or reopened between them.
		item, err := a.store.Get(ctx, tx, key)
		if err != nil {
			if isNotFound(err) {
				continue
			}
			return changed, errors.Wrap(ctx, err, "get awaiting-evidence item failed")
		}
		if item.State != AwaitingEvidenceState {
			continue
		}
		if EvidenceObserved(ctx, item.EvidenceClaim, evidenceRunner) {
			item.State = AnsweredState
			item.EvidenceDeadline = nil
			if err := a.putItem(ctx, tx, *item); err != nil {
				return changed, errors.Wrap(ctx, err, "close verified item failed")
			}
			changed++
			continue
		}
		deadline := item.EvidenceDeadline
		if deadline == nil || now.Before(*deadline) {
			continue
		}
		// The claim was never observed in time. The card goes back on the board
		// with the claim intact and a note saying why, because an answer nobody
		// can verify is a question the operator still has to settle.
		item.State = OpenState
		item.EvidenceDeadline = nil
		item.Context = ItemContext(appendNote(string(item.Context), EvidenceNote))
		if err := a.putItem(ctx, tx, *item); err != nil {
			return changed, errors.Wrap(ctx, err, "reopen unverified item failed")
		}
		changed++
	}
	return changed, nil
}

// appendNote appends one line to an item's context, keeping an empty note empty
// rather than starting it with a newline.
func appendNote(note string, line string) string {
	if note == "" {
		return line
	}
	return note + "\n" + line
}
