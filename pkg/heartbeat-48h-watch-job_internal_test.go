// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// stubHeartbeatJobLister answers from the spec's queue, repeating the last
// answer so a loop can run more cycles than the spec queued.
type stubHeartbeatJobLister struct {
	results [][]HeartbeatJob
	errs    []error
	calls   atomic.Int32
}

func (l *stubHeartbeatJobLister) List(_ context.Context) ([]HeartbeatJob, error) {
	index := int(l.calls.Add(1) - 1)
	if index < len(l.errs) && l.errs[index] != nil {
		return nil, l.errs[index]
	}
	if len(l.results) == 0 {
		return nil, nil
	}
	if index >= len(l.results) {
		return l.results[len(l.results)-1], nil
	}
	return l.results[index], nil
}

// heartbeat48hEmitter builds one successful emitter job at a slot time.
func heartbeat48hEmitter(name string, start time.Time) HeartbeatJob {
	return HeartbeatJob{Name: name, Start: start, Succeeded: 1}
}

// heartbeat48hEightSlots is eight successful emitter slots 6 h apart from start
// — a window that ran every slot it should have.
func heartbeat48hEightSlots(start time.Time) []HeartbeatJob {
	jobs := make([]HeartbeatJob, 0, heartbeat48hMinSuccesses)
	for slot := 0; slot < heartbeat48hMinSuccesses; slot++ {
		jobs = append(jobs, heartbeat48hEmitter(
			heartbeat48hJobNamePrefix+"slot-"+strconv.Itoa(slot),
			start.Add(time.Duration(slot)*6*time.Hour),
		))
	}
	return jobs
}

