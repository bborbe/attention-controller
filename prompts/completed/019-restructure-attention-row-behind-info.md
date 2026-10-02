---
status: completed
spec: [005-card-metadata-behind-info]
summary: Restructured the attention-row card so the ask leads and the producer/provenance/meta machine identity relocates into a per-card info panel behind an `i` toggle
execution_id: attention-controller-card-info-exec-019-restructure-attention-row-behind-info
dark-factory-version: v0.196.0
created: "2026-10-02T08:03:00Z"
queued: "2026-10-02T08:22:35Z"
started: "2026-10-02T08:22:37Z"
completed: "2026-10-02T08:28:23Z"
---

# Restructure the attention row so the ask leads and the machine identity sits behind an info panel

<summary>
- A card on the attention board leads with the ask it exists to deliver — the question, the permission text or the payload — instead of a session UUID.
- The producer line, the host/cwd/tool/pane provenance values and the `state - createdAt` footer move into a per-card information panel that ships hidden in the served markup.
- A small `i` control renders at the card's top right on every card that carries machine identity; it opens that card's panel.
- A card that carries no machine identity at all renders no control and no panel — never a control that opens onto nothing.
- The task, goal, topic and session-name spans stay on the card face, after the ask, in their existing order and with their existing separator.
- Every value that renders today still renders; only its position changes, and the panel carries that card's own values.
- Existing handler specs that asserted a machine-only provenance line are updated to the new placement, so the suite stays green.
- The board's own build-identity footer, the corner X, the read-aloud toggle, the jump control and the answer form are untouched.
</summary>

<objective>
Restructure the `attention-row` sub-template so that a card's first readable line is the ask it exists to deliver, and the machine identity that currently frames it (producer line, host/cwd/tool/pane values, `state - createdAt` footer) renders inside a new per-card information panel behind an `i` affordance at the card's top right. The operator's own report on 2026-09-27 was that cards carry "a lot of information at the top and at the bottom that show UUID and opened and other stuff", and the cost is paid on every triage of every card. This prompt ships the markup, the stylesheet and the row derivation; a sibling prompt wires the control's click behaviour.
</objective>

<context>
Read `docs/dod.md` for this repo's definition of done. (The repo's `CLAUDE.md` is gitignored and is **not** present in this worktree; do not go looking for it.)

Read `pkg/handler/attention-page.go` in full enough to know these regions (line numbers are hints only, measured 2026-10-02 — anchor on the strings):
- the `attentionPageTemplate` string constant and its `<style>` block;
- the `attention-row` sub-template, defined by `{{define "attention-row"}}` and closed by `</li>{{end}}`;
- the `attentionPageRow` struct.

Read `pkg/handler/attention-page-helpers.go` — `newAttentionPageRow`, `affordance`, `jumpCommand`, `jumpURL`, `noJumpReason`.

Read `pkg/provenance.go` — the `Provenance` struct and `Resolved()`.

Read `/home/node/.claude/plugins/marketplaces/coding/docs/go-doc-best-practices.md` for the comment style this file uses (every declaration carries a comment that starts with its own name).

**The current `attention-row` sub-template, verbatim as it stands (this is the region you are restructuring):**

```
{{define "attention-row"}}<li class="item{{if .Dimmed}} dimmed{{end}}" data-item-id="{{ .Item.ItemID }}">
<div class="producer">{{ .Item.ProducerID }} ({{ .Item.ProducerKind }})</div>
{{if not .Dimmed}}<button type="button" class="corner-x" data-corner-x aria-label="Skip this item">✕</button>{{end}}
{{if and .Speak (not .Dimmed)}}<button type="button" class="speak" ...>...</button>
{{end}}{{if .JumpURL}}<button type="button" class="jump-corner" data-jump="{{ .JumpURL }}" ...>...</button>
{{else if .NoJump}}<button type="button" class="jump-corner" disabled ...>...</button>
{{end}}{{if not .Message}}<div class="payload">{{ .Item.Payload }}</div>
{{end}}{{if .Item.Context}}<div class="context">{{ .Item.Context }}</div>
{{end}}{{if .Provenance.Resolved}}<div class="provenance">…nav spans… …machine spans…</div>
{{end}}{{if .Dimmed}}<div class="record">…
{{else if .Message}}<form class="answer" data-multi="{{ .Tabs }}">…</form>
{{end}}{{if and .Ack (not .Dimmed)}}…
{{end}}{{if and .Decide (not .Dimmed)}}…
{{end}}{{if or .Jump .JumpURL}}<div class="jump">…
{{else if .NoJump}}<div class="jump-reason">…
{{end}}<div class="meta">{{ .Item.State }} - {{ .Item.CreatedAt }}</div>
</li>{{end}}
```

