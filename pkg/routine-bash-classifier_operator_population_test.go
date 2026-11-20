// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

// This harness replays a recorded set of `permission:operator` payloads through
// the settle arm's real entry point and asserts every one is refused, with an
// allowlisted command as the positive control.
//
// It exists because the table-driven specs drive a SYNTHETIC payload: they prove
// the switch arm exists, not that a recorded payload set actually reaches it. A
// build that kept `RoutineBashTierOperator` and its mapping while routing the
// recorded shapes around the class would pass those specs.
//
// ⚠️ It is NOT a CI gate. The payload set is supplied by the operator as local
// files, named by environment variables, and is deliberately not committed: it
// contains machine-local paths and private notes. The spec skips when the
// variables are unset, so a green CI run says nothing about it.
//
// Run it with:
//
//	OPERATOR_TIER_REPLAY=1 \
//	OPERATOR_TIER_REPLAY_ATTRIBUTION=/path/to/rows.json \
//	OPERATOR_TIER_REPLAY_SNAPSHOT=/path/to/records.json \
//	go test ./pkg/ -ginkgo.focus="operator-tier payload replay" -ginkgo.v
const (
	// operatorTierReplayEnv opts the harness in.
	operatorTierReplayEnv = "OPERATOR_TIER_REPLAY"

	// operatorTierReplayAttributionEnv names the file of row records, each
	// carrying `item_id` and `kind`.
	operatorTierReplayAttributionEnv = "OPERATOR_TIER_REPLAY_ATTRIBUTION"

	// operatorTierReplaySnapshotEnv names the file of full records the row
	// records are joined against for each row's `payload`.
	operatorTierReplaySnapshotEnv = "OPERATOR_TIER_REPLAY_SNAPSHOT"

	// operatorTierReplayKind selects the rows this harness replays.
	operatorTierReplayKind = "permission:operator"

	// operatorTierReplayPayloadPrefix is the display prefix a stored Bash card
	// puts in front of its command.
	operatorTierReplayPayloadPrefix = "Bash: "

	// operatorTierReplayWorkers bounds the parallel classifier calls. The
	// verdicts are independent and each spawns its own process, so a small pool
	// keeps the wall clock down without a fork storm.
	operatorTierReplayWorkers = 16

	// operatorTierReplayMinClassifiedPercent is the share of rows that must reach
	// the classifier. Every row is refused either way, but a run where the
	// credential guard intercepted ALL of them would assert nothing about the
	// tier mapping — the vacuity this floor exists to catch.
	operatorTierReplayMinClassifiedPercent = 50
)

// replayAttributionRow is one row of the recorded row file.
type replayAttributionRow struct {
	ItemID string `json:"item_id"`
	Kind   string `json:"kind"`
}

// replaySnapshotRow is the two record fields this harness joins on.
type replaySnapshotRow struct {
	ItemID  string `json:"item_id"`
	Payload string `json:"payload"`
}

// replayEvidencePaths returns the two env-var paths, either of which may be
// empty when unset. ⚠️ There is deliberately no home-directory default: the
// payload set is operator-supplied, and a baked-in path convention would both
// assume a machine layout and publish one.
func replayEvidencePaths() (string, string) {
	return os.Getenv(operatorTierReplayAttributionEnv),
		os.Getenv(operatorTierReplaySnapshotEnv)
}

// loadOperatorTierPayloads returns item id -> payload for every row of the
// requested kind.
//
// ⚠️ The record file is streamed rather than decoded whole — it can be large,
// and only the wanted ids are kept. ⚠️ It is an ERROR for a wanted id to be
// absent from the record file: a truncated or mismatched pair would otherwise
// replay a handful of rows and still pass while claiming the whole set.
func loadOperatorTierPayloads(
	attributionPath string,
	snapshotPath string,
) (map[string]string, error) {
	attributionFile, err := os.Open(attributionPath)
	if err != nil {
		return nil, err
	}
	defer attributionFile.Close()
	var rows []replayAttributionRow
	if err := json.NewDecoder(attributionFile).Decode(&rows); err != nil {
		return nil, err
	}
	wanted := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if row.Kind == operatorTierReplayKind {
			wanted[row.ItemID] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return map[string]string{}, nil
	}

	snapshotFile, err := os.Open(snapshotPath)
	if err != nil {
		return nil, err
	}
	defer snapshotFile.Close()
	decoder := json.NewDecoder(bufio.NewReaderSize(snapshotFile, 1<<20))
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	payloads := make(map[string]string, len(wanted))
	for decoder.More() {
		var row replaySnapshotRow
		if err := decoder.Decode(&row); err != nil {
			return nil, err
		}
		if _, ok := wanted[row.ItemID]; !ok {
			continue
		}
		payloads[row.ItemID] = row.Payload
		if len(payloads) == len(wanted) {
			break
		}
	}
	if len(payloads) != len(wanted) {
		return nil, fmt.Errorf(
			"%d of %d wanted rows are absent from the record file",
			len(wanted)-len(payloads),
			len(wanted),
		)
	}
	return payloads, nil
}

