// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

// whoNeedsMeTimeFormat is the wire timestamp format for this feed.
const whoNeedsMeTimeFormat = "2006-01-02T15:04:05Z07:00"

// Source status values for the per-source block.
const (
	// sourceOK means the read succeeded.
	sourceOK = "ok"
	// sourceFailed means the read was attempted and failed.
	sourceFailed = "failed"
)

// WhoNeedsMeSources reports whether each host read succeeded, so a caller can
// tell "no sessions" from "registry down".
type WhoNeedsMeSources struct {
	// Store is the attention store read.
	Store string `json:"store"`
	// Registry is the session-registry read.
	Registry string `json:"registry"`
	// Panes is the WezTerm pane listing.
	Panes string `json:"panes"`
}

// WhoNeedsMeRow is one open attention item joined to its provenance, as DATA.
//
// ⚠️ This is not a rendering: every field is a structured value the caller
// formats itself. A JSON envelope around one rendered feed would satisfy a
// content-type check while being exactly the renderer the charter forbids.
type WhoNeedsMeRow struct {
	// ItemID is the store's item id (unchanged from /api/1.0/attention). In
	// replay mode it equals DedupKey, because there is no store id.
	ItemID string `json:"item_id"`
	// DedupKey is the event log's item id — the classifiers' join key.
	DedupKey string `json:"dedup_key"`
	// SessionID is the session the item belongs to, or "".
	SessionID string `json:"session_id"`
	// Kind is `permission` or `question`; `ack` items are omitted.
	Kind string `json:"kind"`
	// Text is the operator-facing payload.
	Text string `json:"text"`
	// State is the item's lifecycle state.
	State string `json:"state"`
	// CreatedAt is the creation instant, RFC3339.
	CreatedAt string `json:"created_at"`
	// AgeSeconds is the whole seconds since CreatedAt.
	AgeSeconds int64 `json:"age_seconds"`
	// Pane is the resolved, owned pane id, or "".
	Pane string `json:"pane"`
	// PaneListingUnreadable is whether the pane listing failed on this read, so
	// a recorded pane could not be validated.
	PaneListingUnreadable bool `json:"pane_listing_unreadable"`
	// RecordedPane is the pane the event recorded, unvalidated, or "".
	RecordedPane string `json:"recorded_pane"`
	// Routable is whether the recorded pane is proven to be this session's.
	Routable bool `json:"routable"`
	// SessionName is the registry's user-chosen name for the session, or "".
	SessionName string `json:"session_name"`
	// Host is the producer's machine.
	Host string `json:"host"`
	// Cwd is the producer's working directory.
	Cwd string `json:"cwd"`
	// Tool is the tool the producer was waiting on, when this is a tool gate.
	Tool string `json:"tool"`
	// Task is the vault task title this session is anchored to, or "".
	Task string `json:"task"`
	// Goal is the task's first goal title, or "".
	Goal string `json:"goal"`
	// Topic is the goal's topic title, or "".
	Topic string `json:"topic"`
	// Options are the choices a message item offers, verbatim.
	Options []string `json:"options,omitempty"`
	// Headless reports whether the session is a headless worker.
	Headless bool `json:"headless"`
	// Source names where this row came from: `store` or `event-log`.
	Source string `json:"source"`
	// Replayed is true on the store-down fallback.
	Replayed bool `json:"replayed"`
}

// WhoNeedsMeSession is one registry session, reduced to the fields the callers'
// `--ages` and `--pane-for` sub-commands derive.
type WhoNeedsMeSession struct {
	// SessionID is the registry key.
	SessionID string `json:"session_id"`
	// Name is the name the session holds now, or "".
	Name string `json:"name"`
	// AgeState is `ok` when a transcript was found, `absent` otherwise.
	AgeState string `json:"age_state"`
	// AgeSeconds is the seconds since the transcript's last write; 0 when absent.
	AgeSeconds int64 `json:"age_seconds"`
	// Pane is the pane id whose title equals the session's name, or "".
	Pane string `json:"pane"`
}

// WhoNeedsMePane is one live WezTerm pane.
type WhoNeedsMePane struct {
	// PaneID is WezTerm's pane number.
	PaneID int `json:"pane_id"`
	// Title is the pane's title.
	Title string `json:"title"`
}

