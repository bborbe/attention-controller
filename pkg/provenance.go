// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/golang/glog"
)

// Provenance is where an item came from, as far as the page can prove it.
//
// Every field but one is a *claim about the event*, not about the store: the
// store carries none of these (its fifteen-field Item has no host, cwd, tool or
// pane), so they arrive from the producer's own event log and are missing for
// any item whose producer wrote no event.
//
// ⚠️ The exception is SessionName, which comes from the session registry rather
// than from the producer's event log. It is read at render time and never
// stored on the item, so a rename shows up on the next page load; and because
// the registry is a different source from the event log it resolves on items
// whose producer wrote no event at all.
//
// ⚠️ An unresolved value is the empty string here, never a placeholder. The
// page renders it as absent. § Silence 7's rule is that an unresolvable value
// must never be presented as resolved, and a fabricated stand-in — `unknown`,
// `n/a`, a defaulted pane — is exactly that failure wearing a friendlier face.
type Provenance struct {
	// Host is the machine the producer ran on.
	Host string
	// Cwd is the producer's working directory.
	Cwd string
	// Tool is the tool the producer was waiting on, when the item is a tool
	// gate. Empty is the common case: idle items carry no tool.
	Tool string
	// Pane is the producer's terminal pane, rendered only when Routable.
	Pane string
	// PaneRecorded is whether the event recorded a pane id at all. It is what
	// separates "no pane to show" from "a pane we refuse to show".
	PaneRecorded bool
	// Routable is whether the recorded pane is proven to be this producer's.
	// False with PaneRecorded true is the case the page marks `unroutable`.
	Routable bool
	// TaskName is the title of the vault task this item's session is anchored
	// to, from the task file that records the session. Empty when nothing
	// resolved.
	TaskName string
	// TaskPath is the task file's path relative to the vault root, e.g.
	// `25 Tasks/Fix the board.md`. ⚠️ A link needs the vault's own name as well
	// as this path, and the **renderer** derives that name from the configured
	// vault directory rather than reading it here — there is no third field
	// carrying it.
	TaskPath string
	// GoalName is the title of the goal the task names first in its `goals:`
	// list. Empty when nothing resolved.
	//
	// ⚠️ It **derives from the task**: a goal is only ever reached through the
	// task file that names it, so this can resolve nowhere TaskName did not,
	// which is why Resolved needs no conjunct for it.
	GoalName string
	// GoalPath is the goal file's path relative to the vault root, e.g.
	// `24 Goals/Fix the board.md`. Empty when nothing resolved.
	GoalPath string
	// TopicName is the title of the topic page that lists this goal under its
	// `## Goals` heading. Empty when nothing resolved.
	//
	// ⚠️ It **derives from the goal**, and so from the task: a topic is reached
	// only by joining the resolved goal title against the topic pages.
	TopicName string
	// TopicPath is the topic file's path relative to the vault root, e.g.
	// `23 Topics/Attention Board Polish.md`. Empty when nothing resolved.
	TopicPath string
	// SessionName is the name the session registry holds for this item's
	// session, rendered only when the registry records that a human chose it.
	// Empty when the registry holds no entry for the session, when the entry's
	// source is `peer` or `derived`, when the entry carries no source at all, or
	// when the item names no session — an unresolvable name renders absent rather
	// than as a stand-in, the rule every other field of this struct follows.
	SessionName string
	// Headless reports whether the supervisor's spawn ledger records this item's
	// session as a headless worker — one with no pane of its own, whose park
	// lives only in the memory of the manager session that spawned it.
	//
	// ⚠️ It is a control gate, not a line fact, and it is deliberately NOT a
	// member of Resolved(). Resolved() gates whether the provenance div renders
	// at all, and its members are the values that div draws; a boolean that
	// draws nothing would render an empty `<div class="provenance">` on a card
	// whose only resolved fact is headless.
	//
	// ⚠️ Fail-closed: an absent record, an unreadable directory, an unparseable
	// file or an unrecognised mode all leave this false, and false renders no
	// answering control. The opposite default would put a board control on a tab
	// worker's gate — the permission laundering this field exists to remove.
	Headless bool
}

// Resolved reports whether anything about this item's origin could be told.
// When false the page renders exactly what it rendered before this change —
// no provenance line at all — which is how an item with no provenance source
// degrades rather than failing.
//
// ⚠️ TaskName is such a fact, and it is one of two members of this set that
// come from neither the event log nor the pane listing: the vault task a
// session is anchored to says *what* the session was working on, which is as
// much about where the item came from as the host or the cwd is. A Provenance
// carrying nothing but a task name is a real shape, not a hypothetical one — it
// is what an item resolves to when its producer wrote no event log but its
// session's task file was found — and without this conjunct that row would
// render no provenance line at all, so the name would be resolved correctly,
// drawn correctly, and never appear.
//
// ⚠️ SessionName is the second such member, and it comes from the session
// registry rather than from the vault. A session that resolves a `user` name and
// nothing else about its origin is the same shape and the same trap: measured on
// the live board 2026-09-30, 186 of 1,095 rows carried no provenance line at
// all, and without this conjunct the name would be resolved correctly, drawn
// correctly, and never appear because the line that would carry it is
// suppressed.
func (p Provenance) Resolved() bool {
	return p.Host != "" || p.Cwd != "" || p.Tool != "" || p.Pane != "" || p.TaskName != "" ||
		p.SessionName != ""
}

// Provenances is the resolver's answer for a whole page, keyed by item id.
type Provenances map[ItemID]Provenance

// sessionNameSourceUser is the registry's marker for a name a human chose. It is
// the only source that renders: `peer` means the name was inherited from the
// spawning parent and `derived` means it was generated, and the spec withholds
// both. ⚠️ The field's absence is not `user` either — a record carrying no
// nameSource renders no name, the same as `peer`.
const sessionNameSourceUser = "user"

