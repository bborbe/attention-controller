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
// Every field is a *claim about the event*, not about the store: the store
// carries none of these (its fifteen-field Item has no host, cwd, tool or
// pane), so they arrive from the producer's own event log and are missing for
// any item whose producer wrote no event.
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
}

// Resolved reports whether anything about this item's origin could be told.
// When false the page renders exactly what it rendered before this change —
// no provenance line at all — which is how an item with no provenance source
// degrades rather than failing.
func (p Provenance) Resolved() bool {
	return p.Host != "" || p.Cwd != "" || p.Tool != "" || p.Pane != ""
}

// Provenances is the resolver's answer for a whole page, keyed by item id.
type Provenances map[ItemID]Provenance

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
// stateDir, the session registry under sessionsDir, and panes from the lister.
func NewProvenanceResolver(
	stateDir string,
	sessionsDir string,
	panes PaneLister,
) ProvenanceResolver {
	return &provenanceResolver{
		stateDir:    stateDir,
		sessionsDir: sessionsDir,
		panes:       panes,
	}
}

type provenanceResolver struct {
	stateDir    string
	sessionsDir string
	panes       PaneLister
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
	panes := r.panes.List(ctx)
	names := r.sessionNames(ctx)

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
			events = r.readEvents(ctx, item.ProducerID)
			byProducer[item.ProducerID] = events
		}
		record, found := events[string(item.DedupKey)]
		if !found {
			// No event for this item: its producer wrote no log line, or the
			// log has been pruned. Render absent rather than falling back to
			// the session's newest event, which describes a different item.
			continue
		}
		resolved[item.ItemID] = r.build(record, names, panes)
	}
	return resolved
}

// build turns one event record into a Provenance, applying the pane rule.
func (r *provenanceResolver) build(
	record eventRecord,
	names map[string]string,
	panes map[int]Pane,
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
	provenance.PaneRecorded = true
	provenance.Routable = OwnsPane(panes, paneID, names[record.SessionID])
	if provenance.Routable {
		provenance.Pane = strconv.Itoa(paneID)
	}
	return provenance
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

// sessionNames reads the session registry as `session id -> the name it holds now`.
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
func (r *provenanceResolver) sessionNames(ctx context.Context) map[string]string {
	names := map[string]string{}
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
			names[record.SessionID] = record.Name
		}
	}
	return names
}