The `.provenance` line is **one long line** today. It renders, in order: the `task` link span, the `goal` link span, the `topic` link span, then the `host`, `cwd`, `tool`, `pane`-or-`unroutable` spans, then the `session-name` span. The `host`/`cwd`/`tool`/`pane`/`unroutable` spans are the **machine identity**; the `task`/`goal`/`topic`/`session-name` spans are **navigation** and stay on the card face.

**The fields you will derive on, verbatim from the source:**

```go
// pkg.Item (pkg/attention-item.go)
ProducerID   ProducerID   `json:"producer_id"`
ProducerKind ProducerKind `json:"producer_kind"`
State        State        `json:"state"`
CreatedAt    libtime.DateTime `json:"created_at"`   // github.com/bborbe/time

// pkg.Provenance (pkg/provenance.go)
Host string; Cwd string; Tool string; Pane string
PaneRecorded bool; Routable bool
TaskName string; TaskPath string; GoalName string; GoalPath string
TopicName string; TopicPath string; SessionName string; Headless bool
```

`libtime.DateTime` is a struct wrapper over `stdtime.Time`; its `Time()` method is `func (d DateTime) Time() stdtime.Time` — a **value** receiver in the pinned `github.com/bborbe/time v1.27.14` (`time_date-time.go:197`) — and a zero value is what an item with no timestamp carries. In a Go template a `libtime.DateTime` value is **always truthy** (it is a struct), so `{{if .Item.CreatedAt}}` can never be used to test for absence — that is why the meta line becomes a derived string. ⚠️ **Its `String()` is `d.Format(stdtime.RFC3339Nano)` (`time_date-time.go:135`), so a ZERO value renders as `0001-01-01T00:00:00Z` — a NON-empty string.** Never test absence by comparing `String()` against `""`, and never let a zero timestamp reach the panel: the absence test is `item.CreatedAt.Time().IsZero()`, which is what requirement 2 uses.

**Acceptance-criteria ownership for this set.** This prompt ships the markup and the derivation; it owns **no** acceptance criterion. The served-page assertions are prompt 3's and the browser assertions are prompt 4's. Do not write new specs here beyond the minimal edits the changed markup forces on existing ones.
</context>

<requirements>
1. **Add two derived fields to `attentionPageRow`** in `pkg/handler/attention-page.go`, immediately after the `Speak bool` field, each with a doc comment that starts with its own name and says what it gates:

   ```go
   // Info reports whether this row carries machine identity — a producer id or
   // kind, a machine provenance value, or a state/timestamp — and therefore
   // renders the info-toggle and its panel. A row carrying none of them renders
   // no control at all, never a control that opens onto nothing.
   Info bool
   // Meta is the panel's state-and-timestamp line. Empty when the item carries
   // neither, so the panel draws no element for an absent value.
   Meta string
   ```

2. **Add the derivation helper** `infoMetaLine(item pkg.Item) string` to `pkg/handler/attention-page-helpers.go` (package `handler`, beside `noJumpReason`), with a doc comment starting with its own name. It returns:
   - `""` when `item.State` is empty **and** `item.CreatedAt.Time().IsZero()`;
   - `item.State.String()` when the timestamp is zero and the state is not;
   - `item.CreatedAt.String()` when the state is empty and the timestamp is not;
   - `item.State.String() + " - " + item.CreatedAt.String()` otherwise.

   The separator is a single ASCII hyphen with one space either side, exactly as the current meta line renders it.

   **Cover all four branches.** `infoMetaLine` is a pure function over two fields, so add a Ginkgo case that drives each branch directly with the four inputs and asserts its exact string. ⚠️ **Do not rely on the store's own rows to reach the two single-fact branches** — a real `store.Push` always sets both `State` and `CreatedAt`, so "state set, timestamp zero" and "state empty, timestamp set" are unreachable through the served page and would otherwise ship unexercised, which this repo's `docs/dod.md` § Tests ("New functions have tests") does not allow.