// sessionName is what the registry holds for one session: the name, and the
// source that says who chose it. The two are read together because the gate on
// the name is the source, and a second scan of the registry to read the source
// would be a second place the two could disagree.
type sessionName struct {
	Name   string
	Source string
}

// headlessSessionMode is the ledger's marker for a worker with no pane of its
// own. It is the only mode that renders an answering control; every other
// value — `interactive` and anything the ledger adds later — reads as not
// headless, the fail-closed direction.
const headlessSessionMode = "headless"

// spawnRecord is one `<session-id>.json` in the supervisor's spawn ledger,
// reduced to the two fields the page reads. The ledger is written by the
// supervisor and only read here; nothing about the headless/tab split is
// stored on the item.
type spawnRecord struct {
	SessionID string `json:"session_id"`
	Mode      string `json:"mode"`
}

//counterfeiter:generate -o ../mocks/provenance-resolver.go --fake-name ProvenanceResolver . ProvenanceResolver

// ProvenanceResolver resolves where each item came from.
//
// It takes the whole page at once rather than one item at a time, and that is
// deliberate: the panes and the session registry are host-wide reads that are
// identical for every row, so a per-item interface would re-run `wezterm cli
// list` once per item — dozens of subprocesses for one page load.
type ProvenanceResolver interface {
	Resolve(ctx context.Context, items Items) Provenances
}

// NewProvenanceResolver creates a resolver reading the event logs under
// stateDir, the session registry under sessionsDir, the supervisor's spawn
// ledger under spawnDir, panes from the lister, and the vault tasks from the
// index.
//
// ⚠️ The index is built by the caller, once, and handed in — it is never built
// here and never inside Resolve. The live vault holds over 8,000 task files, so
// building it per page load would re-read every task file on every board
// refresh, on a page the SSE stream serves continuously. A nil index is legal
// and resolves no task, which is what a host with no configured vault gets.
func NewProvenanceResolver(
	stateDir string,
	sessionsDir string,
	spawnDir string,
	panes PaneLister,
	tasks TaskIndex,
) ProvenanceResolver {
	return &provenanceResolver{
		stateDir:    stateDir,
		sessionsDir: sessionsDir,
		spawnDir:    spawnDir,
		panes:       panes,
		tasks:       tasks,
	}
}

type provenanceResolver struct {
	stateDir    string
	sessionsDir string
	spawnDir    string
	panes       PaneLister
	tasks       TaskIndex
}

// eventRecord is one line of a producer's event log, reduced to the fields the
// page renders. A line carries more than this; nothing else is read.
type eventRecord struct {
	ItemID    string     `json:"item_id"`
	SessionID string     `json:"session_id"`
	Host      string     `json:"host"`
	Cwd       string     `json:"cwd"`
	ToolName  string     `json:"tool_name"`
	Pane      flexString `json:"pane"`
}

// flexString reads a JSON field that may be either a string or a number. Pane
// ids arrive as strings in some records and numbers in others, and a strict
// string field would fail the whole line on the numeric form.
type flexString string

// UnmarshalJSON accepts a JSON string or number.
func (f *flexString) UnmarshalJSON(data []byte) error {
	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		*f = flexString(asString)
		return nil
	}
	var asNumber json.Number
	if err := json.Unmarshal(data, &asNumber); err != nil {
		return err
	}
	*f = flexString(asNumber.String())
	return nil
}

// Resolve resolves provenance for every item in one pass.
//
// ⚠️ The join key is the item's **DedupKey**, not its ProducerID. Both were
// measured live before this was written, and the distinction is load-bearing:
// the producer is a *session*, which routinely has several open items at once
// (measured: 15 of 26 producers held more than one, one held six), while an
// event describes exactly one item. Joining on the producer would stamp one
// session's host, cwd and pane onto every row that session ever raised — the
// precise failure § Silence 7 forbids, since each of those rows would show a
// resolved-looking value belonging to a different item.
//
// The DedupKey is the producer's own event key (a sha256 over the gate's
// identity), which is the `item_id` the event log is written under. The store's
// own ItemID is a separate 32-character id it generates and never appears in
// the logs, so it cannot be the join.
func (r *provenanceResolver) Resolve(ctx context.Context, items Items) Provenances {
	resolved := make(Provenances, len(items))
	if len(items) == 0 {
		return resolved
	}
	// A listing that could not be read yields no pane claims at all. It is
	// logged, never flattened into an empty map: "no panes" would mark every row
	// unroutable, which asserts the pane does not resolve to this session —
	// something an unreadable listing cannot establish.
	panes, panesErr := r.panes.List(ctx)
	panesAvailable := panesErr == nil
	if !panesAvailable {
		glog.V(2).Infof("pane listing unavailable, rendering no pane: %v", panesErr)
	}
	names := r.sessionNames(ctx)
	modes := r.sessionModes(ctx)

	// One read of each producer's log, reused across that producer's items.
	// A session with six open items would otherwise re-read the same file six
	// times.
	byProducer := make(map[ProducerID]map[string]eventRecord)
	for _, item := range items {
		// readEvents below can scan a large log, so the loop honours
		// cancellation rather than relying on the per-item work being cheap.
		select {
		case <-ctx.Done():
			glog.V(3).Infof("provenance resolution cancelled")
			return resolved
		default:
		}
		events, ok := byProducer[item.ProducerID]
		if !ok {
			events = r.readEventsForProducer(ctx, item.ProducerID)
			byProducer[item.ProducerID] = events
		}
		record, found := events[string(item.DedupKey)]
		if !found {
			// No event for this item: its producer wrote no log line, or the
			// log has been pruned. Render absent rather than falling back to
			// the session's newest event, which describes a different item.
			//
			// The event log is not the only way to locate the producer's pane,
			// though. The session registry and the pane listing are both keyed
			// on the session itself, so an item with no event line can still
			// resolve a pane by name — see resolveByName.
			if fallback, ok := r.resolveByName(item, names, panes, panesAvailable); ok {
				resolved[item.ItemID] = fallback
			}
		} else {
			resolved[item.ItemID] = r.build(record, names, panes, panesAvailable)
		}
		// ⚠️ The task lookup is independent of the pane lookup, and runs after
		// either branch. resolveByName reports ok=false whenever the pane
		// listing is unreadable, the session is absent from the registry, or no
		// pane matches — and on that path the loop stores no Provenance at all,
		// so a task resolved there would be lost even though the session has
		// one. Setting both fields here, on the item's entry, creates that entry
		// when neither branch claimed anything.
		//
		// The session id comes from sessionIDFromItem, the one helper that
		// understands every liveness shape the store writes; a second extractor
		// would be a second place to get that wrong.
		//
		// ⚠️ The goal and the topic ride with the task and inherit its
		// independence from the pane branches: the index carries all four
		// fields on one lookup, so this block is the only place any of them is
		// set and a second lookup is never needed.
		if task, ok := r.lookupTask(item); ok {
			provenance := resolved[item.ItemID]
			provenance.TaskName = task.Name
			provenance.TaskPath = task.Path
			provenance.GoalName = task.GoalName
			provenance.GoalPath = task.GoalPath
			provenance.TopicName = task.TopicName
			provenance.TopicPath = task.TopicPath
			resolved[item.ItemID] = provenance
		}
		// ⚠️ The name is independent of the pane and the task, so this runs on
		// every item, outside every branch above. The read-modify-write is the
		// same shape the task block uses: it takes whatever the entry already
		// holds — possibly the zero Provenance — sets one field and stores it
		// again, which creates the entry when neither pane branch claimed
		// anything and preserves a pane or a task an earlier block resolved.
		if name, ok := r.sessionNameFor(item, names); ok {
			provenance := resolved[item.ItemID]
			provenance.SessionName = name
			resolved[item.ItemID] = provenance
		}
		// ⚠️ The headless fact is independent of the pane, the task and the name, so
		// it runs on every item, outside every branch above, with the same
		// read-modify-write the name block uses. The ledger is read once, before the
		// loop — never once per item — so a 1,070-record directory is one read per
		// board refresh rather than one per card.
		if mode := modes[sessionIDFromItem(item)]; mode == headlessSessionMode {
			provenance := resolved[item.ItemID]
			provenance.Headless = true
			resolved[item.ItemID] = provenance
		}
	}
	return resolved
}

