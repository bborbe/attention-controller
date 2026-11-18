// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"
	stdtime "time"

	libboltkv "github.com/bborbe/boltkv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg/attnlog"
)

// This file pins the expiry-removal close event against the fold that replays
// the event log.
//
// The defect: `pruneExpired` deleted an expired item from the KV store and wrote
// no event, so `foldEventFile` — last-record-wins per item id — kept the item
// open for as long as the open record survived the 30-day event prune. The fix
// appends a `close` record to the session's own log after the removal commits.
//
// ⚠️ THE ASSERTION IS ON THE FOLD, NOT ON THE STORE, AND IT EXERCISES THE
// PRODUCER. The store half already passed before the fix — the prune deletes the
// row — so an assertion on `store.Get` proves nothing new. The spec seeds the
// producer's `open` record, runs the real read path (which classifies the item
// expired and prunes it), and then folds the same file the way
// `foldEventLogs` does. A hand-written `close` would pass on the unchanged code;
// this cannot.
//
// It lives in `package pkg` rather than `package pkg_test` because
// `foldEventFile` is unexported — the same reason the sibling internal specs
// live here. Both packages compile into the same test binary, so this spec
// registers with the suite the rest of the package runs under.
var _ = Describe("expiry removal closes the event log", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	// A well-formed session id: the close is written to `<session_id>.events.jsonl`,
	// so the value has to survive the writer's file-name guard.
	const sessionID = "3f2b8c1e-0000-4000-8000-000000000001"

	It("appends a close the fold sees, so a pruned item stops reading open", func() {
		stateDir := GinkgoT().TempDir()
		db, err := libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())
		DeferCleanup(func() { Expect(db.Close()).To(BeNil()) })

		store := NewAttentionStore(
			db,
			NewItemIDGenerator(),
			NewSessionLivenessChecker(GinkgoT().TempDir()),
			libtime.NewCurrentDateTime(),
			libtime.Duration(15*60*1e9),
			WithExpiryCloseWriter(attnlog.NewExpiryCloseWriter(stateDir)),
		)

		expired := libtime.DateTimeFromUnixMicro(
			stdtime.Now().Add(-stdtime.Hour).UnixMicro(),
		)
		item, err := store.Push(ctx, PushRequest{
			ProducerID:      ProducerID(sessionID),
			ProducerKind:    SessionProducerKind,
			LivenessRef:     LivenessRef("session:" + sessionID),
			DedupKey:        "expiry-close",
			InterruptClass:  "pick",
			Payload:         "which way?",
			AnswerMechanism: MessageAnswerMechanism,
			ExpiresAt:       &expired,
		})
		Expect(err).To(BeNil())

		// The producer's own open record — the row the fold would replay.
		// ⚠️ Its `item_id` is the store's DedupKey, not the generated ItemID:
		// `StoreEventSink.pushRequest` maps `DedupKey: DedupKey(record.ItemID)`
		// on the way in, so the log's id and the item's dedup key are the same
		// value. Seeding the store's generated id would aim the spec at a key
		// no real open record carries — it would pass on a fix that does nothing
		// in production.
		logPath := filepath.Join(stateDir, sessionID+".events.jsonl")
		Expect(os.WriteFile(logPath, []byte(
			`{"item_id":"`+item.DedupKey.String()+`","session_id":"`+sessionID+
				`","kind":"question","ts":1,"type":"open"}`+"\n",
		), 0o600)).To(Succeed())

		Expect(foldEventFile(logPath)).To(HaveLen(1),
			"the seeded open record must fold as open before the prune")

		// The read path IS the producer of the close: it classifies the item
		// expired and prunes it.
		_, err = store.Read(ctx)
		Expect(err).To(BeNil())

		_, err = store.Get(ctx, item.ItemID)
		Expect(err).To(MatchError(ContainSubstring("not found")),
			"the row must be deleted, not merely hidden")

		Expect(foldEventFile(logPath)).To(BeEmpty(),
			"the fold must no longer report the pruned id open — this is the "+
				"defect the close event exists to fix")
	})
})