3. **Compute both fields in `newAttentionPageRow`** (`pkg/handler/attention-page-helpers.go`), after the `row.Message, row.Ack = affordance(item.AnswerMechanism)` line:

   ```go
   row.Info = item.ProducerID != "" ||
       item.ProducerKind != "" ||
       provenance.Host != "" ||
       provenance.Cwd != "" ||
       provenance.Tool != "" ||
       provenance.Pane != "" ||
       provenance.PaneRecorded ||
       item.State != "" ||
       !item.CreatedAt.Time().IsZero()
   row.Meta = infoMetaLine(item)
   ```

   Add a comment saying that `Info` is the gate for both the control and the panel so they cannot disagree, and that it is the implementation of `[[Attention Item Schema]]` silence 26's placement rule rather than a restatement of it.

4. **Delete the `.producer` div from the top of the `attention-row` sub-template** — the line

   ```
   <div class="producer">{{ .Item.ProducerID }} ({{ .Item.ProducerKind }})</div>
   ```

   immediately after the `<li class="item…" …>` opening line. Its markup moves into the panel in requirement 7, unchanged.

5. **Add the info-toggle button** to the corner-controls group, immediately after the `jump-corner` fragment and before the `{{if not .Message}}<div class="payload">` fragment:

   ```
   {{if .Info}}<button type="button" class="info-toggle" data-info-toggle aria-expanded="false" aria-label="Card information">i</button>
   {{end}}
   ```

   The visible glyph is a plain text `i`; the accessible name is the `aria-label`. Do **not** put the control's meaning in a `title` alone, and do **not** make the button icon-only-with-SVG — the operator's ask is "a little icon with \"i\" for information at the top right", and v0.18.0 rejected an icon-only read-aloud control on this board because its meaning lived in a hover-only `title` plus an `aria-label` only assistive tech sees.

6. **Replace the `.provenance` line** (the single long line beginning `{{end}}{{if .Provenance.Resolved}}<div class="provenance">`) with the navigation-only form below, and **move it** so it renders after the card body rather than before it:

   ```
   {{if or .TaskURL .GoalURL .TopicURL .Provenance.SessionName}}<div class="provenance">{{if .TaskURL}}<span class="task"><a href="{{ .TaskURL }}">{{ .Provenance.TaskName }}</a></span>{{end}}{{if .GoalURL}}<span class="goal"><a href="{{ .GoalURL }}">{{ .Provenance.GoalName }}</a></span>{{end}}{{if .TopicURL}}<span class="topic"><a href="{{ .TopicURL }}">{{ .Provenance.TopicName }}</a></span>{{end}}{{if .Provenance.SessionName}}<span class="session-name">{{ .Provenance.SessionName }}</span>{{end}}</div>
   {{end}}
   ```

   ⚠️ Three things are load-bearing here and each has a reason:
   - **The machine spans are gone from this line.** They move into the panel in requirement 7. This line now carries navigation only.
   - **The gate changes from `.Provenance.Resolved` to the four navigation values.** `Resolved()` is true for a host/cwd-only row, and such a row must now carry **no** card-face provenance line — its machine values are in the panel. Do not keep `.Provenance.Resolved` as the gate; an empty `<div class="provenance"></div>` is the placeholder this page forbids.
   - **The spans' own gates are unchanged.** `task`, `goal`, `topic` and `session-name` each still render only when their own value resolved, they keep their order among themselves, and `.provenance span + span::before` still draws the separator.
   - **It moves.** Delete it from its current position (between the `.context` div and the `{{if .Dimmed}}` record branch) and re-insert it immediately **before** the `{{if or .Jump .JumpURL}}<div class="jump">` fragment — i.e. between the `{{end}}` that closes the `{{if and .Decide (not .Dimmed)}}` block and the `{{if or .Jump .JumpURL}}` fragment. On a message card the ask is inside the answer form, which renders before this point, so this move is what puts the navigation after the ask.