// lookupTask resolves the vault task this item's session is anchored to.
//
// It reports ok=false when there is no index, when the item names no session,
// or when the index holds no task for that session — the last of which is the
// honest answer for a session whose task file does not exist or whose vault was
// never configured. No task is ever guessed from a neighbouring session.
func (r *provenanceResolver) lookupTask(item Item) (Task, bool) {
	if r.tasks == nil {
		return Task{}, false
	}
	sessionID := sessionIDFromItem(item)
	if sessionID == "" {
		return Task{}, false
	}
	return r.tasks.Lookup(sessionID)
}

// sessionNameFor returns the registry name to render for this item's session,
// and reports ok=false when there is none to render.
//
// It withholds a name whose source is not `user`, and a record carrying no
// source at all, exactly as it withholds a session the registry does not hold:
// § Silence 7's rule is that an unresolvable value must never be presented as
// resolved, and another session's inherited or generated name is precisely that
// failure wearing a friendlier face.
func (r *provenanceResolver) sessionNameFor(
	item Item,
	names map[string]sessionName,
) (string, bool) {
	sessionID := sessionIDFromItem(item)
	if sessionID == "" {
		return "", false
	}
	entry, ok := names[sessionID]
	if !ok || entry.Source != sessionNameSourceUser || entry.Name == "" {
		return "", false
	}
	return entry.Name, true
}

// LivenessRef markers, and the session id each carries.
//
// The session id is not on the item as its own field — § Fields gives the item
// `producer_id` and `liveness_ref`, and the schema's `session:<id>` form is the
// only place a session is named outright. The store's live shape is the other
// marker: `heartbeat:<path>/<session-id>`, where the watcher touches a file per
// session and the file's name *is* the session id.
const (
	// sessionLivenessPrefix marks a `session:<id>` ref.
	sessionLivenessPrefix = "session:"
	// heartbeatLivenessPrefix marks a `heartbeat:<path>` ref whose final path
	// segment is the session id.
	heartbeatLivenessPrefix = "heartbeat:"
	// sessionProducerPrefix is the same `session:` marker, seen where it does
	// NOT belong: on a ProducerID.
	//
	// A producer that puts it in the wrong field is a live case, not a
	// hypothetical one. The store held an item whose ProducerID was
	// `session:<uuid>` while its LivenessRef carried the same marker, and
	// because ProducerID is validated only as a non-empty string the push was
	// accepted. `readEvents` then opened `session:<uuid>.events.jsonl`, which
	// cannot exist — the log is named for the bare id — so the item resolved
	// nothing even though its producer had written a perfectly good log.
	//
	// Stripping it is confined to locating the session; it does not rewrite the
	// stored value and does not relax the producer contract. A push carrying the
	// marker in the wrong field remains a producer bug to fix at the producer.
	sessionProducerPrefix = "session:"
)

// sessionIDFromItem recovers the session an item belongs to, or "" when the
// item names none.
//
// Both liveness models are handled because both are legal and the store uses
// them: `session:<id>` names the session directly, and `heartbeat:<path>`
// names a file the watcher maintains whose base name is the session id. An
// item on neither model — a cron job, a dark-factory run, an agent — yields
// "", which is the honest answer: those producers have no session to look up,
// so the name-keyed join cannot apply to them.
//
// A `session:`-prefixed ProducerID is accepted as a last resort, because the
// resolver must still work on items pushed before this fallback existed and on
// any push that puts the marker in the wrong field. LivenessRef is preferred:
// it is the field the marker belongs in.
func sessionIDFromItem(item Item) string {
	if ref := string(item.LivenessRef); ref != "" {
		if id, ok := strings.CutPrefix(ref, sessionLivenessPrefix); ok {
			return strings.TrimSpace(id)
		}
		if path, ok := strings.CutPrefix(ref, heartbeatLivenessPrefix); ok {
			return filepath.Base(strings.TrimSpace(path))
		}
	}
	return strings.TrimPrefix(string(item.ProducerID), sessionProducerPrefix)
}

