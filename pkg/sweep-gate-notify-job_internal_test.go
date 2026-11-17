// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
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

// errSweepGateTestEnumeration is the failure the enumeration spec injects.
var errSweepGateTestEnumeration = stderrors.New("enumerator exploded")

// stubSweepGateSubjects answers from the spec's fixed list, repeating it for
// every pass so a loop can run more passes than the spec queued.
type stubSweepGateSubjects struct {
	subjects []SweepGateSubject
	err      error
	calls    atomic.Int32
}

func (s *stubSweepGateSubjects) List(_ context.Context) ([]SweepGateSubject, error) {
	s.calls.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	return s.subjects, nil
}

// stubSweepGateGate answers from the spec's queue and calls onTick before
// answering, so a spec can observe the state the gate is handed and write the
// files a real gate would write.
type stubSweepGateGate struct {
	results []SweepGateTickResult
	errs    []error
	calls   atomic.Int32
	onTick  func(stateDir string)
}

func (g *stubSweepGateGate) Tick(
	_ context.Context,
	_ string,
	_ string,
	stateDir string,
) (SweepGateTickResult, error) {
	index := int(g.calls.Add(1) - 1)
	if g.onTick != nil {
		g.onTick(stateDir)
	}
	if index < len(g.errs) && g.errs[index] != nil {
		return SweepGateTickResult{}, g.errs[index]
	}
	if len(g.results) == 0 {
		return SweepGateTickResult{}, nil
	}
	if index >= len(g.results) {
		return g.results[len(g.results)-1], nil
	}
	return g.results[index], nil
}

// stubSweepGateClassifier records the verdict it was handed and answers from the
// spec's queue.
type stubSweepGateClassifier struct {
	results  []SweepGateClassifyResult
	verdicts []string
	calls    atomic.Int32
}

func (c *stubSweepGateClassifier) Classify(
	_ context.Context,
	_ string,
	_ string,
	gateVerdict string,
	_ string,
) (SweepGateClassifyResult, error) {
	c.verdicts = append(c.verdicts, gateVerdict)
	index := int(c.calls.Add(1) - 1)
	if len(c.results) == 0 {
		return SweepGateClassifyResult{}, nil
	}
	if index >= len(c.results) {
		return c.results[len(c.results)-1], nil
	}
	return c.results[index], nil
}

// stubSweepGateAdhoc answers from the spec's queue and records the state dir it
// was handed, so a spec can assert the pass runs under `<BASE>/adhoc`.
type stubSweepGateAdhoc struct {
	results   []SweepGateAdhocResult
	errs      []error
	calls     atomic.Int32
	stateDirs []string
}

func (a *stubSweepGateAdhoc) Run(
	_ context.Context,
	stateDir string,
) (SweepGateAdhocResult, error) {
	a.stateDirs = append(a.stateDirs, stateDir)
	index := int(a.calls.Add(1) - 1)
	if index < len(a.errs) && a.errs[index] != nil {
		return SweepGateAdhocResult{}, a.errs[index]
	}
	if len(a.results) == 0 {
		return SweepGateAdhocResult{}, nil
	}
	if index >= len(a.results) {
		return a.results[len(a.results)-1], nil
	}
	return a.results[index], nil
}

// stubSweepGateNotifier records every message and fails the calls the spec marks.
type stubSweepGateNotifier struct {
	messages []string
	failures []error
}

func (n *stubSweepGateNotifier) Publish(_ context.Context, message string) error {
	index := len(n.messages)
	n.messages = append(n.messages, message)
	if index < len(n.failures) {
		return n.failures[index]
	}
	return nil
}

// stubSweepGatePoster records every card it is handed.
type stubSweepGatePoster struct {
	cards    []SweepGateCard
	failures []error
}

func (p *stubSweepGatePoster) Post(_ context.Context, card SweepGateCard) (string, error) {
	index := len(p.cards)
	p.cards = append(p.cards, card)
	if index < len(p.failures) {
		return "", p.failures[index]
	}
	return "posted " + card.DedupKey, nil
}

