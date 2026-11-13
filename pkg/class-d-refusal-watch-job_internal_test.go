// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// recordingAttentionStore records every push and dedupes exactly as the real
// store does — on producer id plus dedup key while the item is open — so a spec
// can assert what the operator would end up seeing rather than how many calls
// the job made.
//
// ⚠️ It embeds the interface rather than the counterfeiter fake: this spec lives
// in package pkg, and importing mocks (which imports pkg) would be a cycle. The
// embedded interface is nil, so any method this type does not override panics —
// which is the intent: this job may only Push.
type recordingAttentionStore struct {
	AttentionStore
	pushes          []PushRequest
	items           []*Item
	heartbeatPath   string
	heartbeatAtPush bool
	nextID          int
}

func (s *recordingAttentionStore) Push(_ context.Context, request PushRequest) (*Item, error) {
	s.pushes = append(s.pushes, request)
	if _, err := os.Stat(s.heartbeatPath); err == nil {
		s.heartbeatAtPush = true
	}
	for _, item := range s.items {
		if item.ProducerID == request.ProducerID &&
			item.DedupKey == request.DedupKey &&
			item.State == OpenState {
			item.Payload = request.Payload
			return item, nil
		}
	}
	s.nextID++
	item := &Item{
		ItemID:      ItemID("item-" + strconv.Itoa(s.nextID)),
		ProducerID:  request.ProducerID,
		DedupKey:    request.DedupKey,
		LivenessRef: request.LivenessRef,
		Payload:     request.Payload,
		State:       OpenState,
	}
	s.items = append(s.items, item)
	return item, nil
}

// stubTranscriptTailer returns the queued batches in order, then quiet scans.
type stubTranscriptTailer struct {
	batches []TranscriptPhraseHits
	calls   int
}

func (t *stubTranscriptTailer) Scan(_ context.Context) (TranscriptPhraseHits, error) {
	if t.calls >= len(t.batches) {
		t.calls++
		return nil, nil
	}
	batch := t.batches[t.calls]
	t.calls++
	return batch, nil
}

// stubClauseProbe answers the clause question from the spec.
type stubClauseProbe struct {
	shipped bool
	path    string
	err     error
}

func (p *stubClauseProbe) Shipped(_ context.Context) (bool, string, error) {
	return p.shipped, p.path, p.err
}

