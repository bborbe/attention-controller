// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	libtime "github.com/bborbe/time"
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
	// ProductionTouching reports whether the vault task this item's session is
	// anchored to declares a production-touching step — the operator's authored
	// `- [ ] ⚠️ production-touching — <command>` marker on one of the task's own
	// subtask lines. When true the board withholds the Allow / Deny pair, because
	// such a park is irreversible and a one-click board approval of it is the harm
	// the exclusion exists to prevent.
	//
	// ⚠️ It is a control gate, not a line fact, and it is deliberately NOT a
	// member of Resolved() — the same rule Headless follows. Resolved() gates
	// whether the provenance div renders at all, and its members are the values
	// that div draws; a boolean that draws nothing would put a non-drawing value
	// in the line's gate.
	//
	// ⚠️ Fail-OPEN, the opposite polarity to Headless: an absent, unreadable or
	// unparsable task file, a task file whose session does not match, and an
	// absent marker all leave this false, and false renders the pair. The worst
	// case of that direction is a pair on a park whose task did not declare one —
	// never a missing pair on a park that did.
	ProductionTouching bool
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

// NewProvenanceResolver creates a resolver reading the producers' event logs
// through eventLogs, the session registry under sessionsDir, the supervisor's
// spawn ledger under spawnDir, panes from the lister, and the vault tasks from
// the index.
//
// ⚠️ The event logs are read through the injected reader rather than a bare
// state directory, so the read path's cost is observable: the reader is asked
// only for the bytes appended since the resolver's last read of that producer.
//
// ⚠️ The index is built by the caller, once, and handed in — it is never built
// here and never inside Resolve. The live vault holds over 8,000 task files, so
// building it per page load would re-read every task file on every board
// refresh, on a page the SSE stream serves continuously. A nil index is legal
// and resolves no task, which is what a host with no configured vault gets.
func NewProvenanceResolver(
	eventLogs EventLogReader,
	sessionsDir string,
	spawnDir string,
	panes PaneLister,
	tasks TaskIndex,
	currentDateTimeGetter libtime.CurrentDateTimeGetter,
) ProvenanceResolver {
	return &provenanceResolver{
		eventLogs:             eventLogs,
		sessionsDir:           sessionsDir,
		spawnDir:              spawnDir,
		panes:                 panes,
		tasks:                 tasks,
		currentDateTimeGetter: currentDateTimeGetter,
		producerLogs:          map[ProducerID]*producerEventLog{},
	}
}

// provenanceCacheWindow is how long a host snapshot — the pane listing, the
// session registry and the spawn ledger — is served before all three are read
// again. It bounds the per-resolve host scan so a store with many open boards
// no longer multiplies one scan by the number of connected clients: every
// store change wakes every stream, and without this window each stream re-read
// the pane listing, the registry and the ledger on every wake. The window is
// short enough that a pane that has just closed still disappears within it.
const provenanceCacheWindow = libtime.Duration(2 * time.Second)

type provenanceResolver struct {
	eventLogs             EventLogReader
	sessionsDir           string
	spawnDir              string
	panes                 PaneLister
	tasks                 TaskIndex
	currentDateTimeGetter libtime.CurrentDateTimeGetter
	// eventsMu guards producerLogs, the per-producer incremental read state.
	//
	// ⚠️ It is held across the read itself, and that is deliberate rather than
	// an oversight. The read it guards is a local file read bounded by the
	// bytes appended since the last render — not the pane listing, a subprocess
	// with its own multi-second timeout — so holding the lock costs a bounded
	// read rather than a blocking call. Releasing it would let two concurrent
	// renders read the same bytes from the same base offset and merge them
	// twice, which is precisely the state this field exists to serialize.
	eventsMu sync.Mutex
	// producerLogs is the byte offset and decoded events the resolver has
	// already consumed for each producer's event log.
	producerLogs map[ProducerID]*producerEventLog
	// mu guards the cached snapshot and the refresh flag below. ⚠️ It is NOT
	// held across the refresh itself — see hostState.
	mu sync.Mutex
	// cached is the last host snapshot, or nil before the first refresh.
	cached *hostState
	// cachedAt is the clock reading at which cached was taken.
	cachedAt libtime.DateTime
	// refreshing is the token of the refresh currently in flight, or nil when none
	// is, so the common case — many streams waking at once — does not multiply one
	// subprocess by the number of callers.
	//
	// ⚠️ A token rather than a bool, and the difference is the incident path. Two
	// callers can both start a refresh on the cold path; with a bool the first to
	// finish clears the marker for the second, which is still parked in its exec,
	// so a third caller arriving once provenanceCacheWindow has lapsed finds the
	// cache stale AND the marker clear and starts another subprocess alongside it.
	// Under a wedged mux a refresh takes the full paneListingTimeout — longer than
	// the window — so that is the ordinary case there, not a race. Only the
	// goroutine that set the token clears it.
	//
	// ⚠️ It is single-flight only once a snapshot exists: the stale-serve branch in
	// hostState requires cached != nil, so on a cold start every concurrent caller
	// falls through and runs its own refresh — which is precisely when many streams
	// wake at once. That is bounded rather than unbounded, since each caller runs
	// exactly one exec and the listing carries its own paneListingTimeout, and it
	// is pinned by the cold-start spec.
	refreshing chan struct{}
}