7. **Replace the trailing `.meta` div** (the line `<div class="meta">{{ .Item.State }} - {{ .Item.CreatedAt }}</div>` immediately before `</li>{{end}}`) with the info panel, which becomes the last element of the row:

   ```
   {{if .Info}}<div class="info-panel" data-info-panel hidden>{{if or .Item.ProducerID .Item.ProducerKind}}<div class="producer">{{ .Item.ProducerID }} ({{ .Item.ProducerKind }})</div>{{end}}{{if .Provenance.Host}}<span class="host">{{ .Provenance.Host }}</span>{{end}}{{if .Provenance.Cwd}}<span class="cwd">{{ .Provenance.Cwd }}</span>{{end}}{{if .Provenance.Tool}}<span class="tool">{{ .Provenance.Tool }}</span>{{end}}{{if .Provenance.Pane}}<span class="pane">pane {{ .Provenance.Pane }}</span>{{else if .Provenance.PaneRecorded}}<span class="unroutable">unroutable</span>{{end}}{{if .Meta}}<div class="meta">{{ .Meta }}</div>{{end}}</div>
   {{end}}
   ```

   The panel is **server-rendered and carries the `hidden` attribute**, so a card with no JavaScript still serves its values and the markup a stream row-swap re-renders is the same markup a fresh load serves. The relocated classes are unchanged: `producer`, `host`, `cwd`, `tool`, `pane`, `unroutable`, `meta`. No new wrapper class is introduced for the machine spans — they are direct children of `.info-panel`.

8. **Add the stylesheet rules** to the `<style>` block in `attentionPageTemplate`, beside the `.provenance` and `.meta` rules:

   ```css
   .info-toggle {
     position: absolute;
     top: 10px;
     right: 120px;
     width: 28px;
     height: 28px;
     padding: 0;
     display: inline-flex;
     align-items: center;
     justify-content: center;
     background: transparent;
     color: var(--muted);
     border: 1px solid transparent;
     border-radius: 6px;
     font-family: inherit;
     font-size: 15px;
     line-height: 1;
     cursor: pointer;
   }
   .info-toggle:hover { color: var(--text); border-color: var(--border); }
   .info-toggle[aria-expanded="true"] { color: var(--text); border-color: var(--border); }
   .info-panel {
     color: var(--muted);
     font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
     font-size: 12px;
     margin: 8px 0 0;
   }
   .info-panel span + span::before { content: " · "; }
   .info-panel .unroutable { color: var(--warn); }
   ```

   The `right: 120px` is the corner cluster's next free slot and mirrors the existing geometry rather than sharing a rule with it: the corner X is pinned at `right: 12px`, the read-aloud control at `right: 48px` and the jump corner at `right: 84px`, all 28px wide with an 8px gap, so `right: 120px` places this control 8px to the jump corner's left and no control's position depends on another's. **Do not move the X, the read-aloud control or the jump control.** Add a short comment recording that geometry and that the `.info-panel span + span::before` rule re-expresses the line's separator for the relocated machine spans.

