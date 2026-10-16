# The production-touching marker

⚠️ **The board renders the Allow / Deny pair on every `permission` card, whichever class it belongs to, as of 2026-10-16.** This page records the production-touching marker convention — the operator's authored declaration that a step must not be approved with one click — which the board **no longer consults**. The marker is still parsed and still resolved on every page load (`pkg/provenance.go`), but nothing consumes the result. The convention outlives the board's read of it, so it is documented here rather than deleted.

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

The parked command is available — the event log records it — but it cannot be classified soundly. A command obfuscates, aliases, wraps in a script, or splits across lines, so any classifier over it fails open on exactly the inputs that matter, and a one-click Allow on a destructive action is the harm the convention exists to name. The marker is **authored**: the operator declares which steps are production-touching, and nothing is guessed.

⚠️ **The board no longer acts on the declaration.** Until 2026-10-16 the board withheld the pair from a `permission` park whose task carried the marker; that exclusion was overruled, and the pair now renders on every `permission` card. The convention is kept because it is the operator's own record of which steps are irreversible, and because the parse remains in `pkg/provenance.go`.

## The two properties that matter

**The match is list-item form only, never a bare substring.** Task files discuss the phrase in ordinary prose — an `# Impact`, a `# Success Criteria`, a `# Progress` entry — and the pattern is deliberately narrow so that prose is never mistaken for a declaration. Only the checkbox marker counts.

⚠️ **Two limits the pattern encodes silently — a marker that misses them is not recognised at all:**

- **The checkbox class is exactly `[ x/]`.** A capital `X` — `- [X] ⚠️ production-touching — …` — does **not** match. The vault writes lowercase for a checked box; if you ever write the capital, the marker is inert.
- **The phrase match is case-sensitive.** `Production-Touching`, `PRODUCTION-TOUCHING` and any other casing do **not** match. Write it lowercase, exactly as shown above.

**The reading was task-level, not command-level.** A marker in the item's task once applied to *every* park that task raised, benign ones included. The board no longer reads the marker, so nothing is withheld on that or on any other basis.

## Fail direction: none remains

⚠️ **There is no fail direction left to describe, and that is the point of the change.** Until 2026-10-16 two provenance facts gated the pair and pulled in opposite directions — the session-mode read failed **closed** (an unresolvable session rendered no control) while the marker read failed **open** (an undetected marker rendered the pair). Both gates are gone from the render path: the pair renders on every `permission` item, including one whose provenance cannot be resolved at all. No uncertain input is left to resolve, so there is no polarity to state and none to flip.

## Nothing is withheld at the render level

⚠️ **Nothing is withheld at the render level any more: the pair renders on every `permission` card.** One property of the repository is unchanged, and it never depended on either exclusion: the answer endpoint does not police provenance. `handleAttentionAnswer` accepts a `{"decision":"allow"}` POST against any item id and applies no provenance check at all — so a board control is an *offering*, never an *authorisation*, and an answer posted directly has always been accepted. That is the repo's standing design.
