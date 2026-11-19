---
status: approved
spec: [023-manager-transcript-wake]
created: "2026-11-19T17:20:00Z"
queued: "2026-11-19T16:20:28Z"
---

# Content-keyed re-emit, park ageing, the lost-card re-post, and per-manager state

<summary>
- A gate whose body changes under a stable reason re-emits, because the re-emit key is the gate's content rather than its session.
- A park escalates once at 15 minutes and once at 60, and a stall never escalates at all.
- A parked worker whose card the store lost is re-posted, but never when a card is already open and never when the operator already answered the park in progress.
- A failed store read suppresses the re-post rather than firing it for every parked worker.
- The previous set, the content keys and the park ages survive a restart, so a restart does not re-wake the whole set.
- Two managers sharing one state directory never read each other's previous set.
</summary>

<objective>
Complete the gate computation with the four things that need history: the content key that makes a re-emit exact, the park-age clock that escalates once per threshold, the re-post that restores a lost card without re-raising an answered one, and the per-manager state that makes both survive a restart.
</objective>

<context>
Read before changing anything:

- `pkg/manager-gate.go` (from prompt 3) — `ManagerGateDecision`, `ManagerGateState(s)`, `ManagerGateRecord(Kind)`, `managerTransitions`, `managerStableSessions`, `managerHoldKeepsMembership`, `managerUntrackedDetail`.
- `pkg/manager-wake-job.go` — the emission log, `ManagerWakeEmission`, the `libtime.CurrentDateTimeGetter` injection and the `errors.Wrap`/`glog` style.
- `pkg/attention-store.go` — `PushRequest`, `Item`, `DedupKey`, `Payload`, `AnswerMechanism`.
- `pkg/answer-mechanism.go` — `MessageAnswerMechanism`, `PermissionAnswerMechanism`, `AckAnswerMechanism`.
- `specs/in-progress/023-manager-transcript-wake.md` — § "The semantics, read from the source" (`content_key`, `transitions`, `AGED escalation`, `re-post of a lost card`, the two-poll stability gate), Desired Behavior 5–8 and 11, AC4, AC5, AC8, AC12, AC14.
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-enum-type-pattern.md`, `go-testing-guide.md`, `go-error-wrapping-guide.md`.

⚠️ `pkg/` is Ginkgo-only (`pkg/pkg_suite_test.go` defines the single `func TestSuite`). Every evidence command carries `-run TestSuite -ginkgo.focus='<Name>' -ginkgo.fail-on-empty`.
</context>

<requirements>
1. New file `pkg/manager-gate-transitions.go` (or extend `pkg/manager-gate.go`) with the content key:

```go
// managerContentKey is the identity of a gate's CONTENT — the exact string a
// NEW GATE carries. Per body hash, never per session and never per
// session+reason: a session already in the set never re-emits, and a body
// change under a stable reason must.
func managerContentKey(reason, detail string) string {
	sum := sha1.Sum([]byte(reason + " :: " + detail))
	return hex.EncodeToString(sum[:])[:16]
}
```

(Use `crypto/sha1` + `encoding/hex`; the truncation is the identity, not a security measure.)

2. Upgrade `managerTransitions` so `NEW GATE` is content-keyed:
   - `prev[session]` stores `managerContentKey(reason, detail)`.
   - absent from `prev` → `NEW GATE`.
   - present with an empty string → `UNCHANGED` (a legacy membership-only entry) but commit the key, so the **next** pass can tell a change from a repeat.
   - present with a key → `NEW GATE` **iff** the computed key differs; otherwise no record.
   - a held-kept session keeps its existing content unchanged.
   Keep `ManagerGateRecord.Content` carrying `reason + " :: " + detail` (the human string), while `prev` stores the hash.

3. New file `pkg/manager-gate-park.go`:

```go
// ManagerParkedReasons is the closed set of reasons that mean "parked on the
// operator". Only these may age. Mirrors the source script's PARKED_REASONS.
var ManagerParkedReasons = []string{
	"registry:waiting", "busy+parked-modal", "headless+closer", "idle+closer",
}