// resolveByName locates an item's pane without an event line, by joining the
// item's session to its pane.
//
// ⚠️ The join key is the item's **SessionID**, not its ProducerID. The two are
// usually the same string but they are not the same key: the session registry
// is indexed by `sessionId`, and ProducerID is only *documented* as a session
// id — it may equally be an agent id or a job id (see the field's own comment).
// Looking the registry up by ProducerID would work for every session producer
// and silently fail for every other kind, which is exactly the class this
// fallback exists to serve.
//
// ⚠️ This asserts a name-keyed join rather than an ownership proof. The
// registry records no pane id (sessionRegistryEntry carries SessionID, Name and
// NameSource, and none of them is a pane) and the pane listing carries no
// session id (Pane carries PaneID and Title only), so the glyph-stripped
// title-vs-name comparison is the only session→pane join the controller can
// observe. Two same-named sessions with two panes titled that name are therefore
// indistinguishable, and no available field separates them.
//
// It reports ok=false — making no pane claim at all — when the session cannot
// be named. That is deliberate and is the load-bearing safety property here:
// OwnsPane treats an empty session name as *unprovable* and returns true, so
// handing it an unnamed session would mark every pane as owned and stamp a
// confident route onto a row the controller knows nothing about — the precise
// failure § Silence 7 forbids. A named session still goes through OwnsPane
// unchanged, so the ownership gate is fed rather than bypassed.
func (r *provenanceResolver) resolveByName(
	item Item,
	names map[string]sessionName,
	panes map[int]Pane,
	panesAvailable bool,
) (Provenance, bool) {
	if !panesAvailable {
		// The listing could not be read, so nothing can be said about any pane
		// either way. Same direction as build: no claim, never a negative one.
		return Provenance{}, false
	}
	sessionID := sessionIDFromItem(item)
	if sessionID == "" {
		// The item names no session — a cron job, a dark-factory run, an agent.
		// There is nothing to look up, so nothing is claimed.
		return Provenance{}, false
	}
	name := names[sessionID].Name
	if name == "" {
		// The session is not in the registry — it exited, or this host has no
		// registry. Either way there is no name to compare, so no pane can be
		// proven this session's and none is claimed.
		return Provenance{}, false
	}
	wanted := StripStatusGlyph(name)
	for paneID, pane := range panes {
		if StripStatusGlyph(pane.Title) != wanted {
			continue
		}
		if !OwnsPane(panes, paneID, name) {
			continue
		}
		return Provenance{Pane: strconv.Itoa(paneID), PaneRecorded: true, Routable: true}, true
	}
	return Provenance{}, false
}

// build turns one event record into a Provenance, applying the pane rule.
//
// ⚠️ `panesAvailable` is a separate fact from the contents of `panes`, and the
// two must not be collapsed. An empty-but-read listing proves a recorded pane is
// gone and the row is marked unroutable; an unreadable listing proves nothing
// and the row makes **no pane claim at all**, rendering exactly as it does when
// a value is absent. Marking those rows unroutable would assert that the pane
// does not resolve to this session on a host where the question was never
// answerable.
func (r *provenanceResolver) build(
	record eventRecord,
	names map[string]sessionName,
	panes map[int]Pane,
	panesAvailable bool,
) Provenance {
	provenance := Provenance{
		Host: record.Host,
		Cwd:  record.Cwd,
		Tool: record.ToolName,
	}
	paneID, err := strconv.Atoi(strings.TrimSpace(string(record.Pane)))
	if err != nil {
		// No pane recorded, or an unparseable one. Nothing is shown and the
		// row is not marked unroutable: there is no claim to distrust.
		return provenance
	}
	if !panesAvailable {
		// The pane was recorded but the listing could not be read, so nothing
		// can be said about it either way.
		return provenance
	}
	provenance.PaneRecorded = true
	provenance.Routable = OwnsPane(panes, paneID, names[record.SessionID].Name)
	if provenance.Routable {
		provenance.Pane = strconv.Itoa(paneID)
	}
	return provenance
}

// readEventsForProducer indexes a producer's event log, tolerating a ProducerID
// that carries the `session:` marker it should have put on its LivenessRef.
//
// The bare name is tried first, so the ordinary case is one open and the
// prefixed name is only consulted when the log is genuinely absent — the
// prefixed file cannot exist in a correct deployment, since the watcher names
// its log for the bare id. The retry is what lets an item whose producer wrote
// a perfectly good log resolve it, instead of silently rendering nothing
// because the resolver asked for a filename that was never going to exist.
func (r *provenanceResolver) readEventsForProducer(
	ctx context.Context,
	producerID ProducerID,
) map[string]eventRecord {
	events := r.readEvents(ctx, producerID)
	if len(events) > 0 {
		return events
	}
	bare, ok := strings.CutPrefix(string(producerID), sessionProducerPrefix)
	if !ok {
		return events
	}
	return r.readEvents(ctx, ProducerID(bare))
}

