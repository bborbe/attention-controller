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
	"github.com/bborbe/validation"
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

	// forwardRecord renders one event-log line carrying the hook's `forward`
	// flag. A nil value omits the key entirely — the shape of every record
	// written before the flag existed.
	forwardRecord := func(id string, open bool, forward *bool) string {
		openField := "close"
		if open {
			openField = "open"
		}
		forwardField := ""
		if forward != nil {
			forwardField = fmt.Sprintf(`,"forward":%t`, *forward)
		}
		return fmt.Sprintf(
			`{"item_id":%q,"session_id":"s1","type":%q,"ts":100,"kind":"question"%s}`,
			id,
			openField,
			forwardField,
		)
	}

	boolPtr := func(v bool) *bool { return &v }

	writeLog := func(name string, lines ...string) {
		path := filepath.Join(stateDir, name)
		Expect(os.WriteFile(path, []byte(joinLines(lines)), 0o600)).To(Succeed())
	}

	job := func() pkg.EventIngestJob {
		return pkg.NewEventIngestJob(stateDir, sink, nil, stdtime.Hour)
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

	// The durable delivery mark. The store prunes an open item at its deadline, so
	// without this line a delivered gate reads as lost once the item is gone. It is
	// written ONLY after a push returns nil, which is why the paired control below
	// matters as much as this spec.
	It("records a durable delivery mark for a forwarded open", func() {
		writeLog("s1.events.jsonl", record("a", "permission", true, 100))

		Expect(job().IngestOnce(ctx)).To(Succeed())

		data, err := os.ReadFile(filepath.Join(stateDir, "forwarded.jsonl"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(ContainSubstring(`"item":"a"`))
	})

	// The paired control: a push that failed must leave no delivery mark, or the
	// check would treat a never-delivered gate as delivered. The mark appears only
	// once the retried push succeeds, which is what makes it delivery evidence and
	// not intent.
	It("records no delivery mark when the push fails, and one after it succeeds", func() {
		writeLog("s1.events.jsonl", record("a", "permission", true, 100))
		sink.err = fmt.Errorf("store down")

		Expect(job().IngestOnce(ctx)).To(Succeed())
		_, err := os.Stat(filepath.Join(stateDir, "forwarded.jsonl"))
		Expect(os.IsNotExist(err)).To(BeTrue())

		sink.err = nil
		Expect(job().IngestOnce(ctx)).To(Succeed())
		data, err := os.ReadFile(filepath.Join(stateDir, "forwarded.jsonl"))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(ContainSubstring(`"item":"a"`))
	})

	// A non-gate kind is consumed but never forwarded, so it earns no mark.
	It("records no delivery mark for a non-gate kind", func() {
		writeLog("s1.events.jsonl", record("a", "idle", true, 100))

		Expect(job().IngestOnce(ctx)).To(Succeed())

		_, err := os.Stat(filepath.Join(stateDir, "forwarded.jsonl"))
		Expect(os.IsNotExist(err)).To(BeTrue())
	})

	It("forwards a close record", func() {
		writeLog("s1.events.jsonl", record("a", "permission", false, 100))

		Expect(job().IngestOnce(ctx)).To(Succeed())

		Expect(sink.closed).To(Equal([]string{"a"}))
	})

	It("does not close a record the hook marked forward: false", func() {
		// A second record that MUST forward rides in the same log, so a pass
		// that silently decoded nothing cannot satisfy this spec.
		writeLog(
			"s1.events.jsonl",
			forwardRecord("a", false, boolPtr(false)),
			record("b", "permission", true, 101),
		)

		Expect(job().IngestOnce(ctx)).To(Succeed())

		Expect(sink.closed).To(BeEmpty())
		Expect(sink.opened).To(Equal([]string{"b"}))
	})

	It("closes a record carrying forward: true", func() {
		writeLog("s1.events.jsonl", forwardRecord("a", false, boolPtr(true)))

		Expect(job().IngestOnce(ctx)).To(Succeed())

		Expect(sink.closed).To(Equal([]string{"a"}))
	})

	It("closes a record with no forward key", func() {
		// Every record written before the flag existed omits the key. The
		// decoder defaults it to true, so the filter must not drop them.
		writeLog("s1.events.jsonl", forwardRecord("a", false, nil))

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
		// A transient failure is not a quarantine: nothing was skipped, so no
		// durable skip record may exist.
		_, statErr := os.Stat(filepath.Join(stateDir, "attention-ingest-quarantine.json"))
		Expect(statErr).To(HaveOccurred())

		// The cursor did not advance past the failed record, so the next pass
		// forwards it rather than skipping it.
		sink.err = nil
		Expect(job().IngestOnce(ctx)).To(Succeed())
		Expect(sink.opened).To(Equal([]string{"a"}))
	})

	It("quarantines a record the sink rejects permanently, naming the store's reason", func() {
		writeLog("s1.events.jsonl", record("a", "permission", true, 100))
		// The store's own cardinality rejection, exactly as it arrives: the
		// quarantine reason must be read from this error, not a literal.
		sink.err = errors.Wrap(
			ctx,
			validation.Error,
			"validate Recommended failed: 3 options are marked recommended, at most 1 is allowed",
		)

		Expect(job().IngestOnce(ctx)).To(Succeed())
		Expect(sink.opened).To(BeEmpty())

		content, readErr := os.ReadFile(
			filepath.Join(stateDir, "attention-ingest-quarantine.json"),
		)
		Expect(readErr).To(BeNil())
		Expect(string(content)).To(ContainSubstring(`"item_id": "a"`))
		Expect(string(content)).To(ContainSubstring("3 options are marked recommended"))
		Expect(string(content)).To(ContainSubstring("validate Recommended failed"))

		// The cursor advanced past the record, so the next pass does not retry
		// it — the durable record is what replaces the retry loop.
		sink.err = nil
		Expect(job().IngestOnce(ctx)).To(Succeed())
		Expect(sink.opened).To(BeEmpty())
	})
})
