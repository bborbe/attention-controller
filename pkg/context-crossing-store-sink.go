// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
)

// StoreContextCrossingSink raises the crossing card in the attention store.
type StoreContextCrossingSink struct {
	store       eventIngestStore
	resolveKind EventIngestProducerKindResolver
}

// NewStoreContextCrossingSink creates the crossing sink over the store.
func NewStoreContextCrossingSink(
	store eventIngestStore,
	resolveKind EventIngestProducerKindResolver,
) ContextCrossingSink {
	if resolveKind == nil {
		resolveKind = func(context.Context, string) ProducerKind {
			return SessionProducerKind
		}
	}
	return &StoreContextCrossingSink{store: store, resolveKind: resolveKind}
}

// Raise posts the crossing card for one session.
//
// ⚠️ The dedup key is `compact:<sid>`, which is what makes one card per
// crossing true: the store collapses a repeat push while the earlier item is
// still open, so a session that stays over the threshold does not accumulate
// rows. A session that compacts drops below the threshold and the card closes
// itself, which is the same mechanism the Python watcher relied on.
func (s *StoreContextCrossingSink) Raise(
	ctx context.Context,
	record ContextCrossingRecord,
) error {
	dedupKey := contextCrossingDedupPrefix + record.SessionID
	open, err := s.hasOpenCard(ctx, dedupKey)
	if err != nil {
		return err
	}
	if open {
		return nil
	}
	request := PushRequest{
		ProducerID:      ProducerID(record.SessionID),
		ProducerKind:    s.resolveKind(ctx, record.SessionID),
		ProvenanceClass: "explicit",
		LivenessRef:     LivenessRef("session:" + record.SessionID),
		DedupKey:        DedupKey(dedupKey),
		InterruptClass:  "ack",
		AnswerMechanism: "ack",
		Payload: Payload(fmt.Sprintf(
			"%s is at %d tokens, past the %d-token compact threshold — run the compact trio (prepare, compact, post) before the boundary.",
			record.Name,
			record.Tokens,
			ContextCrossingTokens,
		)),
	}
	if _, err := s.store.Push(ctx, request); err != nil {
		return errors.Wrapf(ctx, err, "push crossing for %s failed", record.SessionID)
	}
	return nil
}

// hasOpenCard reports whether the crossing card is already open.
func (s *StoreContextCrossingSink) hasOpenCard(
	ctx context.Context,
	dedupKey string,
) (bool, error) {
	items, err := s.store.ReadBoard(ctx)
	if err != nil {
		return false, errors.Wrap(ctx, err, "read board failed")
	}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return false, nil
		}
		if item.DedupKey.String() == dedupKey {
			glog.V(3).Infof(
				"job=attention-watcher action=crossing_skip dedup=%s (already open)",
				dedupKey,
			)
			return true, nil
		}
	}
	return false, nil
}

// Compile-time guard: the sink must satisfy the job's own interface.
var _ ContextCrossingSink = (*StoreContextCrossingSink)(nil)