// readEvents indexes a producer's event log by the item id each line carries.
//
// The log is append-only and holds every event for that session — opens,
// closes, idle transitions — so a later line may describe the same item as an
// earlier one. The line that carries provenance wins: a close event records
// only `closed_by` and would otherwise overwrite the open event's host and cwd
// with empties.
func (r *provenanceResolver) readEvents(
	ctx context.Context,
	producerID ProducerID,
) map[string]eventRecord {
	events := map[string]eventRecord{}
	if r.stateDir == "" {
		return events
	}
	// Opened as an os.Root so every read is confined beneath the state dir. The
	// producer id is producer-supplied and validated only by NotEmptyString, so
	// a crafted id containing path separators would otherwise walk out of the
	// directory; scoping the handle makes the confinement structural rather
	// than an assumption about the id. Same pattern as the session registry
	// read in session-liveness-checker.go.
	root, err := os.OpenRoot(r.stateDir)
	if err != nil {
		// Absent state dir is the ordinary case for a store with no Claude Code
		// beside it, not an error worth reporting per item.
		glog.V(3).Infof("open attention state dir %s failed: %v", r.stateDir, err)
		return events
	}
	defer root.Close()

	name := string(producerID) + ".events.jsonl"
	file, err := root.Open(name)
	if err != nil {
		// Absent log is the ordinary case for a producer that never wrote one,
		// not an error worth reporting per item.
		glog.V(3).Infof("open event log %s failed: %v", filepath.Join(r.stateDir, name), err)
		return events
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	// Event lines carry a transcript path and a detail string, so the default
	// 64KB token limit is too small; a line that overflows would silently drop
	// the item's provenance.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		// A page load reads one log per open item's producer, and a log is
		// append-only and unbounded, so a client that has gone away must stop
		// the scan rather than let it run to the end of the file.
		select {
		case <-ctx.Done():
			glog.V(3).Infof("event log scan cancelled for producer %s", producerID)
			return events
		default:
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record eventRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			glog.V(3).
				Infof("parse event line in %s failed: %v", filepath.Join(r.stateDir, name), err)
			continue
		}
		if record.ItemID == "" {
			continue
		}
		previous, seen := events[record.ItemID]
		if seen && (previous.Host != "" || previous.Cwd != "" || previous.Pane != "") {
			continue
		}
		events[record.ItemID] = record
	}
	if err := scanner.Err(); err != nil {
		glog.V(3).Infof("read event log %s failed: %v", filepath.Join(r.stateDir, name), err)
	}
	return events
}

// sessionNames reads the session registry as `session id -> the name it holds
// now and the source that says who chose it`.
//
// The two are returned together rather than in two passes because the gate on
// the name is the source: a second scan of the registry to read the source
// would be a second place the two could disagree. The pane-ownership readers use
// `Name` only — the source gates what is *rendered*, never whether a session
// owns its pane.
//
// The registry is the only source that survives a rename: it rewrites `name`
// when a session is renamed, while an event record holds only the name as of
// the event. Comparing a pane title against a stale snapshot marks a genuinely
// correct pane unroutable, so the registry is preferred and the event's own
// name is not consulted.
//
// An unreadable registry returns an empty map, and every pane then has no name
// to compare against — which `OwnsPane` treats as unprovable and therefore
// routable. That direction is deliberate: reading an unreadable registry as
// "no session owns any pane" would mark every row unroutable the moment the
// store ran somewhere the registry is absent, and the store's own liveness
// checker takes the same position for the same reason.
func (r *provenanceResolver) sessionNames(ctx context.Context) map[string]sessionName {
	names := map[string]sessionName{}
	if r.sessionsDir == "" {
		return names
	}
	entries, err := os.ReadDir(r.sessionsDir)
	if err != nil {
		glog.V(2).Infof("read session registry %s failed: %v", r.sessionsDir, err)
		return names
	}
	for _, entry := range entries {
		select {
		case <-ctx.Done():
			glog.V(3).Infof("session registry scan cancelled")
			return names
		default:
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(r.sessionsDir, entry.Name()))
		if err != nil {
			glog.V(3).Infof("read session registry entry %s failed: %v", entry.Name(), err)
			continue
		}
		var record sessionRegistryEntry
		if err := json.Unmarshal(content, &record); err != nil {
			glog.V(3).Infof("parse session registry entry %s failed: %v", entry.Name(), err)
			continue
		}
		if record.SessionID != "" {
			names[record.SessionID] = sessionName{
				Name:   record.Name,
				Source: record.NameSource,
			}
		}
	}
	return names
}

