---
status: completed
spec: [013-bug-multi-select-recommended]
summary: Made the recommended-option bound cardinality-aware across the option list, item, push request and question unit, with specs at store, HTTP and renderer layers, and a changelog entry.
execution_id: attention-controller-multiselect-exec-048-spec-013-cardinality-aware-recommended
dark-factory-version: v0.198.7
created: "2026-10-20T12:47:34Z"
queued: "2026-10-20T13:01:27Z"
started: "2026-10-20T13:01:30Z"
completed: "2026-10-20T13:05:57Z"
branch: dark-factory/bug-multi-select-recommended
---

# Accept several recommendations on a multi-select card

<summary>
- A multi-select question may now carry a recommendation on every option the asker marked, and the store accepts it.
- A single-select question, or one that declares no cardinality, still carries at most one recommendation and is still rejected above that.
- A multi-question card is judged question by question, so a multi-pick tab can carry two flags while a single-pick tab on the same card still cannot.
- The rejection message for the single-select case is unchanged, so the operator reads exactly what they read before.
- An ask carrying two accidental markers is still delivered with one flag rather than dropped.
- The board renders every flag it is handed, so it now shows two markers on a multi-select card without any renderer change.
- The board renderer is untouched, byte for byte.
- The Claude Code hook that builds the option list is a separate change in another repo and is not part of this prompt.
- `make precommit` passes.
</summary>

<objective>
The schema's recommended-count rule is stale: it bounds every option list to one recommendation, but `answer_cardinality: multiple` declares a checkbox group whose asker is told to mark each option it would take. Thread the unit's existing cardinality into that rule at all three validation call sites, so a `multiple` unit is unconstrained and a `single` (or absent) unit keeps today's bound, with the existing error text. The board needs no change — it already draws every flag it is handed.
</objective>

<context>
There is no `CLAUDE.md` in this repo — follow the coding plugin docs below. Read `docs/dod.md` for the definition of done.

Read `pkg/answer-option.go` — `AnswerOption` (the `Recommended bool` field and its comment), `AnswerOptions`, `AnswerOptions.Validate`, `validateLabels` and `validateSingleRecommended`. This is the only place the at-most-one rule is implemented; it currently has no cardinality in scope, which is the defect.

Read `pkg/answer-cardinality.go` — `AnswerCardinality`, `SingleAnswerCardinality`, `MultipleAnswerCardinality` and `AnswerCardinality.Validate`. An absent value (`""`) validates and reads as `single`; `Validate` itself must stay unchanged.

Read `pkg/attention-item.go` — `Item.Validate` and `Item.validateOptions`. The latter early-returns on an empty list, enforces the message-only rule, then calls `i.Options.Validate(ctx)`. The item's own declaration is `i.AnswerCardinality`.

Read `pkg/attention-store.go` — `PushRequest.Validate` and `PushRequest.validateOptions`, the push-time twin of the item rule. It early-returns on an empty list, enforces the message-only rule, then calls `p.Options.Validate(ctx)`. The request's own declaration is `p.AnswerCardinality`. `pkg/attention-store-impl.go`'s `attentionStore.Push` calls `request.Validate(ctx)` before the write transaction, and `newItem` calls `item.Validate(ctx)` after building the item — both call sites must agree, which is why both are changed.

Read `pkg/question.go` — `Question` (the `Cardinality` and `Options` fields) and `Question.Validate`. Its `validation.All` currently passes `q.Options` directly as a validator, so the question unit's own `q.Cardinality` never reaches the recommended rule.

Read `pkg/answer_test.go` — the outer `Describe("Board answers", ...)` sets up a real `libboltkv.OpenTemp` database, a real `pkg.NewAttentionStore`, and the `messageRequest()` closure. The nested `Describe("Options and context", ...)` holds the existing recommended specs, including `rejects two recommended options` (which covers the absent-cardinality control).

Read `pkg/question_test.go` — the `pushRequest(dedupKey string) pkg.PushRequest` closure, and the nested `Describe("Push", ...)` block where the multi-question round trip lives. Its import block does not currently import `github.com/bborbe/errors` or `github.com/bborbe/validation`.

