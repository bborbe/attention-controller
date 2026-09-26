// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"encoding/json"

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

// The attention item schema's silence 19 added `answered_client`, the
// twenty-sixth field: what the store can say about the client that posted an
// answer, derived from the request rather than declared by it. It exists
// because `answered_by` is a caller declaration, so a human's click and a
// scripted client posting the same body write the identical value there.
//
// These specs cover the store's half of that contract: the field rides the same
// compare-and-set that writes `answered_at` and `answered_by`, it is written on
// the arm-caused `open` -> `closed` row and nowhere else, and an item whose
// stored record has no such key reads exactly as it did before the field
// existed. The derivation itself is the handler's half, and is asserted where
// the request exists — see pkg/handler/answered-client_test.go.
var _ = Describe("Answered client", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		// boltkv, not memorykv, for the same reason the store suite uses it: the
		// answer path's compare-and-set is atomic only because the backend
		// serializes write transactions.
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

		sessionLivenessChecker := &mocks.SessionLivenessChecker{}
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

	// pushItem pushes a message item — the class the board answers, and so the
	// class the field exists for.
	pushItem := func(dedupKey string) *pkg.Item {
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      "session-a",
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:session-a"),
			DedupKey:        pkg.DedupKey(dedupKey),
			InterruptClass:  "pick",
			Payload:         "Which surface should the answer land on?",
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		Expect(err).To(BeNil())
		return item
	}

	// pushAck pushes an ack item — the class the board acknowledges, which is the
	// arm-caused open -> closed row rather than the answer transition.
	pushAck := func(dedupKey string) *pkg.Item {
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      "session-a",
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:session-a"),
			DedupKey:        pkg.DedupKey(dedupKey),
			InterruptClass:  "approve",
			Payload:         "the nightly sweep failed",
			AnswerMechanism: pkg.AckAnswerMechanism,
		})
		Expect(err).To(BeNil())
		return item
	}

	// automation returns a pointer to the value, which is how the hint expresses
	// absent distinctly from false.
	automation := func(value bool) *bool {
		return &value
	}

	// client is the record a handler composes from a request.
	client := func(userAgent string, remoteAddr string, hint *bool) *pkg.AnsweredClient {
		return &pkg.AnsweredClient{
			UserAgent:  userAgent,
			RemoteAddr: remoteAddr,
			Automation: hint,
		}
	}

	Describe("On the answer transition", func() {
		It("stores the client on the same transition that writes answered_at", func() {
			item := pushItem("question-1")

			answered, err := store.Answer(
				ctx,
				item.ItemID,
				"attention-board",
				"",
				"",
				nil,
				nil,
				client("curl/8.7.1", "192.0.2.1:51234", automation(true)),
			)
			Expect(err).To(BeNil())
			Expect(answered.AnsweredClient).NotTo(BeNil())

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.State).To(Equal(pkg.AnsweredState))
			Expect(got.AnsweredAt).NotTo(BeNil())
			Expect(got.AnsweredClient).NotTo(BeNil())
			Expect(got.AnsweredClient.UserAgent).To(Equal("curl/8.7.1"))
			Expect(got.AnsweredClient.RemoteAddr).To(Equal("192.0.2.1:51234"))
			Expect(got.AnsweredClient.Automation).NotTo(BeNil())
			Expect(*got.AnsweredClient.Automation).To(BeTrue())
		})

		// The pointer with omitempty is the whole reason a pre-change item reads
		// as it does: a nil client must leave the key off the wire rather than
		// serialise an empty object.
		It("leaves the field absent when the caller has no client to record", func() {
			item := pushItem("question-1")

			_, err := store.Answer(ctx, item.ItemID, "telegram", "", "", nil, nil, nil)
			Expect(err).To(BeNil())

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.AnsweredClient).To(BeNil())

			encoded, err := json.Marshal(got)
			Expect(err).To(BeNil())
			Expect(string(encoded)).NotTo(ContainSubstring("answered_client"))
		})

		It("keeps an omitted automation hint absent rather than false", func() {
			item := pushItem("question-1")

			_, err := store.Answer(
				ctx,
				item.ItemID,
				"attention-board",
				"",
				"",
				nil,
				nil,
				client("curl/8.7.1", "192.0.2.1:51234", nil),
			)
			Expect(err).To(BeNil())

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.AnsweredClient).NotTo(BeNil())
			Expect(got.AnsweredClient.Automation).To(BeNil())

			encoded, err := json.Marshal(got.AnsweredClient)
			Expect(err).To(BeNil())
			Expect(string(encoded)).NotTo(ContainSubstring("automation"))
		})

		// Negative control for the spec above: if the store dropped a reported
		// false on the way through, "absent" and "reported false" would be the
		// same record and the hint would be unreadable in the one direction it
		// can actually help.
		It("records a reported false as false", func() {
			item := pushItem("question-1")

			_, err := store.Answer(
				ctx,
				item.ItemID,
				"attention-board",
				"",
				"",
				nil,
				nil,
				client("Mozilla/5.0", "192.0.2.1:51234", automation(false)),
			)
			Expect(err).To(BeNil())

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.AnsweredClient.Automation).NotTo(BeNil())
			Expect(*got.AnsweredClient.Automation).To(BeFalse())

			encoded, err := json.Marshal(got.AnsweredClient)
			Expect(err).To(BeNil())
			Expect(string(encoded)).To(ContainSubstring(`"automation":false`))
		})

		// What this actually exercises is the state switch's rejection, which
		// returns before the client is assigned — so it shows that a losing answer
		// records nothing at all, not that the assignment sits inside the
		// compare-and-set. The validator rejection below is the branch that
		// reaches the rollback.
		It("records no client when the answer loses the race", func() {
			item := pushItem("question-1")

			_, err := store.Answer(ctx, item.ItemID, "attention-board", "", "", nil, nil, nil)
			Expect(err).To(BeNil())

			_, err = store.Answer(
				ctx,
				item.ItemID,
				"telegram",
				"",
				"",
				nil,
				nil,
				client("curl/8.7.1", "198.51.100.7:41000", automation(true)),
			)
			Expect(err).NotTo(BeNil())
			Expect(errors.Is(err, pkg.ErrAlreadyAnswered)).To(BeTrue())

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.AnsweredBy).To(Equal("attention-board"))
			Expect(got.AnsweredClient).To(BeNil())
		})

		// ⚠️ The rollback branch, and the spec that actually reaches it. The client
		// is assigned to the item before the item's own validators run, so a
		// validator rejection is the one rejection a rollback has to undo — the
		// state switch above returns before the assignment and proves nothing
		// about it. The item is pushed as a `multiple` question and answered with a
		// single label: Answer.Validate does not hold the question's cardinality,
		// so that pairing passes every check made before the store, and the
		// store's own validateAnswer is where it fails.
		It("records no client when the answer is rejected by the item's own validator", func() {
			item, err := store.Push(ctx, pkg.PushRequest{
				ProducerID:        "session-a",
				ProducerKind:      pkg.SessionProducerKind,
				LivenessRef:       pkg.LivenessRef("session:session-a"),
				DedupKey:          pkg.DedupKey("question-1"),
				InterruptClass:    "pick",
				Payload:           "Which surface should the answer land on?",
				AnswerMechanism:   pkg.MessageAnswerMechanism,
				AnswerCardinality: pkg.MultipleAnswerCardinality,
			})
			Expect(err).To(BeNil())

			_, err = store.Answer(
				ctx,
				item.ItemID,
				"attention-board",
				"",
				"",
				&pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "one"},
				nil,
				client("curl/8.7.1", "198.51.100.7:41000", automation(true)),
			)
			Expect(err).NotTo(BeNil())
			// Pinned to the validator, so the spec cannot pass on some other
			// rejection: this is the branch reached after the client is assigned.
			Expect(errors.Is(err, validation.Error)).To(BeTrue())

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			// The whole write is rolled back rather than only the client field: the
			// item is still open and carries no answered_at or answered_by either.
			Expect(got.State).To(Equal(pkg.OpenState))
			Expect(got.AnsweredAt).To(BeNil())
			Expect(got.AnsweredBy).To(BeEmpty())
			Expect(got.AnsweredClient).To(BeNil())

			encoded, err := json.Marshal(got)
			Expect(err).To(BeNil())
			Expect(string(encoded)).NotTo(ContainSubstring("answered_client"))
		})
	})

	Describe("On the close transition", func() {
		It("records the client when an arm caused the close", func() {
			item := pushAck("report-1")

			closed, err := store.Close(
				ctx,
				item.ItemID,
				"attention-board",
				client("Mozilla/5.0", "192.0.2.1:51234", automation(true)),
			)
			Expect(err).To(BeNil())
			Expect(closed.AnsweredClient).NotTo(BeNil())

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.State).To(Equal(pkg.ClosedState))
			Expect(got.AnsweredClient).NotTo(BeNil())
			Expect(got.AnsweredClient.UserAgent).To(Equal("Mozilla/5.0"))
			Expect(got.AnsweredClient.RemoteAddr).To(Equal("192.0.2.1:51234"))
			Expect(got.AnsweredClient.Automation).NotTo(BeNil())
			Expect(*got.AnsweredClient.Automation).To(BeTrue())
		})

		// The schema sets the field on an *arm-caused* open -> closed. A producer
		// withdrawing its own item names no arm, and recording a client for it
		// would put a causer on the record the schema does not name.
		It("records none when no arm caused the close", func() {
			item := pushAck("report-1")

			_, err := store.Close(
				ctx,
				item.ItemID,
				"",
				client("curl/8.7.1", "192.0.2.1:51234", automation(true)),
			)
			Expect(err).To(BeNil())

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.State).To(Equal(pkg.ClosedState))
			Expect(got.AnsweredBy).To(BeEmpty())
			Expect(got.AnsweredClient).To(BeNil())
		})

		// ⚠️ The answered -> closed row must keep the client that *answered*. The
		// field rides the open -> closed row only, so closing an already-answered
		// item leaves the answering client in place rather than replacing it with
		// whoever closed.
		It("does not overwrite the client that answered when closing an answered item", func() {
			item := pushItem("question-1")

			_, err := store.Answer(
				ctx,
				item.ItemID,
				"supervisor:attention-next",
				"",
				"",
				nil,
				nil,
				client("curl/8.7.1", "192.0.2.1:51234", automation(true)),
			)
			Expect(err).To(BeNil())

			_, err = store.Close(
				ctx,
				item.ItemID,
				"attention-board",
				client("Mozilla/5.0", "198.51.100.7:41000", automation(false)),
			)
			Expect(err).To(BeNil())

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.State).To(Equal(pkg.ClosedState))
			Expect(got.AnsweredBy).To(Equal("supervisor:attention-next"))
			Expect(got.AnsweredClient).NotTo(BeNil())
			Expect(got.AnsweredClient.UserAgent).To(Equal("curl/8.7.1"))
			Expect(got.AnsweredClient.RemoteAddr).To(Equal("192.0.2.1:51234"))
		})
	})

	// An item answered before the field existed is one whose stored record has no
	// `answered_client` key at all. The record is rewritten here in that shape —
	// the stored bytes with the key removed — rather than simulated by passing a
	// nil client, because the point is that the *read* of a record that predates
	// the field is unchanged, and only a record written without the key can say
	// that.
	Describe("A pre-change record", func() {
		It("reads with the field absent when the stored record carries no such key", func() {
			item := pushItem("question-1")

			_, err := store.Answer(
				ctx,
				item.ItemID,
				"attention-board",
				"",
				"",
				nil,
				nil,
				client("curl/8.7.1", "192.0.2.1:51234", automation(true)),
			)
			Expect(err).To(BeNil())

			// The value type is json.RawMessage rather than Item so the bytes
			// round-trip unchanged: RawMessage marshals to itself, so this writes
			// the record as a pre-change store would have left it.
			rawStore := libkv.NewStoreTx[string, json.RawMessage](pkg.AttentionStoreBucketName)
			err = db.Update(ctx, func(ctx context.Context, tx libkv.Tx) error {
				raw, err := rawStore.Get(ctx, tx, item.ItemID.String())
				if err != nil {
					return err
				}
				fields := map[string]any{}
				if err := json.Unmarshal(*raw, &fields); err != nil {
					return err
				}
				delete(fields, "answered_client")
				stripped, err := json.Marshal(fields)
				if err != nil {
					return err
				}
				return rawStore.Add(ctx, tx, item.ItemID.String(), stripped)
			})
			Expect(err).To(BeNil())

			got, err := store.Get(ctx, item.ItemID)
			Expect(err).To(BeNil())
			Expect(got.State).To(Equal(pkg.AnsweredState))
			Expect(got.AnsweredBy).To(Equal("attention-board"))
			Expect(got.AnsweredClient).To(BeNil())

			encoded, err := json.Marshal(got)
			Expect(err).To(BeNil())
			Expect(string(encoded)).NotTo(ContainSubstring("answered_client"))
		})
	})
})