// sessionModes reads the supervisor's spawn ledger as `session id -> mode`.
//
// ⚠️ This is the only source that separates a headless worker from a tab
// worker: nothing on the item does. A headless worker inherits its spawner's
// WEZTERM_PANE, so its item carries the spawner's pane id and reads as
// routable exactly like a tab worker's.
//
// It fails closed in every direction: an absent or unreadable directory, a
// missing record, an unparseable record and a mode outside the ledger's known
// set all leave the session absent from the map, and an absent session
// renders no control. The read is confined beneath spawnDir through an
// os.Root handle, so a name taken from the directory listing can never walk
// out of it; the item's session id is only ever a map key, never a path
// segment.
func (r *provenanceResolver) sessionModes(ctx context.Context) map[string]string {
	modes := map[string]string{}
	if r.spawnDir == "" {
		return modes
	}
	entries, err := os.ReadDir(r.spawnDir)
	if err != nil {
		glog.V(2).Infof("read spawn ledger %s failed: %v", r.spawnDir, err)
		return modes
	}
	root, err := os.OpenRoot(r.spawnDir)
	if err != nil {
		glog.V(2).Infof("open spawn ledger %s failed: %v", r.spawnDir, err)
		return modes
	}
	defer root.Close()
	for _, entry := range entries {
		select {
		case <-ctx.Done():
			glog.V(3).Infof("spawn ledger scan cancelled")
			return modes
		default:
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		content, err := root.ReadFile(entry.Name())
		if err != nil {
			glog.V(3).Infof("read spawn ledger entry %s failed: %v", entry.Name(), err)
			continue
		}
		var record spawnRecord
		if err := json.Unmarshal(content, &record); err != nil {
			glog.V(3).Infof("parse spawn ledger entry %s failed: %v", entry.Name(), err)
			continue
		}
		if record.SessionID == "" {
			continue
		}
		modes[record.SessionID] = record.Mode
	}
	return modes
}

// Vault layout: every task file lives under this directory and records its
// session and its status under these frontmatter keys.
const (
	// taskDirName is the vault directory holding the task files.
	taskDirName = "25 Tasks"
	// taskSessionKey is the frontmatter key recording the session a task
	// belongs to. It is the join key, and it is already written by the vault —
	// no new stored field is needed on the item.
	taskSessionKey = "claude_session_id"
	// taskStatusKey is the frontmatter key carrying the task's status. Read
	// only to break a tie between task files sharing one session id.
	taskStatusKey = "status"
	// goalDirName is the vault directory holding the goal files.
	goalDirName = "24 Goals"
	// topicDirName is the vault directory holding the topic pages.
	topicDirName = "23 Topics"
	// taskGoalsKey is the frontmatter key carrying a task's goals, as quoted
	// Obsidian wikilinks. See frontmatterList and wikilinkTitle for the
	// encoding actually read.
	taskGoalsKey = "goals"
	// topicGoalsHeading is the heading whose section lists a topic's goals.
	topicGoalsHeading = "## Goals"
)

//counterfeiter:generate -o ../mocks/task-index.go --fake-name TaskIndex . TaskIndex

// TaskIndex resolves the vault task a session is anchored to.
//
// The vault is the only source that knows *what* a session is working on: the
// event log and the session registry both describe where a session runs, and
// neither names the task behind it.
//
// ⚠️ It is an index rather than a lookup because the join is the expensive
// half. The vault holds over 8,000 task files and each candidate must be read
// to see which session it records, so the whole vault is read once at
// construction and every page load is a map hit.
type TaskIndex interface {
	// Lookup returns the task recorded for sessionID. ok is false when the
	// session anchors no task: an unnamed or unknown session, a vault that was
	// not configured, or one that could not be read.
	Lookup(sessionID string) (Task, bool)
}

// Task is the vault task a session is anchored to, plus the rung above it.
type Task struct {
	// Name is the task's title, which is its filename without the `.md`
	// suffix.
	Name string
	// Path is the task file's path relative to the vault root, e.g.
	// `25 Tasks/Fix the board.md`.
	Path string
	// GoalName is the title of the goal the task names first in its `goals:`
	// list. Empty when the task names no goal, when no entry is a valid
	// wikilink, or when no file of that title exists under `24 Goals/`.
	GoalName string
	// GoalPath is the goal file's path relative to the vault root, e.g.
	// `24 Goals/Fix the board.md`. Empty exactly when GoalName is.
	GoalPath string
	// TopicName is the title of the topic page that lists this goal under its
	// `## Goals` heading. Empty when no topic lists it.
	TopicName string
	// TopicPath is the topic file's path relative to the vault root, e.g.
	// `23 Topics/Attention Board Polish.md`. Empty exactly when TopicName is.
	TopicPath string
}

// NewTaskIndex builds the session -> task index from vaultDir's task files.
//
// It fails soft in every direction: an empty vaultDir, a vault that does not
// exist, an unreadable `25 Tasks/`, an unreadable `24 Goals/`, an unreadable
// `23 Topics/` and an unreadable file each yield no entry for the affected
// tasks and never an error. An empty vaultDir is the ordinary case for a host
// with no vault configured, not a fault.
//
// The goal rung is built before the task walk, because each indexed task
// carries the goal it names and the topic that lists it: both maps must exist
// before the first task file is read.
func NewTaskIndex(ctx context.Context, vaultDir string) TaskIndex {
	index := &taskIndex{
		bySession:  map[string]taskEntry{},
		goals:      map[string]struct{}{},
		goalTopics: map[string]goalTopic{},
	}
	if vaultDir == "" {
		return index
	}
	index.readGoalTitles(vaultDir)
	index.readGoalTopics(ctx, vaultDir)
	tasksDir := filepath.Join(vaultDir, taskDirName)
	// os.ReadDir returns entries sorted by filename, and the tie-break below
	// depends on it: candidates are added in ascending path order, so the later
	// candidate is the lexicographically greater path.
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		glog.V(2).Infof("read vault tasks dir %s failed: %v", tasksDir, err)
		return index
	}
	// Opened as an os.Root so every read is confined beneath the tasks
	// directory: the file names come from the directory listing, and scoping the
	// handle makes that confinement structural rather than an assumption about
	// the names. Same pattern as the event-log read in readEvents.
	root, err := os.OpenRoot(tasksDir)
	if err != nil {
		glog.V(2).Infof("open vault tasks dir %s failed: %v", tasksDir, err)
		return index
	}
	defer root.Close()
	for _, entry := range entries {
		select {
		case <-ctx.Done():
			glog.V(3).Infof("task index build cancelled")
			return index
		default:
		}
		index.addFile(root, entry)
	}
	return index
}

type taskIndex struct {
	bySession map[string]taskEntry
	// goals is the set of titles the vault holds as goal files under
	// `24 Goals/`. It is the existence guard a task's `goals:` entry must pass
	// before it resolves: the title comes from frontmatter, so it is never
	// trusted as a path. A nil or empty set resolves no goal.
	goals map[string]struct{}
	// goalTopics maps a goal title to the topic page that lists it under its
	// `## Goals` heading. A nil or empty map resolves no topic.
	goalTopics map[string]goalTopic
}

// taskEntry is one indexed task file: the task itself plus the only other
// frontmatter field read, kept solely to break a tie between files sharing a
// session id.
type taskEntry struct {
	task     Task
	terminal bool
}

// Lookup returns the task recorded for sessionID.
//
// An unknown session is an ordinary miss, and an empty sessionID is a miss
// too: addFile never indexes a file under "", so there is nothing for it to
// match.
func (t *taskIndex) Lookup(sessionID string) (Task, bool) {
	if t == nil {
		return Task{}, false
	}
	entry, ok := t.bySession[sessionID]
	if !ok {
		return Task{}, false
	}
	return entry.task, true
}

