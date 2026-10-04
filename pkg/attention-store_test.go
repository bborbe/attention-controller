// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	libboltkv "github.com/bborbe/boltkv"
	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	"github.com/bborbe/validation"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

// Session ids for the actors these specs name. § Escalation requires a session
// id to be a dashed UUID — the store refuses anything else rather than
// normalizing it — so a readable fixture such as "session-manager" cannot be
// handed to Escalate. The actor names live in the constant names instead, and
// the values are shaped like the ids the live store actually carries.
//
// Only the escalation path enforces this. `resolved_by` is the same kind of
// value but § Resolution states no rejection rule for it, so those specs keep
// their plain names — the asymmetry is the schema's, not an oversight here.
const (
	manager1SessionID     = "00000000-0000-4000-8000-000000000001"
	manager2SessionID     = "00000000-0000-4000-8000-000000000002"
	managerSessionID      = "00000000-0000-4000-8000-000000000003"
	fleetManagerSessionID = "00000000-0000-4000-8000-000000000004"
	topicManagerSessionID = "00000000-0000-4000-8000-000000000005"
)

// countingDB counts the write transactions opened against a real libkv.DB, so a
// spec can assert the read path's transaction shape rather than its timing. The
// embedded DB supplies every other method.
type countingDB struct {
	libkv.DB
	updates int
}

func (c *countingDB) Update(
	ctx context.Context,
	fn func(ctx context.Context, tx libkv.Tx) error,
) error {
	c.updates++
	return c.DB.Update(ctx, fn)
}