Read `pkg/handler/attention-board-answers_test.go` — `Describe("Board answers over HTTP", ...)`, the `messageBody` literal (it carries no `answer_cardinality`), the `push(body string)` closure, and `Describe("Push", ...)` with its existing `rejects two recommended options with 400` spec. `NewAttentionPushHandler` decodes the JSON body into `pkg.PushRequest` and calls `validatePushRequest`, which is `PushRequest.Validate` — so an HTTP spec here traverses the JSON field name, the request gate and the store's second gate.

Read `pkg/handler/attention-page_test.go` — the `messageRequest(dedupKey, cardinality)` closure (one option is already recommended), the `rowOf(body, itemID)` helper that scopes an assertion to one card, and the existing spec `renders the cardinality hint and marks the recommended option`. The renderer itself is `pkg/handler/attention-page.go` and must not be edited.

Read `CHANGELOG.md` — its top. It has no `## Unreleased` section; the preamble is followed directly by `## v0.59.0`.

Reference docs (in-container paths):
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-error-wrapping-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-testing-guide.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/go-patterns.md`
- `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`
</context>

<requirements>
1. In `pkg/answer-option.go`, make the recommended-count rule cardinality-aware while keeping `AnswerOptions.Validate` (the `validation.HasValidation` method) behaviourally unchanged for a single-pick list.

   a. Change the `Recommended` field comment from the unconditional bound to the per-unit rule:

   ```go
   // Recommended marks the producer's recommendation. At most one option may
   // carry it when the question takes a single pick; a multiple-pick question
   // may carry several, one per option the asker would take.
   Recommended bool `json:"recommended"`
   ```

   b. Replace the body of `AnswerOptions.Validate` with a delegation. Rewrite its doc comment so it states that it validates a list whose cardinality is absent (which reads as `single`) and that callers holding a cardinality call `ValidateForCardinality`. Keep the existing sentence that an empty list validates — `options` is optional, and absent is what every `permission`- and `ack`-class item carries:

   ```go
   func (o AnswerOptions) Validate(ctx context.Context) error {
       return o.ValidateForCardinality(ctx, "")
   }
   ```

   c. Add `ValidateForCardinality` beside it, with exactly this contract:

   ```go
   // ValidateForCardinality returns an error when any option carries no label, or
   // when more than one carries Recommended on a unit that takes a single pick.
   //
   // A multiple-pick unit may carry any number of recommendations: the /ask rule
   // tells a multi-select asker to mark each option it would take, and the board
   // renders every flag it is handed. Only MultipleAnswerCardinality relaxes the
   // bound; `single`, an absent value and an unknown value all keep it.
   func (o AnswerOptions) ValidateForCardinality(
       ctx context.Context,
       cardinality AnswerCardinality,
   ) error {
       return validation.All{
           validation.Name("Labels", validation.HasValidationFunc(o.validateLabels)),
           validation.Name(
               "Recommended",
               validation.HasValidationFunc(func(ctx context.Context) error {
                   return o.validateRecommended(ctx, cardinality)
               }),
           ),
       }.Validate(ctx)
   }
   ```

   d. Rename `validateSingleRecommended` to `validateRecommended`, add the cardinality parameter, and short-circuit on `MultipleAnswerCardinality`. Keep the counting loop and the error exactly as they are — the format string must remain `"%d options are marked recommended, at most 1 is allowed"` and the sentinel must remain `validation.Error`:

   ```go
   // validateRecommended returns an error when more than one option carries
   // Recommended on a unit that takes a single pick. A multiple-pick unit is
   // unconstrained: every option the asker marked is a recommendation the board
   // should show.
   func (o AnswerOptions) validateRecommended(
       ctx context.Context,
       cardinality AnswerCardinality,
   ) error {
       if cardinality == MultipleAnswerCardinality {
           return nil
       }
       recommended := 0
       for _, option := range o {
           if option.Recommended {
               recommended++
           }
       }
       if recommended > 1 {
           return errors.Wrapf(
               ctx,
               validation.Error,
               "%d options are marked recommended, at most 1 is allowed",
               recommended,
           )
       }
       return nil
   }
   ```

   `validateLabels` is unchanged.

2. In `pkg/attention-item.go`, change the last statement of `Item.validateOptions` so the item's own cardinality reaches the rule. The early return on an empty list and the message-only check above it are unchanged:

   ```go
   return i.Options.ValidateForCardinality(ctx, i.AnswerCardinality)
   ```

3. In `pkg/attention-store.go`, change the last statement of `PushRequest.validateOptions` in the same way, so the push-time gate and the store-time gate cannot disagree:

   ```go
   return p.Options.ValidateForCardinality(ctx, p.AnswerCardinality)
   ```

4. In `pkg/question.go`, change the `"Options"` entry of `Question.Validate`'s `validation.All` so the question unit's own cardinality is used. The `Tab`, `Payload` and `Cardinality` entries stay exactly as they are:

   ```go
   validation.Name(
       "Options",
       validation.HasValidationFunc(func(ctx context.Context) error {
           return q.Options.ValidateForCardinality(ctx, q.Cardinality)
       }),
   ),
   ```

   This is the per-unit half: a multi-question item must not validate every tab against the item-level field.

5. In `pkg/answer_test.go`, inside `Describe("Options and context", ...)`, add three specs. Every new spec name must contain the lowercase word `recommended`, because the verification selects on `-ginkgo.focus='recommended'`.

   a. `It("validates an option list's recommended count against the cardinality it is given", ...)` — call the new method directly (the library-validator boundary), with two `Recommended: true` options: nil for `pkg.MultipleAnswerCardinality`; non-nil for `pkg.SingleAnswerCardinality`, with `errors.Is(err, validation.Error)` true and `err.Error()` containing `at most 1 is allowed`; non-nil for `""` (absent reads as single).

   b. `It("accepts two recommended options on a message item declared multiple", ...)` — start from `messageRequest()`, set `request.AnswerCardinality = pkg.MultipleAnswerCardinality` and `request.Options[1].Recommended = true`, then `store.Push` must return nil error and `store.Get` must return two options with `Recommended` true on both. The read-back is the point: it proves the flag survives the JSON round trip rather than only passing validation.

   c. `It("rejects two recommended options on a message item declared single", ...)` — the same request with `pkg.SingleAnswerCardinality`; `store.Push` must return an error for which `errors.Is(err, validation.Error)` is true, and whose text contains `at most 1 is allowed`.

   d. Strengthen the existing `It("rejects two recommended options", ...)` — the absent-cardinality control — with the same `ContainSubstring("at most 1 is allowed")` assertion. Do not remove it: it is the proof that an item pushed before the field existed keeps its current validation.

6. In `pkg/question_test.go`, inside `Describe("Push", ...)`, add two specs and add `"github.com/bborbe/errors"` and `"github.com/bborbe/validation"` to the third-party import group.

   a. `It("accepts two recommended options in a question unit declared multiple", ...)` — build the request with `pushRequest("card-per-unit-multiple")` and two questions: `"Chores"` with `Cardinality: pkg.MultipleAnswerCardinality` and two `Recommended: true` options, and `"Priority"` with no cardinality and one `Recommended: true` option. `store.Push` must succeed, and `store.Get` must return both flags on the first unit and the one flag on the second.

   b. `It("rejects two recommended options in a question unit declared single", ...)` — build `pushRequest("card-per-unit-single")`, set the ITEM-level `request.AnswerCardinality = pkg.MultipleAnswerCardinality` on purpose (the item-level value must not leak into the unit), and give it one question with `Cardinality: pkg.SingleAnswerCardinality` carrying two `Recommended: true` options. `store.Push` must return an error for which `errors.Is(err, validation.Error)` is true and whose text contains `at most 1 is allowed`. Add a short comment saying why the item-level cardinality is deliberately the permissive one.

7. In `pkg/handler/attention-board-answers_test.go`, inside `Describe("Push", ...)`, add one HTTP-level spec and strengthen the existing one:

   a. `It("accepts a multiple-cardinality push carrying two recommended options over HTTP", ...)` — derive the body from `messageBody` with two `strings.Replace` calls: replace `"answer_mechanism": "message"` with `"answer_mechanism": "message", "answer_cardinality": "multiple"`, then replace `{"label": "the tab"}` with `{"label": "the tab", "recommended": true}`. `push(body)` must return `http.StatusCreated`; unmarshal the response into `pkg.Item` and assert both options carry `Recommended: true`. This traverses the JSON field name `answer_cardinality`, the handler's `PushRequest.Validate` and the store's own gate.

   b. In the existing `It("rejects two recommended options with 400", ...)`, add `Expect(rec.Body.String()).To(ContainSubstring("at most 1 is allowed"))` after the status assertion. The body carries no `answer_cardinality`, so it is the absent-cardinality HTTP control and must keep returning `400`.

8. In `pkg/handler/attention-page_test.go`, add one renderer spec — this edits the TEST file only; `pkg/handler/attention-page.go` stays frozen. Place it after the existing `renders the cardinality hint and marks the recommended option` spec, inside the same `Describe`:

   `It("renders a marker for every recommended option on a multi-select card", ...)` — build the request with `messageRequest("gate-two-recommended", pkg.MultipleAnswerCardinality)`, set `request.Options[1].Recommended = true`, push it, and scope the assertion with `rowOf(get("GET").Body.String(), item.ItemID)`. Assert `strings.Count(block, "(Recommended)")` equals `2`, and that the string `class="recommended"` occurs twice in `block`. This is the build-time evidence for the desired behavior "the board renders two markers for a multi-select card carrying two flags, with no renderer change"; `rowOf` keeps other cards from inflating the count.

9. In `CHANGELOG.md`, add a `## Unreleased` section immediately after the SemVer preamble block (the three MAJOR/MINOR/PATCH bullets) and above `## v0.59.0`, with exactly one `- fix:` bullet. Follow `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`; describe what was implemented, not what was verified. The bullet must name the per-unit rule, the three call sites, the unchanged single/absent behaviour and message, and the fact that the renderer and the hook are unchanged. Do not add a second bullet for the tests.