// ManagerParkAgeThresholds are the escalation thresholds, in minutes. Each fires
// ONCE per continuous park.
var ManagerParkAgeThresholds = []int{15, 60}

func managerIsParkedReason(reason string) bool

// managerParkAge is one session's continuous-park clock.
type managerParkAge struct {
	Since libtime.DateTime `json:"since"`
	Aged  []int            `json:"aged"`
}

// managerAdvanceParkAges drops every session no longer parked and starts a
// clock for every session newly parked. Mutates and returns ages.
func managerAdvanceParkAges(ages map[string]managerParkAge, parked []string, now libtime.DateTime) map[string]managerParkAge

// managerAgedEvents returns (session, minutes) for each threshold crossed since
// the last call, ONCE each. A backwards clock step yields a non-negative age.
func managerAgedEvents(ages map[string]managerParkAge, now libtime.DateTime, thresholds []int) []managerAgedEvent
```

`managerAdvanceParkAges` is keyed on **continuous park**, never on gated-set membership: a worker that answers one question and asks the next stays gated throughout, so a membership-keyed clock would never restart. A non-parked pass ends the park; the next parked pass starts a new one.

4. Emit `AGED` records: add `ManagerGateRecordAged ManagerGateRecordKind = "AGED"` and, in `Observe` (requirement 8), append one record per `managerAgedEvent`, with detail `fmt.Sprintf("%d minutes", minutes)`. ⚠️ A `stuck:<cause>` gate is not in `ManagerParkedReasons`, so it never ages and emits no `AGED`.

5. New file `pkg/manager-gate-state.go`:

```go
// managerScope is the state-file scope key for one manager. sha1 of the
// manager's session id, first 8 hex characters.
func managerScope(manager string) string

// managerStatePath returns state-<scope>.json under stateDir.
func managerStatePath(stateDir, manager string) string

// managerParkPath returns park-<scope>.json under stateDir.
func managerParkPath(stateDir, manager string) string

// managerLoadPrevState reads a state file. ok is false when the file is absent
// or unreadable — the caller warns and treats the pass as a first run.
func managerLoadPrevState(path string) (map[string]string, bool)

// managerLoadParkAges and managerSaveParkAges are the park-age sibling of the pair
// above. ⚠️ Without them the `StatePersistsAcrossRestart` requirement (a park open
// across the restart keeps its age) is unimplementable from this prompt: the ages
// are loaded and saved by name but no function does either.
func managerLoadParkAges(path string) (map[string]managerParkAge, bool)
func managerSaveParkAges(path string, ages map[string]managerParkAge) error   // atomic tmp+Rename

// managerSaveState writes prev atomically: a sibling .tmp file, then os.Rename.
// A bare open-and-write truncates first and a crash leaves a zero-byte file that
// reads as a first run and answers with a NEW GATE burst.
func managerSaveState(path string, prev map[string]string) error
```

Both files are keyed on the **tracked set's identity** (here: the manager's session id), never a bare `state.json` — two managers sharing the default state directory is the ordinary case, and one shared file makes each read the other's previous set and print a bare `CLEARED`.

6. New file `pkg/manager-gate-repost.go`:

```go
// ManagerOpenView is the store's OPEN items, projected to the two fields the
// re-post decision reads. A nil view means the store read FAILED.
type ManagerOpenView struct {
	Producers map[string]struct{} // producer_id (full session id)
	DedupKeys map[string]struct{}
}

// ManagerParkAnswer is one answered `park:<sid8>` history row.
type ManagerParkAnswer struct {
	ItemID    ItemID
	CreatedAt libtime.DateTime
	Answer    string
}

// ManagerAnswerView is the answered park rows, keyed by the 8-char session
// prefix. A nil view means the store read FAILED.
type ManagerAnswerView struct {
	BySession map[string]ManagerParkAnswer
}

type managerTrackedEntry struct {
	FullID string
	Label  string
}

type managerRepostInput struct {
	Parked    []string // 8-char prefixes, parked THIS pass
	Entries   map[string]managerTrackedEntry
	Open      *ManagerOpenView
	Answered  *ManagerAnswerView
	ParkSince map[string]libtime.DateTime
	Reasons   map[string]string
}

