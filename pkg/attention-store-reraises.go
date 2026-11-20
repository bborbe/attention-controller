// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

// This file holds the redundant-re-raise close path. It lives beside
// attention-store-impl.go rather than inside it because that file sits at the
// repository's 2000-line ceiling — the same reason attention-store-close.go
// exists.

import (
	"context"

	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
)

// closeRedundantReraises closes every open item that repeats an ask already
// answered — the same dedup key AND payload.
//
// ⚠️ This is the other half of the fix, and the read-side half alone is not
// enough. suppressAnsweredTwins withholds such a row from the BOARD, but the
// row stays `open` in the store: the producer polling its own item id waits on
// a card the operator is never shown, and nothing in the store ever closes it
// (SweepAnswered closed only `answered` items). The stranding was bounded only
// because the sweep eventually closed the TWIN, at which point suppression
// stopped applying and the row reappeared — measured 2026-10-18 at exactly
// answeredMaxAge: twins answered 09:27:03Z/09:27:07Z, closed 10:27:48Z.
// Closing the re-raise itself gives the producer its outcome immediately
// instead of parking it on the twin's remaining lifetime.
//
// ⚠️ Producer is NOT part of the identity, for the same reason it is not part
// of suppressAnsweredTwins': a manager restart changes the producer of a
// crash-loop card while the ask stays the same, and matching on it let the
// restart defeat the dedup (measured 2026-11-16 on one dedup key across two
// producer sessions). The record consulted is the durable answered index, so a
// twin the sweep has already retired still matches.
//
// ⚠️ `open -> closed` is the schema's own row for this — "the item is resolved
// without an answer" — and it is the honest one. Answering the re-raise with
// the twin's answer was the alternative and is worse: it would write
// `answered_by` and `answer` onto a row the operator never acted on, recording
// them as having answered a card they were never shown, and it would leave two
// dimmed records of one ask on the board.
//
// ⚠️ Payload identity is what keeps this from discarding a real question. A
// producer that reuses a key for a DIFFERENT payload is asking something new —
// the session-keyed form makes that unavoidable, since every ask for one
// session carries the same key — and such a row is left open and rendered. Only
// a byte-identical repeat of an already-answered ask is closed.
//
// ⚠️ It no longer depends on running after the max-age pass. The old ordering
// rule existed because the answered set was built from the live index, so a
// twin closed by the pass above left the set and its re-raise was left alone.
// The record now survives the close, so ordering changes nothing here; the
// sweep still runs the pass second because closing the twin is the cheaper
// change to observe first. § Answer routing makes re-raising a merely-cleared
// ask legitimate, and the durable record is written only on a real answer, so
// this scan still never closes one.
func (a *attentionStore) closeRedundantReraises(
	ctx context.Context,
	tx libkv.Tx,
) (int, error) {
	// ⚠️ The answered set comes from the DURABLE record, not from the live index.
	// A twin the answered sweep has already closed has left the live index, so
	// building the set from it was exactly why a re-raise reappeared once its
	// twin was retired — the same miss suppressAnsweredTwins had. See
	// attention-store-answered-twins.go.
	if err := a.ensureAnsweredIndexTx(ctx, tx); err != nil {
		return 0, err
	}
	due := make([]string, 0)
	if err := a.liveIndex.Map(
		ctx,
		tx,
		func(ctx context.Context, key string, item Item) error {
			// ⚠️ A HELD card (open + AnsweredAt != nil) is the answered card itself,
			// not a re-raise of it: the hold keeps a permission card open until
			// delivery. Its own answer wrote its ask identity into the index, so
			// testing it here would close it as a redundant re-raise within one
			// sweep tick, before delivery — defeating the hold. Only an UNANSWERED
			// open item can be a redundant re-raise.
			if key == liveIndexMarkerKey || item.State != OpenState || item.AnsweredAt != nil {
				return nil
			}
			answered, err := a.answeredIndex.Exists(ctx, tx, askIdentity(item))
			if err != nil {
				return errors.Wrap(ctx, err, "check answered twin failed")
			}
			if answered {
				// ⚠️ A CRON producer re-scans and re-pushes on its own
				// interval, so closing its re-raise is precisely what makes the
				// next scan post it again: suppression is open-scoped (:441), so
				// a closed row no longer collapses the push. Leaving the row
				// open keeps one finding at one card, and the board already
				// withholds it through suppressAnsweredTwins (:439).
				//
				// Measured 2026-11-15 on attention-unforwarded-closers: 119 of
				// 140 distinct findings produced more than one card id in one
				// evening, several 7-8 each, every one closed here unanswered
				// and re-posted by the next 15-minute scan.
				if item.ProducerKind == CronProducerKind {
					return nil
				}
				due = append(due, key)
			}
			return nil
		},
	); err != nil {
		return 0, errors.Wrap(ctx, err, "scan live index for redundant re-raises failed")
	}
	closed := 0
	for _, key := range due {
		didClose, err := a.closeOpenItem(ctx, tx, key)
		if err != nil {
			return closed, err
		}
		if didClose {
			closed++
		}
	}
	return closed, nil
}

// closeOpenItem closes one open item, reporting whether it closed it.
//
// ⚠️ The state is re-read inside the transaction rather than trusted from the
// index snapshot the scan produced, for the same reason closeAnsweredItem
// re-reads: the scan and the close are two steps, and the item can have been
// answered, closed or removed between them. A re-raise that has since been
// ANSWERED must not be closed out from under its answer.
func (a *attentionStore) closeOpenItem(
	ctx context.Context,
	tx libkv.Tx,
	key string,
) (bool, error) {
	item, err := a.store.Get(ctx, tx, key)
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, errors.Wrapf(ctx, err, "get item %s failed", key)
	}
	if item.State != OpenState || item.AnsweredAt != nil {
		return false, nil
	}
	if err := ValidateTransition(ctx, item.State, ClosedState); err != nil {
		return false, err
	}
	// answeredBy is deliberately NOT written: the row was resolved without an
	// answer, and naming a sweeper as the answerer would be a false record.
	//
	// ⚠️ The reason IS written, and it names this path. `closed_at` alone says
	// the row left; it does not say which closer moved it, and for a card the
	// operator never answered that is the only question worth recording.
	item.CloseReason = RedundantReraiseCloseReason
	if err := a.closeItem(ctx, tx, item); err != nil {
		return false, errors.Wrapf(ctx, err, "close open item %s failed", key)
	}
	return true, nil
}