10. Before finishing, re-run every `<verification>` command and confirm it passes, then walk each store-side acceptance criterion in `specs/in-progress/013-bug-multi-select-recommended.md` against the change and confirm it holds. The Post-Deploy (Rung-2) checks — the `curl` reproduction and the deployed-build identity — run on the host after merge and are not attempted in this container. The hook-side criteria (ACs 4-7) are out of scope for this prompt.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Do NOT modify `pkg/handler/attention-page.go`. It is frozen by the spec: it already draws every flag it is handed, and the negative acceptance criterion is that the file is unchanged. The `<verification>` checks its SHA-256 because this container has no `.git`. Adding a spec to `pkg/handler/attention-page_test.go` is expected; editing the template is not.
- Do NOT touch the Claude Code hook (`~/.claude/hooks/attention-log.py`) or any file in another repo. The hook half (DBs 4-5, ACs 4-7) is a direct edit in `bborbe/claude` owned by the vault task; it is deliberately not part of this prompt.
- Do NOT widen the schema: `answer_cardinality`, `questions`, `options` and the answer-routing contract keep their current shapes. This change consults an existing field; it adds no field and no migration.
- Do NOT change the error message text for the single-select rejection. It must remain `"%d options are marked recommended, at most 1 is allowed"` — existing tests assert it and the operator reads it.
- An absent `answer_cardinality` continues to read as `single`, so every item pushed before the field existed keeps its current validation.
- The `permission` and `ack` classes are unaffected: they carry no `options` list, and the message-only rules in `Item.validateOptions`, `PushRequest.validateOptions` and `Item.validateAnswerCardinality` stay exactly as they are.
- Do NOT add a configuration switch, opt-out flag, allowlist or tunable threshold. The rule is a schema rule, applied unconditionally to the unit's declared cardinality.
- Do NOT edit `scenarios/001-board-browser-cases.md` or `scenarios/002-board-answer-shapes.md`. Both assert a hard `36 of 36 Specs` total and this change adds no e2e case; the totals stay as they are.
- In-code comments must move with the rule. The unconditional-bound comments in `pkg/answer-option.go` (the `Recommended` field comment, the `Validate` comment and the old `validateSingleRecommended` comment) must be rewritten, not left contradicting the code.
- Tests follow the repo's conventions: Ginkgo/Gomega, an external `_test` package, and a real `libboltkv.OpenTemp` database rather than a mock.
- Existing tests must still pass.
- Error handling follows `github.com/bborbe/errors` — `errors.Wrapf` / `errors.Wrap`, never `fmt.Errorf`, never `context.Background()` inside `pkg/`.
- `hideGit` is in effect: the container has no `.git`. Do NOT run a bare `git` command in this prompt or in `<verification>`; it fails with `fatal: not a git repository` and the daemon does not check verification exit codes, so a git-based check silently "passes".
- No absolute or home-relative paths in code.
</constraints>