// managerRepostPlan returns the cards to re-post. An empty slice with a non-nil
// reason to re-post nothing is the normal suppressed case; a failed store read
// returns an empty slice and never fires.
func managerRepostPlan(in managerRepostInput) []ManagerRepostRequest

type ManagerRepostRequest struct {
	ProducerID      string
	DedupKey        DedupKey
	Payload         Payload
	AnswerMechanism AnswerMechanism
	Options         AnswerOptions // always empty for an ack card
}

func managerAnswerCoversCurrentPark(row ManagerParkAnswer, since *libtime.DateTime) bool
func managerRepostMechanism(reason string) AnswerMechanism
```

Rules:
   - no parked sessions → nil (do not read the store).
   - `in.Open == nil` → nil (**a failed read is not "the store holds nothing"**).
   - skip a session whose `park:<sid8>` is in `Open.DedupKeys`, and one whose full id is in `Open.Producers`.
   - `in.Answered == nil` → nil (same rule for the second read).
   - skip a candidate whose answered row `managerAnswerCoversCurrentPark(row, since)` is true.
   - `managerAnswerCoversCurrentPark`: `since == nil` → **false** (an undatable answer suppresses nothing); else `row.CreatedAt.Time().After(since.Time())`. ⚠️ An answered row **predating** `park_since` belongs to a previous park and must not suppress a later lost park.
   - `managerRepostMechanism`: `registry:waiting` and `busy+parked-modal` → `AckAnswerMechanism` (a native permission modal gets a card with **no input field and no options**); every other reason → `MessageAnswerMechanism`.

7. `ManagerGateTracker` — the component that owns one manager's history:

```go
type ManagerGateTracker interface {
	Observe(ctx context.Context, in ManagerGateObserveInput) (ManagerGateOutcome, error)
}

type ManagerGateObserveInput struct {
	Manager  string
	States   ManagerGateStates
	Open     *ManagerOpenView
	Answered *ManagerAnswerView
}

type ManagerGateOutcome struct {
	Records []ManagerGateRecord
	Reposts []ManagerRepostRequest
}