// stubSweepGateWaker records every subject it is asked to wake.
type stubSweepGateWaker struct {
	subjects []string
	failures []error
}

func (w *stubSweepGateWaker) Wake(
	_ context.Context,
	subject string,
	_ string,
) (string, error) {
	index := len(w.subjects)
	w.subjects = append(w.subjects, subject)
	if index < len(w.failures) {
		return "", w.failures[index]
	}
	return "WOKEN pane=1 prompt='/supervisor:goal-drive --interval 15m' session=11111111 live=yes\n", nil
}

// stubSweepGateChecker answers from the spec's queue of verdict-only results.
type stubSweepGateChecker struct {
	results []SweepGateCheckResult
	errs    []error
	calls   atomic.Int32
}

func (c *stubSweepGateChecker) Check(
	_ context.Context,
	_ string,
	_ string,
	_ string,
) (SweepGateCheckResult, error) {
	index := int(c.calls.Add(1) - 1)
	if index < len(c.errs) && c.errs[index] != nil {
		return SweepGateCheckResult{}, c.errs[index]
	}
	if len(c.results) == 0 {
		return SweepGateCheckResult{ExitCode: 0}, nil
	}
	if index >= len(c.results) {
		return c.results[len(c.results)-1], nil
	}
	return c.results[index], nil
}

// sweepGateReadLastPass reads the pass record the cycle just wrote, so a spec
// asserts both halves were recorded without repeating the unmarshal.
func sweepGateReadLastPass(baseDir string) sweepGateLastPass {
	content, err := os.ReadFile(filepath.Join(baseDir, sweepGateLastPassFileName))
	ExpectWithOffset(1, err).To(BeNil())
	var record sweepGateLastPass
	ExpectWithOffset(1, json.Unmarshal(content, &record)).To(Succeed())
	return record
}

