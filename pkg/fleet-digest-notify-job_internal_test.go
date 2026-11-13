// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// stubFleetDigestChecker answers from the spec's queue, repeating the last
// result so a loop can run more cycles than the spec queued.
type stubFleetDigestChecker struct {
	results []FleetDigestCheckResult
	errs    []error
	calls   atomic.Int32
}

func (c *stubFleetDigestChecker) Check(_ context.Context) (FleetDigestCheckResult, error) {
	index := int(c.calls.Add(1) - 1)
	if index < len(c.errs) && c.errs[index] != nil {
		return FleetDigestCheckResult{}, c.errs[index]
	}
	if len(c.results) == 0 {
		return FleetDigestCheckResult{}, nil
	}
	if index >= len(c.results) {
		return c.results[len(c.results)-1], nil
	}
	return c.results[index], nil
}

// stubFleetDigestNotifier records every message and fails the calls the spec
// marks, so a spec can assert what the operator would have received.
type stubFleetDigestNotifier struct {
	messages []string
	failures []error
}

func (n *stubFleetDigestNotifier) Publish(_ context.Context, message string) error {
	index := len(n.messages)
	n.messages = append(n.messages, message)
	if index < len(n.failures) {
		return n.failures[index]
	}
	return nil
}

var _ = Describe("fleet digest notify job", func() {
	var ctx context.Context
	var stateDir string
	var check *stubFleetDigestChecker
	var notifier *stubFleetDigestNotifier
	var capture *logCapture
	var job *fleetDigestNotifyJob

	// fleetDigestInstant is the fixed clock every spec runs on, so the ledger and
	// the alert file are asserted byte-for-byte rather than by shape.
	var fleetDigestInstant = time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)

	BeforeEach(func() {
		ctx = context.Background()
		stateDir = GinkgoT().TempDir()
		check = &stubFleetDigestChecker{}
		notifier = &stubFleetDigestNotifier{}
		capture = &logCapture{}

		originalInfo, originalError := fleetDigestNotifyInfof, fleetDigestNotifyErrorf
		fleetDigestNotifyInfof = capture.info
		fleetDigestNotifyErrorf = capture.error
		DeferCleanup(func() {
			fleetDigestNotifyInfof = originalInfo
			fleetDigestNotifyErrorf = originalError
		})

		job = &fleetDigestNotifyJob{
			check:    check,
			notifier: notifier,
			stateDir: stateDir,
			interval: time.Second,
			now: libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
				return libtime.DateTime(fleetDigestInstant)
			}),
		}
	})

	readLedger := func() []string {
		content, err := os.ReadFile(filepath.Join(stateDir, fleetDigestLedgerFileName))
		Expect(err).To(BeNil())
		return strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	}

	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(stateDir, name))
		return err == nil
	}

	It("is quiet on a healthy round and clears the failure streak", func() {
		Expect(os.WriteFile(
			filepath.Join(stateDir, fleetDigestStreakFileName),
			nil,
			0o600,
		)).To(Succeed())
		check.results = []FleetDigestCheckResult{{ExitCode: 0, Output: "round ok\n"}}

		job.cycle(ctx)

		Expect(notifier.messages).To(BeEmpty())
		Expect(exists(fleetDigestStreakFileName)).To(BeFalse())
		Expect(exists(fleetDigestAlertFileName)).To(BeFalse())
		line, found := capture.find("job=fleet-digest-notify action=check")
		Expect(found).To(BeTrue())
		Expect(line).To(ContainSubstring("rc=0"))
		Expect(readLedger()).To(Equal([]string{
			"2026-11-13T10:00:00Z\t0\tround ok\t-",
		}))
	})

	It("publishes one escalation for a lapsed round and clears the streak", func() {
		Expect(os.WriteFile(
			filepath.Join(stateDir, fleetDigestStreakFileName),
			nil,
			0o600,
		)).To(Succeed())
		check.results = []FleetDigestCheckResult{{
			ExitCode: 10,
			Output:   "the fleet round lapsed 42m ago\nsecond line\n",
		}}

		job.cycle(ctx)

		Expect(notifier.messages).To(HaveLen(1))
		Expect(notifier.messages[0]).To(Equal(
			"Fleet round lapsed — the fleet round lapsed 42m ago second line",
		))
		Expect(exists(fleetDigestStreakFileName)).To(BeFalse())
		Expect(exists(fleetDigestAlertFileName)).To(BeFalse())
		_, found := capture.find("job=fleet-digest-notify action=publish rc=0")
		Expect(found).To(BeTrue())
		Expect(readLedger()).To(Equal([]string{
			"2026-11-13T10:00:00Z\t10\tthe fleet round lapsed 42m ago;second line\tsent",
		}))
	})

	It("publishes again on a second lapse, because the engine dedupes the lapse itself", func() {
		check.results = []FleetDigestCheckResult{
			{ExitCode: 10, Output: "lapsed"},
			{ExitCode: 10, Output: "lapsed"},
		}

		job.cycle(ctx)
		job.cycle(ctx)

		Expect(notifier.messages).To(HaveLen(2))
		Expect(readLedger()).To(HaveLen(2))
	})

	It("publishes once for a failure streak and suppresses the next tick", func() {
		check.results = []FleetDigestCheckResult{
			{ExitCode: 2, Output: "unreadable digest directory"},
			{ExitCode: 2, Output: "unreadable digest directory"},
		}

		job.cycle(ctx)
		job.cycle(ctx)

		Expect(notifier.messages).To(HaveLen(1))
		Expect(notifier.messages[0]).To(Equal(
			"Fleet digest watcher FAILED (rc=2) — the check did not complete: " +
				"unreadable digest directory",
		))
		Expect(exists(fleetDigestStreakFileName)).To(BeTrue())
		_, published := capture.find("job=fleet-digest-notify action=publish rc=0")
		Expect(published).To(BeTrue())
		_, suppressed := capture.find("job=fleet-digest-notify action=publish_suppressed rc=2")
		Expect(suppressed).To(BeTrue())
		Expect(readLedger()).To(HaveLen(2))
		Expect(
			readLedger()[1],
		).To(HaveSuffix("\tsuppressed: FAILED streak continues (alerted once)"))
	})

	It("writes the alert file on a failed publish and removes it on the next success", func() {
		notifier.failures = []error{&fleetDigestPublishError{
			exitCode: 3,
			output:   "boom\nsecond line",
		}}
		check.results = []FleetDigestCheckResult{
			{ExitCode: 2, Output: "check exploded"},
			{ExitCode: 10, Output: "lapsed"},
		}

		job.cycle(ctx)

		alertPath := filepath.Join(stateDir, fleetDigestAlertFileName)
		content, err := os.ReadFile(alertPath)
		Expect(err).To(BeNil())
		Expect(string(content)).To(Equal("2026-11-13T10:00:00Z\trc=3\tboom second line\n"))
		Expect(readLedger()[0]).To(HaveSuffix("\tFAILED(rc=3): boom second line"))
		_, failedLine := capture.find("job=fleet-digest-notify action=publish rc=3")
		Expect(failedLine).To(BeTrue())

		job.cycle(ctx)

		Expect(exists(fleetDigestAlertFileName)).To(BeFalse())
		Expect(notifier.messages).To(HaveLen(2))
		Expect(readLedger()[1]).To(HaveSuffix("\tsent"))
	})

	It("treats a check that could not run as a failure with the shell's exit code", func() {
		check.errs = []error{stderrors.New("exec: \"python3\": executable file not found")}

		job.cycle(ctx)

		Expect(notifier.messages).To(HaveLen(1))
		Expect(notifier.messages[0]).To(ContainSubstring("Fleet digest watcher FAILED (rc=127)"))
		Expect(exists(fleetDigestStreakFileName)).To(BeTrue())
		_, checkError := capture.find("job=fleet-digest-notify action=check_error")
		Expect(checkError).To(BeTrue())
		Expect(readLedger()[0]).To(ContainSubstring("\t127\t"))
	})

	It("appends one ledger line per cycle, including a quiet one", func() {
		check.results = []FleetDigestCheckResult{
			{ExitCode: 0, Output: "round ok\n"},
			{ExitCode: 10, Output: "lapsed"},
		}

		job.cycle(ctx)
		job.cycle(ctx)

		Expect(readLedger()).To(Equal([]string{
			"2026-11-13T10:00:00Z\t0\tround ok\t-",
			"2026-11-13T10:00:00Z\t10\tlapsed\tsent",
		}))
	})

	It("runs a cycle immediately when the loop starts and stops on cancellation", func() {
		done := make(chan error, 1)
		runCtx, cancel := context.WithCancel(context.Background())
		job.interval = time.Hour

		go func() {
			done <- NewFleetDigestNotifyJob(
				check,
				notifier,
				stateDir,
				job.interval,
				job.now,
			)(runCtx)
		}()

		Eventually(func() int { return int(check.calls.Load()) }).Should(BeNumerically(">", 0))
		cancel()
		Expect(awaitJobDone(done)).To(BeNil())
	})
})
