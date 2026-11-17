// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// These specs pin the reconcile walk's budget, which the external specs cannot
// reach: the budget is an unexported field so a spec can drive a cut without
// waiting out the real thirty seconds.
//
// ⚠️ The cut spec is the discriminating half of SC3. A spec that only asserted
// taskIndexReconcileBudget's value would pass on a constant wired to nothing;
// this one makes the budget fire mid-walk and requires the previous index to
// survive it.
var _ = Describe("TaskIndex reconcile budget", func() {
	var ctx context.Context
	var vault string
	var tasksDir string
	var clock libtime.CurrentDateTime

	writeTask := func(name, sessionID string) {
		Expect(os.WriteFile(
			filepath.Join(tasksDir, name),
			[]byte(fmt.Sprintf("---\nclaude_session_id: %s\n---\n", sessionID)),
			0o600,
		)).To(Succeed())
	}

	BeforeEach(func() {
		ctx = context.Background()
		clock = libtime.NewCurrentDateTime()
		clock.SetNow(clock.Now())

		vault = GinkgoT().TempDir()
		tasksDir = filepath.Join(vault, "25 Tasks")
		Expect(os.MkdirAll(tasksDir, 0o750)).To(Succeed())
	})

	It("keeps the previous index when the budget cancels the walk", func() {
		writeTask("Anchored.md", "session-anchored")
		index, isIndex := NewTaskIndex(ctx, vault, clock).(*taskIndex)
		Expect(isIndex).To(BeTrue(), "NewTaskIndex must return the concrete index")
		_, ok := index.Lookup("session-anchored")
		Expect(ok).To(BeTrue())

		// An unbounded reconcile would install this file. The tiny budget is the
		// only thing that can prevent it, which is what makes this spec fail when
		// the budget stops being applied.
		writeTask("Added.md", "session-added")
		index.reconcileBudget = time.Nanosecond
		index.Reconcile(ctx)

		_, added := index.Lookup("session-added")
		Expect(added).To(BeFalse(),
			"a budget-cancelled walk must not install a partial index")
		task, ok := index.Lookup("session-anchored")
		Expect(ok).To(BeTrue(), "the previous index must keep serving")
		Expect(task.Name).To(Equal("Anchored"))
	})

	It("installs the new index when the budget is not exhausted", func() {
		// The positive control: without it the cut spec could pass vacuously on a
		// walk that never installs anything at all.
		writeTask("Anchored.md", "session-anchored")
		index, isIndex := NewTaskIndex(ctx, vault, clock).(*taskIndex)
		Expect(isIndex).To(BeTrue(), "NewTaskIndex must return the concrete index")
		writeTask("Added.md", "session-added")

		index.Reconcile(ctx)

		task, ok := index.Lookup("session-added")
		Expect(ok).To(BeTrue())
		Expect(task.Name).To(Equal("Added"))
	})

	It("keeps the walk budget at or under a tenth of the backstop window", func() {
		// The derivation the task pins: at most 10 % of the interval the walk
		// cannot overrun, so a budget is never merely "shorter than" the interval.
		Expect(taskIndexReconcileBudget).To(BeNumerically(">", 0))
		Expect(taskIndexReconcileBudget).
			To(BeNumerically("<=", time.Duration(taskIndexBackstopWindow)/10))
	})

	It("rotates the walk so consecutive passes cover the whole corpus", func() {
		// ⚠️ This is the rotation half of SC3. A budget sized above a whole walk is
		// impossible at this corpus size (see taskIndexReconcileBudget), so each
		// pass covers one slice and the cursor must walk the list: full coverage
		// arrives across passes, never within one. Without the cursor a cut pass
		// would stop at the same point every round and the tail would never be
		// re-stamped — a permanent partial scan that looks bounded.
		const total = 24
		for i := 0; i < total; i++ {
			writeTask(fmt.Sprintf("Task%02d.md", i), fmt.Sprintf("session-%02d", i))
		}
		index, isIndex := NewTaskIndex(ctx, vault, clock).(*taskIndex)
		Expect(isIndex).To(BeTrue(), "NewTaskIndex must return the concrete index")
		Expect(index.cursor()).To(BeEmpty(), "a fresh index starts at the top")
		// One twelfth of 24 would be floored to minSliceSize, which sweeps the
		// whole corpus in one pass; the override is what makes rotation observable
		// on a corpus this small.
		index.sliceOverride = 2

		seen := map[string]struct{}{}
		for pass := 0; pass < taskIndexReconcileSliceDivisor; pass++ {
			index.Reconcile(ctx)
			if cursor := index.cursor(); cursor != "" {
				seen[cursor] = struct{}{}
			}
		}

		Expect(index.cursor()).To(BeEmpty(),
			"one divisor's worth of passes must complete the cycle and return to the top")
		Expect(seen).To(HaveLen(taskIndexReconcileSliceDivisor-1),
			"every pass must advance the cursor to a new position")

		// A rotation must never drop an entry it did not re-stamp: the carried
		// entries keep serving.
		for i := 0; i < total; i++ {
			_, ok := index.Lookup(fmt.Sprintf("session-%02d", i))
			Expect(ok).To(BeTrue(), "task %d must survive the rotation", i)
		}
	})

	It("drops a deleted task file even though no slice ever reaches it", func() {
		// ⚠️ Carry-forward is what makes a cut pass safe, and it is exactly what
		// would keep a deleted file in the index forever: a deleted path appears in
		// no slice (slices are built from the listing), so no pass could ever
		// remove it. The listing is what proves it gone, and this spec pins that the
		// carry-forward consults it.
		writeTask("Gone.md", "session-gone")
		writeTask("Stays.md", "session-stays")
		index, isIndex := NewTaskIndex(ctx, vault, clock).(*taskIndex)
		Expect(isIndex).To(BeTrue(), "NewTaskIndex must return the concrete index")
		_, ok := index.Lookup("session-gone")
		Expect(ok).To(BeTrue())

		Expect(os.Remove(filepath.Join(tasksDir, "Gone.md"))).To(Succeed())
		for pass := 0; pass < taskIndexReconcileSliceDivisor; pass++ {
			index.Reconcile(ctx)
		}

		_, gone := index.Lookup("session-gone")
		Expect(gone).To(BeFalse(), "a deleted task file must not survive in the index")
		_, stays := index.Lookup("session-stays")
		Expect(stays).To(BeTrue(), "the surviving file must still be served")
	})
})