// addFile reads one directory entry and indexes it when it is a task file that
// records a session.
func (t *taskIndex) addFile(root *os.Root, entry os.DirEntry) {
	if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
		return
	}
	content, err := root.ReadFile(entry.Name())
	if err != nil {
		glog.V(3).Infof("read vault task %s failed: %v", entry.Name(), err)
		return
	}
	sessionID := frontmatterValue(content, taskSessionKey)
	if sessionID == "" {
		// No frontmatter, no key, or an empty value after trimming whitespace:
		// the file contributes no entry rather than an entry under "".
		return
	}
	task := Task{
		Name: strings.TrimSuffix(entry.Name(), ".md"),
		Path: filepath.Join(taskDirName, entry.Name()),
	}
	// The goal and the topic ride with the task: both derive from this file's
	// `goals:` list, so they are resolved here, once, rather than at page load.
	goalName, goalPath := firstGoal(content, t.goals)
	task.GoalName = goalName
	task.GoalPath = goalPath
	if goalName != "" {
		if topic, ok := t.goalTopics[goalName]; ok {
			task.TopicName = topic.name
			task.TopicPath = topic.path
		}
	}
	t.add(sessionID, taskEntry{
		task:     task,
		terminal: isTerminalTaskStatus(frontmatterValue(content, taskStatusKey)),
	})
}

// add records one candidate for a session, breaking ties deterministically.
//
// ⚠️ Several files may carry the same claude_session_id — measured in the live
// vault, 33 session ids map to between 2 and 6 task files. The tie-break is:
// prefer the file whose `status` is neither `completed` nor `aborted`, because
// that is the task the session is still anchored to; if every candidate is
// terminal, the lexicographically last path wins. Candidates arrive in
// ascending path order (os.ReadDir sorts), so "the later candidate" is "the
// lexicographically greater path".
func (t *taskIndex) add(sessionID string, candidate taskEntry) {
	existing, seen := t.bySession[sessionID]
	if seen && !existing.terminal && candidate.terminal {
		// The indexed task is in flight and the candidate is not: keep the one
		// the session is still working on.
		return
	}
	t.bySession[sessionID] = candidate
}

// isTerminalTaskStatus reports whether a task's `status` means it is no longer
// in flight. Only the two statuses the tie-break names are terminal; anything
// else — including an absent or unrecognised status — reads as in flight, so a
// vault that spells its statuses differently degrades to the path tie-break
// rather than to no answer at all.
func isTerminalTaskStatus(status string) bool {
	switch status {
	case "completed", "aborted":
		return true
	default:
		return false
	}
}

// frontmatterValue returns the value of the scalar `key` in content's YAML
// frontmatter block, or "" when there is no block, the block is never closed,
// or the key is absent.
//
// ⚠️ Parsed by hand on purpose. The block is a flat list of `key: value` lines
// and this reads exactly one scalar key; go.mod carries a YAML parser only as
// an **indirect** dependency and no Go file imports one, so using it would
// promote a YAML library to a direct dependency to read a single scalar.
func frontmatterValue(content []byte, key string) string {
	for _, line := range frontmatterLines(content) {
		name, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) != key {
			continue
		}
		return strings.TrimSpace(value)
	}
	return ""
}

// frontmatterLines returns the lines between the first two lines that are
// exactly `---`, or nil when there is no such pair.
func frontmatterLines(content []byte) []string {
	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	start := -1
	for i, line := range lines {
		if line == "---" {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	for i := start + 1; i < len(lines); i++ {
		if lines[i] == "---" {
			return lines[start+1 : i]
		}
	}
	return nil
}

// frontmatterList returns the entries of the list `key` in content's YAML
// frontmatter block, or nil when there is no block, the key is absent, or the
// key line carries the inline empty form `[]`.
//
// The entries are the lines that follow the key line while their trimmed form
// starts with `-`; the first line that does not ends the list, so the next
// frontmatter key terminates it. Parsed by hand for the same reason
// frontmatterValue is: the block is a flat list of lines, and go.mod carries a
// YAML parser only as an indirect dependency.
//
// ⚠️ The block comes from frontmatterLines, which excludes the closing `---`
// delimiter. That matters here and not for frontmatterValue: the delimiter line
// starts with `-`, so a reader that re-split the content itself would collect it
// as an entry.
func frontmatterList(content []byte, key string) []string {
	var entries []string
	inList := false
	for _, line := range frontmatterLines(content) {
		if !inList {
			name, value, ok := strings.Cut(line, ":")
			if !ok || strings.TrimSpace(name) != key {
				continue
			}
			if strings.TrimSpace(value) == "[]" {
				return nil
			}
			inList = true
			continue
		}
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "-") {
			break
		}
		entries = append(entries, trimmed)
	}
	return entries
}

// wikilinkTitle returns the title one `goals:` list item names.
//
// It strips the list marker, the surrounding quotes, the `[[ ]]` brackets and
// any `|alias`, then trims. ok is false when the entry carries no `[[ ]]` at
// all — a bare title is **not** a valid entry — and false when the title is
// empty after trimming. A quote is only stripped when both ends carry the same
// one, so a title that legitimately ends in an apostrophe is left alone.
func wikilinkTitle(raw string) (string, bool) {
	entry := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "-"))
	if len(entry) >= 2 {
		first, last := entry[0], entry[len(entry)-1]
		if (first == '"' || first == '\'') && first == last {
			entry = strings.TrimSpace(entry[1 : len(entry)-1])
		}
	}
	open := strings.Index(entry, "[[")
	if open < 0 {
		return "", false
	}
	rest := entry[open+2:]
	end := strings.Index(rest, "]]")
	if end < 0 {
		return "", false
	}
	inner := rest[:end]
	if alias := strings.Index(inner, "|"); alias >= 0 {
		inner = inner[:alias]
	}
	title := strings.TrimSpace(inner)
	if title == "" {
		return "", false
	}
	return title, true
}

