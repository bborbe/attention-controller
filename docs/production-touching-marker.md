# The production-touching marker

The board withholds the Allow / Deny pair from a headless worker's `permission` park when the item's **own task file** declares a production-touching step. This page records the convention that declaration uses, because the board now depends on it and the convention otherwise lives only in the operator's vault guidance.

## The marker

A task's subtask line carries it:

```markdown
- [ ] ⚠️ production-touching — `make install`; …
```

The detected form is a task checkbox, an optional warning glyph, the phrase `production-touching`, and an em-dash separator:

```
^\s*-\s*\[[ x/]\]\s*⚠️?\s*production-touching\s*—
```

## Why the marker and not the command

The parked command is available — the event log records it — but it cannot be classified soundly. A command obfuscates, aliases, wraps in a script, or splits across lines, so any classifier over it fails open on exactly the inputs that matter, and a one-click Allow on a destructive action is the harm the exclusion exists to prevent. The marker is **authored**: the operator declares which steps are production-touching, and the board reads the declaration. Nothing is guessed.

## The two properties that matter

**The match is list-item form only, never a bare substring.** Task files discuss the phrase in ordinary prose — an `# Impact`, a `# Success Criteria`, a `# Progress` entry — and a substring match fires on all of it, removing a control the operator needs. Only the checkbox marker counts.

**The reading is task-level, not command-level.** Any marker in the item's task suppresses the pair for *every* park that task raises. This is deliberate and its cost is known: for a task carrying an ops step, its other, benign parks also lose the control, and a headless worker has no pane to answer in, so those fall to the ~15-minute auto-deny. The fail-safe direction is chosen over the precise one.

## Fail direction

**Absence of the marker renders the pair** — the exclusion is fail-open, and this is the opposite polarity to the headless gate (which is fail-closed). Both polarities are deliberate:

| Fact | Unresolvable input resolves to | Worst case |
|---|---|---|
| `Headless` (spawn ledger `mode`) | **not headless** → no control | a headless card loses a control it should have |
| `ProductionTouching` (task marker) | **not declared** → pair renders | a pair renders on a park whose task did not declare one |

Neither polarity may be flipped: the first prevents permission laundering on a tab worker's gate, the second prevents deleting the control the board exists to provide.