var _ = Describe("heartbeat 48 h watch job", func() {
	var ctx context.Context
	var stateDir string
	var store *recordingAttentionStore
	var lister *stubHeartbeatJobLister
	var capture *logCapture
	var job *heartbeat48hWatchJob
	var instant time.Time

	BeforeEach(func() {
		ctx = context.Background()
		stateDir = GinkgoT().TempDir()
		store = &recordingAttentionStore{
			heartbeatPath: filepath.Join(stateDir, heartbeat48hHeartbeatFileName),
		}
		lister = &stubHeartbeatJobLister{}
		capture = &logCapture{}
		instant = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

		originalInfo, originalError := heartbeat48hWatchInfof, heartbeat48hWatchErrorf
		heartbeat48hWatchInfof = capture.info
		heartbeat48hWatchErrorf = capture.error
		DeferCleanup(func() {
			heartbeat48hWatchInfof = originalInfo
			heartbeat48hWatchErrorf = originalError
		})

		job = &heartbeat48hWatchJob{
			jobLister: lister,
			store:     store,
			stateDir:  stateDir,
			interval:  time.Second,
			now: libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
				return libtime.DateTime(instant)
			}),
		}
	})

	readLedger := func() []string {
		content, err := os.ReadFile(filepath.Join(stateDir, heartbeat48hLedgerFileName))
		Expect(err).To(BeNil())
		return strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	}

	readResult := func() string {
		content, err := os.ReadFile(filepath.Join(stateDir, heartbeat48hResultFileName))
		Expect(err).To(BeNil())
		return string(content)
	}

	It("appends exactly one ledger line per cycle, and nothing else on a quiet one", func() {
		job.cycle(ctx)
		job.cycle(ctx)

		Expect(readLedger()).To(Equal([]string{
			"2026-10-20T12:00:00Z\tprogress\tfailed_total=0\tfailed_48h=0\tsuccesses_after=0" +
				"\twindow_start=-\tclose=-\tlast_success=-\t-",
			"2026-10-20T12:00:00Z\tprogress\tfailed_total=0\tfailed_48h=0\tsuccesses_after=0" +
				"\twindow_start=-\tclose=-\tlast_success=-\t-",
		}))
		_, found := capture.find("job=heartbeat-48h-watch action=scan status=progress")
		Expect(found).To(BeTrue())
		Expect(store.pushes).To(BeEmpty())
	})

	It("stays quiet once result.json exists", func() {
		Expect(os.WriteFile(
			filepath.Join(stateDir, heartbeat48hResultFileName),
			[]byte(`{"window_start":"2026-10-01T00:00:00Z"}`),
			0o600,
		)).To(Succeed())
		lister.results = [][]HeartbeatJob{heartbeat48hEightSlots(
			time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		)}

		job.cycle(ctx)

		_, found := capture.find("job=heartbeat-48h-watch action=scan status=done")
		Expect(found).To(BeTrue())
		Expect(store.pushes).To(BeEmpty())
		Expect(readLedger()[0]).To(HavePrefix("2026-10-20T12:00:00Z\tdone\t"))
	})

	It("posts nothing while no success has followed a failure inside 48 h", func() {
		lister.results = [][]HeartbeatJob{{
			{Name: heartbeat48hJobNamePrefix + "failed", Start: time.Date(
				2026, 10, 19, 12, 0, 0, 0, time.UTC,
			), Failed: 1},
		}}

		job.cycle(ctx)

		_, found := capture.find("job=heartbeat-48h-watch action=scan status=void-in-window")
		Expect(found).To(BeTrue())
		Expect(store.pushes).To(BeEmpty())
		Expect(readLedger()[0]).To(ContainSubstring(
			"\tvoid-in-window\tfailed_total=1\tfailed_48h=1\tsuccesses_after=0",
		))
	})

	It("counts a condition-only failure as a failure", func() {
		lister.results = [][]HeartbeatJob{{
			{
				Name:  heartbeat48hJobNamePrefix + "backoff",
				Start: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC),
				Conditions: []HeartbeatJobCondition{
					{Status: "True", Reason: heartbeat48hReasonBackoffLimitExceeded},
				},
			},
		}}

		job.cycle(ctx)

		Expect(store.pushes).To(BeEmpty())
		Expect(readLedger()[0]).To(ContainSubstring(
			"\tvoid-in-window\tfailed_total=1\tfailed_48h=1",
		))
	})

	It("posts one closed-window card, writes result.json and then stays quiet", func() {
		instant = time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
		lister.results = [][]HeartbeatJob{heartbeat48hEightSlots(
			time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		)}

		job.cycle(ctx)

		Expect(store.pushes).To(HaveLen(1))
		request := store.pushes[0]
		Expect(request.ProducerID).To(Equal(ProducerID("heartbeat-48h-watch")))
		Expect(request.DedupKey).To(Equal(DedupKey("heartbeat-48h-window-closed")))
		Expect(request.LivenessRef).To(Equal(
			LivenessRef("heartbeat:" + filepath.Join(stateDir, heartbeat48hHeartbeatFileName)),
		))
		Expect(request.Payload).To(HavePrefix("Sentry heartbeat 48 h window CLOSED CLEAN — 8 "))
		Expect(request.Payload).To(ContainSubstring(
			"window 2026-10-01T00:00:00Z -> 2026-10-03T00:00:00Z",
		))
		// The heartbeat is written BEFORE the post, so the card cannot arrive
		// already prunable.
		Expect(store.heartbeatAtPush).To(BeTrue())
		_, found := capture.find("job=heartbeat-48h-watch action=create card=item-1")
		Expect(found).To(BeTrue())
		Expect(readResult()).To(Equal(
			`{"window_start":"2026-10-01T00:00:00Z","window_close":"2026-10-03T00:00:00Z",` +
				`"successes":8,"failed_total":0}` + "\n",
		))
		Expect(readLedger()[0]).To(Equal(
			"2026-10-04T00:00:00Z\twindow-closed\tfailed_total=0\tfailed_48h=0" +
				"\tsuccesses_after=8\twindow_start=2026-10-01T00:00:00Z" +
				"\tclose=2026-10-03T00:00:00Z\tlast_success=2026-10-02T18:00:00Z\tsent",
		))

		// The second tick sees result.json and reports nothing.
		job.cycle(ctx)

		Expect(store.pushes).To(HaveLen(1))
		Expect(readLedger()).To(HaveLen(2))
		Expect(readLedger()[1]).To(HavePrefix("2026-10-04T00:00:00Z\tdone\t"))
	})

	It("posts one terminal card when the deadline passes", func() {
		instant = time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
		lister.results = [][]HeartbeatJob{{
			{Name: heartbeat48hJobNamePrefix + "failed", Start: time.Date(
				2026, 10, 26, 0, 0, 0, 0, time.UTC,
			), Failed: 1},
			heartbeat48hEmitter(
				heartbeat48hJobNamePrefix+"after",
				time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC),
			),
		}}

		job.cycle(ctx)

		Expect(store.pushes).To(HaveLen(1))
		Expect(store.pushes[0].DedupKey).To(Equal(DedupKey("heartbeat-48h-watch-terminal")))
		Expect(store.pushes[0].Payload).To(
			ContainSubstring("Sentry heartbeat 48 h watch TERMINAL at 2026-10-28T00:00:00Z"),
		)
		Expect(store.pushes[0].Payload).To(
			ContainSubstring("no clean 48 h window before 2026-10-27T00:00:00Z"),
		)
		Expect(readResult()).To(Equal(
			`{"terminal":"2026-10-28T00:00:00Z","successes":1,"failed_total":1}` + "\n",
		))
		Expect(readLedger()[0]).To(ContainSubstring("\tterminal\t"))
		Expect(readLedger()[0]).To(HaveSuffix("\tsent"))

		job.cycle(ctx)

		Expect(store.pushes).To(HaveLen(1))
	})

	It("writes one ERROR ledger line and posts nothing when the read returns nothing", func() {
		lister.errs = []error{stderrors.New(heartbeat48hEmptyReadMessage)}

		job.cycle(ctx)

		Expect(store.pushes).To(BeEmpty())
		Expect(readLedger()).To(Equal([]string{
			"2026-10-20T12:00:00Z\tERROR\tkubectl returned nothing",
		}))
		_, found := capture.find("job=heartbeat-48h-watch action=scan status=ERROR")
		Expect(found).To(BeTrue())
	})

	It("runs a cycle immediately when the loop starts and stops on cancellation", func() {
		done := make(chan error, 1)
		runCtx, cancel := context.WithCancel(context.Background())
		go func() {
			done <- NewHeartbeat48hWatchJob(
				lister,
				store,
				stateDir,
				10*time.Millisecond,
				libtime.NewCurrentDateTime(),
			)(runCtx)
		}()

		Eventually(func() bool {
			_, found := capture.find("job=heartbeat-48h-watch action=scan")
			return found
		}).Should(BeTrue())

		cancel()
		Expect(awaitJobDone(done)).To(BeNil())
	})
})

