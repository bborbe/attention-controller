// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"

	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
)

// lostAnswersTotalBucketName holds the durable total behind
// attention_lost_answers_total.
//
// ⚠️ The Prometheus counter is in-process and starts at zero on every start, so
// without this bucket the metric resets on every restart and can never evidence
// a week (goal SC3). The durable value is written inside the same transaction as
// the close that counts it, and the process seeds the counter from it at
// startup.
var lostAnswersTotalBucketName = libkv.NewBucketName("attention-lost-answers-total")

// lostAnswersTotalKey is the single key the durable total lives under.
const lostAnswersTotalKey = "total"

// lostAnswersTotalStore is the typed store for the durable total. It carries no
// state of its own, so one package-level value serves every store instance.
var lostAnswersTotalStore = libkv.NewStoreTx[string, uint64](lostAnswersTotalBucketName)

// LoadLostAnswersTotal reads the durable lost-answer total from db.
//
// A store that has never counted a lost answer has no bucket, and that is zero,
// not an error — the same reading libkv's own not-found covers.
//
// ⚠️ This is the reload half of the counter's lifetime: the value it returns is
// what a freshly started process must seed attention_lost_answers_total with, so
// the metric survives a restart instead of starting again at zero.
func LoadLostAnswersTotal(ctx context.Context, db libkv.DB) (uint64, error) {
	var total uint64
	err := db.View(ctx, func(ctx context.Context, tx libkv.Tx) error {
		current, err := lostAnswersTotalStore.Get(ctx, tx, lostAnswersTotalKey)
		switch {
		case err == nil:
			if current != nil {
				total = *current
			}
			return nil
		case isNotFound(err):
			return nil
		default:
			return err
		}
	})
	if err != nil {
		return 0, errors.Wrap(ctx, err, "read lost answers total failed")
	}
	return total, nil
}

// incrementLostAnswersTotal adds one to the durable total inside tx and returns
// the new value.
//
// ⚠️ It shares the close's transaction, so a committed close and the count it
// contributes become durable together: an aborted transaction leaves neither.
func (a *attentionStore) incrementLostAnswersTotal(
	ctx context.Context,
	tx libkv.Tx,
) error {
	var next uint64 = 1
	current, err := lostAnswersTotalStore.Get(ctx, tx, lostAnswersTotalKey)
	switch {
	case err == nil:
		if current != nil {
			next = *current + 1
		}
	case isNotFound(err):
	default:
		return errors.Wrap(ctx, err, "get lost answers total failed")
	}
	return lostAnswersTotalStore.Add(ctx, tx, lostAnswersTotalKey, next)
}