<verification>
- Focused specs. Run the focused suite once, capturing its output, then confirm each new spec name is present. `-v` is required IN ADDITION to `-ginkgo.v`: without `-v`, `go test` discards the package's stdout and no spec name is printed (verified on this suite — the spec's own command omits `-v`).

  ```bash
  go test -v -mod=mod ./pkg/... -run TestSuite -args -ginkgo.focus='recommended' -ginkgo.no-color -ginkgo.v > /tmp/recommended.log 2>&1
  ```

  The command must exit 0, and each of these must print a number ≥ 1:

  ```bash
  grep -c -F "validates an option list's recommended count against the cardinality it is given" /tmp/recommended.log
  grep -c -F "accepts two recommended options on a message item declared multiple" /tmp/recommended.log
  grep -c -F "rejects two recommended options on a message item declared single" /tmp/recommended.log
  grep -c -F "accepts two recommended options in a question unit declared multiple" /tmp/recommended.log
  grep -c -F "rejects two recommended options in a question unit declared single" /tmp/recommended.log
  grep -c -F "accepts a multiple-cardinality push carrying two recommended options over HTTP" /tmp/recommended.log
  grep -c -F "renders a marker for every recommended option on a multi-select card" /tmp/recommended.log
  ```

  Use double quotes, never single: one name contains an apostrophe. A count of 0 makes `grep` exit 1, so a missing spec is visible rather than silently passing.