var _ = Describe("sweep gate notify job", func() {
	var ctx context.Context
	var baseDir string
	var subjectDir string
	var subjects *stubSweepGateSubjects
	var gate *stubSweepGateGate
	var checker *stubSweepGateChecker
	var classifier *stubSweepGateClassifier
	var adhoc *stubSweepGateAdhoc
	var notifier *stubSweepGateNotifier
	var poster *stubSweepGatePoster
	var waker *stubSweepGateWaker
	var capture *logCapture
	var job *sweepGateNotifyJob

	// sweepGateInstant is the fixed clock every spec runs on, so the ledger, the
	// alert file and the heartbeat are asserted byte-for-byte rather than by shape.
	var sweepGateInstant = time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)

	// sweepGateSlugOfSubject is the filename stem the job and the gate share.
	const sweepGateSlugOfSubject = "my-topic"

	BeforeEach(func() {
		ctx = context.Background()
		baseDir = GinkgoT().TempDir()
		subjectDir = filepath.Join(baseDir, "private-personal")
		subjects = &stubSweepGateSubjects{subjects: []SweepGateSubject{{
			Name:   "My Topic",
			Vault:  "/vaults/private-personal",
			Branch: "topic",
		}}}
		gate = &stubSweepGateGate{}
		checker = &stubSweepGateChecker{}
		classifier = &stubSweepGateClassifier{}
		adhoc = &stubSweepGateAdhoc{}
		notifier = &stubSweepGateNotifier{}
		poster = &stubSweepGatePoster{}
		waker = &stubSweepGateWaker{}
		capture = &logCapture{}

		originalInfo, originalError := sweepGateNotifyInfof, sweepGateNotifyErrorf
		sweepGateNotifyInfof = capture.info
		sweepGateNotifyErrorf = capture.error
		DeferCleanup(func() {
			sweepGateNotifyInfof = originalInfo
			sweepGateNotifyErrorf = originalError
		})

		job = &sweepGateNotifyJob{
			subjects:   subjects,
			gate:       gate,
			checker:    checker,
			classifier: classifier,
			adhoc:      adhoc,
			notifier:   notifier,
			poster:     poster,
			waker:      waker,
			stateDir:   baseDir,
			interval:   time.Second,
			now: libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
				return libtime.DateTime(sweepGateInstant)
			}),
		}
	})

	pathIn := func(name string) string { return filepath.Join(subjectDir, name) }

	writeFile := func(path string, content string) {
		Expect(os.MkdirAll(filepath.Dir(path), 0o750)).To(Succeed())
		Expect(os.WriteFile(path, []byte(content), 0o600)).To(Succeed())
	}

	exists := func(path string) bool {
		_, err := os.Stat(path)
		return err == nil
	}

	readLedger := func() []string {
		content, err := os.ReadFile(pathIn(sweepGateSlugOfSubject + sweepGateLedgerSuffix))
		Expect(err).To(BeNil())
		return strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	}

	// ledgerColumns splits one row, so a spec asserts the five documented columns
	// without pinning the sixth to this test binary's pid.
	ledgerColumns := func(line string) []string {
		return strings.Split(line, "\t")
	}

	It("clears the failure streak on EMPTY and publishes nothing", func() {
		writeFile(pathIn(sweepGateSlugOfSubject+sweepGateStreakSuffix), "")
		gate.results = []SweepGateTickResult{{ExitCode: 0, Output: "tick ok\n"}}

		job.cycle(ctx)

		Expect(notifier.messages).To(BeEmpty())
		Expect(poster.cards).To(BeEmpty())
		Expect(exists(pathIn(sweepGateSlugOfSubject + sweepGateStreakSuffix))).To(BeFalse())
		Expect(exists(pathIn(sweepGateSlugOfSubject + sweepGateAlertSuffix))).To(BeFalse())
		Expect(readLedger()).To(HaveLen(1))
		columns := ledgerColumns(readLedger()[0])
		Expect(columns[0]).To(Equal("2026-11-13T10:00:00Z"))
		Expect(columns[1]).To(Equal("0"))
		Expect(columns[2]).To(Equal("tick ok"))
		Expect(columns[3]).To(Equal(sweepGateNotifiedNotSent))
		Expect(columns[4]).To(Equal(sweepGateWakeNone))
		Expect(columns[5]).To(HavePrefix("pid="))
		_, passLine := capture.find(
			"job=sweep-gate-notify action=pass subjects=1 adhoc_rc=0",
		)
		Expect(passLine).To(BeTrue())
	})

	It("reports a MISS when an EMPTY tick's re-read says the digest moved", func() {
		writeFile(pathIn(sweepGateMissGuardFileName), "")
		gate.results = []SweepGateTickResult{{ExitCode: 0, Output: "unchanged\n"}}
		checker.results = []SweepGateCheckResult{{ExitCode: 10, Output: "change: digest differs\n"}}

		job.cycle(ctx)

		_, missed := capture.find("job=sweep-gate-notify action=miss")
		Expect(missed).To(BeTrue())
		Expect(checker.calls.Load()).To(BeNumerically("==", 1))
	})

	It("does not run the miss check without the opt-in marker", func() {
		gate.results = []SweepGateTickResult{{ExitCode: 0, Output: "unchanged\n"}}
		checker.results = []SweepGateCheckResult{{ExitCode: 10}}

		job.cycle(ctx)

		Expect(checker.calls.Load()).To(BeNumerically("==", 0))
		_, missed := capture.find("job=sweep-gate-notify action=miss")
		Expect(missed).To(BeFalse())
	})

	It("reports a guard error, not a MISS, when the check did not run", func() {
		writeFile(pathIn(sweepGateMissGuardFileName), "")
		gate.results = []SweepGateTickResult{{ExitCode: 0, Output: "unchanged\n"}}
		checker.results = []SweepGateCheckResult{{ExitCode: 1, Output: "gate exploded\n"}}

		job.cycle(ctx)

		_, missed := capture.find("job=sweep-gate-notify action=miss")
		Expect(missed).To(BeFalse())
		_, failed := capture.find("job=sweep-gate-notify action=guard_error")
		Expect(failed).To(BeTrue())
		_, ok := capture.find("job=sweep-gate-notify action=guard_ok")
		Expect(ok).To(BeFalse())
	})

	It("reports a guard error when the check itself errored", func() {
		writeFile(pathIn(sweepGateMissGuardFileName), "")
		gate.results = []SweepGateTickResult{{ExitCode: 0, Output: "unchanged\n"}}
		checker.errs = []error{stderrors.New("check exploded")}

		job.cycle(ctx)

		_, failed := capture.find("job=sweep-gate-notify action=guard_error")
		Expect(failed).To(BeTrue())
		_, ok := capture.find("job=sweep-gate-notify action=guard_ok")
		Expect(ok).To(BeFalse())
	})

	It("does not report a MISS when the re-read still says unchanged", func() {
		writeFile(pathIn(sweepGateMissGuardFileName), "")
		gate.results = []SweepGateTickResult{{ExitCode: 0, Output: "unchanged\n"}}
		checker.results = []SweepGateCheckResult{{ExitCode: 0, Output: "unchanged\n"}}

		job.cycle(ctx)

		_, missed := capture.find("job=sweep-gate-notify action=miss")
		Expect(missed).To(BeFalse())
		_, ok := capture.find("job=sweep-gate-notify action=guard_ok")
		Expect(ok).To(BeTrue())
		Expect(checker.calls.Load()).To(BeNumerically("==", 1))
	})

	It("does not run the miss check when no checker is wired", func() {
		writeFile(pathIn(sweepGateMissGuardFileName), "")
		job.checker = nil
		gate.results = []SweepGateTickResult{{ExitCode: 0, Output: "unchanged\n"}}

		job.cycle(ctx)

		Expect(checker.calls.Load()).To(BeNumerically("==", 0))
	})

	It("records progress-only on a CHANGE-WAKE the classifier calls quiet", func() {
		writeFile(pathIn(sweepGateSlugOfSubject+sweepGateStreakSuffix), "")
		gate.results = []SweepGateTickResult{{ExitCode: 10, Output: "digest moved\n"}}
		classifier.results = []SweepGateClassifyResult{{ExitCode: 0}}

		job.cycle(ctx)

		Expect(notifier.messages).To(BeEmpty())
		Expect(poster.cards).To(BeEmpty())
		Expect(waker.subjects).To(BeEmpty())
		Expect(classifier.verdicts).To(Equal([]string{"digest moved"}))
		Expect(exists(pathIn(sweepGateSlugOfSubject + sweepGateStreakSuffix))).To(BeFalse())
		Expect(ledgerColumns(readLedger()[0])[3]).To(Equal(sweepGateNotifiedProgress))
		Expect(ledgerColumns(readLedger()[0])[4]).To(Equal(sweepGateWakeNone))
	})

	It("publishes once on a CHANGE-WAKE the classifier calls actionable", func() {
		writeFile(pathIn(sweepGateSlugOfSubject+sweepGateTickFileSuffix), "Topic headline\nrow\n")
		gate.results = []SweepGateTickResult{{ExitCode: 10, Output: "digest moved\n"}}
		classifier.results = []SweepGateClassifyResult{{
			ExitCode: 10,
			Output:   "3 tasks blocked\n",
		}}

		job.cycle(ctx)

		Expect(notifier.messages).To(HaveLen(1))
		Expect(notifier.messages[0]).To(Equal(
			"Sweep gate: Topic headline — needs you:\n3 tasks blocked\n",
		))
		_, published := capture.find("job=sweep-gate-notify action=publish")
		Expect(published).To(BeTrue())
		Expect(waker.subjects).To(Equal([]string{"My Topic"}))
		Expect(ledgerColumns(readLedger()[0])[3]).To(Equal("sent: 3 tasks blocked"))
		Expect(ledgerColumns(readLedger()[0])[4]).To(
			Equal("wake=WOKEN session=11111111 live=yes"),
		)
	})

	It("records a wake that could not run as ERROR beside the alert it did not cost", func() {
		gate.results = []SweepGateTickResult{{ExitCode: 10, Output: "digest moved\n"}}
		classifier.results = []SweepGateClassifyResult{{ExitCode: 10, Output: "needs you\n"}}
		waker.failures = []error{stderrors.New("wake timed out")}

		job.cycle(ctx)

		Expect(notifier.messages).To(HaveLen(1))
		columns := ledgerColumns(readLedger()[0])
		Expect(columns[3]).To(Equal("sent: needs you"))
		Expect(columns[4]).To(Equal("wake=ERROR session=- live=unknown"))
	})

	It("publishes once for a failure streak and suppresses the next tick", func() {
		gate.results = []SweepGateTickResult{
			{ExitCode: 2, Output: "gate exploded"},
			{ExitCode: 2, Output: "gate exploded"},
		}

		job.cycle(ctx)
		job.cycle(ctx)

		Expect(notifier.messages).To(HaveLen(1))
		Expect(notifier.messages[0]).To(Equal(
			"Sweep gate FAILED: My Topic — the gate did not complete (rc=2):\ngate exploded",
		))
		Expect(exists(pathIn(sweepGateSlugOfSubject + sweepGateStreakSuffix))).To(BeTrue())
		_, suppressed := capture.find("job=sweep-gate-notify action=publish_suppressed")
		Expect(suppressed).To(BeTrue())
		Expect(readLedger()).To(HaveLen(2))
		Expect(ledgerColumns(readLedger()[0])[2]).To(Equal("FAILED(rc=2): gate exploded"))
		Expect(ledgerColumns(readLedger()[1])[3]).To(Equal(sweepGateNotifiedSuppressed))
	})

	It("clears the streak on a non-FAILED tick, so the next streak alerts again", func() {
		gate.results = []SweepGateTickResult{
			{ExitCode: 2, Output: "gate exploded"},
			{ExitCode: 10, Output: "digest moved"},
			{ExitCode: 2, Output: "gate exploded"},
		}
		classifier.results = []SweepGateClassifyResult{{ExitCode: 0}}

		job.cycle(ctx)
		Expect(exists(pathIn(sweepGateSlugOfSubject + sweepGateStreakSuffix))).To(BeTrue())
		job.cycle(ctx)
		Expect(exists(pathIn(sweepGateSlugOfSubject + sweepGateStreakSuffix))).To(BeFalse())
		job.cycle(ctx)

		Expect(notifier.messages).To(HaveLen(2))
		Expect(exists(pathIn(sweepGateSlugOfSubject + sweepGateStreakSuffix))).To(BeTrue())
	})

	It("clears the card before the gate runs and posts the one the gate writes", func() {
		staleCard := pathIn(sweepGateSlugOfSubject + sweepGateCardSuffix)
		writeFile(staleCard, `{"dedup_key": "stale", "payload": "stale"}`)
		gate.results = []SweepGateTickResult{{ExitCode: 10, Output: "digest moved"}}
		gate.onTick = func(stateDir string) {
			// The file must be gone while the gate runs: an absent card after a
			// failed tick is the honest "no verdict this tick".
			Expect(exists(filepath.Join(stateDir, sweepGateSlugOfSubject+sweepGateCardSuffix))).
				To(BeFalse())
			Expect(os.WriteFile(
				filepath.Join(stateDir, sweepGateSlugOfSubject+sweepGateCardSuffix),
				[]byte(`{
  "dedup_key": "open-card-invariant:my-topic",
  "payload": "My Topic is blocked on you.",
  "context": "no card",
  "options": ["Look now", "Leave it"],
  "recommend": "Look now"
}`),
				0o600,
			)).To(Succeed())
		}
		classifier.results = []SweepGateClassifyResult{{ExitCode: 0}}

		job.cycle(ctx)

		Expect(poster.cards).To(HaveLen(1))
		Expect(poster.cards[0].DedupKey).To(Equal("open-card-invariant:my-topic"))
		Expect(poster.cards[0].Options).To(Equal([]string{"Look now", "Leave it"}))
		Expect(poster.cards[0].Recommend).To(Equal("Look now"))
		_, created := capture.find(
			"job=sweep-gate-notify action=create card=open-card-invariant:my-topic",
		)
		Expect(created).To(BeTrue())
		Expect(readLedger()).To(HaveLen(2))
		Expect(ledgerColumns(readLedger()[0])[2]).To(Equal("INVARIANT"))
		Expect(ledgerColumns(readLedger()[0])[4]).To(Equal(sweepGateWakeNone))
		Expect(ledgerColumns(readLedger()[0])[3]).To(
			Equal("sent: invariant posted open-card-invariant:my-topic"),
		)
	})

	It("writes the alert file on a failed publish and removes it on the next success", func() {
		notifier.failures = []error{&fleetDigestPublishError{
			exitCode: 3,
			output:   "boom\nsecond line",
		}}
		gate.results = []SweepGateTickResult{
			{ExitCode: 2, Output: "gate exploded"},
			{ExitCode: 10, Output: "digest moved"},
		}
		classifier.results = []SweepGateClassifyResult{{ExitCode: 10, Output: "needs you"}}

		job.cycle(ctx)

		alertPath := pathIn(sweepGateSlugOfSubject + sweepGateAlertSuffix)
		content, err := os.ReadFile(alertPath)
		Expect(err).To(BeNil())
		Expect(string(content)).To(Equal("2026-11-13T10:00:00Z\trc=3\tboom second line\n"))
		Expect(ledgerColumns(readLedger()[0])[3]).To(Equal("FAILED(rc=3): boom second line"))
		_, failed := capture.find("job=sweep-gate-notify action=publish_failed rc=3")
		Expect(failed).To(BeTrue())

		job.cycle(ctx)

		Expect(exists(alertPath)).To(BeFalse())
		Expect(notifier.messages).To(HaveLen(2))
		Expect(ledgerColumns(readLedger()[1])[3]).To(Equal("sent: needs you"))
	})

	It("writes last-pass.json on every pass with a measured duration", func() {
		calls := 0
		job.now = libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
			calls++
			if calls == 1 {
				return libtime.DateTime(sweepGateInstant)
			}
			return libtime.DateTime(sweepGateInstant.Add(5 * time.Second))
		})
		gate.results = []SweepGateTickResult{{ExitCode: 0, Output: "tick ok"}}

		job.cycle(ctx)

		content, err := os.ReadFile(filepath.Join(baseDir, sweepGateLastPassFileName))
		Expect(err).To(BeNil())
		var record sweepGateLastPass
		Expect(json.Unmarshal(content, &record)).To(Succeed())
		Expect(record.StartedAt).To(Equal(sweepGateInstant.Unix()))
		Expect(record.StartedAtISO).To(Equal("2026-11-13T10:00:00Z"))
		Expect(record.EndEpoch).To(Equal(sweepGateInstant.Add(5 * time.Second).Unix()))
		Expect(record.DurationS).To(Equal(int64(5)))
		Expect(record.PID).To(BeNumerically(">", 0))
		Expect(record.Subjects).To(Equal(1))
		Expect(record.AdhocRC).To(Equal(0))
		Expect(record.RC).To(Equal(0))
		_, passLine := capture.find(
			"job=sweep-gate-notify action=pass subjects=1 adhoc_rc=0 duration=5",
		)
		Expect(passLine).To(BeTrue())
	})

	It("records a pass that enumerated nothing rather than staying silent", func() {
		subjects.err = errSweepGateTestEnumeration

		job.cycle(ctx)

		content, err := os.ReadFile(filepath.Join(baseDir, sweepGateLastPassFileName))
		Expect(err).To(BeNil())
		var record sweepGateLastPass
		Expect(json.Unmarshal(content, &record)).To(Succeed())
		Expect(record.Subjects).To(Equal(0))
		_, errorLine := capture.find("job=sweep-gate-notify action=subjects_error")
		Expect(errorLine).To(BeTrue())
	})

	// --- ad-hoc park pass ---------------------------------------------------
	// The pass runs after the subject loop on every cycle, under its own
	// `<BASE>/adhoc/` state. Its three outcomes are asserted separately, because
	// the whole reason the pass exists is that the subject loop cannot see an
	// unanchored worker at all.

	adhocPath := func(name string) string {
		return filepath.Join(baseDir, sweepGateAdhocDirName, name)
	}

	readAdhocLedger := func() []string {
		content, err := os.ReadFile(adhocPath(sweepGateAdhocLedgerName))
		Expect(err).To(BeNil())
		return strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	}

	It("records a quiet ad-hoc pass and clears its failure streak", func() {
		writeFile(adhocPath(sweepGateAdhocStreakName), "")
		gate.results = []SweepGateTickResult{{ExitCode: 0, Output: "tick ok"}}
		adhoc.results = []SweepGateAdhocResult{{ExitCode: 0}}

		job.cycle(ctx)

		Expect(notifier.messages).To(BeEmpty())
		Expect(exists(adhocPath(sweepGateAdhocStreakName))).To(BeFalse())
		Expect(exists(adhocPath(sweepGateAdhocAlertFileName))).To(BeFalse())
		Expect(adhoc.stateDirs).To(Equal([]string{
			filepath.Join(baseDir, sweepGateAdhocDirName),
		}))
		rows := readAdhocLedger()
		Expect(rows).To(HaveLen(1))
		columns := ledgerColumns(rows[0])
		Expect(columns).To(HaveLen(4))
		Expect(columns[0]).To(Equal("2026-11-13T10:00:00Z"))
		Expect(columns[1]).To(Equal("0"))
		Expect(columns[2]).To(Equal(sweepGateNotifiedNotSent))
		Expect(columns[3]).To(HavePrefix("pid="))
		heartbeat, err := os.ReadFile(adhocPath(sweepGateAdhocStem + sweepGateHeartbeatSuffix))
		Expect(err).To(BeNil())
		Expect(string(heartbeat)).To(Equal(
			strconv.FormatInt(sweepGateInstant.Unix(), 10) + " 2026-11-13T10:00:00Z rc=0\n",
		))
		record := sweepGateReadLastPass(baseDir)
		Expect(record.AdhocRC).To(Equal(0))
	})

	It("publishes one alert when the ad-hoc pass finds an unanchored park", func() {
		writeFile(adhocPath(sweepGateAdhocStreakName), "")
		gate.results = []SweepGateTickResult{{ExitCode: 0, Output: "tick ok"}}
		adhoc.results = []SweepGateAdhocResult{{
			ExitCode: 10,
			Output:   "gate raised (ad-hoc, no task anchor): Worker [abcd1234] in /tmp\n",
		}}

		job.cycle(ctx)

		Expect(notifier.messages).To(Equal([]string{
			"Sweep gate: unanchored park needs you:\n" +
				"gate raised (ad-hoc, no task anchor): Worker [abcd1234] in /tmp\n",
		}))
		Expect(exists(adhocPath(sweepGateAdhocStreakName))).To(BeFalse())
		Expect(exists(adhocPath(sweepGateAdhocAlertFileName))).To(BeFalse())
		Expect(ledgerColumns(readAdhocLedger()[0])[2]).To(Equal(
			"sent: gate raised (ad-hoc, no task anchor): Worker [abcd1234] in /tmp",
		))
		Expect(sweepGateReadLastPass(baseDir).AdhocRC).To(Equal(10))
	})

	It("publishes once for an ad-hoc failure streak and suppresses the next pass", func() {
		gate.results = []SweepGateTickResult{{ExitCode: 0, Output: "tick ok"}}
		adhoc.results = []SweepGateAdhocResult{
			{ExitCode: 2, Output: "pass exploded"},
			{ExitCode: 2, Output: "pass exploded"},
		}

		job.cycle(ctx)
		job.cycle(ctx)

		Expect(notifier.messages).To(HaveLen(1))
		Expect(notifier.messages[0]).To(Equal(
			"Sweep gate ad-hoc pass FAILED (rc=2) — the pass did not complete:\npass exploded",
		))
		Expect(exists(adhocPath(sweepGateAdhocStreakName))).To(BeTrue())
		_, suppressed := capture.find(
			"job=sweep-gate-notify action=publish_suppressed pass=adhoc rc=2",
		)
		Expect(suppressed).To(BeTrue())
		rows := readAdhocLedger()
		Expect(rows).To(HaveLen(2))
		Expect(ledgerColumns(rows[0])[1]).To(Equal("2"))
		Expect(ledgerColumns(rows[1])[2]).To(Equal(sweepGateNotifiedSuppressed))
	})

	It("re-arms the ad-hoc streak after a quiet pass, so the next failure alerts", func() {
		gate.results = []SweepGateTickResult{{ExitCode: 0, Output: "tick ok"}}
		adhoc.results = []SweepGateAdhocResult{
			{ExitCode: 2, Output: "pass exploded"},
			{ExitCode: 0},
			{ExitCode: 2, Output: "pass exploded"},
		}

		job.cycle(ctx)
		job.cycle(ctx)
		Expect(exists(adhocPath(sweepGateAdhocStreakName))).To(BeFalse())
		job.cycle(ctx)

		Expect(notifier.messages).To(HaveLen(2))
		Expect(exists(adhocPath(sweepGateAdhocStreakName))).To(BeTrue())
	})

	It("walks a pass immediately when the loop starts and stops on cancellation", func() {
		done := make(chan error, 1)
		runCtx, cancel := context.WithCancel(context.Background())
		job.interval = time.Hour
		gate.results = []SweepGateTickResult{{ExitCode: 0, Output: "tick ok"}}

		go func() {
			done <- NewSweepGateNotifyJob(
				subjects,
				gate,
				checker,
				classifier,
				adhoc,
				notifier,
				poster,
				waker,
				baseDir,
				job.interval,
				job.now,
			)(runCtx)
		}()

		Eventually(func() int { return int(gate.calls.Load()) }).Should(BeNumerically(">", 0))
		cancel()
		Expect(awaitJobDone(done)).To(BeNil())
	})
})