// replayItem builds an open permission gate owned by a session of its own, so a
// single settle cycle can carry a distinct question per item.
func replayItem(id string, sessionID string) pkg.Item {
	return pkg.Item{
		ItemID:          pkg.ItemID(id),
		LivenessRef:     pkg.LivenessRef("session:" + sessionID),
		State:           pkg.OpenState,
		AnswerMechanism: pkg.PermissionAnswerMechanism,
	}
}

// questionWrapper serves one wrapper question per session id and records every
// answer it is asked to deliver.
type questionWrapper struct {
	questions map[string]*pkg.WrapperSession
	answered  []string
}

func (f *questionWrapper) Probe(
	_ context.Context,
	sessionID string,
) (pkg.WrapperProbeState, *pkg.WrapperSession) {
	session, ok := f.questions[sessionID]
	if !ok {
		return pkg.WrapperProbeNone, nil
	}
	return pkg.WrapperProbeOK, session
}

func (f *questionWrapper) AnswerViaWrapper(
	_ context.Context,
	sessionID string,
	decision string,
	_ *string,
) (pkg.DeliveryVerdict, string) {
	f.answered = append(f.answered, sessionID+":"+decision)
	return pkg.DeliveryOK, "scripted"
}

func (f *questionWrapper) DeliverMessage(
	_ context.Context,
	_ string,
	_ string,
	_ string,
) (pkg.DeliveryVerdict, string) {
	return pkg.DeliveryRefuse, "not used by the settle arm"
}

func (f *questionWrapper) SessionMode(_ context.Context, _ string) (string, bool) {
	return "", false
}

// questionSessions serves a live session entry per id, with no tty so the
// settle arm — not the pane carrier — decides the gate.
type questionSessions struct {
	entries map[string]pkg.SessionEntry
}

func (f *questionSessions) SessionEntry(
	_ context.Context,
	sessionID string,
) (pkg.SessionEntry, bool) {
	entry, ok := f.entries[sessionID]
	return entry, ok
}

func (f *questionSessions) SessionTTY(_ context.Context, _ string) string { return "" }

