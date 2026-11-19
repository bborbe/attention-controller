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

// recordingCrossingSink records the crossings it was asked to raise.
type recordingCrossingSink struct {
	raised []string
}

func (s *recordingCrossingSink) Raise(
	_ context.Context,
	record pkg.ContextCrossingRecord,
) error {
	s.raised = append(s.raised, record.SessionID)
	return nil
}

var _ = Describe("ContextCrossingJob", func() {
	var ctx context.Context
	var dir string
	var bookPath string
	var sink *recordingCrossingSink

	writeState := func(name string, sessionID string, tokens int) {
		body := fmt.Sprintf(
			`{"session_id":%q,"session_name":"n","total_input_tokens":%d}`,
			sessionID,
			tokens,
		)
		Expect(os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600)).To(Succeed())
	}

	job := func(live bool, threshold int) pkg.ContextCrossingJob {
		return pkg.NewContextCrossingJob(
			dir,
			sink,
			&fixedLiveness{live: live},
			threshold,
			stdtime.Hour,
			bookPath,
		)
	}

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		dir, err = os.MkdirTemp("", "crossing-")
		Expect(err).NotTo(HaveOccurred())
		// The book lives in its own directory: a *.json file beside the readings
		// would be listed as one of them and decoded on every scan.
		bookPath = filepath.Join(dir, "state", "crossing-book.json")
		sink = &recordingCrossingSink{}
	})

	AfterEach(func() {
		_ = os.RemoveAll(dir)
	})

	It("raises a card for a live session over the threshold", func() {
		writeState("a.json", "sid-1", 600000)

		Expect(job(true, 500000).ScanOnce(ctx)).To(Succeed())

		Expect(sink.raised).To(Equal([]string{"sid-1"}))
	})

	It("raises nothing below the threshold", func() {
		writeState("a.json", "sid-1", 100000)

		Expect(job(true, 500000).ScanOnce(ctx)).To(Succeed())

		Expect(sink.raised).To(BeEmpty())
	})

	It("raises nothing for a session that is not live", func() {
		writeState("a.json", "sid-1", 600000)

		Expect(job(false, 500000).ScanOnce(ctx)).To(Succeed())

		Expect(sink.raised).To(BeEmpty())
	})

	It("treats a zero as no measurement, not as a context of no tokens", func() {
		writeState("a.json", "sid-1", 0)

		Expect(job(true, 0).ScanOnce(ctx)).To(Succeed())

		Expect(sink.raised).To(BeEmpty())
	})

	It("ignores a file carrying no session id", func() {
		Expect(os.WriteFile(
			filepath.Join(dir, "a.json"),
			[]byte(`{"total_input_tokens":600000}`),
			0o600,
		)).To(Succeed())

		Expect(job(true, 500000).ScanOnce(ctx)).To(Succeed())

		Expect(sink.raised).To(BeEmpty())
	})

	It("raises once for a session that stays over the threshold across ticks", func() {
		writeState("a.json", "sid-1", 600000)
		one := job(true, 500000)

		Expect(one.ScanOnce(ctx)).To(Succeed())
		Expect(one.ScanOnce(ctx)).To(Succeed())
		Expect(one.ScanOnce(ctx)).To(Succeed())

		Expect(sink.raised).To(Equal([]string{"sid-1"}))
	})

	It("re-arms after a real sub-threshold reading", func() {
		one := job(true, 500000)

		writeState("a.json", "sid-1", 600000)
		Expect(one.ScanOnce(ctx)).To(Succeed())
		writeState("a.json", "sid-1", 100000)
		Expect(one.ScanOnce(ctx)).To(Succeed())
		writeState("a.json", "sid-1", 600000)
		Expect(one.ScanOnce(ctx)).To(Succeed())

		Expect(sink.raised).To(Equal([]string{"sid-1", "sid-1"}))
	})

	It("does not re-arm on a zero reading", func() {
		one := job(true, 500000)

		writeState("a.json", "sid-1", 600000)
		Expect(one.ScanOnce(ctx)).To(Succeed())
		writeState("a.json", "sid-1", 0)
		Expect(one.ScanOnce(ctx)).To(Succeed())
		writeState("a.json", "sid-1", 600000)
		Expect(one.ScanOnce(ctx)).To(Succeed())

		Expect(sink.raised).To(Equal([]string{"sid-1"}))
	})

	It("does not re-raise after a restart over the same book", func() {
		writeState("a.json", "sid-1", 600000)

		Expect(job(true, 500000).ScanOnce(ctx)).To(Succeed())
		Expect(job(true, 500000).ScanOnce(ctx)).To(Succeed())

		Expect(sink.raised).To(Equal([]string{"sid-1"}))
	})

	It("re-arms a session that is no longer live", func() {
		writeState("a.json", "sid-1", 600000)
		liveness := &switchableLiveness{live: true}
		one := pkg.NewContextCrossingJob(
			dir,
			sink,
			liveness,
			500000,
			stdtime.Hour,
			bookPath,
		)

		Expect(one.ScanOnce(ctx)).To(Succeed())
		liveness.live = false
		Expect(one.ScanOnce(ctx)).To(Succeed())
		liveness.live = true
		Expect(one.ScanOnce(ctx)).To(Succeed())

		Expect(sink.raised).To(Equal([]string{"sid-1", "sid-1"}))
	})
})

// switchableLiveness flips between live and not, so a spec can drive a
// session's death and resume within one job.
type switchableLiveness struct {
	live bool
}

func (l *switchableLiveness) IsLive(_ context.Context, _ string) bool {
	return l.live
}

var _ = Describe("StoreContextCrossingSink", func() {
	var ctx context.Context
	var store *recordingIngestStore

	BeforeEach(func() {
		ctx = context.Background()
		store = &recordingIngestStore{}
	})

	It("posts the crossing card under the compact dedup key", func() {
		sink := pkg.NewStoreContextCrossingSink(store, nil)

		Expect(sink.Raise(ctx, pkg.ContextCrossingRecord{
			SessionID: "sid-1",
			Name:      "worker",
			Tokens:    600000,
		})).To(Succeed())

		Expect(store.requests).To(HaveLen(1))
		Expect(store.requests[0].DedupKey).To(Equal(pkg.DedupKey("compact:sid-1")))
		Expect(store.requests[0].ProvenanceClass).To(Equal(pkg.ProvenanceClass("explicit")))
		Expect(store.requests[0].AnswerMechanism).To(Equal(pkg.AnswerMechanism("ack")))
		Expect(string(store.requests[0].Payload)).To(ContainSubstring("600000"))
	})

	It("does not post a second card while the first is open", func() {
		store.board = pkg.Items{
			{ItemID: "store-1", DedupKey: pkg.DedupKey("compact:sid-1")},
		}
		sink := pkg.NewStoreContextCrossingSink(store, nil)

		Expect(sink.Raise(ctx, pkg.ContextCrossingRecord{
			SessionID: "sid-1",
			Tokens:    600000,
		})).To(Succeed())

		Expect(store.requests).To(BeEmpty())
	})
})
