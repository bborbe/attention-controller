#!/usr/bin/env bash
# Copyright (c) 2026 Benjamin Borbe All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.
#
# guard-mutation.sh — prove every gate guard is RED when its decision is deleted.
#
# ⚠️ A focus loop proves a focus MATCHES a spec, not that the spec asserts
# anything: empty `It(...)` bodies satisfy it. This harness mutates each guard's
# branch, runs that guard's focus, and REQUIRES the run to fail with an
# ASSERTION — a compile error is rejected explicitly, because a build break also
# exits non-zero and would otherwise count as coverage.
#
# One pre-loop backup of every source file the mutations touch; one restore after
# every guard; a post-loop cmp -s proof that the restore is byte-identical, plus a
# git-diff half that runs only where .git is reachable (the container masks it).
set -uo pipefail
cd "$(dirname "$0")/.."

WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

# The table: guard|file|old|new — no `|` inside any snippet.
TABLE=$(cat <<'EOF'
TranscriptReaderLastAssistantText|pkg/transcript-reader.go|found = true|found = false
TranscriptReaderPendingToolCall|pkg/transcript-reader.go|return pendingName, pending|return pendingName, false
StuckReasonCauses|pkg/manager-gate-stuck.go|return managerStuckCauseStreamClosed|return ""
GatedUnionMatchesTable|pkg/manager-gate.go|if in.Stuck != ""|if false && in.Stuck != ""
ClearedFromBothFalseSites|pkg/manager-gate.go|return ManagerGateCleared, "headless:no-ask"|return ManagerGateHeld, "headless:no-ask"
HeldCausesAreDistinct|pkg/manager-gate.go|strings.HasPrefix(reason, "held:")|false && strings.HasPrefix(reason, "held:")
BusyParkedModalIsGated|pkg/manager-gate.go|&& in.Frozen|&& true
CloserAskIsNormalized|pkg/manager-gate-closer.go|replaced := managerCloserEntityPattern.ReplaceAllString(text, " ")|replaced := text
StabilityGateSuppressesChurn|pkg/manager-gate.go|if inKey == inPending|if inKey == inKey && inPending == inPending
StabilityGateIsPerSession|pkg/manager-gate.go|if inKey == inPending|if inKey == inPending && len(key) == len(pending)
NewGateIsContentKeyed|pkg/manager-gate-transitions.go|known == managerContentKey(state.Reason, state.Detail)|known == state.Reason
AgedEscalatesOncePerThreshold|pkg/manager-gate-park.go|record.Aged = append(record.Aged, threshold)|record.Aged = record.Aged
AgedOnlyForParkedReasons|pkg/manager-gate-tracker.go|&& managerIsParkedReason(state.Reason)|&& true
RepostOnlyWhenNoCardOpen|pkg/manager-gate-repost.go|if _, open := in.Open.DedupKeys["park:"+session]; open {|if _, open := in.Open.DedupKeys["park:"+session]; open && false {
AnswerPredatingParkDoesNotSuppress|pkg/manager-gate-repost.go|return row.CreatedAt.Time().After(since.Time())|return true
StoreReadFailureSuppressesRepost|pkg/manager-gate-repost.go|in.Open == nil {|in.Open == nil { in.Open = &ManagerOpenView{} } else if false {
StatePersistsAcrossRestart|pkg/manager-gate-state.go|return prev, true|return map[string]string{}, true
StateIsScopedPerManager|pkg/manager-gate-state.go|"state-"+managerScope(manager)+".json"|"state.json"
FailOpenIsSplitByInput|pkg/manager-transcript-source.go|HeadlessLive: headlessLive|HeadlessLive: headlessLive && false
AnswerRow1NotActedOn|pkg/answered-signal.go|if !wasActedOn(item) {|if false && !wasActedOn(item) {|classifies every vector as declared
AnswerRow2CloseIsNotAnAnswer|pkg/answered-signal.go|if item.State == ClosedState && item.AnsweredAt == nil {|if false && item.State == ClosedState && item.AnsweredAt == nil {|classifies every vector as declared
AnswerRow3SkipIsDeclined|pkg/answered-signal.go|if item.Answer != nil && item.Answer.Kind == SkipAnswerKind {|if false && item.Answer != nil && item.Answer.Kind == SkipAnswerKind {|classifies every vector as declared
AnswerRow4HiddenIsHidden|pkg/answered-signal.go|if item.Answer != nil && item.Answer.Kind == HiddenAnswerKind {|if false && item.Answer != nil && item.Answer.Kind == HiddenAnswerKind {|classifies every vector as declared
AnswerRow5NoClientIsUnattributed|pkg/answered-signal.go|if item.AnsweredClient == nil {|if false && item.AnsweredClient == nil {|classifies every vector as declared
AnswerRow6AutomationIsAutomated|pkg/answered-signal.go|if item.AnsweredClient.Automation != nil && *item.AnsweredClient.Automation {|if false && item.AnsweredClient.Automation != nil && *item.AnsweredClient.Automation {|never reads a scripted click as the operator
AnswerRow7NeedsNoResolvedBy|pkg/answered-signal.go|return AttributionVerdictAttributed,|return AttributionVerdictUnattributed,|does not require resolved_by
AnswerRenderedVocabularyIsDerived|pkg/answered-signal.go|return "ANSWERED"|return "NOT_OPERATOR_ANSWERED"|derives the rendered vocabulary from the verdict
AnswerNilWriterDiscards|pkg/answered-signal.go|if r.writer == nil {|if false && r.writer == nil {|discards on a nil writer instead of panicking
AnswerProducerScoping|pkg/answered-signal.go|string(item.ProducerID) != sessionID|string(item.ProducerID) != sessionID && false|declines an attributed card whose producer is a peer session
AnswerAtMostOncePerItem|pkg/answered-signal.go|if s.signalled[item.ItemID] {|if false && s.signalled[item.ItemID] {|signals one answered card exactly once over three ticks
WakeEmitOnlyOnEnter|pkg/manager-wake-job.go|if last, seen := previous[item.ItemID]; seen && !p.stale(last) {|if last, seen := previous[item.ItemID]; seen && false && !p.stale(last) {|TestEmitOnlyOnEnterNotOnRepoll
WakeRearmReEmits|pkg/manager-wake-job.go|return p.now.Sub(last) > libtime.Duration(p.job.reemitAfter)|return true|TestRearmReEmitsEntryOlderThanReemitAfter
WakeRoleHumanNeverWakes|pkg/manager-wake-job.go|if p.isHuman(sessionID) {|if false && p.isHuman(sessionID) {|TestRoleHumanNeverWakes
WakeForeignGoalNeverWakes|pkg/manager-wake-job.go|if scoped && foreign {|if false && scoped && foreign {|TestForeignGoalNeverWakes
WakeUnownedOnlyFleetManager|pkg/manager-wake-job.go|!p.job.goalScope.SessionResolved(sessionID) {|p.job.goalScope.SessionResolved(sessionID) {|TestUnownedClassGoesToTheFleetManagerOnly
WakeHop3bDropsLiveNonWorker|pkg/manager-wake-job.go|if p.isManager(sessionID) && p.isLive(sessionID) {|if false && p.isManager(sessionID) && p.isLive(sessionID) {|TestHop3bDropsOnLiveNonWorker
WakePeerManagerDropped|pkg/manager-wake-job.go|return !p.isLive(spawner)|return true|TestProducerIDScopingIgnoresPeerCard
WakeSelfProducedNeverWakes|pkg/manager-wake-job.go|if strings.TrimPrefix(string(item.ProducerID), sessionProducerPrefix) == manager {|if false && strings.TrimPrefix(string(item.ProducerID), sessionProducerPrefix) == manager {|TestSelfProducedCardNeverWakesItsProducer
WakeBatchWaitsWindow|pkg/manager-wake-job.go|if p.now.Sub(batch.firstSeen) < libtime.Duration(ManagerWakeBatchWindow) {|if p.now.Sub(batch.firstSeen) < 0 {|TestBatchWaitsFullWindow
WakePruneDropsAnswered|pkg/manager-wake-job.go|if _, ok := kept[item.ItemID]; ok {|if _, ok := kept[item.ItemID]; ok == ok {|TestBatchSkipsCardAnsweredBeforeFlush
WakeNoPaneParks|pkg/manager-wake-job.go|if parked {|if false && parked {|TestWakeNoPaneParks
WakeEmissionRecorded|pkg/manager-wake-job.go|func (p *wakePass) recordBatch(manager string, items []Item) {|func (p *wakePass) recordBatch(manager string, items []Item) { return|TestWakeWritesEmissionRecord
WakePreviousSetMemory|pkg/manager-wake-job.go|previous := p.job.emitted[manager]|previous := map[ItemID]libtime.DateTime{}|TestEmitOnlyOnEnterNotOnRepoll
# ⚠️ Guard 12 is a property, not a branch: the store's read already drops an expired
# item, so the mutation is the expiry filter itself. It is focused on `isExpired`
# and NOT on `TestExpiredItemNeverWakes`, which drives a MOCKED store
# (`store.ReadReturns`) whose input never contains the expired card — measured
# 2026-11-22: with this mutation applied that spec stays green while three
# `isExpired` specs go red.
ExpiredItemNeverWakes|pkg/attention-store-impl.go|return !now.Time().Before(effectiveExpiresAt(item).Time())|return false && !now.Time().Before(effectiveExpiresAt(item).Time())|isExpired
EOF
)

FILES=$(printf '%s\n' "$TABLE" | awk -F'|' 'NF>=4 {print $2}' | sort -u)
GUARD_COUNT=$(printf '%s\n' "$TABLE" | awk -F'|' 'NF>=4' | wc -l | tr -d ' ')

cat > "$WORKDIR/mutate.py" <<'PY'
import sys

path, old, new = sys.argv[1], sys.argv[2], sys.argv[3]
with open(path, "r", encoding="utf-8") as handle:
    body = handle.read()
count = body.count(old)
if count != 1:
    sys.stderr.write(f"mutation anchor occurs {count} times in {path}: {old!r}\n")
    sys.exit(3)
with open(path, "w", encoding="utf-8") as handle:
    handle.write(body.replace(old, new, 1))
PY

# ONE pre-loop backup of every source file the mutations touch.
for file in $FILES; do
    mkdir -p "$WORKDIR/$(dirname "$file")"
    cp "$file" "$WORKDIR/$file"
done

# The pre-loop git state, so a pre-existing dirty file is not read as a failed
# restore. The cmp -s proof below is authoritative; this half only catches a
# mutation that survived the loop.
if git rev-parse --git-dir >/dev/null 2>&1; then
    git diff -- $FILES > "$WORKDIR/pre.diff" 2>/dev/null || true
fi

failures=0
reds=0

while IFS='|' read -r guard file old new focus; do
    [ -z "$guard" ] && continue
    case "$guard" in \#*) continue ;; esac
    FOCUS="${focus:-$guard}"

    if ! python3 -I "$WORKDIR/mutate.py" "$file" "$old" "$new"; then
        echo "NO-ASSERTION $guard (mutation anchor not unique)"
        cp "$WORKDIR/$file" "$file"
        failures=$((failures + 1))
        continue
    fi

    output="$(go test ./pkg -run TestSuite -ginkgo.focus="$FOCUS" -ginkgo.fail-on-empty 2>&1)"
    status=$?
    # Restore THIS file from the pre-loop backup after every guard.
    cp "$WORKDIR/$file" "$file"

    if [ "$status" -eq 0 ]; then
        echo "GREEN $guard"
        failures=$((failures + 1))
        continue
    fi
    if printf '%s' "$output" | grep -Eq '\[build failed\]|undefined:|syntax error|cannot use'; then
        echo "COMPILE-ERROR $guard"
        failures=$((failures + 1))
        continue
    fi
    # ⚠️ A focus that matched NOTHING also exits non-zero under -ginkgo.fail-on-empty.
    # That is a typo in the table, not a guard proven: reject it explicitly, or every
    # misspelled focus reads as coverage. The shape, measured 2026-11-22 on a
    # deliberately wrong focus, is `FAIL! - Detected no specs ran and
    # --fail-on-empty is set` plus `Ran 0 of 1344 Specs` — note "no specs RAN", not
    # "matched", and note that the RED pattern below would otherwise claim it.
    if printf '%s' "$output" | grep -Eqi 'fail-on-empty|Detected no specs|Ran 0 of'; then
        echo "NO-ASSERTION $guard (focus matched no spec: $FOCUS)"
        failures=$((failures + 1))
        continue
    fi
    if printf '%s' "$output" | grep -Eq '\[FAIL\]|FAIL!|Summarizing|Failure \[|Expected'; then
        echo "RED $guard"
        reds=$((reds + 1))
        continue
    fi
    echo "NO-ASSERTION $guard"
    failures=$((failures + 1))
done <<< "$TABLE"

# Prove the restore byte-identical for every file.
for file in $FILES; do
    if ! cmp -s "$WORKDIR/$file" "$file"; then
        echo "RESTORE-DIFF $file"
        failures=$((failures + 1))
    fi
done

# The git half: skipped where .git is masked (the container). A pre-existing
# dirty file is compared against its pre-loop diff, so only a mutation that
# survived the restore is a failure.
if git rev-parse --git-dir >/dev/null 2>&1; then
    git diff --exit-code -- $FILES > "$WORKDIR/post.diff" 2>/dev/null
    if ! cmp -s "$WORKDIR/pre.diff" "$WORKDIR/post.diff"; then
        echo "RESTORE-DIFF git worktree differs from the pre-loop state"
        failures=$((failures + 1))
    fi
fi

echo "RED=$reds guards=$GUARD_COUNT failures=$failures"
if [ "$failures" -ne 0 ] || [ "$reds" -ne "$GUARD_COUNT" ]; then
    exit 1
fi
exit 0