// WhoNeedsMeFeed is the whole answer.
type WhoNeedsMeFeed struct {
	// Source is `store` or `event-log`.
	Source string `json:"source"`
	// Sources reports whether each host read succeeded.
	Sources WhoNeedsMeSources `json:"sources"`
	// GeneratedAt is the instant this answer was built, RFC3339.
	GeneratedAt string `json:"generated_at"`
	// Items are the open gates, oldest first.
	Items []WhoNeedsMeRow `json:"items"`
	// Sessions are the registry sessions.
	Sessions []WhoNeedsMeSession `json:"sessions"`
	// Panes is the live pane listing.
	Panes []WhoNeedsMePane `json:"panes"`
}

//counterfeiter:generate -o ../mocks/who-needs-me-reader.go --fake-name WhoNeedsMeReader . WhoNeedsMeReader

// WhoNeedsMeReader answers the who-needs-me data feed.
type WhoNeedsMeReader interface {
	// Read returns the joined feed. It never fails on a store outage: it falls
	// back to the hook-written event logs and names that source.
	Read(ctx context.Context) (WhoNeedsMeFeed, error)
}

// NewWhoNeedsMeReader creates the reader over the store, the provenance
// resolver, the pane lister, the transcript reader and the registry.
//
// ⚠️ Every input is already held by this process, so this adds no HTTP client
// and no second liveness model — which is the whole point of moving the read
// here from the per-tick Python script.
func NewWhoNeedsMeReader(
	store AttentionStore,
	provenance ProvenanceResolver,
	panes PaneLister,
	transcripts TranscriptReader,
	sessionsDir string,
	stateDir string,
	now libtime.CurrentDateTimeGetter,
) WhoNeedsMeReader {
	return &whoNeedsMeReader{
		store:       store,
		provenance:  provenance,
		panes:       panes,
		transcripts: transcripts,
		sessionsDir: sessionsDir,
		stateDir:    stateDir,
		now:         now,
	}
}

type whoNeedsMeReader struct {
	store       AttentionStore
	provenance  ProvenanceResolver
	panes       PaneLister
	transcripts TranscriptReader
	sessionsDir string
	stateDir    string
	now         libtime.CurrentDateTimeGetter
}

// Read builds the feed, falling back to the event log when the store is down.
func (r *whoNeedsMeReader) Read(ctx context.Context) (WhoNeedsMeFeed, error) {
	items, err := r.store.Read(ctx)
	if err != nil {
		// A stopped store must blind no manager. The source is named so a caller
		// cannot mistake a replay for a live read.
		glog.V(2).Infof("who-needs-me: store read failed, using event log: %v", err)
		return r.fallback(ctx)
	}
	return r.fromStore(ctx, items)
}

// fromStore joins the store's open items to provenance and the host snapshot.
func (r *whoNeedsMeReader) fromStore(ctx context.Context, items Items) (WhoNeedsMeFeed, error) {
	now := r.now.Now().Time()
	provenances := r.provenance.Resolve(ctx, items)
	rows := make([]WhoNeedsMeRow, 0, len(items))
	for _, item := range items {
		row, ok := rowFromItem(item, provenances[item.ItemID], now)
		if !ok {
			continue
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].CreatedAt < rows[j].CreatedAt })
	sessions, panes, sources := r.hostSnapshot(ctx, now)
	return WhoNeedsMeFeed{
		Source: "store",
		Sources: WhoNeedsMeSources{
			Store:    sourceOK,
			Registry: sources.Registry,
			Panes:    sources.Panes,
		},
		GeneratedAt: now.Format(whoNeedsMeTimeFormat),
		Items:       rows,
		Sessions:    sessions,
		Panes:       panes,
	}, nil
}