var _ = Describe("operator-tier payload replay through the settle arm", func() {
	var (
		ctx       context.Context
		workspace string
		clock     libtime.CurrentDateTime
	)

	BeforeEach(func() {
		ctx = context.Background()
		workspace = GinkgoT().TempDir()
		clock = libtime.NewCurrentDateTime()
		clock.SetNow(libtime.DateTime(time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)))
	})

	// settleOne runs one settle cycle over the given command per item id and
	// returns the store the cycle recorded into and the wrapper it asked.
	settleOne := func(
		policy pkg.RoutinePolicy,
		commands map[string]string,
	) (*mocks.AttentionStore, *questionWrapper) {
		ids := make([]string, 0, len(commands))
		for id := range commands {
			ids = append(ids, id)
		}
		sort.Strings(ids)

		store := &mocks.AttentionStore{}
		store.RecordAttemptReturns(&pkg.DeliveryAttempt{}, nil)
		store.DeliveryReportsReturns(map[pkg.ItemID]*pkg.DeliveryReport{}, nil)

		items := make(pkg.Items, 0, len(ids))
		wrapper := &questionWrapper{questions: map[string]*pkg.WrapperSession{}}
		sessions := &questionSessions{entries: map[string]pkg.SessionEntry{}}
		for _, id := range ids {
			sessionID := "replay-session-" + id
			items = append(items, replayItem(id, sessionID))
			wrapper.questions[sessionID] = &pkg.WrapperSession{Question: &pkg.WrapperQuestion{
				RequestID: "req-" + id,
				Raw:       bashQuestion("req-"+id, commands[id]),
			}}
			sessions.entries[sessionID] = pkg.SessionEntry{
				SessionID:  sessionID,
				PID:        os.Getpid(),
				Entrypoint: "sdk-cli",
				CWD:        workspace,
			}
		}
		store.ReadReturns(items, nil)

		job := pkg.NewRoutineGateSettleJob(store, wrapper, policy, sessions, time.Hour, clock)
		Expect(job.SettleOnce(ctx)).To(Succeed())
		return store, wrapper
	}

	It("refuses every operator-tier payload and still answers an allowlisted one", func() {
		if os.Getenv(operatorTierReplayEnv) != "1" {
			Skip(
				"set " + operatorTierReplayEnv + "=1 to replay the recorded operator-tier payloads",
			)
		}
		attributionPath, snapshotPath := replayEvidencePaths()
		if attributionPath == "" || snapshotPath == "" {
			Skip("set " + operatorTierReplayAttributionEnv + " and " +
				operatorTierReplaySnapshotEnv + " to the recorded row and record files")
		}
		classifierPath := pkg.ResolveRoutineBashClassifier("")
		if classifierPath == "" {
			Skip(
				"claude-supervisor permission classifier absent; the tier mapping cannot be driven",
			)
		}

		payloads, err := loadOperatorTierPayloads(attributionPath, snapshotPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(payloads).NotTo(BeEmpty(), "the row file named no rows of the requested kind")

		classifier := pkg.NewRoutineBashClassifier(
			pkg.RoutineBashClassifierInterpreter,
			[]string{"-I"},
			classifierPath,
			pkg.RoutineBashClassifierTimeout,
		)

		// Half 1 — the tier itself. Every payload must still classify `operator`
		// when driven through the real classifier. This covers the rows the
		// credential guard intercepts before the classifier is ever consulted.
		//
		// ⚠️ Classified in bounded parallel, not one at a time: each verdict starts
		// a python process, and a sequential run is slow enough to threaten this
		// suite's timeout on its own.
		commands := make(map[string]string, len(payloads))
		ids := make([]string, 0, len(payloads))
		for id, payload := range payloads {
			Expect(payload).To(HavePrefix(operatorTierReplayPayloadPrefix))
			commands[id] = strings.TrimPrefix(payload, operatorTierReplayPayloadPrefix)
			ids = append(ids, id)
		}
		sort.Strings(ids)
		type tierResult struct {
			id   string
			tier string
			err  error
		}
		results := make([]tierResult, len(ids))
		semaphore := make(chan struct{}, operatorTierReplayWorkers)
		var waitGroup sync.WaitGroup
		for index, id := range ids {
			waitGroup.Add(1)
			go func(index int, id string) {
				defer waitGroup.Done()
				semaphore <- struct{}{}
				defer func() { <-semaphore }()
				tier, err := classifier.Classify(ctx, commands[id])
				results[index] = tierResult{id: id, tier: tier, err: err}
			}(index, id)
		}
		waitGroup.Wait()
		for _, result := range results {
			Expect(result.err).NotTo(HaveOccurred(), "item %s: classifier failed", result.id)
			Expect(result.tier).To(Equal(pkg.RoutineBashTierOperator),
				"item %s: tier narrowed", result.id)
		}

		// Half 2 — the mapping. One settle cycle over the whole set, each item
		// carrying its own question.
		policy := pkg.NewRoutinePolicy(nil, pkg.WithRoutineBashClassifier(classifier, clock))
		store, wrapper := settleOne(policy, commands)

		Expect(wrapper.answered).To(BeEmpty(), "a settled gate was answered")

		classified := 0
		carriers := map[string]int{}
		Expect(store.RecordAttemptCallCount()).To(Equal(len(commands)))
		for index := 0; index < store.RecordAttemptCallCount(); index++ {
			_, _, carrier, outcome := store.RecordAttemptArgsForCall(index)
			carriers[carrier]++
			Expect(outcome).To(Equal(pkg.HeldOutcome), "a gate was not held")
			// The classifier's own verdict maps to `tier-operator`; the credential
			// guard refuses a payload naming a denied path before the classifier
			// runs, and that refusal is stricter, not a different answer.
			Expect(carrier).To(BeElementOf("tier-operator", "bash-denied-path"))
			if strings.HasPrefix(carrier, "tier-") {
				classified++
				Expect(carrier).To(Equal("tier-operator"))
			}
		}
		GinkgoWriter.Printf("operator-tier payload carriers: %v\n", carriers)
		Expect(classified).To(
			BeNumerically(">=", len(commands)*operatorTierReplayMinClassifiedPercent/100),
			"too few payloads reached the classifier, so the mapping was barely driven",
		)

		// Positive control — the same policy and the same harness must still
		// ANSWER an allowlisted command. A refuse-everything stub fails here.
		controlStore, controlWrapper := settleOne(
			policy,
			map[string]string{"allowlist-control": "ls -la"},
		)
		Expect(
			controlWrapper.answered,
		).To(Equal([]string{"replay-session-allowlist-control:allow"}))
		Expect(controlStore.RecordAttemptCallCount()).To(Equal(1))
		_, _, _, controlOutcome := controlStore.RecordAttemptArgsForCall(0)
		Expect(controlOutcome).To(Equal(pkg.DeliveredOutcome))
	})
})
