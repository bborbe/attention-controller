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

var _ = Describe("FinishedTaskFilter", func() {
	var ctx context.Context
	var askedAt stdtime.Time

	record := func(event string, detail string, ts *float64) pkg.EventIngestRecord {
		return pkg.EventIngestRecord{
			ItemID: "iid-1",
			Kind:   "permission",
			Open:   true,
			Event:  event,
			Detail: detail,
			TS:     ts,
		}
	}

	tsOf := func(t stdtime.Time) *float64 {
		value := float64(t.Unix())
		return &value
	}

	filter := func(tasks ...pkg.CompletedTask) pkg.EventIngestFilter {
		return pkg.NewFinishedTaskFilter(func(context.Context) []pkg.CompletedTask {
			return tasks
		})
	}

	BeforeEach(func() {
		ctx = context.Background()
		askedAt = stdtime.Now()
	})

	It("skips a Stop closer naming a task that finished before it was written", func() {
		f := filter(pkg.CompletedTask{
			Name:        "Port attention-watcher Into attention-controller",
			CompletedAt: askedAt.Add(-stdtime.Hour),
		})

		Expect(f.Skip(ctx, record(
			"Stop",
			"done with Port attention-watcher Into attention-controller",
			tsOf(askedAt),
		))).To(BeTrue())
	})

	It("keeps a Stop closer naming a task that finished after it was written", func() {
		f := filter(pkg.CompletedTask{
			Name:        "Port attention-watcher Into attention-controller",
			CompletedAt: askedAt.Add(stdtime.Hour),
		})

		Expect(f.Skip(ctx, record(
			"Stop",
			"Port attention-watcher Into attention-controller",
			tsOf(askedAt),
		))).To(BeFalse())
	})

	It("never skips an AskUserQuestion, even for a finished task", func() {
		f := filter(pkg.CompletedTask{
			Name:        "Some Task",
			CompletedAt: askedAt.Add(-stdtime.Hour),
		})

		Expect(f.Skip(ctx, record("AskUserQuestion", "Some Task", tsOf(askedAt)))).To(BeFalse())
	})

	It("keeps a record with no usable ask time", func() {
		f := filter(pkg.CompletedTask{
			Name:        "Some Task",
			CompletedAt: askedAt.Add(-stdtime.Hour),
		})

		Expect(f.Skip(ctx, record("Stop", "Some Task", nil))).To(BeFalse())
	})

	It("keeps a close record", func() {
		f := filter(pkg.CompletedTask{
			Name:        "Some Task",
			CompletedAt: askedAt.Add(-stdtime.Hour),
		})
		rec := record("Stop", "Some Task", tsOf(askedAt))
		rec.Open = false

		Expect(f.Skip(ctx, rec)).To(BeFalse())
	})

	It("keeps a record whose detail names no finished task", func() {
		f := filter(pkg.CompletedTask{
			Name:        "Some Task",
			CompletedAt: askedAt.Add(-stdtime.Hour),
		})

		Expect(f.Skip(ctx, record("Stop", "something else entirely", tsOf(askedAt)))).To(BeFalse())
	})
})
