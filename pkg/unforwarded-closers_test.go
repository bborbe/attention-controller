// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

// unforwardedNow is the fixed clock every spec runs at, so a window boundary is
// crossed by arithmetic rather than by sleeping.
const unforwardedNow = int64(1_700_000_000)

// unforwardedClock returns a clock pinned at unforwardedNow.
func unforwardedClock() libtime.CurrentDateTimeGetter {
	return libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
		return libtime.DateTimeFromUnixMicro(unforwardedNow * 1_000_000)
	})
}

// unforwardedFixture is one temp state dir plus a transcript root and a watcher
// log, laid out the way the live attention state dir is.
type unforwardedFixture struct {
	root       string
	stateDir   string
	watcherLog string
}

// newUnforwardedFixture creates an empty fixture for one spec.
func newUnforwardedFixture() *unforwardedFixture {
	root := GinkgoT().TempDir()
	stateDir := filepath.Join(root, "state")
	Expect(os.MkdirAll(stateDir, 0o750)).To(Succeed())
	watcherLog := filepath.Join(root, "watcher.log")
	Expect(os.WriteFile(watcherLog, nil, 0o600)).To(Succeed())
	return &unforwardedFixture{root: root, stateDir: stateDir, watcherLog: watcherLog}
}

// scanner builds the scanner under test over the fixture.
func (f *unforwardedFixture) scanner() *pkg.UnforwardedClosersScanner {
	return pkg.NewUnforwardedClosersScanner(pkg.UnforwardedClosersConfig{
		StateDir:       f.stateDir,
		WatcherLog:     f.watcherLog,
		TranscriptRoot: f.root,
		Window:         24 * time.Hour,
		Grace:          120 * time.Second,
		Now:            unforwardedClock(),
	})
}

// openRow appends one `open` record to a session's event log.
func (f *unforwardedFixture) openRow(
	session string,
	item string,
	kind string,
	transcript string,
	at int64,
	event string,
) {
	record := map[string]any{
		"type":       "open",
		"item_id":    item,
		"session_id": session,
		"kind":       kind,
		"event":      event,
		"ts":         fmt.Sprintf("%d", at),
		"transcript": transcript,
	}
	f.writeEvent(session, record)
}

// writeEvent appends a record to a session's event log.
func (f *unforwardedFixture) writeEvent(session string, record map[string]any) {
	data, err := json.Marshal(record)
	Expect(err).To(BeNil())
	path := filepath.Join(f.stateDir, session+".events.jsonl")
	fh, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	Expect(err).To(BeNil())
	defer fh.Close()
	_, err = fh.Write(append(data, '\n'))
	Expect(err).To(BeNil())
}

// writeTranscript writes a one-message assistant transcript and returns its path.
func (f *unforwardedFixture) writeTranscript(name string, message string, at int64) string {
	path := filepath.Join(f.root, name+".jsonl")
	record := map[string]any{
		"type":      "assistant",
		"timestamp": time.Unix(at, 0).UTC().Format(time.RFC3339Nano),
		"message": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": message}},
		},
	}
	data, err := json.Marshal(record)
	Expect(err).To(BeNil())
	Expect(os.WriteFile(path, append(data, '\n'), 0o600)).To(Succeed())
	return path
}

// writeSuppressed appends one hook suppression record.
func (f *unforwardedFixture) writeSuppressed(session string, at float64) {
	record := map[string]any{"ts": fmt.Sprintf("%.3f", at), "session_id": session}
	data, err := json.Marshal(record)
	Expect(err).To(BeNil())
	path := filepath.Join(f.stateDir, "suppressed.jsonl")
	fh, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	Expect(err).To(BeNil())
	defer fh.Close()
	_, err = fh.Write(append(data, '\n'))
	Expect(err).To(BeNil())
}

// writePosted records the closer a session already posted a card for.
func (f *unforwardedFixture) writePosted(session string, closer string) {
	record := map[string]any{"closer": closer, "ts": unforwardedNow}
	data, err := json.Marshal(record)
	Expect(err).To(BeNil())
	path := filepath.Join(f.stateDir, session+".posted.json")
	Expect(os.WriteFile(path, data, 0o600)).To(Succeed())
}