var _ = Describe("AttentionStore", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var sessionLivenessChecker *mocks.SessionLivenessChecker

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		// boltkv, not memorykv: the compare-and-set this suite exists to prove
		// is atomic only because the backend serializes write transactions.
		// Bolt's db.Update holds a write lock for the whole closure, so the
		// read-compare-write inside it cannot interleave. memorykv takes no
		// such lock — it is a plain map — so testing against it would assert a
		// guarantee the double cannot provide, and would pass or fail on
		// timing rather than on the store. This is also the backend the
		// deployed service opens (boltkv.OpenDir).
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

		sessionLivenessChecker = &mocks.SessionLivenessChecker{}
		sessionLivenessChecker.IsLiveReturns(true)

		store = pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			sessionLivenessChecker,
			libtime.NewCurrentDateTime(),
			libtime.Duration(15*60*1e9),
		)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	// pushRequest builds a declaration. It is a question the producer asked
	// (`message`), which is what makes removal-on-death the correct expectation
	// in the liveness specs below.
	pushRequest := func(producerID pkg.ProducerID, dedupKey pkg.DedupKey) pkg.PushRequest {
		return pkg.PushRequest{
			ProducerID:      producerID,
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:" + producerID.String()),
			DedupKey:        dedupKey,
			InterruptClass:  "approve",
			Payload:         "deploy prod?",
			AnswerMechanism: pkg.MessageAnswerMechanism,
		}
	}

	// Regression: PushRequest must be validated as a request, not as an Item.
	// An earlier revision built an Item to reuse Item.Validate and left ItemID
	// empty — which is store-assigned and therefore never present in a
	// producer's declaration — so EVERY push failed validation with
	// "validate ItemID failed: empty string". Found by the first end-to-end run
	// against a live process, not by the unit suite, because the suite called
	// the store directly and never went through the handler's validation.
	Describe("PushRequest validation", func() {
		It("accepts a declaration that omits the store-owned fields", func() {
			request := pushRequest("session-a", "gate-1")
			Expect(request.Validate(ctx)).To(BeNil())
		})

		It("rejects a declaration missing a producer-owned field", func() {
			request := pushRequest("session-a", "gate-1")
			request.Payload = ""
			Expect(request.Validate(ctx)).NotTo(BeNil())
		})
	})

	// ProvenanceClass is declared by the producer, stored, never derived. An
	// empty value must be accepted — it marks a pre-2026-09-23 item pushed
	// before this field existed. See the attention item schema § Fields,
	// `provenance_class`, and silence 9.
	Describe("ProvenanceClass", func() {
		It("round-trips a valid value via push and read", func() {
			request := pushRequest("session-a", "gate-1")
			request.ProvenanceClass = pkg.HookProvenanceClass
			item, err := store.Push(ctx, request)
			Expect(err).To(BeNil())
			Expect(item.ProvenanceClass).To(Equal(pkg.HookProvenanceClass))

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.ProvenanceClass).To(Equal(pkg.HookProvenanceClass))
		})

		It("accepts an empty value", func() {
			request := pushRequest("session-a", "gate-1")
			request.ProvenanceClass = ""
			Expect(request.Validate(ctx)).To(BeNil())

			item, err := store.Push(ctx, request)
			Expect(err).To(BeNil())
			Expect(item.ProvenanceClass).To(Equal(pkg.ProvenanceClass("")))
		})

		It("carries the re-pushed value through a dedup update", func() {
			first := pushRequest("session-a", "gate-1")
			created, err := store.Push(ctx, first)
			Expect(err).To(BeNil())
			Expect(created.ProvenanceClass).To(Equal(pkg.ProvenanceClass("")))

			second := pushRequest("session-a", "gate-1")
			second.ProvenanceClass = pkg.HookProvenanceClass
			updated, err := store.Push(ctx, second)
			Expect(err).To(BeNil())
			Expect(updated.ItemID).To(Equal(created.ItemID))

			got, err := store.Get(ctx, created.ItemID)
			Expect(err).To(BeNil())
			Expect(got.ProvenanceClass).To(Equal(pkg.HookProvenanceClass))
		})

		It("rejects an invalid value", func() {
			request := pushRequest("session-a", "gate-1")
			request.ProvenanceClass = pkg.ProvenanceClass("bogus")
			err := request.Validate(ctx)
			Expect(err).NotTo(BeNil())
			Expect(errors.Is(err, validation.Error)).To(BeTrue())
		})
	})

	Describe("Push", func() {
		It("creates an open item carrying a stable id", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())
			Expect(item.ItemID).NotTo(BeEmpty())
			Expect(item.State).To(Equal(pkg.OpenState))
			Expect(item.CreatedAt.IsZero()).To(BeFalse())

			read, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(read.ItemID).To(Equal(item.ItemID))
		})

		It(
			"updates rather than duplicates when the same live producer re-pushes the same dedup key",
			func() {
				first, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
				Expect(err).To(BeNil())

				second := pushRequest("session-a", "gate-1")
				second.Payload = "deploy prod? (updated)"
				updated, err := store.Push(ctx, second)
				Expect(err).To(BeNil())

				Expect(updated.ItemID).To(Equal(first.ItemID))
				Expect(updated.Payload).To(Equal(pkg.Payload("deploy prod? (updated)")))

				items, err := store.Read(ctx)
				Expect(err).To(BeNil())
				Expect(items).To(HaveLen(1))
			},
		)

		It("does not deduplicate across two different producers", func() {
			_, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())
			_, err = store.Push(ctx, pushRequest("session-b", "gate-1"))
			Expect(err).To(BeNil())

			items, err := store.Read(ctx)
			Expect(err).To(BeNil())
			Expect(items).To(HaveLen(2))
		})

		It(
			"creates a new row when the same dedup key comes from a producer whose liveness failed",
			func() {
				first, err := store.Push(ctx, pushRequest("session-dead", "gate-1"))
				Expect(err).To(BeNil())

				// The producer is gone. Suppression is scoped to a *live*
				// producer_id, so this push must not revive the dead row.
				sessionLivenessChecker.IsLiveReturns(false)
				second, err := store.Push(ctx, pushRequest("session-dead", "gate-1"))
				Expect(err).To(BeNil())

				Expect(second.ItemID).NotTo(Equal(first.ItemID))
			},
		)
	})

	// ResolvedBy names the session that resolved an item, as distinct from
	// AnsweredBy, the arm that carried the answer. See the attention item
	// schema § Resolution and silence 10.
	Describe("ResolvedBy", func() {
		It("is absent until the item is answered", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())
			Expect(item.ResolvedBy).To(BeEmpty())
		})

		It("is stamped on the answer path, separately from the arm", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())

			answered, err := store.Answer(
				ctx,
				item.ItemID,
				"supervisor:attention-next",
				"manager-1",
				"",
				nil,
				nil,
				nil,
			)
			Expect(err).To(BeNil())
			Expect(answered.ResolvedBy).To(Equal("manager-1"))
			Expect(answered.AnsweredBy).To(Equal("supervisor:attention-next"))

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.ResolvedBy).To(Equal("manager-1"))
		})

		// Negative control: a constant or a value copied from the arm would pass
		// the stamp spec above. Two resolvers must read back as two values.
		It("tracks the resolver rather than the arm", func() {
			first, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())
			second, err := store.Push(ctx, pushRequest("session-a", "gate-2"))
			Expect(err).To(BeNil())

			a, err := store.Answer(
				ctx,
				first.ItemID,
				"supervisor:attention-next",
				"manager-1",
				"",
				nil,
				nil,
				nil,
			)
			Expect(err).To(BeNil())
			b, err := store.Answer(
				ctx,
				second.ItemID,
				"supervisor:attention-next",
				"manager-2",
				"",
				nil,
				nil,
				nil,
			)
			Expect(err).To(BeNil())

			Expect(a.ResolvedBy).NotTo(Equal(b.ResolvedBy))
			Expect(a.AnsweredBy).To(Equal(b.AnsweredBy))
		})

		It("is not written by an escalation, which is not an answer", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())

			escalated, err := store.Escalate(ctx, item.ItemID, manager1SessionID)
			Expect(err).To(BeNil())
			Expect(escalated.EscalatedBy).To(Equal(manager1SessionID))
			Expect(escalated.ResolvedBy).To(BeEmpty())
		})

		It("is not written by a close, which is not an answer", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())

			closed, err := store.Close(ctx, item.ItemID, "", nil)
			Expect(err).To(BeNil())
			Expect(closed.ResolvedBy).To(BeEmpty())
		})

		It("stays empty when the caller omits it, never backfilled from the arm", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())

			answered, err := store.Answer(ctx, item.ItemID, "telegram", "", "", nil, nil, nil)
			Expect(err).To(BeNil())
			Expect(answered.ResolvedBy).To(BeEmpty())
		})

		It("lets the three classes be told apart", func() {
			resolved, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())
			escalated, err := store.Push(ctx, pushRequest("session-a", "gate-2"))
			Expect(err).To(BeNil())
			untouched, err := store.Push(ctx, pushRequest("session-a", "gate-3"))
			Expect(err).To(BeNil())

			_, err = store.Answer(
				ctx,
				resolved.ItemID,
				"supervisor:attention-next",
				"manager-1",
				"",
				nil,
				nil,
				nil,
			)
			Expect(err).To(BeNil())
			_, err = store.Escalate(ctx, escalated.ItemID, manager2SessionID)
			Expect(err).To(BeNil())

			items, err := store.History(ctx, 0, 0)
			Expect(err).To(BeNil())
			byID := map[pkg.ItemID]pkg.Item{}
			for _, item := range items {
				byID[item.ItemID] = item
			}

			Expect(byID[resolved.ItemID].ResolvedBy).To(Equal("manager-1"))
			Expect(byID[resolved.ItemID].EscalatedBy).To(BeEmpty())
			Expect(byID[escalated.ItemID].EscalatedBy).To(Equal(manager2SessionID))
			Expect(byID[escalated.ItemID].ResolvedBy).To(BeEmpty())
			Expect(byID[untouched.ItemID].State).To(Equal(pkg.OpenState))
			Expect(byID[untouched.ItemID].ResolvedBy).To(BeEmpty())
			Expect(byID[untouched.ItemID].EscalatedBy).To(BeEmpty())
		})
	})

	// Decision is what an answer decided, as distinct from AnsweredBy, the arm
	// that carried it. See the attention item schema § Fields, `decision`, and
	// silence 11: before this field an item answered allow and one answered deny
	// were the same record, so "was this answered" could not be told from "what
	// was decided".
	Describe("Decision", func() {
		It("is stamped on the answer path, separately from the arm", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())

			answered, err := store.Answer(
				ctx,
				item.ItemID,
				"supervisor:attention-next",
				"manager-1",
				pkg.AllowDecision,
				nil,
				nil,
				nil,
			)
			Expect(err).To(BeNil())
			Expect(answered.Decision).To(Equal(pkg.AllowDecision))
			Expect(answered.AnsweredBy).To(Equal("supervisor:attention-next"))

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.Decision).To(Equal(pkg.AllowDecision))
		})

		// Negative control: a constant, or a value copied from the arm, would pass
		// the stamp spec above. One arm must be able to record both verdicts, and
		// they must read back as two different values.
		It("tracks the decision rather than the arm", func() {
			first, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())
			second, err := store.Push(ctx, pushRequest("session-a", "gate-2"))
			Expect(err).To(BeNil())

			allowed, err := store.Answer(
				ctx,
				first.ItemID,
				"telegram",
				"",
				pkg.AllowDecision,
				nil,
				nil,
				nil,
			)
			Expect(err).To(BeNil())
			denied, err := store.Answer(
				ctx,
				second.ItemID,
				"telegram",
				"",
				pkg.DenyDecision,
				nil,
				nil,
				nil,
			)
			Expect(err).To(BeNil())

			Expect(allowed.Decision).NotTo(Equal(denied.Decision))
			Expect(allowed.AnsweredBy).To(Equal(denied.AnsweredBy))
		})

		// The schema adds no write-time rejection for an omitted decision, so an
		// answer without one stores an item with no verdict recorded rather than
		// failing. This is what a message- or ack-class item reads as, and what
		// every item answered before this field existed reads as.
		It("accepts an omitted decision and stores no verdict", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())

			answered, err := store.Answer(ctx, item.ItemID, "telegram", "", "", nil, nil, nil)
			Expect(err).To(BeNil())
			Expect(answered.Decision).To(BeEmpty())

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.Decision).To(BeEmpty())
		})

		It("rejects a value outside AvailableDecisions", func() {
			err := pkg.Decision("maybe").Validate(ctx)
			Expect(err).NotTo(BeNil())
			Expect(errors.Is(err, validation.Error)).To(BeTrue())
		})

		It("accepts an empty value, which is absent rather than unknown", func() {
			Expect(pkg.Decision("").Validate(ctx)).To(BeNil())
		})
	})

	// History is the closed-inclusive read the resolved-versus-escalated split
	// is counted from. Read cannot serve: it returns only open items and prunes.
	Describe("History", func() {
		It("returns answered and closed items that Read omits", func() {
			open, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())
			answered, err := store.Push(ctx, pushRequest("session-a", "gate-2"))
			Expect(err).To(BeNil())
			closed, err := store.Push(ctx, pushRequest("session-a", "gate-3"))
			Expect(err).To(BeNil())
			_, err = store.Answer(ctx, answered.ItemID, "telegram", "manager-1", "", nil, nil, nil)
			Expect(err).To(BeNil())
			_, err = store.Close(ctx, closed.ItemID, "", nil)
			Expect(err).To(BeNil())

			read, err := store.Read(ctx)
			Expect(err).To(BeNil())
			Expect(read).To(HaveLen(1))

			history, err := store.History(ctx, 0, 0)
			Expect(err).To(BeNil())
			ids := make([]pkg.ItemID, 0, len(history))
			for _, item := range history {
				ids = append(ids, item.ItemID)
			}
			Expect(ids).To(ConsistOf(open.ItemID, answered.ItemID, closed.ItemID))
		})

		// A pruning read cannot report a history, because reading would delete
		// part of what it reports.
		It("never prunes a dead asker", func() {
			item, err := store.Push(ctx, pushRequest("session-dead", "gate-1"))
			Expect(err).To(BeNil())
			sessionLivenessChecker.IsLiveReturns(false)

			history, err := store.History(ctx, 0, 0)
			Expect(err).To(BeNil())
			Expect(history).To(HaveLen(1))

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.ItemID).To(Equal(item.ItemID))
		})

		// ⚠️ The ordering claim is asserted as "non-increasing by CreatedAt"
		// rather than "history[0] is item-3". Naming the newest item would pass
		// on a store whose clock happens to separate the pushes and fail on one
		// that does not, which is a spec testing the clock rather than the
		// index. The property is what the reverse cursor has to deliver.
		It("returns items newest-first", func() {
			for _, dedup := range []string{"gate-1", "gate-2", "gate-3"} {
				_, err := store.Push(ctx, pushRequest("session-a", pkg.DedupKey(dedup)))
				Expect(err).To(BeNil())
			}

			history, err := store.History(ctx, 0, 0)
			Expect(err).To(BeNil())
			Expect(history).To(HaveLen(3))
			for i := 1; i < len(history); i++ {
				Expect(
					history[i].CreatedAt.Time().After(history[i-1].CreatedAt.Time()),
				).To(BeFalse(), "history is not newest-first at index %d", i)
			}
		})

		It("bounds the page to limit", func() {
			for _, dedup := range []string{"gate-1", "gate-2", "gate-3"} {
				_, err := store.Push(ctx, pushRequest("session-a", pkg.DedupKey(dedup)))
				Expect(err).To(BeNil())
			}

			all, err := store.History(ctx, 0, 0)
			Expect(err).To(BeNil())
			Expect(all).To(HaveLen(3))

			page, err := store.History(ctx, 2, 0)
			Expect(err).To(BeNil())
			Expect(page).To(HaveLen(2))
			// The bound must not silently reorder: the page is the FIRST two of
			// the full history, not an arbitrary two.
			Expect(page[0].ItemID).To(Equal(all[0].ItemID))
			Expect(page[1].ItemID).To(Equal(all[1].ItemID))
		})

		It("skips the first offset", func() {
			for _, dedup := range []string{"gate-1", "gate-2", "gate-3"} {
				_, err := store.Push(ctx, pushRequest("session-a", pkg.DedupKey(dedup)))
				Expect(err).To(BeNil())
			}

			all, err := store.History(ctx, 0, 0)
			Expect(err).To(BeNil())
			Expect(all).To(HaveLen(3))

			page, err := store.History(ctx, 0, 1)
			Expect(err).To(BeNil())
			Expect(page).To(HaveLen(2))
			Expect(page[0].ItemID).To(Equal(all[1].ItemID))
			Expect(page[1].ItemID).To(Equal(all[2].ItemID))
		})

		It("treats a negative offset as zero", func() {
			_, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())

			page, err := store.History(ctx, 0, -1)
			Expect(err).To(BeNil())
			Expect(page).To(HaveLen(1))
		})

		It("returns every item when the limit is zero", func() {
			for _, dedup := range []string{"gate-1", "gate-2", "gate-3"} {
				_, err := store.Push(ctx, pushRequest("session-a", pkg.DedupKey(dedup)))
				Expect(err).To(BeNil())
			}

			history, err := store.History(ctx, 0, 0)
			Expect(err).To(BeNil())
			Expect(history).To(HaveLen(3))
		})

		// The reachability claim the endpoint's consumers depend on: a bounded
		// page is only safe if paging reaches everything, with no gap and no
		// duplicate. This is the spec that would catch an offset applied to the
		// wrong side of the cursor.
		It("reaches every item by paging", func() {
			pushed := make([]pkg.ItemID, 0, 5)
			for _, dedup := range []string{"g1", "g2", "g3", "g4", "g5"} {
				item, err := store.Push(ctx, pushRequest("session-a", pkg.DedupKey(dedup)))
				Expect(err).To(BeNil())
				pushed = append(pushed, item.ItemID)
			}

			seen := make([]pkg.ItemID, 0, len(pushed))
			for offset := 0; offset < len(pushed); offset += 2 {
				page, err := store.History(ctx, 2, offset)
				Expect(err).To(BeNil())
				for _, item := range page {
					seen = append(seen, item.ItemID)
				}
			}

			Expect(seen).To(HaveLen(len(pushed)))
			Expect(seen).To(ConsistOf(pushed))
		})

		// A closed item is the population the endpoint exists to count, and the
		// history index admits it because its predicate is constant. A spec that
		// only paged open items would pass on an index that filtered states.
		It("pages closed items too", func() {
			for _, dedup := range []string{"gate-1", "gate-2"} {
				item, err := store.Push(ctx, pushRequest("session-a", pkg.DedupKey(dedup)))
				Expect(err).To(BeNil())
				_, err = store.Close(ctx, item.ItemID, "", nil)
				Expect(err).To(BeNil())
			}

			page, err := store.History(ctx, 1, 0)
			Expect(err).To(BeNil())
			Expect(page).To(HaveLen(1))
			Expect(page[0].State).To(Equal(pkg.ClosedState))
		})
	})

	Describe("Answer", func() {
		It("applies open -> answered and records who answered", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())

			answered, err := store.Answer(ctx, item.ItemID, "telegram", "", "", nil, nil, nil)
			Expect(err).To(BeNil())
			Expect(answered.State).To(Equal(pkg.AnsweredState))
			Expect(answered.AnsweredBy).To(Equal("telegram"))
			Expect(answered.AnsweredAt).NotTo(BeNil())
		})

		It("rejects a second answer as already-answered", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())
			_, err = store.Answer(ctx, item.ItemID, "telegram", "", "", nil, nil, nil)
			Expect(err).To(BeNil())

			_, err = store.Answer(ctx, item.ItemID, "discord", "", "", nil, nil, nil)
			Expect(err).NotTo(BeNil())
			Expect(errors.Is(err, pkg.ErrAlreadyAnswered)).To(BeTrue())
		})

		It("rejects an answer to a closed item as an illegal transition", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())
			_, err = store.Close(ctx, item.ItemID, "", nil)
			Expect(err).To(BeNil())

			_, err = store.Answer(ctx, item.ItemID, "telegram", "", "", nil, nil, nil)
			Expect(err).NotTo(BeNil())
			Expect(errors.Is(err, pkg.ErrIllegalTransition)).To(BeTrue())
		})

		It("rejects an answer to an unknown item", func() {
			_, err := store.Answer(
				ctx,
				pkg.ItemID("does-not-exist"),
				"telegram",
				"",
				"",
				nil,
				nil,
				nil,
			)
			Expect(err).NotTo(BeNil())
			Expect(errors.Is(err, pkg.ErrItemNotFound)).To(BeTrue())
		})

		// The criterion the task exists to prove: exactly one of two concurrent
		// answers transitions the item. A single pair can pass on a store that
		// merely serializes its writes, so this fires N pairs from a shared
		// barrier and asserts the totals.
		It("lets exactly one of many concurrent answer pairs win, per item", func() {
			const itemCount = 100
			itemIDs := make([]pkg.ItemID, 0, itemCount)
			for i := 0; i < itemCount; i++ {
				item, err := store.Push(ctx, pushRequest("session-a", pkg.DedupKey(
					"gate-"+string(rune('a'+i%26))+string(rune('0'+i/26)),
				)))
				Expect(err).To(BeNil())
				itemIDs = append(itemIDs, item.ItemID)
			}

			var start sync.WaitGroup
			start.Add(1)
			var done sync.WaitGroup
			var mu sync.Mutex
			successes := 0
			alreadyAnswered := 0

			for _, itemID := range itemIDs {
				for _, arm := range []string{"telegram", "discord"} {
					done.Add(1)
					go func(itemID pkg.ItemID, arm string) {
						defer done.Done()
						defer GinkgoRecover()
						start.Wait()
						_, err := store.Answer(ctx, itemID, arm, "", "", nil, nil, nil)
						mu.Lock()
						defer mu.Unlock()
						switch {
						case err == nil:
							successes++
						case errors.Is(err, pkg.ErrAlreadyAnswered):
							alreadyAnswered++
						}
					}(itemID, arm)
				}
			}
			start.Done()
			done.Wait()

			Expect(successes).To(Equal(itemCount))
			Expect(alreadyAnswered).To(Equal(itemCount))

			// Exactly one answered_by recorded per item, and every item reached
			// answered exactly once.
			for _, itemID := range itemIDs {
				item, err := store.Get(ctx, itemID)
				Expect(err).To(BeNil())
				Expect(item.State).To(Equal(pkg.AnsweredState))
				Expect(item.AnsweredBy).NotTo(BeEmpty())
			}
		})
	})

	Describe("Escalate", func() {
		It("records which session escalated and leaves the item open", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())

			escalated, err := store.Escalate(ctx, item.ItemID, managerSessionID)
			Expect(err).To(BeNil())
			Expect(escalated.EscalatedBy).To(Equal(managerSessionID))
			// Escalation is not a transition: the item stays where it was, so
			// an arm still renders it. A store that moved it to a new state
			// would be redefining the schema rather than implementing it.
			Expect(escalated.State).To(Equal(pkg.OpenState))
			Expect(escalated.AnsweredAt).To(BeNil())
			Expect(escalated.ClosedAt).To(BeNil())
		})

		// The stamp the operator rung's latency is measured from. Before it the
		// rung had a count and no starting stamp, so time-to-answer was not
		// computable at all — the schema page's silence 13.
		It("stamps when the escalation happened, beside who did it", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())

			before := libtime.NewCurrentDateTime().Now()
			escalated, err := store.Escalate(ctx, item.ItemID, managerSessionID)
			Expect(err).To(BeNil())
			after := libtime.NewCurrentDateTime().Now()

			Expect(escalated.EscalatedAt).NotTo(BeNil())
			Expect(escalated.EscalatedAt.Time()).To(BeTemporally(">=", before.Time()))
			Expect(escalated.EscalatedAt.Time()).To(BeTemporally("<=", after.Time()))
		})

		It("leaves the timestamp absent while no one has escalated", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())

			Expect(item.EscalatedBy).To(BeEmpty())
			Expect(item.EscalatedAt).To(BeNil())
		})

		It("does not move the timestamp when the same session re-escalates", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())

			first, err := store.Escalate(ctx, item.ItemID, managerSessionID)
			Expect(err).To(BeNil())
			Expect(first.EscalatedAt).NotTo(BeNil())

			again, err := store.Escalate(ctx, item.ItemID, managerSessionID)
			Expect(err).To(BeNil())
			Expect(again.EscalatedAt).NotTo(BeNil())
			// A re-run of the same sweep must not reset the clock: the measure is
			// time-to-answer, and a moving start would shorten it silently.
			//
			// BeTemporally, not Equal: `first` is the struct the write returned,
			// `again` is one read back through the store, and only the first still
			// carries a monotonic reading — Equal would compare that too and fail
			// on two values naming the same instant.
			Expect(again.EscalatedAt.Time()).To(BeTemporally("==", first.EscalatedAt.Time()))
		})

		// § Escalation says the field holds a session id. The live store was
		// carrying values that are not one, so the store refuses them rather than
		// storing a placeholder no reader can attribute.
		It("rejects a session id that is not a well-formed uuid", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())

			// The three shapes the live store actually carried, plus a plain
			// word. The bare 8-hex prefix is the interesting one: it is a real
			// session id's prefix, so it looks attributable and is not.
			for _, malformed := range []string{"session-a", "5ad28987", "not-a-uuid", ""} {
				_, err := store.Escalate(ctx, item.ItemID, malformed)
				Expect(errors.Is(err, pkg.ErrInvalidSessionID)).To(BeTrue())
			}

			// Refused, not half-applied: no stamp was written.
			read, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(read.EscalatedBy).To(BeEmpty())
			Expect(read.EscalatedAt).To(BeNil())
		})

		It("persists the stamp across a read", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())
			_, err = store.Escalate(ctx, item.ItemID, managerSessionID)
			Expect(err).To(BeNil())

			read, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(read.EscalatedBy).To(Equal(managerSessionID))
		})

		It("rejects a second escalation by a different session", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())
			_, err = store.Escalate(ctx, item.ItemID, fleetManagerSessionID)
			Expect(err).To(BeNil())

			_, err = store.Escalate(ctx, item.ItemID, topicManagerSessionID)
			Expect(err).NotTo(BeNil())
			Expect(errors.Is(err, pkg.ErrAlreadyEscalated)).To(BeTrue())

			// The loser must not have stamped over the winner: the first
			// escalator is still the one on record.
			read, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(read.EscalatedBy).To(Equal(fleetManagerSessionID))
		})

		// The self-stamp rule: the stamp answers "is someone already carrying
		// this", so a manager re-running its own sweep over an item it already
		// escalated must proceed rather than skip itself.
		It("lets the escalating session re-escalate its own item", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())
			_, err = store.Escalate(ctx, item.ItemID, managerSessionID)
			Expect(err).To(BeNil())

			again, err := store.Escalate(ctx, item.ItemID, managerSessionID)
			Expect(err).To(BeNil())
			Expect(again.EscalatedBy).To(Equal(managerSessionID))
			Expect(again.State).To(Equal(pkg.OpenState))
		})

		It("rejects escalation of a closed item", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())
			_, err = store.Close(ctx, item.ItemID, "", nil)
			Expect(err).To(BeNil())

			_, err = store.Escalate(ctx, item.ItemID, managerSessionID)
			Expect(err).NotTo(BeNil())
			Expect(errors.Is(err, pkg.ErrItemNotOpen)).To(BeTrue())
			// Deliberately not ErrIllegalTransition: escalation is not a
			// transition, so it must not borrow the transition vocabulary.
			Expect(errors.Is(err, pkg.ErrIllegalTransition)).To(BeFalse())
		})

		It("rejects escalation of an answered item", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())
			_, err = store.Answer(ctx, item.ItemID, "telegram", "", "", nil, nil, nil)
			Expect(err).To(BeNil())

			_, err = store.Escalate(ctx, item.ItemID, managerSessionID)
			Expect(err).NotTo(BeNil())
			Expect(errors.Is(err, pkg.ErrItemNotOpen)).To(BeTrue())
		})

		It("rejects escalation of an unknown item", func() {
			_, err := store.Escalate(ctx, pkg.ItemID("does-not-exist"), managerSessionID)
			Expect(err).NotTo(BeNil())
			Expect(errors.Is(err, pkg.ErrItemNotFound)).To(BeTrue())
		})

		// The criterion this task exists to prove: exactly one of two
		// concurrent escalations stamps the item, which is what stops two
		// managers escalating the same gate without either knowing. A single
		// pair can pass on a store that merely serializes its writes, so this
		// fires N pairs from a shared barrier and asserts the totals.
		It("lets exactly one of many concurrent escalation pairs win, per item", func() {
			const itemCount = 100
			itemIDs := make([]pkg.ItemID, 0, itemCount)
			for i := 0; i < itemCount; i++ {
				item, err := store.Push(ctx, pushRequest("session-a", pkg.DedupKey(
					"gate-"+string(rune('a'+i%26))+string(rune('0'+i/26)),
				)))
				Expect(err).To(BeNil())
				itemIDs = append(itemIDs, item.ItemID)
			}

			var start sync.WaitGroup
			start.Add(1)
			var done sync.WaitGroup
			var mu sync.Mutex
			successes := 0
			alreadyEscalated := 0

			for _, itemID := range itemIDs {
				for _, manager := range []string{fleetManagerSessionID, topicManagerSessionID} {
					done.Add(1)
					go func(itemID pkg.ItemID, manager string) {
						defer done.Done()
						defer GinkgoRecover()
						start.Wait()
						_, err := store.Escalate(ctx, itemID, manager)
						mu.Lock()
						defer mu.Unlock()
						switch {
						case err == nil:
							successes++
						case errors.Is(err, pkg.ErrAlreadyEscalated):
							alreadyEscalated++
						}
					}(itemID, manager)
				}
			}
			start.Done()
			done.Wait()

			Expect(successes).To(Equal(itemCount))
			Expect(alreadyEscalated).To(Equal(itemCount))

			// Exactly one escalator recorded per item, every item still open,
			// and no item silently unstamped by a losing write.
			for _, itemID := range itemIDs {
				item, err := store.Get(ctx, itemID)
				Expect(err).To(BeNil())
				Expect(item.State).To(Equal(pkg.OpenState))
				Expect(item.EscalatedBy).NotTo(BeEmpty())
			}
		})
	})

	Describe("Close", func() {
		It("applies open -> closed", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())
			closed, err := store.Close(ctx, item.ItemID, "", nil)
			Expect(err).To(BeNil())
			Expect(closed.State).To(Equal(pkg.ClosedState))
			Expect(closed.ClosedAt).NotTo(BeNil())
		})

		It("applies answered -> closed", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())
			_, err = store.Answer(ctx, item.ItemID, "telegram", "", "", nil, nil, nil)
			Expect(err).To(BeNil())
			closed, err := store.Close(ctx, item.ItemID, "", nil)
			Expect(err).To(BeNil())
			Expect(closed.State).To(Equal(pkg.ClosedState))
		})

		It("rejects closed -> closed", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())
			_, err = store.Close(ctx, item.ItemID, "", nil)
			Expect(err).To(BeNil())
			_, err = store.Close(ctx, item.ItemID, "", nil)
			Expect(err).NotTo(BeNil())
			Expect(errors.Is(err, pkg.ErrIllegalTransition)).To(BeTrue())
		})
	})

	Describe("Read and liveness", func() {
		It("omits an item whose session producer asked and has exited", func() {
			dead, err := store.Push(ctx, pushRequest("session-dead", "gate-dead"))
			Expect(err).To(BeNil())
			live, err := store.Push(ctx, pushRequest("session-live", "gate-live"))
			Expect(err).To(BeNil())

			sessionLivenessChecker.IsLiveCalls(func(_ context.Context, sessionID string) bool {
				return sessionID != "session-dead"
			})

			items, err := store.Read(ctx)
			Expect(err).To(BeNil())

			ids := make([]pkg.ItemID, 0, len(items))
			for _, item := range items {
				ids = append(ids, item.ItemID)
			}
			// The positive control is required: a read path that returned
			// nothing at all would otherwise pass this.
			Expect(ids).To(ContainElement(live.ItemID))
			Expect(ids).NotTo(ContainElement(dead.ItemID))

			// And the dead item is gone from the store, not merely filtered at
			// read time.
			_, err = store.Get(ctx, dead.ItemID)
			Expect(errors.Is(err, pkg.ErrItemNotFound)).To(BeTrue())
		})

		It("keeps an item from a short-lived producer whose heartbeat went stale", func() {
			dir := GinkgoT().TempDir()
			heartbeatPath := filepath.Join(dir, "heartbeat")

			// A stale heartbeat: the file exists but its mtime is outside the
			// window.
			Expect(os.WriteFile(heartbeatPath, []byte("x"), 0600)).To(BeNil())
			stale := libtime.NewCurrentDateTime().Now().Time().Add(-1 * 60 * 60 * 1e9)
			Expect(os.Chtimes(heartbeatPath, stale, stale)).To(BeNil())

			request := pkg.PushRequest{
				ProducerID:      "cron-a",
				ProducerKind:    pkg.CronProducerKind,
				LivenessRef:     pkg.LivenessRef("heartbeat:" + heartbeatPath),
				DedupKey:        "report-1",
				InterruptClass:  "ack",
				Payload:         "nightly job failed",
				AnswerMechanism: pkg.AckAnswerMechanism,
			}
			reported, err := store.Push(ctx, request)
			Expect(err).To(BeNil())

			items, err := store.Read(ctx)
			Expect(err).To(BeNil())
			Expect(items).To(HaveLen(1))
			Expect(items[0].ItemID).To(Equal(reported.ItemID))
			Expect(items[0].State).To(Equal(pkg.OpenState))
		})

		It(
			"removes a dead asker while keeping a stale-heartbeat reporter in the same read",
			func() {
				// The discriminating assertion: one sweep, two producers, opposite
				// outcomes. A store that never consulted liveness would keep both; a
				// store that read "not running" uniformly would drop both.
				dir := GinkgoT().TempDir()
				heartbeatPath := filepath.Join(dir, "heartbeat")
				Expect(os.WriteFile(heartbeatPath, []byte("x"), 0600)).To(BeNil())
				stale := libtime.NewCurrentDateTime().Now().Time().Add(-1 * 60 * 60 * 1e9)
				Expect(os.Chtimes(heartbeatPath, stale, stale)).To(BeNil())

				asked, err := store.Push(ctx, pushRequest("session-dead", "gate-dead"))
				Expect(err).To(BeNil())

				reporterRequest := pkg.PushRequest{
					ProducerID:      "cron-a",
					ProducerKind:    pkg.CronProducerKind,
					LivenessRef:     pkg.LivenessRef("heartbeat:" + heartbeatPath),
					DedupKey:        "report-1",
					InterruptClass:  "ack",
					Payload:         "nightly job failed",
					AnswerMechanism: pkg.AckAnswerMechanism,
				}
				reported, err := store.Push(ctx, reporterRequest)
				Expect(err).To(BeNil())

				sessionLivenessChecker.IsLiveReturns(false)

				items, err := store.Read(ctx)
				Expect(err).To(BeNil())
				Expect(items).To(HaveLen(1))
				Expect(items[0].ItemID).To(Equal(reported.ItemID))
				Expect(items[0].ItemID).NotTo(Equal(asked.ItemID))
			},
		)

		It("returns every pushed item while every producer is live", func() {
			// The orphan-item half of the falsifier: an item no read path ever
			// returns is the falsifier firing.
			mechanisms := []pkg.AnswerMechanism{
				pkg.MessageAnswerMechanism,
				pkg.PermissionAnswerMechanism,
				pkg.AckAnswerMechanism,
			}
			pushed := make([]pkg.ItemID, 0, len(mechanisms))
			for i, mechanism := range mechanisms {
				request := pushRequest("session-a", pkg.DedupKey("gate-"+mechanism.String()))
				request.AnswerMechanism = mechanism
				item, err := store.Push(ctx, request)
				Expect(err).To(BeNil())
				pushed = append(pushed, item.ItemID)
				_ = i
			}

			items, err := store.Read(ctx)
			Expect(err).To(BeNil())
			Expect(items).To(HaveLen(len(pushed)))
			returned := make([]pkg.ItemID, 0, len(items))
			for _, item := range items {
				returned = append(returned, item.ItemID)
			}
			Expect(returned).To(ConsistOf(pushed))
		})
	})

	// ReadBoard is Read plus the answered items the board renders as dimmed
	// records. The two readers share one private body, so the pair below asserts
	// both halves of that sharing: an answered item is returned and survives, and
	// a dead open asker is still removed in the same pass. The first half alone
	// is satisfied by a store that returns every stored row; the second alone by
	// the old Read. Together they pin the widened filter to the answered item and
	// prove the prune was not disabled with it.
	Describe("ReadBoard", func() {
		// itemIDs lists the ids a read returned, so membership is asserted rather
		// than a length that a different item could satisfy.
		itemIDs := func(items pkg.Items) []pkg.ItemID {
			ids := make([]pkg.ItemID, 0, len(items))
			for _, item := range items {
				ids = append(ids, item.ItemID)
			}
			return ids
		}

		It("returns an answered item that Read omits", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())
			_, err = store.Answer(ctx, item.ItemID, "telegram", "", "", nil, nil, nil)
			Expect(err).To(BeNil())

			read, err := store.Read(ctx)
			Expect(err).To(BeNil())
			Expect(itemIDs(read)).NotTo(ContainElement(item.ItemID))

			board, err := store.ReadBoard(ctx)
			Expect(err).To(BeNil())
			Expect(itemIDs(board)).To(ContainElement(item.ItemID))
		})

		It("still returns an open item", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())

			board, err := store.ReadBoard(ctx)
			Expect(err).To(BeNil())
			Expect(itemIDs(board)).To(ConsistOf(item.ItemID))
			Expect(board[0].State).To(Equal(pkg.OpenState))
		})

		// The `closed` item leaves the queue and the card leaves with it, in both
		// readers. The pair is a table because the rule is about the readers, not
		// about either one alone.
		DescribeTable("omits a closed item",
			func(reader string) {
				item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
				Expect(err).To(BeNil())
				_, err = store.Close(ctx, item.ItemID, "", nil)
				Expect(err).To(BeNil())

				var items pkg.Items
				switch reader {
				case "Read":
					items, err = store.Read(ctx)
				case "ReadBoard":
					items, err = store.ReadBoard(ctx)
				default:
					Fail("unknown reader " + reader)
				}
				Expect(err).To(BeNil())
				Expect(itemIDs(items)).NotTo(ContainElement(item.ItemID))
			},
			Entry("Read", "Read"),
			Entry("ReadBoard", "ReadBoard"),
		)

		// The answered item is never liveness-tested, so its producer being gone
		// cannot prune it. Asserted as returned *and* still in the store: a store
		// that merely filtered it out at read time would pass the second half
		// alone.
		It("returns an answered item whose producer is gone, without pruning it", func() {
			item, err := store.Push(ctx, pushRequest("session-dead", "gate-answered"))
			Expect(err).To(BeNil())
			_, err = store.Answer(ctx, item.ItemID, "telegram", "", "", nil, nil, nil)
			Expect(err).To(BeNil())

			// The producer exited after the item was answered.
			sessionLivenessChecker.IsLiveReturns(false)

			board, err := store.ReadBoard(ctx)
			Expect(err).To(BeNil())
			Expect(itemIDs(board)).To(ContainElement(item.ItemID))

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.State).To(Equal(pkg.AnsweredState))
		})

		// The other half of the pair: widening the filter must not have widened
		// the prune. One sweep, two dead producers, opposite outcomes — an
		// answered item survives and an open question is removed. The removal is
		// asserted against the store, not merely against the returned slice.
		It("still removes a dead open asker in the same read that keeps the answered item", func() {
			answered, err := store.Push(ctx, pushRequest("session-dead-a", "gate-answered"))
			Expect(err).To(BeNil())
			_, err = store.Answer(ctx, answered.ItemID, "telegram", "", "", nil, nil, nil)
			Expect(err).To(BeNil())

			asked, err := store.Push(ctx, pushRequest("session-dead-b", "gate-asked"))
			Expect(err).To(BeNil())

			// Both producers are gone by the time the board reads.
			sessionLivenessChecker.IsLiveReturns(false)

			board, err := store.ReadBoard(ctx)
			Expect(err).To(BeNil())
			Expect(itemIDs(board)).To(ContainElement(answered.ItemID))
			Expect(itemIDs(board)).NotTo(ContainElement(asked.ItemID))

			// The removal actually happened: the row is gone, not filtered at read
			// time. A read that only omitted it would leave it retrievable.
			_, err = store.Get(ctx, asked.ItemID)
			Expect(errors.Is(err, pkg.ErrItemNotFound)).To(BeTrue())

			// And the sweep did not take the answered item with it.
			got, err := store.Get(ctx, answered.ItemID)
			Expect(err).To(BeNil())
			Expect(got.State).To(Equal(pkg.AnsweredState))
		})

		// ⚠️ The operator's report, reproduced as a spec. Answer one card and the
		// producer's next push of the SAME dedup_key writes a new row, which
		// rendered as a fresh prompt — so one ask cost two answers. The store's
		// suppression is open-scoped, so that second row is correct AT THE STORE'S
		// LAYER; the repeat is refused at the board's. Measured live 2026-09-28:
		// 710 (producer_id, dedup_key) groups held more than one row, 26 of them
		// with two or more answered rows, and one ask was answered three times.
		It("omits an open re-push whose ask was already answered", func() {
			answered, err := store.Push(ctx, pushRequest("session-a", "gate-answered"))
			Expect(err).To(BeNil())
			_, err = store.Answer(ctx, answered.ItemID, "attention-board", "", "", nil, nil, nil)
			Expect(err).To(BeNil())

			// The producer re-raises the same ask after the answer. The store writes
			// a second row — that is the open-scoped suppression, and this spec does
			// NOT ask the store to stop doing it.
			repushed, err := store.Push(ctx, pushRequest("session-a", "gate-answered"))
			Expect(err).To(BeNil())
			Expect(repushed.ItemID).NotTo(Equal(answered.ItemID))
			Expect(repushed.State).To(Equal(pkg.OpenState))

			board, err := store.ReadBoard(ctx)
			Expect(err).To(BeNil())

			// The answered record stands, and the re-push does not render as a prompt.
			Expect(itemIDs(board)).To(ContainElement(answered.ItemID))
			Expect(itemIDs(board)).NotTo(ContainElement(repushed.ItemID))

			// ⚠️ Asserted against the STORE, not only against the returned slice: the
			// row is withheld from the board, not deleted. A fix that removed it would
			// pass the two assertions above while quietly pre-empting the schema
			// change that owns the suppression rule.
			got, err := store.Get(ctx, repushed.ItemID)
			Expect(err).To(BeNil())
			Expect(got.State).To(Equal(pkg.OpenState))
		})

		// The first negative control: the rule is scoped to an ANSWERED sibling, so
		// an open item with no answered twin must still render. Without this, a
		// filter that dropped every open row would pass the spec above.
		It("still returns an open item whose key has no answered sibling", func() {
			other, err := store.Push(ctx, pushRequest("session-a", "gate-other"))
			Expect(err).To(BeNil())

			board, err := store.ReadBoard(ctx)
			Expect(err).To(BeNil())
			Expect(itemIDs(board)).To(ConsistOf(other.ItemID))
		})

		// ⚠️ The second negative control, and the one that would do real damage if
		// it were wrong: a close is a CLEAR, not an answer (§ Answer routing), so a
		// cleared ask has NOT been answered and its re-raise must still reach the
		// operator. The scoping is load-bearing rather than cosmetic — the measured
		// population is dominated by closed pairs (665) over answered ones (26), so
		// suppressing on a close would silently drop re-raises nobody answered.
		It("still returns an open re-push whose sibling was cleared, not answered", func() {
			cleared, err := store.Push(ctx, pushRequest("session-a", "gate-cleared"))
			Expect(err).To(BeNil())
			_, err = store.Close(ctx, cleared.ItemID, "", nil)
			Expect(err).To(BeNil())

			repushed, err := store.Push(ctx, pushRequest("session-a", "gate-cleared"))
			Expect(err).To(BeNil())

			board, err := store.ReadBoard(ctx)
			Expect(err).To(BeNil())
			Expect(itemIDs(board)).To(ConsistOf(repushed.ItemID))
		})
	})

	Describe("durability", func() {
		It("returns the same rows from a fresh store over the same database", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())

			fresh := pkg.NewAttentionStore(
				db,
				pkg.NewItemIDGenerator(),
				sessionLivenessChecker,
				libtime.NewCurrentDateTime(),
				libtime.Duration(15*60*1e9),
			)
			read, err := fresh.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(read.ItemID).To(Equal(item.ItemID))
			Expect(read.State).To(Equal(pkg.OpenState))
			Expect(read.CreatedAt.Equal(item.CreatedAt)).To(BeTrue())
		})
	})

	// ⚠️ These specs pin the read path's transaction SHAPE, which is a
	// correctness property and not an optimisation: bbolt permits one read-write
	// transaction at a time, so a read that opens one blocks every Push behind
	// it for as long as it runs.
	//
	// Measured 2026-09-27 against the deployed store, before the split: the read
	// path answered in 0.03 s when quiet and 10.6 s under load, and
	// `attention-ask.py post` failed on every attempt because a valid write
	// could not get the lock a list read was holding across a scan that did file
	// I/O per item.
	//
	// countingDB counts the transactions the store opens while delegating to a
	// real boltkv, so the assertion is on what the code did, not on how long it
	// took.
	Describe("read path transactions", func() {
		var counting *countingDB
		var countingStore pkg.AttentionStore

		BeforeEach(func() {
			counting = &countingDB{DB: db}
			countingStore = pkg.NewAttentionStore(
				counting,
				pkg.NewItemIDGenerator(),
				sessionLivenessChecker,
				libtime.NewCurrentDateTime(),
				libtime.Duration(15*60*1e9),
			)
		})

		It("opens no write transaction when every producer is live", func() {
			_, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())

			counting.updates = 0
			items, err := countingStore.Read(ctx)
			Expect(err).To(BeNil())
			Expect(items).To(HaveLen(1))
			// The point of the fix: a read with nothing to prune is read-only
			// end to end, so it cannot starve a Push.
			Expect(counting.updates).To(Equal(0))
		})

		It("opens exactly one write transaction when it prunes a dead asker", func() {
			_, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())

			sessionLivenessChecker.IsLiveReturns(false)

			counting.updates = 0
			items, err := countingStore.Read(ctx)
			Expect(err).To(BeNil())
			Expect(items).To(BeEmpty())
			// One short prune — not a writer lock held across the whole scan.
			Expect(counting.updates).To(Equal(1))
		})

		// ⚠️ The two specs below pin the window the split introduced. A
		// `remove` disposition is computed from the snapshot `readItems` takes,
		// so anything that changes between that snapshot and the prune is
		// invisible to it unless the prune re-reads. The old single-transaction
		// read could not reach this: `Answer` needed the same write lock and
		// serialized behind it. Both were reproduced against the first version
		// of this change, which deleted the item in each case.
		It("does not prune an item answered between the scan and the prune", func() {
			item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
			Expect(err).To(BeNil())

			// Classification runs the liveness check between the scan and the
			// prune, so it is exactly the window the race lives in. Answer the
			// item from inside it, then report the producer gone.
			answered := false
			sessionLivenessChecker.IsLiveCalls(func(_ context.Context, _ string) bool {
				if !answered {
					answered = true
					_, answerErr := store.Answer(
						ctx,
						item.ItemID,
						"telegram",
						"",
						"",
						nil,
						nil,
						nil,
					)
					Expect(answerErr).To(BeNil())
				}
				return false
			})

			items, err := countingStore.Read(ctx)
			Expect(err).To(BeNil())
			Expect(items).To(BeEmpty())

			// The answer must survive. Pruning it would rewrite the history the
			// schema says is never rewritten — the one thing the prune exists
			// not to do.
			history, err := store.History(ctx, 0, 0)
			Expect(err).To(BeNil())
			Expect(history).To(HaveLen(1))
			Expect(history[0].State).To(Equal(pkg.AnsweredState))
		})

		It(
			"does not prune an item whose producer came back between the scan and the prune",
			func() {
				item, err := store.Push(ctx, pushRequest("session-a", "gate-1"))
				Expect(err).To(BeNil())

				// Dead when classified, live again by the time the prune re-checks
				// — what a resumed session, a refreshed heartbeat, or a registry
				// that briefly read as unreadable all look like. Liveness is not
				// monotonic, so the prune cannot inherit it from the snapshot.
				calls := 0
				sessionLivenessChecker.IsLiveCalls(func(_ context.Context, _ string) bool {
					calls++
					return calls > 1
				})

				items, err := countingStore.Read(ctx)
				Expect(err).To(BeNil())
				Expect(items).To(BeEmpty())

				history, err := store.History(ctx, 0, 0)
				Expect(err).To(BeNil())
				Expect(history).To(HaveLen(1))
				Expect(history[0].ItemID).To(Equal(item.ItemID))
				Expect(history[0].State).To(Equal(pkg.OpenState))
			},
		)
	})
})
