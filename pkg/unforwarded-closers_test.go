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

	libboltkv "github.com/bborbe/boltkv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
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

// unforwardedFixture is one temp state dir plus a transcript root and the
// attention store the forwarded lookup reads, laid out the way the live
// attention state dir is.
type unforwardedFixture struct {
	root     string
	stateDir string
	store    pkg.AttentionStore
}

// newUnforwardedFixture creates an empty fixture for one spec.
//
// ⚠️ A real boltkv-backed store, never a mock. The forwarded lookup is the thing
// under test, and a mocked store would let the spec assert a set the real
// `History` never returns.
func newUnforwardedFixture() *unforwardedFixture {
	root := GinkgoT().TempDir()
	stateDir := filepath.Join(root, "state")
	Expect(os.MkdirAll(stateDir, 0o750)).To(Succeed())
	db, err := libboltkv.OpenTemp(context.Background())
	Expect(err).To(BeNil())
	DeferCleanup(func() { Expect(db.Close()).To(BeNil()) })
	liveness := &mocks.SessionLivenessChecker{}
	liveness.IsLiveReturns(true)
	store := pkg.NewAttentionStore(
		db,
		pkg.NewItemIDGenerator(),
		liveness,
		libtime.NewCurrentDateTime(),
		libtime.Duration(15*60*1e9),
	)
	return &unforwardedFixture{root: root, stateDir: stateDir, store: store}
}

// scanner builds the scanner under test over the fixture.
func (f *unforwardedFixture) scanner() *pkg.UnforwardedClosersScanner {
	return pkg.NewUnforwardedClosersScanner(pkg.UnforwardedClosersConfig{
		StateDir:       f.stateDir,
		Store:          f.store,
		TranscriptRoot: f.root,
		Window:         24 * time.Hour,
		Grace:          120 * time.Second,
		Now:            unforwardedClock(),
	})
}

