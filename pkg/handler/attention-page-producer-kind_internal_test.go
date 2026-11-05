// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

var _ = Describe("filterByProducerKind and worker coverage", func() {
	row := func(id string, kind pkg.ProducerKind, uncovered bool) attentionPageRow {
		return attentionPageRow{
			Item:       pkg.Item{ItemID: pkg.ItemID(id), ProducerKind: kind},
			Provenance: pkg.Provenance{Uncovered: uncovered},
		}
	}
	ids := func(rows []attentionPageRow) []string {
		out := make([]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, string(r.Item.ItemID))
		}
		return out
	}

	It("shows an uncovered worker card in the manager-only default view", func() {
		rows := []attentionPageRow{
			row("manager-card", pkg.ManagerProducerKind, false),
			row("uncovered-worker", pkg.SessionProducerKind, true),
		}
		shown, hiddenOpen, hiddenAll := filterByProducerKind(rows, pkg.ManagerProducerKind, false)
		Expect(ids(shown)).To(Equal([]string{"manager-card", "uncovered-worker"}))
		Expect(hiddenOpen).To(Equal(0))
		Expect(hiddenAll).To(Equal(0))
	})

	It("still hides a covered worker card, so the fix is not a widened view", func() {
		rows := []attentionPageRow{
			row("covered-worker", pkg.SessionProducerKind, false),
			row("uncovered-worker", pkg.SessionProducerKind, true),
			row("cron-card", pkg.CronProducerKind, false),
		}
		shown, hiddenOpen, _ := filterByProducerKind(rows, pkg.ManagerProducerKind, false)
		Expect(ids(shown)).To(Equal([]string{"uncovered-worker"}))
		Expect(hiddenOpen).To(Equal(2))
	})

	It("leaves the all-kinds view untouched", func() {
		rows := []attentionPageRow{
			row("covered-worker", pkg.SessionProducerKind, false),
			row("uncovered-worker", pkg.SessionProducerKind, true),
		}
		shown, _, _ := filterByProducerKind(rows, "", true)
		Expect(ids(shown)).To(Equal([]string{"covered-worker", "uncovered-worker"}))
	})
})
