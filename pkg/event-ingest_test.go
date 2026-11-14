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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

// recordingSink is a scripted EventIngestSink that records what it was asked to
// forward and can fail on demand.
type recordingSink struct {
	opened []string
	closed []string
	err    error
}

func (s *recordingSink) Open(_ context.Context, record pkg.EventIngestRecord) error {
	if s.err != nil {
		return s.err
	}
	s.opened = append(s.opened, record.ItemID)
	return nil
}

func (s *recordingSink) Close(_ context.Context, record pkg.EventIngestRecord) error {
	if s.err != nil {
		return s.err
	}
	s.closed = append(s.closed, record.ItemID)
	return nil
}

var _ = Describe("EventIngestJob", func() {
	var ctx context.Context
	var stateDir string
	var sink *recordingSink

	// record renders one event-log line.
	record := func(id string, kind string, open bool, ts int64) string {
		kindField := ""
		if kind != "" {
			kindField = fmt.Sprintf(`,"kind":%q`, kind)
		}
		openField := "close"
		if open {
			openField = "open"
		}
		return fmt.Sprintf(
			`{"item_id":%q,"session_id":"s1","type":%q,"ts":%d%s}`,
			id,
			openField,
			ts,
			kindField,
		)
	}

	writeLog := func(name string, lines ...string) {
		path := filepath.Join(stateDir, name)
		Expect(os.WriteFile(path, []byte(joinLines(lines)), 0o600)).To(Succeed())
	}

	job := func() pkg.EventIngestJob {
		return pkg.NewEventIngestJob(stateDir, sink, stdtime.Hour)
	}

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		stateDir, err = os.MkdirTemp("", "event-ingest-")
		Expect(err).NotTo(HaveOccurred())
		sink = &recordingSink{}
	})

	AfterEach(func() {
		_ = os.RemoveAll(stateDir)
	})

	It("forwards an open permission record", func() {
		writeLog("s1.events.jsonl", record("a", "permission", true, 100))

		Expect(job().IngestOnce(ctx)).To(Succeed())

		Expect(sink.opened).To(Equal([]string{"a"}))
		Expect(sink.closed).To(BeEmpty())
	})

	It("forwards a close record", func() {
		writeLog("s1.events.jsonl", record("a", "permission", false, 100))

		Expect(job().IngestOnce(ctx)).To(Succeed())

		Expect(sink.closed).To(Equal([]string{"a"}))
	})

	It("consumes a non-gate kind without forwarding it", func() {
		writeLog("s1.events.jsonl", record("a", "idle", true, 100))

		Expect(job().IngestOnce(ctx)).To(Succeed())

		Expect(sink.opened).To(BeEmpty())
		Expect(sink.closed).To(BeEmpty())
	})

	It("does not forward a record twice", func() {
		writeLog("s1.events.jsonl", record("a", "permission", true, 100))
		Expect(job().IngestOnce(ctx)).To(Succeed())

		Expect(job().IngestOnce(ctx)).To(Succeed())

		Expect(sink.opened).To(Equal([]string{"a"}))
	})

	It("leaves a torn final line for the next pass", func() {
		path := filepath.Join(stateDir, "s1.events.jsonl")
		torn := record("a", "permission", true, 100) + "\n" + `{"item_id":"b","ty`
		Expect(os.WriteFile(path, []byte(torn), 0o600)).To(Succeed())

		Expect(job().IngestOnce(ctx)).To(Succeed())
		Expect(sink.opened).To(Equal([]string{"a"}))

		// The hook completes the line; the next pass picks it up.
		complete := record("a", "permission", true, 100) + "\n" +
			`{"item_id":"b","session_id":"s1","type":"open","kind":"question","ts":101}` + "\n"
		Expect(os.WriteFile(path, []byte(complete), 0o600)).To(Succeed())
		Expect(job().IngestOnce(ctx)).To(Succeed())

		Expect(sink.opened).To(Equal([]string{"a", "b"}))
	})

	It("skips a settled line when the file was truncated under the cursor", func() {
		writeLog("s1.events.jsonl", record("a", "permission", true, 100))
		Expect(job().IngestOnce(ctx)).To(Succeed())
		Expect(sink.opened).To(Equal([]string{"a"}))

		// The log is pruned back below the cursor, so the next pass re-reads
		// from 0. The settled-ts mark is what stops a second forward.
		writeLog("s1.events.jsonl", record("a", "permission", true, 100))
		Expect(job().IngestOnce(ctx)).To(Succeed())

		Expect(sink.opened).To(Equal([]string{"a"}))
	})

	It("retries the record when the sink fails", func() {
		writeLog("s1.events.jsonl", record("a", "permission", true, 100))
		sink.err = fmt.Errorf("store down")

		Expect(job().IngestOnce(ctx)).To(Succeed())
		Expect(sink.opened).To(BeEmpty())

		// The cursor did not advance past the failed record, so the next pass
		// forwards it rather than skipping it.
		sink.err = nil
		Expect(job().IngestOnce(ctx)).To(Succeed())
		Expect(sink.opened).To(Equal([]string{"a"}))
	})
})