// rowFromItem projects one store item plus its provenance into a row.
func rowFromItem(item Item, provenance Provenance, now time.Time) (WhoNeedsMeRow, bool) {
	kind, ok := kindFromMechanism(item.AnswerMechanism)
	if !ok {
		// An `ack` is not a gate; rendering it as one would invent a block.
		return WhoNeedsMeRow{}, false
	}
	return WhoNeedsMeRow{
		ItemID:                string(item.ItemID),
		DedupKey:              string(item.DedupKey),
		SessionID:             sessionIDFromItem(item),
		Kind:                  kind,
		Text:                  singleLine(string(item.Payload)),
		State:                 string(item.State),
		CreatedAt:             item.CreatedAt.Time().UTC().Format(whoNeedsMeTimeFormat),
		AgeSeconds:            int64(now.Sub(item.CreatedAt.Time()).Seconds()),
		Pane:                  provenance.Pane,
		PaneListingUnreadable: provenance.PaneListingUnreadable,
		RecordedPane:          provenance.RecordedPane,
		Routable:              provenance.Routable,
		SessionName:           singleLine(provenance.SessionName),
		Host:                  singleLine(provenance.Host),
		Cwd:                   singleLine(provenance.Cwd),
		Tool:                  singleLine(provenance.Tool),
		Task:                  singleLine(provenance.TaskName),
		Goal:                  singleLine(provenance.GoalName),
		Topic:                 singleLine(provenance.TopicName),
		Options:               optionsOf(item),
		Headless:              provenance.Headless,
		Source:                "store",
	}, true
}

// singleLine collapses a value's whitespace runs — including newlines — to
// single spaces, so no data field carries a multi-line preformatted feed (AC1).
func singleLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// kindFromMechanism maps the store's mechanism enum to the reader's kind
// vocabulary; `ack` maps to nothing.
func kindFromMechanism(mechanism AnswerMechanism) (string, bool) {
	switch mechanism {
	case PermissionAnswerMechanism:
		return "permission", true
	case MessageAnswerMechanism:
		return "question", true
	default:
		return "", false
	}
}

// optionsOf returns a message item's option labels, or nil.
func optionsOf(item Item) []string {
	if len(item.Options) == 0 {
		return nil
	}
	labels := make([]string, 0, len(item.Options))
	for _, option := range item.Options {
		labels = append(labels, option.Label)
	}
	return labels
}

// hostSnapshot reads the registry and the pane listing.
func (r *whoNeedsMeReader) hostSnapshot(
	ctx context.Context,
	now time.Time,
) ([]WhoNeedsMeSession, []WhoNeedsMePane, WhoNeedsMeSources) {
	panesByID, panesErr := r.panes.List(ctx)
	sources := WhoNeedsMeSources{Registry: sourceOK, Panes: sourceOK}
	if panesErr != nil {
		// ⚠️ A failed listing is not "no panes" — it makes no claim at all.
		glog.V(2).Infof("who-needs-me: pane listing failed: %v", panesErr)
		sources.Panes = sourceFailed
		panesByID = nil
	}
	names, err := readSessionNamesWithError(ctx, r.sessionsDir)
	if err != nil {
		sources.Registry = sourceFailed
	}
	sessions := make([]WhoNeedsMeSession, 0, len(names))
	for sessionID, entry := range names {
		sessions = append(sessions, r.sessionRow(sessionID, entry, panesByID, now))
	}
	sort.SliceStable(
		sessions,
		func(i, j int) bool { return sessions[i].SessionID < sessions[j].SessionID },
	)
	return sessions, paneRows(panesByID), sources
}

// sessionRow builds one session row, including its transcript age and pane.
func (r *whoNeedsMeReader) sessionRow(
	sessionID string,
	entry sessionName,
	panesByID map[int]Pane,
	now time.Time,
) WhoNeedsMeSession {
	row := WhoNeedsMeSession{SessionID: sessionID, Name: entry.Name, AgeState: "absent"}
	if path := r.transcripts.Path(sessionID); path != "" {
		if info, err := os.Stat(path); err == nil {
			row.AgeState = sourceOK
			row.AgeSeconds = int64(now.Sub(info.ModTime()).Seconds())
		}
	}
	row.Pane = resolveSessionPane(panesByID, entry.Name)
	return row
}

// resolveSessionPane finds the pane whose glyph-stripped title equals the
// session's name and which the session provably owns.
func resolveSessionPane(panesByID map[int]Pane, name string) string {
	if name == "" {
		return ""
	}
	wanted := StripStatusGlyph(name)
	for paneID, pane := range panesByID {
		if StripStatusGlyph(pane.Title) != wanted {
			continue
		}
		if !OwnsPane(panesByID, paneID, name) {
			continue
		}
		return strconv.Itoa(paneID)
	}
	return ""
}