var _ = DescribeTable("sweepGateWakeField",
	func(output string, expected string) {
		Expect(sweepGateWakeField(output)).To(Equal(expected))
	},
	Entry("a confirmed wake",
		"WOKEN pane=12 prompt='/supervisor:goal-drive --interval 15m' session=0dbb4336 live=yes\n",
		"wake=WOKEN session=0dbb4336 live=yes"),
	Entry("a guard refusal folds its reason into the result",
		"SKIP skip:spinner pane=12 session=0dbb4336 live=yes\n",
		"wake=SKIP:spinner session=0dbb4336 live=yes"),
	Entry("a live manager whose pane was missed",
		"NO-PANE reason=tty-not-a-pane:ttys009 subject='X' session=0dbb4336 live=yes\n",
		"wake=NO-PANE session=0dbb4336 live=yes"),
	Entry("a subject whose registrations are all dead",
		"NO-MANAGER subject='X' tier=auto session=339d0e33 live=no\n",
		"wake=NO-MANAGER session=339d0e33 live=no"),
	Entry("a subject nothing has registered",
		"NO-MANAGER subject='X' tier=auto session=- live=no\n",
		"wake=NO-MANAGER session=- live=no"),
	Entry("a live manager running headless",
		"HEADLESS subject='X' session=6f14c290 live=yes\n",
		"wake=HEADLESS session=6f14c290 live=yes"),
	Entry(
		"an unconfirmed submit",
		"UNVERIFIED pane=7 prompt='/x' — the submit could not be confirmed session=5bedea87 live=yes\n",
		"wake=UNVERIFIED session=5bedea87 live=yes",
	),
	Entry("a line from a script that predates the session fields",
		"NO-PANE session=0dbb4336 subject='X'\n",
		"wake=NO-PANE session=0dbb4336 live=unknown"),
	Entry("leading blank lines are skipped",
		"\n  \nWOKEN pane=1 session=11111111 live=yes\n",
		"wake=WOKEN session=11111111 live=yes"),
	Entry("empty output is unknown, never a guess",
		"",
		"wake=UNKNOWN session=- live=unknown"),
)