var _ = Describe("class-D refusal watch job", func() {
	var ctx context.Context
	var stateDir string
	var store *recordingAttentionStore
	var tailer *stubTranscriptTailer
	var probe *stubClauseProbe
	var capture *logCapture
	var job *classDRefusalWatchJob

	BeforeEach(func() {
		ctx = context.Background()
		stateDir = GinkgoT().TempDir()
		store = &recordingAttentionStore{
			heartbeatPath: filepath.Join(stateDir, classDRefusalHeartbeatFileName),
		}
		tailer = &stubTranscriptTailer{}
		probe = &stubClauseProbe{shipped: true}
		capture = &logCapture{}

		originalInfo, originalError := classDRefusalWatchInfof, classDRefusalWatchErrorf
		classDRefusalWatchInfof = capture.info
		classDRefusalWatchErrorf = capture.error
		DeferCleanup(func() {
			classDRefusalWatchInfof = originalInfo
			classDRefusalWatchErrorf = originalError
		})

		job = &classDRefusalWatchJob{
			store:    store,
			tailer:   tailer,
			clause:   probe,
			stateDir: stateDir,
			interval: time.Second,
			now:      libtime.NewCurrentDateTime(),
		}
	})

	It("logs its scan line on a quiet cycle", func() {
		job.cycle(ctx)

		line, found := capture.find("job=class-d-refusal-watch action=scan")
		Expect(found).To(BeTrue())
		Expect(line).To(ContainSubstring("hits=0"))
		Expect(store.pushes).To(BeEmpty())
		Expect(store.items).To(BeEmpty())
	})

	It("posts one card carrying the exact producer, dedup key and liveness ref", func() {
		tailer.batches = []TranscriptPhraseHits{{
			{
				File:      "/tmp/session-a.jsonl",
				Session:   "session-a",
				Timestamp: "2026-10-05T10:00:05Z",
				Excerpt:   "That is not an answer",
			},
		}}

		job.cycle(ctx)

		Expect(store.pushes).To(HaveLen(1))
		request := store.pushes[0]
		Expect(request.ProducerID).To(Equal(ProducerID("class-d-refusal-watch")))
		Expect(request.DedupKey).To(Equal(DedupKey("class-d-refusal-hit")))
		Expect(request.LivenessRef).To(Equal(
			LivenessRef("heartbeat:" + filepath.Join(stateDir, classDRefusalHeartbeatFileName)),
		))
		Expect(request.Payload).To(HavePrefix("Class-D refusal observed — "))
		Expect(request.Payload).To(
			ContainSubstring("HIT session-a 2026-10-05T10:00:05Z :: That is not an answer"),
		)
		// The heartbeat is written BEFORE the post, so the card cannot arrive
		// already prunable.
		Expect(store.heartbeatAtPush).To(BeTrue())
		_, found := capture.find("job=class-d-refusal-watch action=create card=item-1")
		Expect(found).To(BeTrue())
		Expect(store.items).To(HaveLen(1))
	})

	It("appends the hit lines to the hits log", func() {
		tailer.batches = []TranscriptPhraseHits{{
			{
				File:      "/tmp/session-a.jsonl",
				Session:   "session-a",
				Timestamp: "2026-10-05T10:00:05Z",
				Excerpt:   "That is not an answer",
			},
		}}

		job.cycle(ctx)

		content, err := os.ReadFile(filepath.Join(stateDir, classDRefusalHitsFileName))
		Expect(err).To(BeNil())
		Expect(string(content)).To(
			ContainSubstring("HIT session-a 2026-10-05T10:00:05Z :: That is not an answer"),
		)
	})

	It("posts once when a later cycle reports a fresh hit under the same key", func() {
		tailer.batches = []TranscriptPhraseHits{
			{{
				File:      "/tmp/session-a.jsonl",
				Session:   "session-a",
				Timestamp: "2026-10-05T10:00:05Z",
				Excerpt:   "That is not an answer",
			}},
			{{
				File:      "/tmp/session-b.jsonl",
				Session:   "session-b",
				Timestamp: "2026-10-05T10:02:05Z",
				Excerpt:   "the pane needs your own keystroke",
			}},
		}

		job.cycle(ctx)
		job.cycle(ctx)

		// The job pushes both times; the store's dedup key is what keeps the
		// operator from seeing the same card twice.
		Expect(store.pushes).To(HaveLen(2))
		Expect(store.items).To(HaveLen(1))
		Expect(store.items[0].DedupKey).To(Equal(DedupKey("class-d-refusal-hit")))
	})

	It("posts the clause-gone card when the shipped clause is absent", func() {
		probe.shipped = false
		probe.path = "/cache/supervisor/1.2.3/commands/goal-loop.md"

		job.cycle(ctx)

		Expect(store.items).To(HaveLen(1))
		Expect(store.items[0].DedupKey).To(Equal(DedupKey("class-d-refusal-clause-gone")))
		Expect(store.items[0].Payload).To(
			ContainSubstring("is ABSENT from /cache/supervisor/1.2.3/commands/goal-loop.md"),
		)
	})

	It("prefers a hit over the clause-gone card", func() {
		probe.shipped = false
		tailer.batches = []TranscriptPhraseHits{{
			{
				File:      "/tmp/session-a.jsonl",
				Session:   "session-a",
				Timestamp: "2026-10-05T10:00:05Z",
				Excerpt:   "That is not an answer",
			},
		}}

		job.cycle(ctx)

		Expect(store.items).To(HaveLen(1))
		Expect(store.items[0].DedupKey).To(Equal(DedupKey("class-d-refusal-hit")))
	})

	It("skips the scan entirely when the heartbeat cannot be written", func() {
		// A regular file where the state directory should be makes MkdirAll fail.
		blocker := filepath.Join(GinkgoT().TempDir(), "blocker")
		Expect(os.WriteFile(blocker, []byte("x"), 0o600)).To(Succeed())
		job.stateDir = blocker

		job.cycle(ctx)

		_, scanned := capture.find("action=scan")
		Expect(scanned).To(BeFalse())
		_, errored := capture.find("action=heartbeat_error")
		Expect(errored).To(BeTrue())
		Expect(store.pushes).To(BeEmpty())
	})

	It("runs a cycle immediately when the loop starts and stops on cancellation", func() {
		done := make(chan error, 1)
		runCtx, cancel := context.WithCancel(context.Background())
		go func() {
			done <- NewClassDRefusalWatchJob(
				store,
				tailer,
				probe,
				stateDir,
				10*time.Millisecond,
				libtime.NewCurrentDateTime(),
			)(runCtx)
		}()

		Eventually(func() bool {
			_, found := capture.find("job=class-d-refusal-watch action=scan")
			return found
		}).Should(BeTrue())

		cancel()
		Expect(awaitJobDone(done)).To(BeNil())
	})
})

var _ = Describe("classDRefusalHitPayload", func() {
	It("keeps the first five lines, each bounded", func() {
		lines := []string{
			"HIT a 1 :: one",
			"HIT a 2 :: two",
			"HIT a 3 :: three",
			"HIT a 4 :: four",
			"HIT a 5 :: five",
			"HIT a 6 :: six",
		}

		payload := classDRefusalHitPayload(lines)

		Expect(payload).To(HavePrefix("Class-D refusal observed — "))
		Expect(payload).NotTo(ContainSubstring("six"))
		Expect(payload).To(ContainSubstring("five"))
	})
})
