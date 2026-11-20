// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

// The operator-tier breach population is the 126 `permission:operator` cards a
// lower tier answered inside the pre-fix 168 h window. The criterion that
// removed the breach requires the controller to be driven over THOSE payloads,
// not over a synthetic table entry: a build that keeps `RoutineBashTierOperator`
// and its switch arm while routing the real shapes around the class must fail.
//
// ⚠️ The payloads are NOT committed. `bborbe/attention-controller` is a public
// repository and every payload carries a machine-local home path plus the text
// of a private vault task, so a fixture here would publish both. The harness
// therefore reads the operator's own evidence files and SKIPS when they are
// absent — a skipped run proves nothing, which is the honest state on a machine
// that does not hold the evidence.
//
// Run it with:
//
//	OPERATOR_TIER_BREACH_REPLAY=1 go test ./pkg/ \
//	    -ginkgo.focus="operator-tier breach population" -ginkgo.v
const (
	// operatorTierBreachReplayEnv opts the harness in. It is opt-in rather than
	// always-on for two reasons: the payloads exist only on the machine that
	// holds the evidence, and replaying 126 real classifier verdicts costs ~9 s
	// of wall clock — enough to push this suite's 60 s timeout to its edge.
	operatorTierBreachReplayEnv = "OPERATOR_TIER_BREACH_REPLAY"

	// operatorTierBreachAttributionEnv overrides the per-row attribution file
	// (one row per breach, carrying `item_id` and `kind`).
	operatorTierBreachAttributionEnv = "OPERATOR_TIER_BREACH_ATTRIBUTION"

	// operatorTierBreachSnapshotEnv overrides the store snapshot the attribution
	// rows are joined against for each row's `payload`.
	operatorTierBreachSnapshotEnv = "OPERATOR_TIER_BREACH_SNAPSHOT"

	// operatorTierBreachKind is the attribution row kind this harness replays.
	// The other 3 population rows are `approve` / `pick:manager` cards, which are
	// not permission-mechanism cards and carry no Bash payload.
	operatorTierBreachKind = "permission:operator"

	// operatorTierBreachPayloadPrefix is the display prefix the store puts in
	// front of a Bash card's command.
	operatorTierBreachPayloadPrefix = "Bash: "

	// operatorTierBreachClassifyWorkers bounds the parallel classifier calls. The
	// verdicts are independent and each spawns its own process, so a small pool
	// keeps the suite's wall clock down without a fork storm.
	operatorTierBreachClassifyWorkers = 16

	// operatorTierBreachMinClassified is the floor on rows that must reach the
	// classifier. Every row is refused either way, but a run where the credential
	// guard intercepted ALL of them would assert nothing about the tier mapping —
	// the vacuity this floor exists to catch.
	operatorTierBreachMinClassified = 1
)

// breachAttributionRow is one row of the per-row breach record.
type breachAttributionRow struct {
	ItemID string `json:"item_id"`
	Kind   string `json:"kind"`
}

// breachSnapshotRow is the two snapshot fields this harness joins on.
type breachSnapshotRow struct {
	ItemID  string `json:"item_id"`
	Payload string `json:"payload"`
}

// breachEvidencePaths returns the attribution and snapshot paths, falling back
// to the operator's state directory for either override that is unset.
func breachEvidencePaths() (string, string) {
	attribution := os.Getenv(operatorTierBreachAttributionEnv)
	snapshot := os.Getenv(operatorTierBreachSnapshotEnv)
	if attribution != "" && snapshot != "" {
		return attribution, snapshot
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return attribution, snapshot
	}
	state := filepath.Join(home, ".claude", "state")
	if attribution == "" {
		attribution = filepath.Join(state, "operator-tier-breach-attribution.json")
	}
	if snapshot == "" {
		snapshot = filepath.Join(state, "operator-tier-breach-snapshot.json")
	}
	return attribution, snapshot
}

// loadOperatorTierBreachPayloads returns item id -> payload for every
// `permission:operator` attribution row.
//
// ⚠️ The snapshot is streamed rather than decoded whole: it holds 161,844
// records, and only the wanted ids are kept.
func loadOperatorTierBreachPayloads(
	attributionPath string,
	snapshotPath string,
) (map[string]string, error) {
	attributionFile, err := os.Open(attributionPath)
	if err != nil {
		return nil, err
	}
	defer attributionFile.Close()
	var rows []breachAttributionRow
	if err := json.NewDecoder(attributionFile).Decode(&rows); err != nil {
		return nil, err
	}
	wanted := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if row.Kind == operatorTierBreachKind {
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
		var row breachSnapshotRow
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
	return payloads, nil
}

// breachItem builds an open permission gate owned by a session of its own, so a
// single settle cycle can carry a distinct question per item.
func breachItem(id string, sessionID string) pkg.Item {
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

var _ = Describe("operator-tier breach population through the settle arm", func() {
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
			sessionID := "breach-session-" + id
			items = append(items, breachItem(id, sessionID))
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
		if os.Getenv(operatorTierBreachReplayEnv) != "1" {
			Skip(
				"set " + operatorTierBreachReplayEnv + "=1 to replay the 126-row operator-tier breach population",
			)
		}
		attributionPath, snapshotPath := breachEvidencePaths()
		if _, err := os.Stat(attributionPath); err != nil {
			Skip("operator-tier breach attribution absent: " + attributionPath)
		}
		if _, err := os.Stat(snapshotPath); err != nil {
			Skip("operator-tier breach snapshot absent: " + snapshotPath)
		}
		classifierPath := pkg.ResolveRoutineBashClassifier("")
		if classifierPath == "" {
			Skip(
				"claude-supervisor permission classifier absent; the tier mapping cannot be driven",
			)
		}

		payloads, err := loadOperatorTierBreachPayloads(attributionPath, snapshotPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(payloads).NotTo(BeEmpty(), "the attribution file named no permission:operator rows")

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
		// a python process, and 126 sequential spawns cost ~13 s — enough to push
		// this suite past its 60 s Ginkgo timeout on its own.
		commands := make(map[string]string, len(payloads))
		ids := make([]string, 0, len(payloads))
		for id, payload := range payloads {
			Expect(payload).To(HavePrefix(operatorTierBreachPayloadPrefix))
			commands[id] = strings.TrimPrefix(payload, operatorTierBreachPayloadPrefix)
			ids = append(ids, id)
		}
		sort.Strings(ids)
		type tierResult struct {
			id   string
			tier string
			err  error
		}
		results := make([]tierResult, len(ids))
		semaphore := make(chan struct{}, operatorTierBreachClassifyWorkers)
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

		// Half 2 — the mapping. One settle cycle over the whole population, each
		// item carrying its own question.
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
		GinkgoWriter.Printf("operator-tier population carriers: %v\n", carriers)
		Expect(classified).To(BeNumerically(">=", operatorTierBreachMinClassified),
			"every payload was intercepted before the classifier, so the mapping was never driven")

		// Positive control — the same policy and the same harness must still
		// ANSWER an allowlisted command. A refuse-everything stub fails here.
		controlStore, controlWrapper := settleOne(
			policy,
			map[string]string{"allowlist-control": "ls -la"},
		)
		Expect(
			controlWrapper.answered,
		).To(Equal([]string{"breach-session-allowlist-control:allow"}))
		Expect(controlStore.RecordAttemptCallCount()).To(Equal(1))
		_, _, _, controlOutcome := controlStore.RecordAttemptArgsForCall(0)
		Expect(controlOutcome).To(Equal(pkg.DeliveredOutcome))
	})
})