- The new method exists and the old unconditional helper is gone:

  ```bash
  grep -c 'ValidateForCardinality' pkg/answer-option.go
  grep -c 'ValidateForCardinality' pkg/attention-item.go
  grep -c 'ValidateForCardinality' pkg/attention-store.go
  grep -c 'ValidateForCardinality' pkg/question.go
  ```

  Each must print ≥ 1; the first must print ≥ 2 (the method plus the delegation in `Validate`). And:

  ```bash
  ! grep -q 'validateSingleRecommended' pkg/answer-option.go
  ```

  must succeed (no match).

- The rejection text is unchanged and still appears exactly once in the source:

  ```bash
  grep -c '%d options are marked recommended, at most 1 is allowed' pkg/answer-option.go
  ```

  must print `1`, and `grep -n 'at most 1 is allowed' pkg/answer-option.go` must show that line.

- The board renderer is unchanged. The container has no `.git`, so the spec's host-side renderer diff cannot run here; this checksum is the container-side proxy for that acceptance criterion, and the diff remains operator-side:

  ```bash
  sha256sum pkg/handler/attention-page.go
  ```

  must print exactly:

  ```
  41d9f2b6257cc7a035f8f2f51db678a22e0348777df3cc44976fb88113891235  pkg/handler/attention-page.go
  ```

  If it differs, the frozen renderer was modified — restore it.

- No scenario was added and the hard totals are untouched:

  ```bash
  ls scenarios/*.md | wc -l
  grep -c '36 of 36' scenarios/001-board-browser-cases.md
  grep -c '36 of 36' scenarios/002-board-answer-shapes.md
  ```

  must print `2`, `1`, `1`.

- The changelog section exists and is the first one:

  ```bash
  grep -n '^## Unreleased' CHANGELOG.md
  awk '/^## /{sec=$0} /^- fix:/{print sec}' CHANGELOG.md | head -1
  ```

  The first must print exactly one line; the second must print `## Unreleased`.

- `ROOTDIR=/workspace make precommit` — must exit 0. (`hideGit` is in effect, so pass `ROOTDIR=/workspace` on every `make` call; never run a bare `git` command.)
</verification>