9. **Update the existing specs the moved markup breaks.** Each of these asserts a card-face provenance line on a row whose only resolved provenance is machine values; after the split such a row carries no `.provenance` div at all, so each must be re-anchored on the panel. Change only these assertions and their comments — do not weaken any other assertion.

   - `pkg/handler/attention-page_test.go`, the `It("renders no anchor for an item whose session anchors no task", …)` case: replace

     ```go
     Expect(row).To(ContainSubstring(`class="provenance"`))
     Expect(row).To(ContainSubstring(`<span class="host">burn</span>`))
     ```

     with

     ```go
     // The machine values relocated into the info panel; the card face carries
     // no navigation line because nothing navigational resolved.
     Expect(strings.Count(row, `class="info-panel"`)).To(Equal(1))
     Expect(row).To(ContainSubstring(`<span class="host">burn</span>`))
     ```

     ⚠️ **In the same case, a pre-existing comment two lines above that assertion becomes false and must be corrected in the same edit** — the row no longer renders a provenance line at all, so a comment calling it the positive control for that line is wrong. Replace

     ```go
     			// Positive control: the line rendered, so the absences below are a
     			// withheld link rather than an absent line.
     ```

     with

     ```go
     			// Positive control: the panel rendered, so the absences below are
     			// about the card face rather than a row that was not drawn.
     ```

   - `pkg/handler/attention-page_test.go`, the `It("draws no session-name span when the name is absent", …)` case: replace

     ```go
     Expect(strings.Count(row, `class="provenance"`)).To(Equal(1))
     Expect(strings.Count(row, `class="host"`)).To(Equal(1))
     ```

     with

     ```go
     Expect(strings.Count(row, `class="provenance"`)).To(Equal(0))
     Expect(strings.Count(row, `class="info-panel"`)).To(Equal(1))
     Expect(strings.Count(row, `class="host"`)).To(Equal(1))
     ```

   - `pkg/handler/attention-session-name-page_test.go`, the `It("a peer name renders no session-name span", …)` case: the row resolves a host and a cwd and nothing navigational, so its face carries no provenance div. Replace

     ```go
     Expect(strings.Count(row, `class="provenance"`)).To(Equal(1))
     Expect(strings.Count(row, `class="host"`)).To(Equal(1))
     Expect(strings.Count(row, `class="session-name"`)).To(Equal(0))
     ```

     with

     ```go
     // The card face carries no navigation line at all, so no placeholder can
     // stand where a span would; the machine values relocated into the panel.
     Expect(strings.Count(row, `class="provenance"`)).To(Equal(0))
     Expect(strings.Count(row, `class="info-panel"`)).To(Equal(1))
     Expect(strings.Count(row, `class="host"`)).To(Equal(1))
     Expect(strings.Count(row, `class="session-name"`)).To(Equal(0))
     ```

     and **delete** the trailing block

     ```go
     div := provenanceDivOf(row)
     Expect(div).NotTo(ContainSubstring("session-peer"))
     Expect(div).NotTo(ContainSubstring("unknown"))
     Expect(div).NotTo(ContainSubstring("-"))
     ```

     — the div it scopes no longer exists, and the div's absence is a stronger statement than the three assertions it carried. Keep the row-wide `NotTo(ContainSubstring("Peer Name"))`, `NotTo(ContainSubstring("unknown"))` and `NotTo(ContainSubstring("n/a"))` assertions above it, and update the surrounding comment to say why the dash assertion is gone (the row's own `.meta` line carries a ` - ` and now lives inside the panel, so a dash assertion has no scope that excludes it).

   - `pkg/handler/attention-session-name-page_test.go`, the `It("an unknown session renders no session-name span", …)` case: the same treatment — `class="provenance"` count `1` → `0`, add `class="info-panel"` count `1`, and delete the trailing `div := provenanceDivOf(row)` block with its comment updated the same way.

   - `pkg/handler/attention-session-name-page_test.go`: the local closure `provenanceDivOf` (defined near the `rowOf` helper) now has no caller. **Delete it** so `make precommit`'s linter does not flag an unused closure. If the linter does not flag it, delete it anyway — a helper kept for callers that no longer exist reads as coverage.

10. **Update the stale comments** in `pkg/handler/attention-goal-topic-page_test.go` and `pkg/handler/attention-session-name-page_test.go` that say the row's meta line renders `{{ .Item.State }} - {{ .Item.CreatedAt }}`; it now renders `{{ .Meta }}`. Change the sentence, not the assertions.

11. **Add a short comment above the new panel markup** in the template recording that this is `[[Attention Item Schema]]` silence 26's placement rule implemented — the ask leads, the machine identity relocates behind the affordance, the navigation spans stay on the face — and that the rule is implemented rather than restated. Keep it brief; the schema page owns the statement of the rule.

12. **`pkg/handler/attention-page.go` is 1,820 lines against revive's 2,000-line `file-length-limit`.** The template and its script deliberately stay in that file. If your change pushes the file over the limit, move the new `infoMetaLine` helper or the `attentionPageRow` doc comments into `pkg/handler/attention-page-helpers.go` — never move the template.

13. In `CHANGELOG.md`, add this change's entry under `## Unreleased` — create that section directly above the topmost `## v` section (the file currently starts its version sections at `## v0.33.0`). One bullet, prefix `feat:`, naming what a reader sees: a board card now leads with the ask it exists to deliver, and the producer line, the host/cwd/tool/pane provenance values and the `state - createdAt` footer move behind a per-card `i` affordance at the card's top right, revealed on activation; a card carrying no machine identity renders no affordance; the task, goal, topic and session-name spans stay on the card face after the ask, unchanged in order and separator; no value is deleted, and the board's build-identity footer, the corner X, the read-aloud toggle and the jump control are untouched. Follow `/home/node/.claude/plugins/marketplaces/coding/docs/changelog-guide.md`.

14. Self-check before finishing: re-run `<verification>` and confirm every line passes, then walk each numbered requirement above against the change you made.
</requirements>