// hostState is the resolver's view of the host at one instant: the pane
// listing and its error, the session registry's names, and the spawn ledger's
// modes. The three are captured together because they are read together — one
// refresh produces all three, so a page never renders a pane listing from one
// instant against names from another.
type hostState struct {
	panes    map[int]Pane
	panesErr error
	names    map[string]sessionName
	modes    map[string]string
}

// hostState returns the pane listing, the session registry and the spawn
// ledger, serving them from a cache refreshed at most once per
// provenanceCacheWindow.
//
// ⚠️ The mutex guards the cache, NOT the refresh. It is released across the
// refresh deliberately, because a refresh runs a subprocess — and holding a
// lock across a call that can block is what turns one slow reader into a
// process-wide outage. The previous shape held it across the whole
// check-and-refresh and claimed the wait was "bounded rather than unbounded
// because the owning request's ctx cancellation propagates"; that claim was
// false whenever the owner's ctx carried no deadline, which is the normal case
// for a stream handler. Measured 2026-10-03: a wedged `wezterm cli list` held
// the lock for over ten minutes, and every Resolve in the process — the board
// page, the jump, and every connected stream — queued behind it, while
// /healthz and the API stayed fast because those never call Resolve.
//
// Single-flight is kept by the refreshing token instead, and the request path
// never waits on the refresh at all: a caller past the window starts one
// BEHIND itself, serves the snapshot already in hand, and returns. Only a cold
// start — no snapshot to serve — reads synchronously, and that path is bounded
// by paneListingTimeout rather than by luck.
//
// ⚠️ The price of that is staleness: a caller past the window is served a
// snapshot up to one refresh cycle old, where the previous shape served it a
// freshly-read one at the cost of the subprocess. Measured on the deployed
// service, that cost was the whole page: a board page loaded after a quiet
// period — always past the window — took a median 235 ms against 23 ms when
// the snapshot was fresh, because it waited on `wezterm cli list` before it
// could render. A pane listing a second or two old is better than a stalled
// request, which is the trade the stale-serve branch already made for the
// concurrent case.
//
// ⚠️ The per-producer event-log reads are deliberately NOT cached here. Those
// describe individual items and change as items are posted, so a newly pushed
// item must still resolve on the first push after it lands; only the three
// host-wide reads above are shared.
func (r *provenanceResolver) hostState(ctx context.Context) hostState {
	now := r.currentDateTimeGetter.Now()

	r.mu.Lock()
	if r.cached != nil {
		state := *r.cached
		if now.Sub(r.cachedAt) >= provenanceCacheWindow && r.refreshing == nil {
			// Past the window and nothing is in flight. Start a refresh BEHIND
			// this request and serve what is already in hand; the caller never
			// waits on it. This is the path a page loaded after a quiet period
			// always takes, and it is the one the measured medians above are
			// about.
			//
			// ⚠️ context.WithoutCancel, never ctx. The ctx handed in is the HTTP
			// request's and is cancelled the moment the response is written,
			// which is before a background refresh finishes — passing it through
			// would cancel every refresh the page path starts. WithoutCancel
			// keeps the values and drops the cancellation.
			//
			// ⚠️ The bound covers the SUBPROCESS, not the whole read. panes.List
			// applies paneListingTimeout itself, but readHostState evaluates
			// sessionNames and sessionModes BEFORE it, and those honour ctx.Done()
			// alone — which WithoutCancel has just disarmed. Both are finite local
			// directory walks with no subprocess, so this is a loose bound rather
			// than a hang, but the guarantee is narrower than "the read".
			//
			// ⚠️ Started only when r.refreshing == nil. That is what stops one
			// stale window multiplying into one subprocess per caller — the
			// property the refreshing token's own comment exists to protect, and
			// the property the "reads the pane listing once for a whole page"
			// spec depends on.
			//
			// ⚠️ A raw goroutine, deliberately, against go-concurrency/no-raw-go-func.
			// That rule's premise is fan-out — several concurrent operations needing
			// run.All / run.Sequential and a hand-rolled WaitGroup to join them. This
			// is the opposite shape: one detached task, no fan-out, no WaitGroup, and
			// no leak beyond a single refresh bounded by paneListingTimeout. Every
			// run.* primitive WAITS, so routing this through one would re-introduce
			// exactly the request-path stall the change removes. Recorded here rather
			// than left as a silent exemption.
			//
			// ⚠️ Nothing on this path recovers. On the request goroutine a panic would
			// have been absorbed per-connection by net/http; here it terminates the
			// process. No panic site is reachable today — sessionNames and sessionModes
			// guard every error, and paneLister.List guards cmd.Output and
			// json.Unmarshal — but the blast radius is wider than it was.
			token := make(chan struct{})
			r.refreshing = token
			go r.refresh(context.WithoutCancel(ctx), token)
		}
		r.mu.Unlock()
		return state
	}

	// Cold start: there is nothing to serve, so this path stays blocking and
	// reads the snapshot itself. Every concurrent caller falls through here,
	// which is the bounded exposure the refreshing field's comment records.
	token := make(chan struct{})
	r.refreshing = token
	r.mu.Unlock()

	// ⚠️ The plain ctx here, not WithoutCancel: this caller is waiting for the
	// read, so its cancellation must end it.
	return r.refresh(ctx, token)
}