func NewManagerGateTracker(stateDir string, currentDateTimeGetter libtime.CurrentDateTimeGetter) ManagerGateTracker
```

`Observe` runs one pass: load `prev` and the park ages for `in.Manager`; on an absent or corrupt file log a warning and treat the pass as a first run: the FIRST pass writes the state and emits nothing (with `pending` empty no session is `stable`, so prompt 3's stability gate suppresses it), and the SECOND pass emits the current gated set once. ⚠️ Do not emit on the first pass — that contradicts the stability gate and would make the restart spec pass for the wrong reason; compute `gatedNow` and `parkedNow` from `in.States`; `stable := managerStableSessions(prevKeys, t.pending[manager], gatedNow)`; `records, nextPrev := managerTransitions(prev, in.States, stable)`; advance and age the park clock; append the `AGED` records; compute the re-post plan; save `nextPrev` and the ages atomically; record `gatedNow` as `t.pending[manager]`; return the outcome. Guard the tracker's in-memory `pending` map with a mutex.

8. Specs (package `pkg`, one `_test.go` per concern, or one `pkg/manager-gate-history_internal_test.go`):
   - `It("NewGateIsContentKeyed", ...)` — a session held at `idle+closer` whose detail changes from `approve: …` to `pick — …` re-emits `NEW GATE`; an unchanged detail emits nothing on the second pass.
   - `It("AgedEscalatesOncePerThreshold", ...)` — with the injected clock, a park crossing 15 minutes emits one `AGED`; crossing 60 emits one more; neither repeats on later passes at the same age.
   - `It("AgedOnlyForParkedReasons", ...)` — a `stuck:stream-closed` gate held past 60 minutes emits **zero** `AGED`; assert zero for every reason outside `ManagerParkedReasons`.
   - `It("RepostOnlyWhenNoCardOpen", ...)` — (a) parked, no open card, no answer → one request; (b) an open card (`park:<sid8>` in dedup keys, or the producer in producers) → none; (c) a permission-class reason → `AckAnswerMechanism` with `len(Options) == 0`.
   - `It("AnswerPredatingParkDoesNotSuppress", ...)` — an answered row **after** `park_since` suppresses; the same row **before** `park_since` re-posts; a nil `since` re-posts.
   - `It("StoreReadFailureSuppressesRepost", ...)` — `Open == nil` → none; `Answered == nil` → none.
   - `It("StatePersistsAcrossRestart", ...)` — tracker A over a state dir emits `NEW GATE`s and writes the state; tracker B (new instance, same dir) over the same states emits **no** `NEW GATE`; with the state file removed tracker C re-emits once and a fourth run emits none; a park open across the restart keeps its age so its next `AGED` lands at the original age.
   - `It("StateIsScopedPerManager", ...)` — two trackers over two different tracked sets, one state dir: after a first poll by each, neither emits a `CLEARED` for the other's sessions, and two distinct `state-<scope>.json` files exist.

9. Before finishing: re-run `<verification>` and confirm each line passes; walk AC4, AC5, AC8, AC12 and AC14 against the change.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass.
- `NEW GATE` is keyed on `reason :: detail`, not on session id and not on session+reason.
- `AGED` fires once per threshold per continuous park, and only for `ManagerParkedReasons`; a `stuck:` gate is a stall and never ages.
- A failed store read is `None`, never an empty set: `Open` and `Answered` nil views must suppress the re-post rather than fire it for every parked worker.
- An answered `park:` row predating the current park does **not** suppress a later lost park; a `since` the caller does not have means do **not** suppress.
- A permission-class park posts an `ack` card with no input field and no options.
- The previous set, the content keys and the park ages are persisted per manager and reloaded on start; both files are keyed on the tracked-set identity, never one shared `state.json`, and a corrupt state file warns and is treated as a first run.
- Writes are atomic (`tmp` + `os.Rename`); never a bare open-and-truncate.
- `PARK_AGE_THRESHOLDS` and the park-age window are constants, not config.
- Reuse the injected `libtime.CurrentDateTimeGetter`; never `time.Now()`.
- `pkg/` is Ginkgo-only — a bare `go test -run Test<Name>` selects nothing and exits 0.
- New files carry the repo's license header.
</constraints>

<verification>
- `go test ./pkg -run TestSuite -ginkgo.focus='NewGateIsContentKeyed' -ginkgo.fail-on-empty` — exits 0
- `go test ./pkg -run TestSuite -ginkgo.focus='AgedEscalatesOncePerThreshold' -ginkgo.fail-on-empty` — exits 0
- `go test ./pkg -run TestSuite -ginkgo.focus='AgedOnlyForParkedReasons' -ginkgo.fail-on-empty` — exits 0
- `go test ./pkg -run TestSuite -ginkgo.focus='RepostOnlyWhenNoCardOpen' -ginkgo.fail-on-empty` — exits 0
- `go test ./pkg -run TestSuite -ginkgo.focus='AnswerPredatingParkDoesNotSuppress' -ginkgo.fail-on-empty` — exits 0
- `go test ./pkg -run TestSuite -ginkgo.focus='StoreReadFailureSuppressesRepost' -ginkgo.fail-on-empty` — exits 0
- `go test ./pkg -run TestSuite -ginkgo.focus='StatePersistsAcrossRestart' -ginkgo.fail-on-empty` — exits 0
- `go test ./pkg -run TestSuite -ginkgo.focus='StateIsScopedPerManager' -ginkgo.fail-on-empty` — exits 0
- `grep -q 'ManagerParkedReasons' pkg/manager-gate-park.go` — exits 0
- `grep -q 'PARKED_REASONS' pkg/*.go` — exits 0 (the source name is named in a comment)
- `grep -q 'managerStatePath' pkg/manager-gate-state.go` — exits 0
- `grep -q 'managerRepostPlan' pkg/manager-gate-repost.go` — exits 0
- `make test` — the Ginkgo suite passes, including the new specs
- `make precommit` — exits 0
</verification>
