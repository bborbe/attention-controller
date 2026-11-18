// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	stdtime "time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// These specs pin the prune pass's budget, which the external specs cannot
// reach: the budget and the per-file seam are unexported, so a spec can drive a
// cut deterministically instead of racing a real file read.
//
// ⚠️ The cut spec is the discriminating half of SC3. A spec that only asserted
// eventPrunePassBudget's value would pass on a constant wired to nothing; this
// one makes the budget fire mid-pass and requires the pass to make no
// completed-pass claim.
var _ = Describe("EventPruneJob pass budget", func() {
	var ctx context.Context
	var stateDir string
	var now stdtime.Time

	line := func(offset stdtime.Duration, id string) string {
		ts := now.Add(offset).Unix()
		return fmt.Sprintf(`{"ts":%d,"item_id":%q}`, ts, id)
	}

	writeLog := func(name string, lines ...string) string {
		path := filepath.Join(stateDir, name)
		body := ""
		for _, l := range lines {
			body += l + "\n"
		}
		Expect(os.WriteFile(path, []byte(body), 0o600)).To(Succeed())
		return path
	}

	readLog := func(name string) string {
		raw, err := os.ReadFile(filepath.Join(stateDir, name))
		Expect(err).NotTo(HaveOccurred())
		return string(raw)
	}

	job := func(budget stdtime.Duration) *eventPruneJob {
		clock := libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
			return libtime.DateTime(now)
		})
		return &eventPruneJob{
			stateDir:   stateDir,
			retain:     24 * stdtime.Hour,
			interval:   stdtime.Hour,
			now:        clock,
			passBudget: budget,
		}
	}

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		stateDir, err = os.MkdirTemp("", "event-prune-budget-")
		Expect(err).NotTo(HaveOccurred())
		now = stdtime.Now()
	})

	AfterEach(func() {
		_ = os.RemoveAll(stateDir)
	})

	It("makes no completed-pass claim when the budget cuts the pass", func() {
		// Two logs, so the cut is observed at the TOP of the loop for the second
		// file — the exact check the completed-pass summary sits after. One file
		// would let the loop end and emit the summary even after a mid-file cut.
		writeLog("a.events.jsonl", line(-48*stdtime.Hour, "aged-a"))
		writeLog("b.events.jsonl", line(-48*stdtime.Hour, "aged-b"))

		original := pruneOneFile
		DeferCleanup(func() { pruneOneFile = original })
		pruneOneFile = func(
			_ *eventPruneJob,
			ctx context.Context,
			_ *os.Root,
			_ string,
			_ stdtime.Time,
		) (int, error) {
			// Block until the pass budget fires, then report no work done. The
			// next loop iteration observes ctx.Err() and returns before the
			// summary.
			<-ctx.Done()
			return 0, nil
		}

		completed, err := job(50 * stdtime.Millisecond).prunePassUnderBudget(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(completed).To(BeFalse(),
			"a budget-cut pass must not claim a completed pass")
		// Nothing was written, so the aged lines survive for the next tick.
		Expect(readLog("a.events.jsonl")).To(ContainSubstring("aged-a"))
		Expect(readLog("b.events.jsonl")).To(ContainSubstring("aged-b"))
	})

	It("completes and drops aged lines when the budget is not exhausted", func() {
		// The positive control: without it the cut spec could pass vacuously on a
		// pass that never completes for some unrelated reason.
		writeLog("a.events.jsonl", line(-48*stdtime.Hour, "aged-a"))
		writeLog("b.events.jsonl", line(-1*stdtime.Hour, "fresh-b"))

		completed, err := job(eventPrunePassBudget).prunePassUnderBudget(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(completed).To(BeTrue(), "an ordinary pass must complete")
		Expect(readLog("a.events.jsonl")).NotTo(ContainSubstring("aged-a"),
			"the aged line must be dropped")
		Expect(readLog("b.events.jsonl")).To(ContainSubstring("fresh-b"),
			"the fresh line must be kept")
	})

	It("keeps the pass budget at or under a tenth of the prune interval", func() {
		Expect(eventPrunePassBudget).To(BeNumerically(">", 0))
		Expect(eventPrunePassBudget).To(BeNumerically("<=", EventPruneInterval/10))
	})

	It("rotates the pass so consecutive passes cover every log", func() {
		// ⚠️ This is the rotation half of SC3. A budget sized above a whole pass is
		// impossible at this corpus size (see eventPrunePassBudget), so each pass
		// covers one slice and the cursor walks the list: full coverage arrives
		// across passes. Without the cursor a cut pass would stop at the same point
		// every round and the tail would never be pruned — unbounded growth that
		// looks bounded.
		const total = 24
		for i := 0; i < total; i++ {
			writeLog(
				fmt.Sprintf("log%02d.events.jsonl", i),
				line(-48*stdtime.Hour, fmt.Sprintf("aged-%02d", i)),
			)
		}
		j := job(eventPrunePassBudget)
		// One twelfth of 24 would be floored to minSliceSize, which sweeps every
		// log in one pass; the override is what makes rotation observable here.
		j.sliceOverride = 2

		seen := map[string]struct{}{}
		for pass := 0; pass < eventPruneSliceDivisor; pass++ {
			completed, err := j.prunePassUnderBudget(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(completed).To(BeTrue(), "each slice pass must complete")
			if cursor := j.cursor(); cursor != "" {
				seen[cursor] = struct{}{}
			}
		}

		Expect(j.cursor()).To(BeEmpty(),
			"one divisor's worth of passes must complete the cycle and return to the top")
		Expect(seen).To(HaveLen(eventPruneSliceDivisor-1),
			"every pass must advance the cursor to a new position")

		// Every aged line is gone: that is what "full coverage across passes"
		// means, and it cannot hold if the rotation skipped a slice.
		for i := 0; i < total; i++ {
			Expect(readLog(fmt.Sprintf("log%02d.events.jsonl", i))).
				NotTo(ContainSubstring(fmt.Sprintf("aged-%02d", i)),
					"log %d must have been pruned by some pass in the cycle", i)
		}
	})
})