<constraints>
- Do NOT commit — dark-factory handles git.
- Existing tests must still pass, unchanged except for the five assertion edits and the two comment edits named in requirements 9 and 10.
- **No deletion of any metadata.** Every value that renders today renders after this change; only its position changes.
- **The card-face spans keep their classes.** `provenance`, `task`, `goal`, `topic` and `session-name` are frozen; none may be renamed, reordered among themselves, or have its own gate changed. They move only in one respect: they render after the ask rather than before it.
- **The relocated values keep their existing classes.** `producer`, `meta`, `host`, `cwd`, `tool`, `pane` and `unroutable` are the identifiers the acceptance criteria and the `## Verification` greps key on; they move into the panel unchanged. Two identifiers are frozen and new: the control is `class="info-toggle"` and its panel is `class="info-panel"`.
- ⚠️ **The served document must contain none of the strings `unknown`, `n/a`, `N/A`, `—` or `??`.** A guard asserting their absence does run over the whole served body — `pkg/handler/attention-page_test.go:348` iterates the five over `body` — but it is **case-scoped**, not page-wide: it fires only in the provenance cases that reach it. ⚠️ **So do not rely on it to catch a placeholder you introduce.** A stray `n/a` inside a new panel label would redden nothing, and the absence rule is yours to honour rather than a test's to enforce. Name every absent state deliberately, and render absence as **nothing**, never as a placeholder. (The `i` glyph, `info-toggle`, `info-panel` and `aria-label="Card information"` are all clear of this vocabulary.)
- ⚠️ **The absent-not-placeholder rule governs every value in the panel:** a field that is empty renders no element, and no `-`, no `n/a`, no empty span. That is why the meta line is a derived string gated on `{{if .Meta}}` rather than a template expression over `libtime.DateTime`.
- ⚠️ **`href` values must be `template.URL`, never `string`** — a plain-string `href` renders `#ZgotmplZ` while every field-asserting test still passes. This change adds no link, and the existing `TaskURL` / `GoalURL` / `TopicURL` fields keep their type.
- **The board's existing behaviour must not regress:** the corner X's `(not .Dimmed)` gate, the read-aloud toggle's `and .Speak (not .Dimmed)` gate, the jump control's two arms, the `Hide answered` switch, the stream-stale notice, and the dimmed record card's rendering.
- **Not the task, goal, topic or session-name spans' semantics.** `[[Attention Item Schema]]` silences 22, 24 and 25 own them; this work must not regress their presence, their order among themselves, or their separator rule.
- **Not the page-level build-identity footer.** `class="build-identity"` is the board's own provenance and stays where it is.
- **Not the store, the item schema or any producer.** No field is added to `Item`, `PushRequest` or the push path.
- **Not a board-wide filter, collapse-all, or persisted open/closed state.** The affordance is per card and per page load.
- Test types follow the repo's guide: Ginkgo only, no stdlib table tests, no direct `testing.T`, a real in-memory libkv DB rather than a mocked one.
- Errors wrap with `github.com/bborbe/errors`; no `fmt.Errorf`, no bare `return err`.
- Repo-relative paths only — no absolute or home-relative paths.
</constraints>

<verification>
Run `make precommit` — must exit 0.

Run `make test` — must exit 0.

- `grep -c 'info-toggle' pkg/handler/attention-page.go` — must print a value of at least 1.
- `grep -c 'info-panel' pkg/handler/attention-page.go` — must print a value of at least 1.
- `grep -n 'class="producer"' pkg/handler/attention-page.go` — must print exactly one line, and that line must not be the `<li …>` opening line.
- `awk 'BEGIN{p=0;ok=0} /class="info-panel"/{p=NR} /class="producer"/{if (p>0) ok=1} END{exit ok?0:1}' pkg/handler/attention-page.go` — must exit 0, i.e. the producer div renders on or after the line that opens the panel in the same sub-template.
- `! grep -qE 'class="provenance".*class="(host|cwd|tool|pane|unroutable)"' pkg/handler/attention-page.go` — must exit 0, i.e. the card-face provenance line carries no machine span.
- `grep -c 'infoMetaLine' pkg/handler/attention-page-helpers.go` — must print a value of at least 1.
- `grep -c 'Info bool' pkg/handler/attention-page.go` — must print a value of at least 1.
- `grep -n '^## Unreleased' CHANGELOG.md` — must print one line.

⚠️ Do **not** use `-mod=vendor` anywhere: this repo does not commit `vendor/`, and `make precommit`'s `ensure` target removes it.

⚠️ Do **not** put a bare `git` command in this block. This worktree's `.git` is masked, so a `git` command dies with `fatal: not a git repository`, and the daemon does not check verification exit codes — the check would ship having never run.
</verification>