// paneRows flattens the pane listing into a stable, sorted slice.
func paneRows(panesByID map[int]Pane) []WhoNeedsMePane {
	rows := make([]WhoNeedsMePane, 0, len(panesByID))
	for paneID, pane := range panesByID {
		rows = append(rows, WhoNeedsMePane{PaneID: paneID, Title: pane.Title})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].PaneID < rows[j].PaneID })
	return rows
}

// fallback folds every session's event log into currently-open items.
//
// ⚠️ The source is `event-log` and every row carries Replayed, so a caller can
// tell a replayed gate from a live one — a fallback that announced nothing
// would be indistinguishable from a healthy store.
func (r *whoNeedsMeReader) fallback(ctx context.Context) (WhoNeedsMeFeed, error) {
	now := r.now.Now().Time()
	records, err := r.foldEventLogs(ctx)
	if err != nil {
		return WhoNeedsMeFeed{}, errors.Wrap(ctx, err, "fold event logs failed")
	}
	rows := make([]WhoNeedsMeRow, 0, len(records))
	for _, record := range records {
		row, ok := rowFromEvent(record, now)
		if !ok {
			continue
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].CreatedAt < rows[j].CreatedAt })
	// ⚠️ The host snapshot is still readable when only the store is down, so the
	// fallback populates sessions/panes and their source status too — AC1's
	// arrays and per-source block must hold on this path, not only the store path.
	sessions, panes, hostSources := r.hostSnapshot(ctx, now)
	return WhoNeedsMeFeed{
		Source: "event-log",
		Sources: WhoNeedsMeSources{
			Store:    sourceFailed,
			Registry: hostSources.Registry,
			Panes:    hostSources.Panes,
		},
		GeneratedAt: now.Format(whoNeedsMeTimeFormat),
		Items:       rows,
		Sessions:    sessions,
		Panes:       panes,
	}, nil
}

// foldEventLogs reads every `<sid>.events.jsonl` under the state dir and
// returns the open records, using the repo's one event decoder.
func (r *whoNeedsMeReader) foldEventLogs(ctx context.Context) ([]EventIngestRecord, error) {
	if r.stateDir == "" {
		return nil, nil
	}
	paths, err := filepath.Glob(filepath.Join(r.stateDir, "*.events.jsonl"))
	if err != nil {
		return nil, errors.Wrap(ctx, err, "glob event logs failed")
	}
	out := make([]EventIngestRecord, 0, len(paths))
	for _, path := range paths {
		select {
		case <-ctx.Done():
			return out, nil
		default:
		}
		out = append(out, foldEventFile(path)...)
	}
	return out, nil
}

// foldEventFile folds one log into its open records.
func foldEventFile(path string) []EventIngestRecord {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	// ⚠️ The LAST record per item id decides: a key opened, closed, then
	// RE-opened has its last record Open and must stay open. Tracking separate
	// opened/closed sets dropped it, which the spec's "last record per dedup key
	// has Open: true" rule forbids.
	last := map[string]EventIngestRecord{}
	for _, raw := range strings.Split(string(content), "\n") {
		record, ok := decodeEventRecord([]byte(raw))
		if !ok {
			continue
		}
		last[record.ItemID] = record
	}
	open := make([]EventIngestRecord, 0, len(last))
	for _, record := range last {
		if record.Open {
			open = append(open, record)
		}
	}
	return open
}

// rowFromEvent projects one folded event record into a fallback row.
func rowFromEvent(record EventIngestRecord, now time.Time) (WhoNeedsMeRow, bool) {
	if record.Kind != "permission" && record.Kind != "question" {
		// Only real gates are rows; `idle` and tool records are not.
		return WhoNeedsMeRow{}, false
	}
	created := now.UTC()
	if record.TS != nil {
		created = time.Unix(int64(*record.TS), 0).UTC()
	}
	return WhoNeedsMeRow{
		ItemID:     record.ItemID,
		DedupKey:   record.ItemID,
		SessionID:  record.SessionID,
		Kind:       record.Kind,
		Text:       singleLine(record.Detail),
		State:      "open",
		CreatedAt:  created.Format(whoNeedsMeTimeFormat),
		AgeSeconds: int64(now.Sub(created).Seconds()),
		Source:     "event-log",
		Replayed:   true,
	}, true
}
