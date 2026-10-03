# The production-touching marker

The board withholds the Allow / Deny pair from a headless worker's `permission` park when the item's **own task file** declares a production-touching step. This page records the convention that declaration uses, because the board now depends on it and the convention otherwise lives only in the operator's vault guidance.

## The marker

A task's subtask line carries it:

```markdown
- [ ] ⚠️ production-touching — `make install`; …
```

The detected form is a task checkbox, an optional warning glyph, the phrase `production-touching`, and an em-dash separator:

```
(?m)^\s*-\s*\[[ x/]\]\s*(?:⚠️?)?\s*production-touching\s*—
```

⚠️ **The leading `(?m)` is required, not decoration.** Without it `^` anchors only at the start of the whole text, so the pattern matches a marker on the **first line** of a task file and silently misses every one below it. The implementation carries it (`pkg/provenance.go`).

⚠️ **The glyph group is `(?:⚠️?)?`, and the outer group is load-bearing.** `⚠️` is two code points — U+26A0 plus the variation selector U+FE0F — so a bare `⚠️?` makes only the *selector* optional and still **requires** the base glyph. That silently fails every marker written without it, which is the opposite of what "optional glyph" means. The outer non-capturing group is what makes the whole glyph optional.

## Why the marker and not the command

The parked command is available — the event log records it — but it cannot be classified soundly. A command obfuscates, aliases, wraps in a script, or splits across lines, so any classifier over it fails open on exactly the inputs that matter, and a one-click Allow on a destructive action is the harm the exclusion exists to prevent. The marker is **authored**: the operator declares which steps are production-touching, and the board reads the declaration. Nothing is guessed.

## The two properties that matter

**The match is list-item form only, never a bare substring.** Task files discuss the phrase in ordinary prose — an `# Impact`, a `# Success Criteria`, a `# Progress` entry — and a substring match fires on all of it, removing a control the operator needs. Only the checkbox marker counts.

⚠️ **Two limits the pattern encodes silently, and both fail open — a marker that misses them renders the pair with no indication why:**

- **The checkbox class is exactly `[ x/]`.** A capital `X` — `- [X] ⚠️ production-touching — …` — does **not** match. The vault writes lowercase for a checked box; if you ever write the capital, the marker is inert.
- **The phrase match is case-sensitive.** `Production-Touching`, `PRODUCTION-TOUCHING` and any other casing do **not** match. Write it lowercase, exactly as shown above.

**The reading is task-level, not command-level.** Any marker in the item's task suppresses the pair for *every* park that task raises. This is deliberate and its cost is known: for a task carrying an ops step, its other, benign parks also lose the control, and a headless worker has no pane to answer in, so those fall to the ~15-minute auto-deny. The fail-safe direction is chosen over the precise one.

## Fail direction

**Absence of the marker renders the pair** — the exclusion is fail-open, and this is the opposite polarity to the headless gate (which is fail-closed). Both polarities are deliberate:

| Fact | Unresolvable input resolves to | Worst case |
|---|---|---|
| `Headless` (spawn ledger `mode`) | **not headless** → no control | a headless card loses a control it should have |
| `ProductionTouching` (task marker) | **not declared** → pair renders | a pair renders on a park whose task did not declare one |

Neither polarity may be flipped: the first prevents permission laundering on a tab worker's gate, the second prevents deleting the control the board exists to provide.

## What this withholds, and what it does not

⚠️ **The exclusion is render-level. It withholds a control; it does not refuse an answer.** `handleAttentionAnswer` accepts a `{"decision":"allow"}` POST against any item id and applies no provenance check at all — so the board stops *offering* the button, and the answer endpoint does not *reject* one posted directly. That is the repo's standing design and it is exactly what the `Headless` gate beside it already does; this exclusion neither introduces nor worsens it.

Read the feature as **"the board does not offer a one-click approval here"**, never as **"a board answer cannot approve this"**. The second is a stronger claim than what ships, and it is the kind of phrasing a later reader would rely on.

⚠️ **The withheld pair renders as nothing, with no affordance naming the reason.** A production-touching park simply carries no `.actions` div, so an operator cannot tell *"excluded as production-touching"* from *"not a headless worker"* from *"a rendering bug"* — and the headless park silently auto-denies ~15 minutes later. The safety property holds (an absent control is the safe direction), but the observability gap is real; a sibling of the existing `jump-reason` span, naming the exclusion, is the natural fix and is deliberately not part of this change.