// firstGoal returns the first goal the task's `goals:` list names, as
// `(title, path)`, or two empty strings when the task names no goal.
//
// ⚠️ The encoding, measured live over 1,619 goal-carrying tasks: every entry is
// a **quoted Obsidian wikilink** — `- "[[Goal Title]]"` — with single-quoted
// (`- '[[Goal Title]]'`) and 4-space-indented variants also occurring, and the
// empty form is inline (`goals: []`). Quotes, `[[ ]]` brackets and any
// `|alias` are stripped before the path is built. A bare title is not a valid
// entry and yields nothing rather than being accepted as a title.
//
// ⚠️ A task carrying several goals names the **first** only; the rest are
// unrendered, deliberately.
//
// ⚠️ The map is the existence guard, and it is the only thing that lets a
// title become a path: the title comes from frontmatter, so a path built from
// it unguarded would be a vault-derived string reaching the filesystem. The
// first entry that is a valid wikilink *and* names a known goal wins.
func firstGoal(content []byte, goals map[string]struct{}) (string, string) {
	for _, entry := range frontmatterList(content, taskGoalsKey) {
		title, ok := wikilinkTitle(entry)
		if !ok {
			continue
		}
		if _, exists := goals[title]; !exists {
			continue
		}
		return title, filepath.Join(goalDirName, title+".md")
	}
	return "", ""
}

// goalTopic is the topic page a goal is listed by: its title and its path
// relative to the vault root, e.g. `23 Topics/Attention Board Polish.md`.
type goalTopic struct {
	name string
	path string
}

// readGoalTitles records on the index the titles the vault holds as goals: one
// per `*.md` entry under `<vault>/24 Goals/`, keyed by the filename without its
// `.md` suffix.
//
// A missing or unreadable directory yields an empty set, logged at V(2) —
// fail-soft, because a vault with no goal directory is a legal state and must
// resolve no goal rather than fail the page. Only os.ReadDir is needed: this
// reads no file by name.
func (t *taskIndex) readGoalTitles(vaultDir string) {
	t.goals = map[string]struct{}{}
	goalsDir := filepath.Join(vaultDir, goalDirName)
	entries, err := os.ReadDir(goalsDir)
	if err != nil {
		glog.V(2).Infof("read vault goals dir %s failed: %v", goalsDir, err)
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		t.goals[strings.TrimSuffix(entry.Name(), ".md")] = struct{}{}
	}
}

// readGoalTopics records on the index, per goal title, the topic page that lists
// it, built from every `*.md` under `<vault>/23 Topics/`.
//
// ⚠️ The topic encoding, measured over all 12 topic pages in the live vault:
// each uses exactly the heading `## Goals`, with entries as **bare wikilinks**
// (`- [[Title]]`). The section runs from the line after that heading to the
// next line whose trimmed form starts with `#`, or to the end of the file; only
// lines inside it whose trimmed form starts with `-` contribute an entry. A
// page with no `## Goals` heading, or with a `### Goals` subheading instead,
// resolves no topic.
//
// ⚠️ A `## Goals` section **mixes goals and tasks** — `23 Topics/Attention
// Board Polish.md` lists 8 entries, all tasks and zero goals, and four titles
// exist as both a goal file and a task file. A title is therefore kept only
// when it is in `goals`, the set read from `24 Goals/`; the section is never
// trusted to contain only goals.
//
// ⚠️ No tie-break for a goal listed by several topics: the first topic found
// wins, and with os.ReadDir's sorted order that is the lexicographically first
// topic path. A title's topic is recorded only when it has none yet.
//
// It fails soft: an unreadable directory or file yields no topic for the
// affected goals, logged at V(2)/V(3), never an error. Each file is read
// through an os.Root handle opened once, so a name taken from the directory
// listing can never walk out of the directory.
func (t *taskIndex) readGoalTopics(ctx context.Context, vaultDir string) {
	t.goalTopics = map[string]goalTopic{}
	if len(t.goals) == 0 {
		return
	}
	topicsDir := filepath.Join(vaultDir, topicDirName)
	entries, err := os.ReadDir(topicsDir)
	if err != nil {
		glog.V(2).Infof("read vault topics dir %s failed: %v", topicsDir, err)
		return
	}
	root, err := os.OpenRoot(topicsDir)
	if err != nil {
		glog.V(2).Infof("open vault topics dir %s failed: %v", topicsDir, err)
		return
	}
	defer root.Close()
	for _, entry := range entries {
		select {
		case <-ctx.Done():
			glog.V(3).Infof("goal topic index build cancelled")
			return
		default:
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		content, err := root.ReadFile(entry.Name())
		if err != nil {
			glog.V(3).Infof("read vault topic %s failed: %v", entry.Name(), err)
			continue
		}
		for _, title := range goalSectionTitles(content) {
			if _, isGoal := t.goals[title]; !isGoal {
				continue
			}
			if _, seen := t.goalTopics[title]; seen {
				continue
			}
			t.goalTopics[title] = goalTopic{
				name: strings.TrimSuffix(entry.Name(), ".md"),
				path: filepath.Join(topicDirName, entry.Name()),
			}
		}
	}
}

// goalSectionTitles returns the titles listed under the `## Goals` heading of a
// topic page, as bare wikilinks (`- [[Title]]`).
//
// The section runs from the line after that heading to the next line whose
// trimmed form starts with `#`, or to the end of the file; only lines inside it
// whose trimmed form starts with `-` contribute an entry. A page with no
// `## Goals` heading, or with a `### Goals` subheading instead, contributes
// nothing — the subheading ends the scan before any entry is read.
func goalSectionTitles(content []byte) []string {
	var titles []string
	inSection := false
	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inSection {
			if trimmed == topicGoalsHeading {
				inSection = true
			}
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			break
		}
		if !strings.HasPrefix(trimmed, "-") {
			continue
		}
		title, ok := wikilinkTitle(trimmed)
		if !ok {
			continue
		}
		titles = append(titles, title)
	}
	return titles
}