var _ = Describe("parseHeartbeatJobs", func() {
	It("keeps the emitter jobs and drops everything else", func() {
		jobs, err := parseHeartbeatJobs(context.Background(), []byte(`{
			"items": [
				{
					"metadata": {
						"name": "sentry-heartbeat-emitter-29000000",
						"creationTimestamp": "2026-10-01T00:00:00Z"
					},
					"status": {"startTime": "2026-10-01T00:00:05Z", "succeeded": 1}
				},
				{
					"metadata": {
						"name": "sentry-heartbeat-emitter-29000006",
						"creationTimestamp": "2026-10-01T06:00:00Z"
					},
					"status": {
						"failed": 2,
						"conditions": [
							{"status": "True", "reason": "BackoffLimitExceeded"},
							{"status": "False", "reason": "Ignored"}
						]
					}
				},
				{"metadata": {"name": "some-other-job"}, "status": {"succeeded": 1}}
			]
		}`))

		Expect(err).To(BeNil())
		Expect(jobs).To(HaveLen(2))
		Expect(jobs[0].Name).To(Equal("sentry-heartbeat-emitter-29000000"))
		Expect(jobs[0].Start).To(Equal(time.Date(2026, 10, 1, 0, 0, 5, 0, time.UTC)))
		Expect(jobs[0].Succeeded).To(Equal(1))
		// The second job carries no startTime, so the creation timestamp stands.
		Expect(jobs[1].Start).To(Equal(time.Date(2026, 10, 1, 6, 0, 0, 0, time.UTC)))
		Expect(jobs[1].Failed).To(Equal(2))
		Expect(heartbeat48hJobFailed(jobs[1])).To(BeTrue())
	})

	It("reports an undecodable read as an error", func() {
		_, err := parseHeartbeatJobs(context.Background(), []byte("not json"))

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("decode kubectl jobs failed"))
	})
})
