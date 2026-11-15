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

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

var _ = Describe("EventPruneJob", func() {
	var ctx context.Context
	var stateDir string
	var now stdtime.Time

	// line builds one event-log line carrying a `ts` at the given offset from
	// now, in the same shape the hooks write.
	line := func(offset stdtime.Duration, id string) string {
		ts := now.Add(offset).Unix()
		return fmt.Sprintf(`{"ts":%d,"item_id":%q}`, ts, id)
	}

	writeLog := func(name string, lines ...string) string {
		path := filepath.Join(stateDir, name)
		Expect(os.WriteFile(path, []byte(joinLines(lines)), 0o600)).To(Succeed())
		return path
	}

	job := func(retain stdtime.Duration) pkg.EventPruneJob {
		clock := libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
			return libtime.DateTime(now)
		})
		return pkg.NewEventPruneJob(stateDir, retain, stdtime.Hour, clock)
	}

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		stateDir, err = os.MkdirTemp("", "event-prune-")
		Expect(err).NotTo(HaveOccurred())
		now = stdtime.Now()
	})

	AfterEach(func() {
		_ = os.RemoveAll(stateDir)
	})

	It("drops an aged line and keeps the fresh one", func() {
		path := writeLog(
			"s1.events.jsonl",
			line(-48*stdtime.Hour, "old"),
			line(-1*stdtime.Hour, "fresh"),
		)

		Expect(job(24 * stdtime.Hour).PruneOnce(ctx)).To(Succeed())

		content, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).NotTo(ContainSubstring(`"old"`))
		Expect(string(content)).To(ContainSubstring(`"fresh"`))
	})

	It("keeps a line carrying no usable timestamp", func() {
		path := writeLog(
			"s1.events.jsonl",
			`{"item_id":"no-ts"}`,
			line(-48*stdtime.Hour, "old"),
		)

		Expect(job(24 * stdtime.Hour).PruneOnce(ctx)).To(Succeed())

		content, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(ContainSubstring(`"no-ts"`))
		Expect(string(content)).NotTo(ContainSubstring(`"old"`))
	})

	It("keeps a torn line rather than guessing at its age", func() {
		path := writeLog(
			"s1.events.jsonl",
			`{"ts":`,
			line(-48*stdtime.Hour, "old"),
		)

		Expect(job(24 * stdtime.Hour).PruneOnce(ctx)).To(Succeed())

		content, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(ContainSubstring(`{"ts":`))
	})

	It("writes nothing when no line is aged", func() {
		path := writeLog("s1.events.jsonl", line(-1*stdtime.Hour, "fresh"))
		before, err := os.Stat(path)
		Expect(err).NotTo(HaveOccurred())

		Expect(job(24 * stdtime.Hour).PruneOnce(ctx)).To(Succeed())

		after, err := os.Stat(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(after.ModTime()).To(Equal(before.ModTime()))
		Expect(filepath.Join(stateDir, "s1.events.jsonl.tmp")).NotTo(BeAnExistingFile())
	})

	It("ignores files that are not event logs", func() {
		other := filepath.Join(stateDir, "cursors.json")
		Expect(os.WriteFile(other, []byte(`{"ts":1}`), 0o600)).To(Succeed())

		Expect(job(stdtime.Hour).PruneOnce(ctx)).To(Succeed())

		content, err := os.ReadFile(other)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(Equal(`{"ts":1}`))
	})

	It("is a no-op when the state directory is absent", func() {
		stateDir = filepath.Join(stateDir, "absent")

		Expect(job(stdtime.Hour).PruneOnce(ctx)).To(Succeed())
	})
})

// joinLines renders lines as a newline-terminated log body.
func joinLines(lines []string) string {
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	return body
}