// unforwardedLogRecorder captures the job's cycle lines.
type unforwardedLogRecorder struct {
	mu    sync.Mutex
	lines []string
}

// Logf records one formatted line.
func (r *unforwardedLogRecorder) Logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

// Lines returns a copy of the recorded lines.
func (r *unforwardedLogRecorder) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lines...)
}

// The seven specs SC2 pins. The focus string `UnforwardedClosers` selects
// exactly this Describe, so `Ran 7 of <package total> Specs` is the probe.
var _ = Describe("UnforwardedClosers", func() {
	var fixture *unforwardedFixture

	BeforeEach(func() {
		fixture = newUnforwardedFixture()
	})

	It(
		"UnforwardedClosers reports an idle row whose final assistant message carries a waiting-verb closer",
		func() {
			at := unforwardedNow - 600
			transcript := fixture.writeTranscript(
				"t1",
				"Done.\n🔵 READY · nothing running · you decide\n👤 You: `approve: repair` — run it",
				at-1,
			)
			fixture.openRow("s1", "aaaaaaaa", "idle", transcript, at, "Stop")

			result := fixture.scanner().Scan(context.Background())

			Expect(result.Outcome()).To(Equal(pkg.UnforwardedOutcomeFound))
			Expect(result.Findings).To(HaveLen(1))
			Expect(result.Findings[0].Kind).To(Equal("idle"))
			Expect(result.Findings[0].Ask).To(ContainSubstring("approve:"))
		},
	)

	It("UnforwardedClosers reports a question row with no forward line", func() {
		fixture.openRow("s2", "bbbbbbbb", "question", "", unforwardedNow-600, "Stop")

		result := fixture.scanner().Scan(context.Background())

		Expect(result.Outcome()).To(Equal(pkg.UnforwardedOutcomeFound))
		Expect(result.Findings).To(HaveLen(1))
		Expect(result.Findings[0].Kind).To(Equal("question"))
		Expect(result.Findings[0].Why).To(Equal("no forward line"))
	})

	It(
		"UnforwardedClosers exempts an idle row suppressed within one second before the open",
		func() {
			at := unforwardedNow - 600
			transcript := fixture.writeTranscript(
				"t3",
				"Done.\n🔵 READY · nothing running · you decide\n👤 You: pick — 1. A · 2. B",
				at-1,
			)
			fixture.openRow("s3", "cccccccc", "idle", transcript, at, "Stop")
			fixture.writeSuppressed("s3", float64(at)-0.02)

			result := fixture.scanner().Scan(context.Background())

			Expect(result.Outcome()).To(Equal(pkg.UnforwardedOutcomeNone))
			Expect(result.Findings).To(BeEmpty())
		},
	)

	It("UnforwardedClosers exempts a parked later (on …) closer", func() {
		at := unforwardedNow - 600
		transcript := fixture.writeTranscript(
			"t4",
			"Done.\n🔵 READY · nothing running · you decide\n👤 You: later (on the deploy): approve it",
			at-1,
		)
		fixture.openRow("s4", "dddddddd", "idle", transcript, at, "Stop")

		result := fixture.scanner().Scan(context.Background())

		Expect(result.Outcome()).To(Equal(pkg.UnforwardedOutcomeNone))
		Expect(result.Findings).To(BeEmpty())
	})

	It("UnforwardedClosers exempts a trailing no-reply clause", func() {
		at := unforwardedNow - 600
		transcript := fixture.writeTranscript(
			"t5",
			"Done.\n🔵 READY · nothing running · you decide\n👤 You: you run: make deploy — no reply needed",
			at-1,
		)
		fixture.openRow("s5", "eeeeeeee", "idle", transcript, at, "Stop")

		result := fixture.scanner().Scan(context.Background())

		Expect(result.Outcome()).To(Equal(pkg.UnforwardedOutcomeNone))
		Expect(result.Findings).To(BeEmpty())
	})

	It("UnforwardedClosers exempts a closer already on the board as a posted card", func() {
		at := unforwardedNow - 600
		transcript := fixture.writeTranscript(
			"t6",
			"Done.\n🔵 READY · nothing running · you decide\n👤 You: approve: repair — run it",
			at-1,
		)
		fixture.openRow("s6", "ffffffff", "idle", transcript, at, "Stop")
		fixture.writePosted("s6", "approve:  repair — run it")

		result := fixture.scanner().Scan(context.Background())

		Expect(result.Outcome()).To(Equal(pkg.UnforwardedOutcomeNone))
		Expect(result.Findings).To(BeEmpty())
	})

	It("UnforwardedClosers does not report a question row younger than the grace window", func() {
		fixture.openRow("s7", "gggggggg", "question", "", unforwardedNow-5, "Stop")

		result := fixture.scanner().Scan(context.Background())

		Expect(result.Outcome()).To(Equal(pkg.UnforwardedOutcomeNone))
		Expect(result.Findings).To(BeEmpty())
	})
})

