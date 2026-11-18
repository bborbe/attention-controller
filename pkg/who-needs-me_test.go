// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	stdtime "time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

var _ = Describe("WhoNeedsMeReader", func() {
	var ctx context.Context
	var now libtime.CurrentDateTimeGetter

	BeforeEach(func() {
		ctx = context.Background()
		now = libtime.NewCurrentDateTime()
	})

	newReader := func(
		store pkg.AttentionStore,
		provenance pkg.ProvenanceResolver,
		panes pkg.PaneLister,
		sessionsDir, stateDir string,
	) pkg.WhoNeedsMeReader {
		return pkg.NewWhoNeedsMeReader(
			store, provenance, panes, pkg.NewTranscriptReader(""), sessionsDir, stateDir, now,
		)
	}

	permissionItem := func() pkg.Item {
		return pkg.Item{
			ItemID:          "store-1",
			ProducerID:      "session-abc",
			LivenessRef:     "session:session-abc",
			DedupKey:        "dedup-1",
			AnswerMechanism: pkg.PermissionAnswerMechanism,
			Payload:         "Approve the deploy?",
			State:           pkg.OpenState,
			CreatedAt:       libtime.DateTime(stdtime.Now().Add(-90 * stdtime.Second)),
		}
	}

	It(
		"joins item fields to a resolver answer of generated values, single-line (AC1, AC2)",
		func() {
			// Generated, not constant: a hardcoded literal in the reader cannot match.
			name := fmt.Sprintf("worker-%d", stdtime.Now().UnixNano())
			task := fmt.Sprintf("task-%d", stdtime.Now().UnixNano())
			item := permissionItem()
			item.Payload = "Approve the\ndeploy?\n\nYes/no" // multi-line must collapse
			store := &mocks.AttentionStore{}
			store.ReadReturns(pkg.Items{item}, nil)
			provenance := &mocks.ProvenanceResolver{}
			provenance.ResolveReturns(pkg.Provenances{
				"store-1": {Pane: "3", Routable: true, SessionName: name, TaskName: task},
			})
			panes := &mocks.PaneLister{}
			panes.ListReturns(map[int]pkg.Pane{}, nil)

			feed, err := newReader(store, provenance, panes, "", "").Read(ctx)
			Expect(err).To(BeNil())
			Expect(feed.Source).To(Equal("store"))
			Expect(feed.Sources.Store).To(Equal("ok"))
			Expect(feed.Items).To(HaveLen(1))
			row := feed.Items[0]
			Expect(row.ItemID).To(Equal("store-1"))
			Expect(row.DedupKey).To(Equal("dedup-1"))
			Expect(row.SessionID).To(Equal("session-abc"))
			Expect(row.Kind).To(Equal("permission"))
			Expect(row.SessionName).To(Equal(name))
			Expect(row.Task).To(Equal(task))
			Expect(row.Pane).To(Equal("3"))
			Expect(row.AgeSeconds).To(BeNumerically(">=", 89))
			// AC1: no field carries a multi-line preformatted feed.
			Expect(row.Text).ToNot(ContainSubstring("\n"))
			// AC7: the reader reaches the store through Read only.
			Expect(store.ReadCallCount()).To(Equal(1))
		},
	)

	It("omits an ack item — it is not a gate (AC1)", func() {
		store := &mocks.AttentionStore{}
		ack := permissionItem()
		ack.AnswerMechanism = pkg.AckAnswerMechanism
		store.ReadReturns(pkg.Items{ack}, nil)
		provenance := &mocks.ProvenanceResolver{}
		provenance.ResolveReturns(pkg.Provenances{})
		panes := &mocks.PaneLister{}
		panes.ListReturns(map[int]pkg.Pane{}, nil)

		feed, err := newReader(store, provenance, panes, "", "").Read(ctx)
		Expect(err).To(BeNil())
		Expect(feed.Items).To(BeEmpty())
	})

	It("marks a recorded pane unreadable when the listing fails (AC2 negative, AC5)", func() {
		store := &mocks.AttentionStore{}
		store.ReadReturns(pkg.Items{permissionItem()}, nil)
		provenance := &mocks.ProvenanceResolver{}
		provenance.ResolveReturns(pkg.Provenances{
			"store-1": {PaneListingUnreadable: true, RecordedPane: "3"},
		})
		panes := &mocks.PaneLister{}
		panes.ListReturns(nil, errors.New(ctx, "wezterm unreachable"))

		feed, err := newReader(store, provenance, panes, "", "").Read(ctx)
		Expect(err).To(BeNil())
		Expect(feed.Sources.Panes).To(Equal("failed"))
		Expect(feed.Items).To(HaveLen(1))
		Expect(feed.Items[0].Pane).To(Equal("")) // no fabricated pane
		Expect(feed.Items[0].PaneListingUnreadable).To(BeTrue())
	})

	It("answers differently for a populated and an empty store (AC3)", func() {
		prov := &mocks.ProvenanceResolver{}
		prov.ResolveReturns(pkg.Provenances{})
		panes := &mocks.PaneLister{}
		panes.ListReturns(map[int]pkg.Pane{}, nil)

		populated := &mocks.AttentionStore{}
		populated.ReadReturns(pkg.Items{permissionItem()}, nil)
		empty := &mocks.AttentionStore{}
		empty.ReadReturns(pkg.Items{}, nil)

		first, err := newReader(populated, prov, panes, "", "").Read(ctx)
		Expect(err).To(BeNil())
		second, err := newReader(empty, prov, panes, "", "").Read(ctx)
		Expect(err).To(BeNil())
		Expect(first.Items).To(HaveLen(1)) // positive control
		Expect(second.Items).To(BeEmpty())
	})

	It("reports a failed registry read as a source status (AC5)", func() {
		store := &mocks.AttentionStore{}
		store.ReadReturns(pkg.Items{}, nil)
		prov := &mocks.ProvenanceResolver{}
		prov.ResolveReturns(pkg.Provenances{})
		panes := &mocks.PaneLister{}
		panes.ListReturns(map[int]pkg.Pane{}, nil)

		// A regular file where a directory is expected makes ReadDir return ENOTDIR.
		notADir := filepath.Join(GinkgoT().TempDir(), "regular-file")
		Expect(os.WriteFile(notADir, []byte("x"), 0o600)).To(BeNil())

		feed, err := newReader(store, prov, panes, notADir, "").Read(ctx)
		Expect(err).To(BeNil())
		Expect(feed.Sources.Registry).To(Equal("failed"))
		Expect(feed.Sessions).To(BeEmpty())
	})

	It("falls back to the event log, keeping a closed-then-reopened key (AC4)", func() {
		store := &mocks.AttentionStore{}
		store.ReadReturns(nil, errors.New(ctx, "store down"))
		prov := &mocks.ProvenanceResolver{}
		panes := &mocks.PaneLister{}
		panes.ListReturns(map[int]pkg.Pane{}, nil)

		stateDir := GinkgoT().TempDir()
		// `a` is opened, closed, then RE-opened — its last record is open, so it
		// must stay; `b` is open on a second session; `c` is opened then closed.
		lines := `{"type":"open","item_id":"a","session_id":"s1","kind":"permission","detail":"Approve?","ts":1000000000}
{"type":"close","item_id":"a"}
{"type":"open","item_id":"a","session_id":"s1","kind":"permission","detail":"Approve again?","ts":1000000500}
{"type":"open","item_id":"b","session_id":"s2","kind":"question","detail":"Which?","ts":1000000100}
{"type":"open","item_id":"c","session_id":"s2","kind":"question","detail":"Closed?","ts":1000000200}
{"type":"close","item_id":"c"}
`
		Expect(os.WriteFile(filepath.Join(stateDir, "s1.events.jsonl"), []byte(lines), 0o600)).
			To(BeNil())

		feed, err := newReader(store, prov, panes, "", stateDir).Read(ctx)
		Expect(err).To(BeNil())
		Expect(feed.Source).To(Equal("event-log"))
		Expect(feed.Sources.Store).To(Equal("failed"))
		Expect(feed.Items).To(HaveLen(2)) // a (reopened) and b; c stays closed
		ids := map[string]string{}
		sessions := map[string]bool{}
		for _, row := range feed.Items {
			Expect(row.Replayed).To(BeTrue())
			Expect(row.ItemID).To(Equal(row.DedupKey))
			Expect(row.Source).To(Equal("event-log"))
			ids[row.ItemID] = row.Text
			sessions[row.SessionID] = true
		}
		Expect(ids).To(HaveKey("a"))
		Expect(ids["a"]).To(Equal("Approve again?")) // the reopened record wins
		Expect(ids).To(HaveKey("b"))
		// AC4: two sessions with differing states both appear.
		Expect(sessions).To(HaveKey("s1"))
		Expect(sessions).To(HaveKey("s2"))
		// AC1 on the fallback path: the arrays are present (non-nil), not null.
		Expect(feed.Sessions).ToNot(BeNil())
		Expect(feed.Panes).ToNot(BeNil())
	})
})
