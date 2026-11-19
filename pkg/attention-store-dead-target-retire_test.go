// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	libboltkv "github.com/bborbe/boltkv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

// A card whose target pane AND session are both provably gone is retired
// instead of reopened. Reopening it asks the operator to type into a pane that
// no longer exists — "more useless attention cards". Unprovable is never gone.
var _ = Describe("Dead-target retire", func() {
	var (
		ctx         context.Context
		store       pkg.AttentionStore
		checker     *mocks.SessionLivenessChecker
		panes       *mocks.PaneLister
		sessionsDir string
		now         libtime.DateTime
	)

	BeforeEach(func() {
		ctx = context.Background()
		db, err := libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())
		DeferCleanup(func() { Expect(db.Close()).To(BeNil()) })

		sessionsDir = GinkgoT().TempDir()
		Expect(os.WriteFile(
			filepath.Join(sessionsDir, "session-a.json"),
			[]byte(`{"sessionId":"session-a","name":"Attention Cards Retire"}`),
			0o600,
		)).To(Succeed())

		now = libtime.NewCurrentDateTime().Now()
		clock := libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime { return now })

		checker = &mocks.SessionLivenessChecker{}
		checker.IsLiveReturns(false)
		panes = &mocks.PaneLister{}
		panes.ListReturns(map[int]pkg.Pane{}, nil)

		store = pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			checker,
			clock,
			libtime.Duration(15*60*1e9),
			pkg.WithPaneLister(panes),
			pkg.WithSessionsDir(sessionsDir),
		)
	})

	// park answers a done-class claim so the item sits awaiting evidence.
	// ⚠️ An OPTION answer, not a text one: it carries no claim, so the sweep
	// never shells out to `gh` and this spec stays hermetic.
	park := func() *pkg.Item {
		item, err := store.Push(ctx, pkg.PushRequest{
			ProducerID:      "session-a",
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     pkg.LivenessRef("session:session-a"),
			DedupKey:        "dead-target",
			InterruptClass:  "pick",
			Payload:         "type anything in pane 229",
			AnswerMechanism: pkg.MessageAnswerMechanism,
			Options: pkg.AnswerOptions{
				{Label: "done — reviewed and flipped both", Recommended: true},
				{Label: "not yet"},
			},
		})
		Expect(err).To(BeNil())
		_, err = store.Answer(ctx, item.ItemID, "attention-board", "", "", &pkg.Answer{
			Kind:  pkg.OptionAnswerKind,
			Value: "done — reviewed and flipped both",
		}, nil, nil)
		Expect(err).To(BeNil())
		return item
	}

	sweepPastDeadline := func() {
		now = now.Add(libtime.Duration(pkg.EvidenceDeadline))
		_, err := store.SweepAnswered(ctx, libtime.Duration(15*60*1e9))
		Expect(err).To(BeNil())
	}

	It("retires the card when the pane listing succeeds and neither target survives", func() {
		item := park()
		sweepPastDeadline()

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.State).To(Equal(pkg.ClosedState), "a dead target must be retired, not reopened")
		Expect(got.ResolvedBy).To(Equal("retired: target pane and session are gone"))
		Expect(string(got.Context)).To(ContainSubstring(pkg.RetiredNote))
	})

	It("reopens while the session is still live", func() {
		checker.IsLiveReturns(true)
		item := park()
		sweepPastDeadline()

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.State).To(Equal(pkg.OpenState), "one live half is not a dead target")
	})

	It("reopens when a pane still carries the session's name", func() {
		panes.ListReturns(map[int]pkg.Pane{
			294: {PaneID: 294, Title: "✳ Attention Cards Retire"},
		}, nil)
		item := park()
		sweepPastDeadline()

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.State).To(Equal(pkg.OpenState), "a live pane is not a dead target")
	})

	It("reopens when the pane listing fails, because that proves nothing", func() {
		panes.ListReturns(nil, errors.New("wezterm not running"))
		item := park()
		sweepPastDeadline()

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.State).To(Equal(pkg.OpenState), "an unreadable listing must never retire a card")
	})

	It("reopens when the session name cannot be resolved", func() {
		Expect(os.Remove(filepath.Join(sessionsDir, "session-a.json"))).To(Succeed())
		item := park()
		sweepPastDeadline()

		got, err := store.Get(ctx, item.ItemID)
		Expect(err).To(BeNil())
		Expect(got.State).To(Equal(pkg.OpenState), "an unresolvable name is unprovable, not gone")
	})
})