// The SC3 contract: three outcomes plus the log line that replaces the launchd
// exit code. The Describe name deliberately does not contain the SC2 focus
// string, so the two probes select disjoint spec sets.
var _ = Describe("UnforwardedOutcome", func() {
	It("returns none when the window holds no lost closer", func() {
		fixture := newUnforwardedFixture()

		result := fixture.scanner().Scan(context.Background())

		Expect(result.Outcome()).To(Equal(pkg.UnforwardedOutcomeNone))
		Expect(result.Scanned).To(Equal(0))
	})

	It("returns found when a lost closer is in the window", func() {
		fixture := newUnforwardedFixture()
		fixture.openRow("s8", "hhhhhhhh", "question", "", unforwardedNow-600, "Stop")

		result := fixture.scanner().Scan(context.Background())

		Expect(result.Outcome()).To(Equal(pkg.UnforwardedOutcomeFound))
		Expect(result.Findings).To(HaveLen(1))
	})

	It("returns a read error when the state dir does not exist", func() {
		fixture := newUnforwardedFixture()
		scanner := pkg.NewUnforwardedClosersScanner(pkg.UnforwardedClosersConfig{
			StateDir:       filepath.Join(fixture.root, "missing"),
			WatcherLog:     fixture.watcherLog,
			TranscriptRoot: fixture.root,
			Now:            unforwardedClock(),
		})

		result := scanner.Scan(context.Background())

		Expect(result.Outcome()).To(Equal(pkg.UnforwardedOutcomeReadError))
		Expect(result.Err).NotTo(BeNil())
	})

	It(
		"logs exactly one job=attention-unforwarded-closers action=error line when the state dir does not exist",
		func() {
			fixture := newUnforwardedFixture()
			recorder := &unforwardedLogRecorder{}
			scanner := pkg.NewUnforwardedClosersScanner(pkg.UnforwardedClosersConfig{
				StateDir:       filepath.Join(fixture.root, "missing"),
				WatcherLog:     fixture.watcherLog,
				TranscriptRoot: fixture.root,
				Now:            unforwardedClock(),
			})
			job := pkg.NewUnforwardedClosersJob(scanner, time.Hour, recorder.Logf)

			job.RunOnce(context.Background())

			lines := recorder.Lines()
			Expect(lines).To(HaveLen(1))
			Expect(lines[0]).To(ContainSubstring(
				"job=attention-unforwarded-closers action=error",
			))
		},
	)

	It(
		"logs the job=attention-unforwarded-closers action=scan line with the scanned and unforwarded counts",
		func() {
			fixture := newUnforwardedFixture()
			fixture.openRow("s9", "iiiiiiii", "question", "", unforwardedNow-600, "Stop")
			recorder := &unforwardedLogRecorder{}
			job := pkg.NewUnforwardedClosersJob(fixture.scanner(), time.Hour, recorder.Logf)

			job.RunOnce(context.Background())

			lines := recorder.Lines()
			Expect(lines).To(HaveLen(1))
			Expect(strings.TrimSpace(lines[0])).To(Equal(
				"job=attention-unforwarded-closers action=scan scanned=1 unforwarded=1",
			))
		},
	)
})