// refresh reads one host snapshot and publishes it, clearing the single-flight
// token on the way out whichever way it exits. It returns what it read so the
// cold-start path can serve the same value without a second read.
func (r *provenanceResolver) refresh(ctx context.Context, token chan struct{}) hostState {
	// Deferred rather than inlined after the read: a panic in any of the three
	// readers must still clear the token, or the resolver stays pinned to a
	// snapshot that will never be replaced.
	defer r.finishRefresh(token)

	state := r.readHostState(ctx)
	r.mu.Lock()
	r.cached = &state
	// ⚠️ Stamped at publication, not at entry. The clock was read before the
	// refresh, and a refresh can take up to paneListingTimeout — so stamping it
	// there would publish a snapshot already older than provenanceCacheWindow
	// and the next sequential caller would refresh again for another full bound,
	// leaving the cache giving zero relief in exactly the case it exists for.
	// The window is measured from when the snapshot became available, which is
	// the only reading that makes it a window.
	//
	// ⚠️ It bounds the STAMP, not the content. A refresh that took the full
	// paneListingTimeout is stamped at publication but began reading a bound
	// earlier, so the worst-case age of a value a reader sees is
	// provenanceCacheWindow plus one refresh — about five seconds, not two.
	//
	// ⚠️ And because the lock is released across the refresh, two overlapping
	// cold-start refreshes can publish out of READ order: the one that read first
	// but finished last overwrites the fresher snapshot, so the window can serve
	// content one refresh older than this stamp suggests, for one window after the
	// overwrite. Bounded and self-healing — the next refresh replaces it — rather
	// than an age that grows.
	r.cachedAt = r.currentDateTimeGetter.Now()
	r.mu.Unlock()
	return state
}

// readHostState reads all three host-wide sources in one pass.
func (r *provenanceResolver) readHostState(ctx context.Context) hostState {
	state := hostState{
		names: r.sessionNames(ctx),
		modes: r.sessionModes(ctx),
	}
	state.panes, state.panesErr = r.panes.List(ctx)
	if state.panesErr != nil {
		// Logged, never flattened into an empty map: "no panes" would mark every
		// row unroutable, which asserts the pane does not resolve to this session —
		// something an unreadable listing cannot establish.
		glog.V(2).Infof("pane listing unavailable, rendering no pane: %v", state.panesErr)
	}
	return state
}

