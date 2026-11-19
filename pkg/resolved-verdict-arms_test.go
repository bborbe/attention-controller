// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

// SC4's probe: one named `resolved` verdict id, driven through BOTH delivery
// arms, so the two cannot silently disagree about what `resolved` means.
//
// The arms are not interchangeable at runtime — the prose arm owns a prose card
// and the menu arm owns a hook-menu card, chosen by MenuQuestionsOf — but both
// can be handed the same DeliveryResolved verdict for the same item id. The
// expected equality is exact: the same carrier and the same outcome, which is
// what makes the attempt trail readable without knowing which arm wrote it.
//
// The id is a live one, measured 2026-11-19: the prose arm took `resolved` for
// it, wrote no record, and the close's `watcher:gate-resolved` backfill was all
// the trail held — so the lost-answer counter counted the look as a loss.
var _ = Describe("resolved verdict across both delivery arms", func() {
	const verdictID = "3a5a4ab93340ce790757b8ae3881bfd4"

	It("writes the same attempt row (carrier + outcome) in the prose arm and the menu arm", func() {
		proseStore := &mocks.AttentionStore{}
		proseStore.DeliveryReportsReturns(map[pkg.ItemID]*pkg.DeliveryReport{}, nil)
		proseStore.RecordAttemptReturns(&pkg.DeliveryAttempt{}, nil)
		proseJob := pkg.NewAttentionWatcherJob(
			proseStore,
			&recordingCarrier{verdict: pkg.DeliveryResolved},
			&fakeHeadlessDeliverer{},
			time.Hour,
			libtime.NewCurrentDateTime(),
		)
		proseStore.ReadBoardReturns(pkg.Items{
			answeredItem(verdictID, "operator", pkg.Decision("allow")),
		}, nil)
		Expect(proseJob.DeliverOnce(context.Background())).To(Succeed())

		menuStore := &mocks.AttentionStore{}
		menuStore.DeliveryReportsReturns(map[pkg.ItemID]*pkg.DeliveryReport{}, nil)
		menuStore.RecordAttemptReturns(&pkg.DeliveryAttempt{}, nil)
		menuJob := pkg.NewMenuAnswerJob(
			menuStore,
			&fakeLocator{verdict: pkg.DeliveryResolved, pane: 42},
			&fakeMenuWriter{unreadableAfter: -1},
			time.Hour,
			pkg.WithMenuAnswerTimings(0, 2),
			pkg.WithMenuAnswerMaxCycles(2),
		)
		menuItem := answeredMenuItem(verdictID, pkg.Questions{
			{Tab: "A", Options: pkg.AnswerOptions{{Label: "x"}}},
		})
		menuItem.Options = pkg.AnswerOptions{{Label: "x"}}
		menuItem.Answer = &pkg.Answer{Kind: pkg.OptionAnswerKind, Value: "x"}
		menuStore.ReadBoardReturns(pkg.Items{menuItem}, nil)
		Expect(menuJob.DeliverOnce(context.Background())).To(Succeed())

		Expect(proseStore.RecordAttemptCallCount()).To(Equal(1),
			"the prose arm must record the look")
		Expect(menuStore.RecordAttemptCallCount()).To(Equal(1),
			"the menu arm must record the look")
		_, _, proseCarrier, proseOutcome := proseStore.RecordAttemptArgsForCall(0)
		_, _, menuCarrier, menuOutcome := menuStore.RecordAttemptArgsForCall(0)

		Expect(proseCarrier).To(Equal(menuCarrier),
			"both arms must attribute the same verdict to the same carrier")
		Expect(proseOutcome).To(Equal(menuOutcome),
			"both arms must agree what `resolved` means")
		Expect(proseOutcome).To(Equal(pkg.HeldOutcome))
	})
})
