// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"

	libboltkv "github.com/bborbe/boltkv"
	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/handler"
)

// The vault-and-ledger half of the provenance chain, end to end: a fixture task
// file and the real spawn-ledger store, read by the real pkg.NewTaskIndex and
// pkg.NewProvenanceResolver and served by the real board JSON read. The marker's
// own grammar (box states, glyph, prose) is pinned in pkg/provenance_test.go;
// this file pins that the resolved fact reaches the card's derived risk, and
// that an unreadable ledger fails soft.
var _ = Describe("the provenance chain in the board JSON", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var panes *mocks.PaneLister
	var vault string
	var stateDir string
	var sessionsDir string
	var sessionLedger pkg.SessionLedgerStore
	var clock libtime.CurrentDateTime

	BeforeEach(func() {
		ctx = context.Background()
		var err error
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
		panes = &mocks.PaneLister{}
		panes.ListReturns(map[int]pkg.Pane{}, nil)
		vault = filepath.Join(GinkgoT().TempDir(), "Personal")
		stateDir = GinkgoT().TempDir()
		sessionsDir = GinkgoT().TempDir()
		sessionLedger = pkg.NewSessionLedgerStore(db)
		clock = libtime.NewCurrentDateTime()
		clock.SetNow(clock.Now())
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	// sessionIDOf derives a UUID-shaped session id, the shape the task index and
	// the ledger key on.
	sessionIDOf := func(label string) string {
		sum := sha256.Sum256([]byte(label))
		h := hex.EncodeToString(sum[:16])
		return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
	}

	writeTask := func(name, sessionID, body string) {
		path := filepath.Join(vault, "25 Tasks")
		Expect(os.MkdirAll(path, 0o750)).To(BeNil())
		content := "---\nclaude_session_id: " + sessionID + "\nstatus: active\n---\n\n# Steps\n\n" + body + "\n"
		Expect(os.WriteFile(filepath.Join(path, name), []byte(content), 0o600)).To(BeNil())
	}

	writeSpawn := func(sessionID, mode string) {
		Expect(sessionLedger.Post(ctx, pkg.SessionLedgerEntry{
			SessionID: sessionID,
			Mode:      mode,
			Location:  pkg.LocalSessionHeartbeatLocation,
			SpawnedAt: clock.Now(),
		})).To(Succeed())
	}

	pushPark := func(sessionID, dedupKey string) *pkg.Item {
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      pkg.ProducerID("producer-" + sessionID),
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:" + sessionID),
			DedupKey:        pkg.DedupKey(dedupKey),
			InterruptClass:  "approve",
			Payload:         pkg.Payload("Approve the next step for " + dedupKey + "?"),
			AnswerMechanism: pkg.PermissionAnswerMechanism,
		})
		Expect(err).To(BeNil())
		return item
	}

	buildBoardWithLedger := func(ledger pkg.SessionLedgerStore) http.Handler {
		return handler.NewAttentionBoardReadHandler(
			store,
			pkg.NewProvenanceResolver(
				pkg.NewEventLogReader(stateDir),
				sessionsDir,
				ledger,
				panes,
				pkg.NewTaskIndex(ctx, vault, clock),
				clock,
			),
			false,
			vault,
		)
	}

	It("raises a production-touching session's card to the elevated risk, and leaves a plain one", func() {
		marked := sessionIDOf("marked")
		plain := sessionIDOf("plain")
		writeTask("Marked.md", marked, "- [ ] ⚠️ production-touching — `make install`")
		writeTask("Plain.md", plain, "- [ ] ordinary step")
		writeSpawn(marked, "headless")
		writeSpawn(plain, "headless")
		markedItem := pushPark(marked, "park-marked")
		plainItem := pushPark(plain, "park-plain")

		board := readBoardByID(buildBoardWithLedger(sessionLedger))

		// Positive control: both cards resolved their task, so the difference
		// below is the marker and nothing else.
		Expect(board[markedItem.ItemID].Board.Provenance.TaskName).To(Equal("Marked"))
		Expect(board[plainItem.ItemID].Board.Provenance.TaskName).To(Equal("Plain"))
		Expect(board[markedItem.ItemID].Board.Risk).To(Equal(pkg.ElevatedRisk))
		Expect(board[plainItem.ItemID].Board.Risk).To(Equal(pkg.RiskOf(*plainItem, false)))
		Expect(board[plainItem.ItemID].Board.Risk).NotTo(Equal(pkg.ElevatedRisk))
	})

	It("serves the card when the session resolves no task", func() {
		sessionID := sessionIDOf("no-task")
		writeSpawn(sessionID, "headless")
		item := pushPark(sessionID, "park-no-task")

		entry := boardEntryOf(buildBoardWithLedger(sessionLedger), item.ItemID)

		Expect(entry.Payload).To(Equal(item.Payload))
		Expect(entry.Board.Provenance.TaskName).To(BeEmpty())
		Expect(entry.Board.Risk).NotTo(Equal(pkg.ElevatedRisk))
	})

	It("serves the card when the ledger store is nil", func() {
		item := pushPark(sessionIDOf("no-ledger"), "park-no-ledger")

		entry := boardEntryOf(buildBoardWithLedger(nil), item.ItemID)

		Expect(entry.Payload).To(Equal(item.Payload))
	})

	It("serves the card when the ledger store cannot be read", func() {
		failing := &mocks.SessionLedgerStore{}
		failing.ListReturns(nil, errors.New(ctx, "ledger unreadable"))
		item := pushPark(sessionIDOf("broken"), "park-broken")

		entry := boardEntryOf(buildBoardWithLedger(failing), item.ItemID)

		Expect(entry.Payload).To(Equal(item.Payload))
	})
})