// finishRefresh clears the in-flight token, but only if it is still the one this
// refresh set. A second refresh that started afterwards overwrote it, and
// clearing it here would reopen the single-flight window while that second
// refresh is still parked in its exec — the amplification the token exists to
// prevent.
func (r *provenanceResolver) finishRefresh(token chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.refreshing == token {
		r.refreshing = nil
	}
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
	// The pane listing, the session registry and the spawn ledger are host-wide
	// reads that are identical for every row, so they are fetched together from
	// hostState's cache rather than re-read on every resolve. A listing that could
	// not be read yields no pane claims at all — see hostState, which carries the
	// error through and logs it rather than flattening it into an empty map.
	state := r.hostState(ctx)
	panes := state.panes
	panesAvailable := state.panesErr == nil
	names := state.names
	modes := state.modes

	// One read of each producer's log, reused across that producer's items.
	// A session with six open items would otherwise re-read the same file six
	// times.
	byProducer := make(map[ProducerID]map[string]eventRecord)
	for _, item := range items {
		// readEvents below reads only the bytes appended since the last render,
		// but a producer that has just written a large tail still hands the
		// decode real work, so the loop honours cancellation rather than
		// relying on the per-item work being cheap.
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
			provenance.ProductionTouching = task.ProductionTouching
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
	// ownerLivenessPrefix marks an `owner:<id>` ref — a producer that is
	// supposed to exit, whose item survives while the OWNER lives. The value is
	// a session id exactly as `session:`'s is, so the identity path recovers it
	// the same way.
	ownerLivenessPrefix = "owner:"
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
// All three liveness models are handled because all three are legal:
// `session:<id>` names the session directly, `owner:<id>` names the session the
// answer belongs to — a worker's operator gate resolves its OWNER's pane and
// name rather than falling through to the exited worker's id — and
// `heartbeat:<path>` names a file the watcher maintains whose base name is the
// session id. An item on none of them — a cron job, a dark-factory run, an
// agent — yields "", which is the honest answer: those producers have no session
// to look up, so the name-keyed join cannot apply to them.
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
		// An `owner:<id>` ref carries a session id exactly as `session:` does,
		// so it resolves the same way. Without this the ref falls through to
		// ProducerID, and for a worker-posted operator gate that names the
		// EXITED worker — so the card resolves no pane and claims none, while
		// the store itself resolves the owner fine.
		if id, ok := strings.CutPrefix(ref, ownerLivenessPrefix); ok {
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

// producerEventLog is the resolver's accumulated state for one producer's
// event log: how many bytes of it have been consumed, and the records those
// bytes decoded to.
//
// ⚠️ offset only ever advances by READING the file, never by waiting for a
// notification or a timer. That is what preserves the freshness guarantee: a
// just-appended line is visible to the very next render, because the render
// stats the file and reads whatever lies beyond the offset.
type producerEventLog struct {
	offset int64
	events map[string]eventRecord
}

// readEvents indexes a producer's event log by the item id each line carries,
// reading only the bytes appended since the last render.
//
// The log is append-only and holds every event for that session — opens,
// closes, idle transitions — so a later line may describe the same item as an
// earlier one. The line that carries provenance wins: a close event records
// only `closed_by` and would otherwise overwrite the open event's host and cwd
// with empties.
//
// ⚠️ A render over an unchanged log reads NOTHING: the size is unchanged and
// the accumulated events are returned as they stand. Only the bytes beyond the
// remembered offset are read and decoded, so the work is proportional to what
// was appended rather than to the whole log.
func (r *provenanceResolver) readEvents(
	ctx context.Context,
	producerID ProducerID,
) map[string]eventRecord {
	// A nil reader resolves no event, mirroring how a nil task index resolves
	// no task. A host with no event-log source is a legal configuration.
	if r.eventLogs == nil {
		return map[string]eventRecord{}
	}

	r.eventsMu.Lock()
	defer r.eventsMu.Unlock()

	log, ok := r.producerLogs[producerID]
	if !ok {
		log = &producerEventLog{events: map[string]eventRecord{}}
		r.producerLogs[producerID] = log
	}

	size, ok := r.eventLogs.Size(producerID)
	if !ok {
		// An absent log is the ordinary case for a producer that never wrote
		// one. It must not erase what the log yielded before: a log that
		// vanished is not the same as a log that says the item is gone.
		return log.events
	}
	if size == log.offset {
		// Nothing was appended since the last read. This is the whole point of
		// the change: a render over an unchanged log reads no bytes at all.
		return log.events
	}

	base := log.offset
	accumulated := log.events
	if size < log.offset {
		// The log shrank: it was truncated or replaced, so the remembered
		// offset points into a file that no longer exists in that shape.
		// Re-read it from the start rather than misread it from the middle.
		accumulated = map[string]eventRecord{}
		base = 0
	}

	data, err := r.eventLogs.ReadFrom(ctx, producerID, base)
	if err != nil {
		// Fail soft: an unreadable log proves nothing, so the accumulated
		// events come back unchanged rather than being discarded.
		glog.V(3).Infof("read event log for producer %s failed: %v", producerID, err)
		return log.events
	}

	merged, consumed, ok := r.mergeEventBytes(ctx, producerID, accumulated, data)
	if !ok {
		// Cancelled mid-decode. Nothing is published and the offset does not
		// move, so the next render re-reads the same bytes and merges them
		// idempotently.
		return log.events
	}

	// ⚠️ Publish a FRESH map, never mutate one already returned. Resolve reads
	// the returned map OUTSIDE this lock, so a map that has been handed out
	// must be immutable; copy-on-write is what keeps that true. The no-change
	// paths above return the existing map, which is never written again.
	log.events = merged
	// ⚠️ Advanced by the bytes actually consumed — the index of the last
	// newline plus one, added to the base — never by the file size. A torn
	// final line lies beyond the last newline and must not be counted against
	// the offset, or its remainder would be lost when the line completes.
	log.offset = base + int64(consumed)
	return merged
}

// mergeEventBytes decodes the complete lines in data and merges them into
// accumulated, returning the merged map, the number of bytes consumed and
// whether the decode ran to completion.
//
// ⚠️ Only complete lines are consumed. Everything up to and including the LAST
// newline is complete; anything after it is a torn final line — a write caught
// mid-line — and is left unconsumed so the next read sees it whole.
func (r *provenanceResolver) mergeEventBytes(
	ctx context.Context,
	producerID ProducerID,
	accumulated map[string]eventRecord,
	data []byte,
) (map[string]eventRecord, int, bool) {
	lastNewline := bytes.LastIndexByte(data, '\n')
	if lastNewline < 0 {
		// No complete line in the tail: consume nothing and leave the offset
		// where it was.
		return accumulated, 0, true
	}

	merged := make(map[string]eventRecord, len(accumulated))
	for itemID, record := range accumulated {
		merged[itemID] = record
	}

	for _, line := range strings.Split(string(data[:lastNewline+1]), "\n") {
		// A client that has gone away must stop the decode rather than let it
		// run to the end of a large tail.
		select {
		case <-ctx.Done():
			glog.V(3).Infof("event log scan cancelled for producer %s", producerID)
			return accumulated, 0, false
		default:
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var record eventRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			glog.V(3).Infof("parse event line for producer %s failed: %v", producerID, err)
			continue
		}
		if record.ItemID == "" {
			continue
		}
		previous, seen := merged[record.ItemID]
		if seen && (previous.Host != "" || previous.Cwd != "" || previous.Pane != "") {
			continue
		}
		merged[record.ItemID] = record
	}
	return merged, lastNewline + 1, true
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
//
// ⚠️ It calls `os.ReadDir` / `os.OpenRoot` / `json.Unmarshal` directly rather
// than taking them as injected dependencies, and that is deliberate rather than
// an oversight: this file **is** the I/O boundary. `sessionNames`, `readEvents`
// and `readGoalTopics` beside it all read their own directory the same way, and
// the resolver takes the whole page at once precisely so those host-wide reads
// happen once per refresh instead of once per row. Injecting a filesystem
// abstraction here would make this method the only one of the four that is
// shaped differently, and would move the seam away from the place the batching
// argument lives.
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

// productionTouchingMarker matches the operator's authored production-touching
// marker on a task's own subtask line, e.g.
// `- [ ] ⚠️ production-touching — `make install“.
//
// ⚠️ List-item form only, never a bare substring. Task files discuss the phrase
// in ordinary prose — an `# Impact`, a `# Success Criteria` or a `# Progress`
// entry, or a non-checkbox bullet — and a substring match fires on all of it,
// removing a control the operator needs. Only the checkbox marker counts.
//
// The parts, in order:
//   - `(?m)^` — the start of any line, so one Match over the whole file finds
//     the marker on any line;
//   - `\s*-\s*` — a list item, indented or not (the vault indents subtasks);
//   - `\[[ x/]\]` — a task checkbox in every state the vault writes: open
//     (` `), checked (`x`) and in-progress (`/`);
//   - `(?:⚠️?)?` — the warning glyph, optional as a whole. ⚠️ The outer group
//     is what makes the glyph optional: the glyph is two code points (U+26A0
//     and the variation selector U+FE0F), so a bare `⚠️?` would make only the
//     selector optional and require the base glyph, failing a marker written
//     without it;
//   - `\s*production-touching\s*—` — the phrase and the em-dash (U+2014)
//     separator, each required.
var productionTouchingMarker = regexp.MustCompile(
	`(?m)^\s*-\s*\[[ x/]\]\s*(?:⚠️?)?\s*production-touching\s*—`,
)

// hasProductionTouchingMarker reports whether content carries the operator's
// production-touching marker on any line. It scans content the caller already
// holds in memory during the task-index build, so it opens no file and reads
// no directory. Every input it cannot match — including content that never
// reached it because the file read failed — resolves to false, the fail-open
// direction.
func hasProductionTouchingMarker(content []byte) bool {
	return productionTouchingMarker.Match(content)
}
