// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	stdtime "time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

var _ = Describe("CompletedTaskSource", func() {
	// The fixture is committed under pkg/testdata/ and is read from disk on every
	// pass. A source spec that built its own files in a temporary directory would
	// prove nothing about the vault shapes this reads: the date-only form, the
	// terminal-but-undated file and the live task are the fixture's whole point.
	const fixture = "testdata/completed-task-source"

	var ctx context.Context
	var tasks []pkg.CompletedTask

	names := func(tasks []pkg.CompletedTask) map[string]pkg.CompletedTask {
		byName := make(map[string]pkg.CompletedTask, len(tasks))
		for _, task := range tasks {
			byName[task.Name] = task
		}
		return byName
	}

	record := func(detail string, ts stdtime.Time) pkg.EventIngestRecord {
		value := float64(ts.Unix())
		return pkg.EventIngestRecord{
			ItemID: "iid-1",
			Kind:   "permission",
			Open:   true,
			Event:  "Stop",
			Detail: detail,
			TS:     &value,
		}
	}

	filter := func() pkg.EventIngestFilter {
		return pkg.NewFinishedTaskFilter(func(context.Context) []pkg.CompletedTask {
			return tasks
		})
	}

	BeforeEach(func() {
		ctx = context.Background()
		tasks = pkg.NewCompletedTaskSource(fixture)(ctx)
	})

	It("treats a completed and an aborted task as finished and an in_progress one as not", func() {
		byName := names(tasks)

		Expect(byName).To(HaveKey("Completed Alpha"))
		Expect(byName).To(HaveKey("Aborted Beta"))
		Expect(byName).NotTo(HaveKey("In Progress Gamma"))
		// Terminal but undated: no completion moment, so it may suppress nothing.
		Expect(byName).NotTo(HaveKey("Undated Delta"))
		// The instant form must resolve exactly, not merely appear — it is the shape
		// almost every real completion date uses.
		Expect(byName["Aborted Beta"].CompletedAt.Equal(
			stdtime.Date(2026, 10, 17, 17, 0, 0, 0, stdtime.UTC),
		)).To(BeTrue())
	})

	It("resolves a date-only completion to end of day, not start of day", func() {
		alpha, ok := names(tasks)["Completed Alpha"]
		Expect(ok).To(BeTrue())

		Expect(alpha.CompletedAt.Year()).To(Equal(2026))
		Expect(alpha.CompletedAt.Month()).To(Equal(stdtime.October))
		Expect(alpha.CompletedAt.Day()).To(Equal(18))
		Expect(alpha.CompletedAt.Hour()).To(Equal(23))
		Expect(alpha.CompletedAt.Minute()).To(Equal(59))
		Expect(alpha.CompletedAt.Second()).To(Equal(59))

		// The fixture's date is chosen so the two readings disagree at midday: a
		// start-of-day implementation has the task finished before the ask and
		// suppresses, end-of-day delivers. This half is what makes the value above
		// discriminating rather than merely present.
		noon := stdtime.Date(2026, 10, 18, 12, 0, 0, 0, stdtime.Local)
		Expect(filter().Skip(ctx, record("done with [[Completed Alpha]]", noon))).To(BeFalse())

		after := stdtime.Date(2026, 10, 19, 0, 0, 0, 0, stdtime.Local)
		Expect(filter().Skip(ctx, record("done with [[Completed Alpha]]", after))).To(BeTrue())
	})

	It("parses a wikilink to the task name and ignores surrounding prose", func() {
		askedAt := stdtime.Date(2026, 10, 19, 0, 0, 0, 0, stdtime.Local)

		// The link resolves to the bare task name, so the surrounding prose neither
		// helps nor blocks the match.
		Expect(filter().Skip(ctx, record(
			"All done — see [[Completed Alpha]] for the details.",
			askedAt,
		))).To(BeTrue())

		// A bare mention is not a link: the name appears, but nothing names a task,
		// so the closer is delivered.
		Expect(filter().Skip(ctx, record(
			"All done with Completed Alpha for the details.",
			askedAt,
		))).To(BeFalse())
	})
})
