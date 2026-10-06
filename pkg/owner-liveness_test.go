// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"fmt"
	"path/filepath"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

// This file is the probe for the `owner:` liveness model.
//
// A worker posts an operator gate and then exits, because a headless worker is
// SUPPOSED to end its turn. The read path removed any item whose producer had
// exited and whose `answer_mechanism` was not `ack` — which is exactly that
// gate — so the question left the board before the operator could answer it,
// and it failed in the silent direction: a clean board and no gate.
//
// `owner:<id>` is the model that fixes it, and its one rule is the thing these
// specs pin: an `owner:` item is NEVER pruned by a liveness probe.
//
// ⚠️ The bound on this model is `expires_at`, and it is enforced — see
// expiry_test.go, which pins that a past deadline removes an `owner:` item and
// a future one does not. These specs assert survival only, so the two files
// together cover both halves: the marker keeps the gate, the deadline ends it.
//
// ⚠️ The reason is not a preference, and it is why the obvious implementation
// is wrong. The session registry deletes an entry when a session exits, so
// "never registered" and "exited" produce the same signal — and a HUMAN owner
// has no registry row at all. Reading that absence as gone would prune the very
// gate this model exists to keep, one layer in: the operator would still be
// waiting on a card that had already cleared itself.

var _ = Describe("Owner liveness model", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	// newStore opens a real temp-file DB and an attention store over it.
	newStore := func(checker pkg.SessionLivenessChecker) (pkg.AttentionStore, libkv.DB) {
		db, err := libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())
		store := pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			checker,
			libtime.NewCurrentDateTime(),
			libtime.Duration(15*60*1e9),
		)
		return store, db
	}

	// push posts one open ask carrying the given liveness ref. The producer is
	// the same in every case, so the model is the only variable.
	push := func(store pkg.AttentionStore, ref pkg.LivenessRef, dedupKey string) {
		_, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      pkg.ProducerID("worker-1"),
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     ref,
			DedupKey:        pkg.DedupKey(dedupKey),
			InterruptClass:  "pick",
			Payload:         "which way?",
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		Expect(err).To(BeNil())
	}

	It("keeps an owner item whose owner has no registry entry", func() {
		// The registry directory is empty: the producer is gone, and so is the
		// owner — which is the shape of every human owner, and of every manager
		// whose session has ended.
		store, db := newStore(pkg.NewSessionLivenessChecker(GinkgoT().TempDir()))

		push(store, pkg.LivenessRef("owner:operator-1"), "gate-owner")

		items, err := store.ReadBoard(ctx)
		Expect(err).To(BeNil())
		Expect(items).To(HaveLen(1),
			"an owner item was pruned although its owner cannot be read — "+
				"absence is not evidence for this model")
		Expect(string(items[0].LivenessRef)).To(Equal("owner:operator-1"))

		Expect(db.Close()).To(BeNil())
	})

	It("still prunes a session item whose session is gone", func() {
		// The discriminator, in the same shape as the spec above: same absent
		// producer, same ask, only the model differs. Pruning is re-pointed for
		// the declared operator gate, not turned off.
		store, db := newStore(pkg.NewSessionLivenessChecker(GinkgoT().TempDir()))

		push(store, pkg.LivenessRef("session:worker-1"), "gate-session")

		items, err := store.ReadBoard(ctx)
		Expect(err).To(BeNil())
		Expect(items).To(BeEmpty(), "a dead session's ask must still be pruned")

		Expect(db.Close()).To(BeNil())
	})

	It("still prunes a report from a producer with a stale heartbeat", func() {
		store, db := newStore(pkg.NewSessionLivenessChecker(GinkgoT().TempDir()))

		push(
			store,
			pkg.LivenessRef(fmt.Sprintf("heartbeat:%s", filepath.Join(GinkgoT().TempDir(), "hb"))),
			"gate-heartbeat",
		)

		items, err := store.ReadBoard(ctx)
		Expect(err).To(BeNil())
		Expect(items).To(BeEmpty(), "a stale heartbeat means the producer is gone")

		Expect(db.Close()).To(BeNil())
	})

	// ⚠️ A table over the whole enum, so a fourth model added later cannot be
	// accepted by `AvailableLivenessModels` and then fall through
	// `isProducerLiveWith` to the unknown-model error. The set is asserted
	// explicitly rather than counted, because a count passes for any three.
	It("accepts exactly the three declared models and rejects a fourth", func() {
		Expect(pkg.AvailableLivenessModels).To(ConsistOf(
			pkg.SessionLivenessModel,
			pkg.OwnerLivenessModel,
			pkg.HeartbeatLivenessModel,
		))

		store, db := newStore(pkg.NewSessionLivenessChecker(GinkgoT().TempDir()))

		// Every declared model parses; an undeclared one is refused at push.
		for _, model := range pkg.AvailableLivenessModels {
			_, err := store.Push(ctx, pkg.PushRequest{
				ProducerID:      pkg.ProducerID("worker-1"),
				ProducerKind:    pkg.SessionProducerKind,
				LivenessRef:     pkg.LivenessRef(string(model) + ":value-1"),
				DedupKey:        pkg.DedupKey("gate-" + string(model)),
				InterruptClass:  "pick",
				Payload:         "which way?",
				AnswerMechanism: pkg.MessageAnswerMechanism,
			})
			Expect(err).To(BeNil(), "the declared model %q must be accepted", model)
		}

		_, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      pkg.ProducerID("worker-1"),
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("pane:42"),
			DedupKey:        pkg.DedupKey("gate-pane"),
			InterruptClass:  "pick",
			Payload:         "which way?",
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		Expect(err).NotTo(BeNil(), "`pane:` is deliberately not a fourth model")

		Expect(db.Close()).To(BeNil())
	})
})