// forward pushes an item the way the Go ingest does: the dedup key IS the row's
// own item id. The store record is the proof the ingest carried it, so a row
// with one is forwarded and a row without one is not.
func (f *unforwardedFixture) forward(session string, itemID string, kind string) {
	mechanism := pkg.MessageAnswerMechanism
	if kind == "permission" {
		mechanism = pkg.PermissionAnswerMechanism
	}
	_, err := f.store.Push(context.Background(), pkg.PushRequest{
		ProducerID:      pkg.ProducerID(session),
		ProducerKind:    pkg.SessionProducerKind,
		ProvenanceClass: "hook",
		LivenessRef:     pkg.LivenessRef("session:" + session),
		DedupKey:        pkg.DedupKey(itemID),
		InterruptClass:  "question",
		Payload:         pkg.Payload("ask for " + itemID),
		AnswerMechanism: mechanism,
	})
	Expect(err).To(BeNil())
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
		// The ask the row carried. Every real `open` line has one — it is what
		// the hook's `open_item` writes — so a fixture without it models a row
		// the producer could never have emitted.
		"detail": "ask for " + item,
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

// writeForwardedLedger appends one delivery mark, the way the ingest does after a
// successful push. `at` is the row's own ts, which is what the reader bounds on.
func (f *unforwardedFixture) writeForwardedLedger(item string, at int64) {
	record := map[string]any{"item": item, "ts": float64(at)}
	data, err := json.Marshal(record)
	Expect(err).To(BeNil())
	path := filepath.Join(f.stateDir, "forwarded.jsonl")
	fh, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	Expect(err).To(BeNil())
	defer fh.Close()
	_, err = fh.Write(append(data, '\n'))
	Expect(err).To(BeNil())
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

	It("UnforwardedClosers reports a question row with no forward line, carrying the ask", func() {
		fixture.openRow("s2", "bbbbbbbb", "question", "", unforwardedNow-600, "Stop")

		result := fixture.scanner().Scan(context.Background())

		Expect(result.Outcome()).To(Equal(pkg.UnforwardedOutcomeFound))
		Expect(result.Findings).To(HaveLen(1))
		Expect(result.Findings[0].Kind).To(Equal("question"))
		Expect(result.Findings[0].Why).To(Equal("no forward line"))
		// The row's own ask, whole. It used to be dropped here, and the push then
		// fell back to `lost <kind> gate — no forward line`, which the operator
		// could read but not answer.
		Expect(result.Findings[0].Ask).To(Equal("ask for bbbbbbbb"))
	})

	// The negative half of the same rule: a row with no captured ask is recorded
	// and never carded. A card whose payload is a placeholder costs a wake and
	// cannot be answered, which is worse than no card at all.
	It("records, and does not card, a question row carrying no ask", func() {
		fixture := newUnforwardedFixture()
		fixture.writeEvent("s2b", map[string]any{
			"type":       "open",
			"item_id":    "bbbbbbbc",
			"session_id": "s2b",
			"kind":       "question",
			"event":      "Stop",
			"ts":         fmt.Sprintf("%d", unforwardedNow-600),
			"transcript": "",
		})

		result := fixture.scanner().Scan(context.Background())

		Expect(result.Findings).To(BeEmpty())

		store := &mocks.AttentionStore{}
		recorder := &unforwardedLogRecorder{}
		job := pkg.NewUnforwardedClosersJob(fixture.scanner(), store, nil, time.Hour, recorder.Logf)
		job.RunOnce(context.Background())

		Expect(store.PushCallCount()).To(Equal(0))
	})

	It(
		"UnforwardedClosers exempts an idle row suppressed within the window before the open",
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

	// ⚠️ The 5 s case. The hook writes the suppression and then opens the idle row
	// in the same turn, and on a loaded host that gap measured 5.006 s — three of
	// the 2026-11-17 findings. The one-second window this replaced read all three
	// as losses. The exemption must cover the real write-to-open gap, not an
	// idealised sub-second one.
	It(
		"UnforwardedClosers exempts an idle row suppressed 5.006 s before the open",
		func() {
			at := unforwardedNow - 600
			transcript := fixture.writeTranscript(
				"t3b",
				"Done.\n🔵 READY · nothing running · you decide\n👤 You: pick — 1. A · 2. B",
				at-1,
			)
			fixture.openRow("s3b", "cccccccd", "idle", transcript, at, "Stop")
			fixture.writeSuppressed("s3b", float64(at)-5.006)

			result := fixture.scanner().Scan(context.Background())

			Expect(result.Outcome()).To(Equal(pkg.UnforwardedOutcomeNone))
			Expect(result.Findings).To(BeEmpty())
		},
	)

	// The paired control for the widened window: a suppression far enough before
	// the open that it cannot be the same turn's write must NOT exempt the row.
	// Without this, widening to 6 s could be widened to anything. 30 s is the
	// bound the criterion names — comfortably outside the 5.006 s measured gap,
	// still inside a plausible later turn.
	It(
		"UnforwardedClosers still reports an idle row whose suppression is well before the window",
		func() {
			at := unforwardedNow - 600
			transcript := fixture.writeTranscript(
				"t3c",
				"Done.\n🔵 READY · nothing running · you decide\n👤 You: pick — 1. A · 2. B",
				at-1,
			)
			fixture.openRow("s3c", "ccccccce", "idle", transcript, at, "Stop")
			fixture.writeSuppressed("s3c", float64(at)-30)

			result := fixture.scanner().Scan(context.Background())

			Expect(result.Outcome()).To(Equal(pkg.UnforwardedOutcomeFound))
			Expect(result.Findings).To(HaveLen(1))
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

	// ⚠️ The paired control. Without it, a scanner that returns no findings at
	// all satisfies every criterion here at once — the fix would be
	// indistinguishable from deleting the detector.
	It("reports no finding for a row the store already holds", func() {
		fixture := newUnforwardedFixture()
		fixture.openRow("s7", "gggggggg", "question", "", unforwardedNow-600, "Stop")
		fixture.forward("s7", "gggggggg", "question")

		result := fixture.scanner().Scan(context.Background())

		Expect(result.Err).To(BeNil())
		Expect(result.Outcome()).To(Equal(pkg.UnforwardedOutcomeNone))
		Expect(result.Findings).To(BeEmpty())
	})

	// ⚠️ The delivered-then-pruned case. The store prunes an open item at
	// `expires_at`, so a gate that DID reach the board disappears from the store
	// and would otherwise read as lost — 25 of the 26 question/permission findings
	// on 2026-11-17. The ingest's delivery ledger is what proves it landed.
	It("reports no finding for a row whose delivery the ledger records after the item was pruned", func() {
		fixture := newUnforwardedFixture()
		fixture.openRow("s7b", "ggggggge", "question", "", unforwardedNow-600, "Stop")
		// No store item: the prune already removed it. Only the ledger knows.
		fixture.writeForwardedLedger("ggggggge", unforwardedNow-600)

		result := fixture.scanner().Scan(context.Background())

		Expect(result.Err).To(BeNil())
		Expect(result.Outcome()).To(Equal(pkg.UnforwardedOutcomeNone))
		Expect(result.Findings).To(BeEmpty())
	})

	// The paired control for the ledger: a mark for a DIFFERENT item must not
	// excuse this row. Without it, a reader that returned every marked id
	// regardless of the row would pass the spec above.
	It("still reports a row the ledger does not mark", func() {
		fixture := newUnforwardedFixture()
		fixture.openRow("s7c", "gggggggf", "question", "", unforwardedNow-600, "Stop")
		fixture.writeForwardedLedger("ffffffff", unforwardedNow-600)

		result := fixture.scanner().Scan(context.Background())

		Expect(result.Err).To(BeNil())
		Expect(result.Findings).To(HaveLen(1))
		Expect(result.Findings[0].Item).To(Equal("gggggggf"))
	})

	// ⚠️ The arm's own recovery must stop the row counting as lost. It re-presents
	// a lost gate as an `unforwarded:<item>` card; once that card is on the board
	// the gate HAS been delivered, and counting it again would make the check
	// report its own recovery as a loss forever.
	It("reports no finding for a row the job already re-presented as a card", func() {
		fixture := newUnforwardedFixture()
		fixture.openRow("s7d", "gggggggg", "question", "", unforwardedNow-600, "Stop")
		_, err := fixture.store.Push(context.Background(), pkg.PushRequest{
			ProducerID:      pkg.UnforwardedClosersProducerID,
			ProducerKind:    pkg.CronProducerKind,
			LivenessRef:     pkg.LivenessRef("owner:attention-unforwarded-closers"),
			DedupKey:        pkg.DedupKey("unforwarded:gggggggg"),
			InterruptClass:  "pick",
			Payload:         pkg.Payload("ask for gggggggg"),
			AnswerMechanism: pkg.MessageAnswerMechanism,
		})
		Expect(err).To(BeNil())

		result := fixture.scanner().Scan(context.Background())

		Expect(result.Err).To(BeNil())
		Expect(result.Outcome()).To(Equal(pkg.UnforwardedOutcomeNone))
		Expect(result.Findings).To(BeEmpty())
	})

	It("still reports a row the store does not hold, beside one it does", func() {
		fixture := newUnforwardedFixture()
		fixture.openRow("s7", "gggggggg", "question", "", unforwardedNow-600, "Stop")
		fixture.forward("s7", "gggggggg", "question")
		fixture.openRow("s7", "hhhhhhhh", "question", "", unforwardedNow-600, "Stop")

		result := fixture.scanner().Scan(context.Background())

		Expect(result.Err).To(BeNil())
		Expect(result.Findings).To(HaveLen(1))
		Expect(result.Findings[0].Item).To(Equal("hhhhhhhh"))
	})

	It("returns a read error when the state dir does not exist", func() {
		fixture := newUnforwardedFixture()
		scanner := pkg.NewUnforwardedClosersScanner(pkg.UnforwardedClosersConfig{
			StateDir:       filepath.Join(fixture.root, "missing"),
			Store:          fixture.store,
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
				Store:          fixture.store,
				TranscriptRoot: fixture.root,
				Now:            unforwardedClock(),
			})
			job := pkg.NewUnforwardedClosersJob(scanner, nil, nil, time.Hour, recorder.Logf)

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
			job := pkg.NewUnforwardedClosersJob(
				fixture.scanner(),
				nil,
				nil,
				time.Hour,
				recorder.Logf,
			)

			job.RunOnce(context.Background())

			lines := recorder.Lines()
			Expect(lines).To(HaveLen(1))
			Expect(strings.TrimSpace(lines[0])).To(Equal(
				"job=attention-unforwarded-closers action=scan scanned=1 unforwarded=1",
			))
		},
	)
})

// The card re-presentation the launchd job's `--post` performed. Its own
// Describe, so the SC2 focus selects exactly its seven specs.
var _ = Describe("UnforwardedPosting", func() {
	It(
		"pushes one card per finding with a dedup key, an owner liveness ref and the two options",
		func() {
			fixture := newUnforwardedFixture()
			fixture.openRow("s10", "jjjjjjjj", "question", "", unforwardedNow-600, "Stop")
			store := &mocks.AttentionStore{}
			store.PushReturns(&pkg.Item{ItemID: pkg.ItemID("card-1")}, nil)
			recorder := &unforwardedLogRecorder{}
			job := pkg.NewUnforwardedClosersJob(
				fixture.scanner(),
				store,
				nil,
				time.Hour,
				recorder.Logf,
			)

			job.RunOnce(context.Background())

			Expect(store.PushCallCount()).To(Equal(1))
			_, request := store.PushArgsForCall(0)
			Expect(request.ProducerID).To(Equal(pkg.UnforwardedClosersProducerID))
			Expect(request.ProducerKind).To(Equal(pkg.CronProducerKind))
			Expect(request.LivenessRef).To(Equal(
				pkg.LivenessRef("owner:attention-unforwarded-closers"),
			))
			Expect(request.DedupKey).To(Equal(pkg.DedupKey("unforwarded:jjjjjjjj")))
			Expect(request.Payload).To(Equal(pkg.Payload("ask for jjjjjjjj")))
			Expect(request.AnswerMechanism).To(Equal(pkg.MessageAnswerMechanism))
			Expect(request.Options).To(HaveLen(2))
			Expect(request.Options[0].Recommended).To(BeTrue())
			Expect(request.ExpiresAt).NotTo(BeNil())
			// The scan line plus one post line.
			Expect(recorder.Lines()).To(HaveLen(2))
		},
	)

	It("does not push when no store is configured", func() {
		fixture := newUnforwardedFixture()
		fixture.openRow("s11", "kkkkkkkk", "question", "", unforwardedNow-600, "Stop")
		recorder := &unforwardedLogRecorder{}
		job := pkg.NewUnforwardedClosersJob(fixture.scanner(), nil, nil, time.Hour, recorder.Logf)

		job.RunOnce(context.Background())

		Expect(recorder.Lines()).To(HaveLen(1))
	})
})

// The session-liveness filter on the mint site.
//
// ⚠️ The two halves are deliberately paired. The DEAD spec alone is satisfied by
// an arm that mints nothing at all — a blanket disable also produces zero cards
// for a dead session. The LIVE spec is the positive control that proves the arm
// is still a recovery path: a gate from a session that is still running is
// re-presented, because that session can still act on the answer. Measured
// 2026-11-16: 601 cards from 112 sessions, of which the sampled exited ones
// (2e074f82, e577041e, aa160ccd, 1d2c9896) had no registry entry and the live
// ones (4314b3f3, 3052a109) did.
var _ = Describe("UnforwardedDeadSession", func() {
	It("mints a card for a LIVE session's unforwarded gate", func() {
		fixture := newUnforwardedFixture()
		fixture.openRow("live-session", "llllllll", "question", "", unforwardedNow-600, "Stop")
		store := &mocks.AttentionStore{}
		store.PushReturns(&pkg.Item{ItemID: pkg.ItemID("card-live")}, nil)
		liveness := &mocks.SessionLivenessChecker{}
		liveness.IsLiveReturns(true)
		recorder := &unforwardedLogRecorder{}
		job := pkg.NewUnforwardedClosersJob(
			fixture.scanner(), store, liveness, time.Hour, recorder.Logf,
		)

		job.RunOnce(context.Background())

		Expect(store.PushCallCount()).To(Equal(1))
		_, sessionID := liveness.IsLiveArgsForCall(0)
		Expect(sessionID).To(Equal("live-session"))
	})

	It("does not mint a card for a session that is no longer live", func() {
		fixture := newUnforwardedFixture()
		fixture.openRow("dead-session", "mmmmmmmm", "question", "", unforwardedNow-600, "Stop")
		store := &mocks.AttentionStore{}
		liveness := &mocks.SessionLivenessChecker{}
		liveness.IsLiveReturns(false)
		recorder := &unforwardedLogRecorder{}
		job := pkg.NewUnforwardedClosersJob(
			fixture.scanner(), store, liveness, time.Hour, recorder.Logf,
		)

		job.RunOnce(context.Background())

		Expect(store.PushCallCount()).To(Equal(0))
		lines := recorder.Lines()
		// The scan line still reports the loss; the skip line names why it was
		// not carded. Dropping the count too would hide the loss entirely.
		Expect(lines).To(HaveLen(2))
		Expect(lines[0]).To(ContainSubstring("action=scan scanned=1 unforwarded=1"))
		Expect(lines[1]).To(ContainSubstring("action=skip_dead_session item=mmmmmmmm"))
	})

	It("cards a live session's gate and skips a dead one in the same scan", func() {
		fixture := newUnforwardedFixture()
		fixture.openRow("live-session", "nnnnnnnn", "question", "", unforwardedNow-600, "Stop")
		fixture.openRow("dead-session", "oooooooo", "question", "", unforwardedNow-600, "Stop")
		store := &mocks.AttentionStore{}
		store.PushReturns(&pkg.Item{ItemID: pkg.ItemID("card-1")}, nil)
		liveness := &mocks.SessionLivenessChecker{}
		liveness.IsLiveStub = func(_ context.Context, sessionID string) bool {
			return sessionID == "live-session"
		}
		recorder := &unforwardedLogRecorder{}
		job := pkg.NewUnforwardedClosersJob(
			fixture.scanner(), store, liveness, time.Hour, recorder.Logf,
		)

		job.RunOnce(context.Background())

		Expect(store.PushCallCount()).To(Equal(1))
		_, request := store.PushArgsForCall(0)
		Expect(request.DedupKey).To(Equal(pkg.DedupKey("unforwarded:nnnnnnnn")))
	})
})
