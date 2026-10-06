// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
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
// to see which session it records, so the whole vault is read once and every
// page load is a map hit.
//
// ⚠️ Two writers keep it current, and they are not interchangeable. **ApplyPaths**
// is the ordinary path: the watcher hands it the paths a vault change named and
// it re-reads exactly those files — one open and one file's bytes for one write,
// against the 6,057 opens and 61 MB a wholesale rebuild spent to learn the same
// fact. **Reconcile** is the safety net under it: a directory listing compared
// against the stamps of the files already held, so a change the watcher never
// delivered is still caught, at one ReadDir and one stat per entry rather than a
// read of every file.
type TaskIndex interface {
	// Lookup returns the task recorded for sessionID. ok is false when the
	// session anchors no task: an unnamed or unknown session, a vault that was
	// not configured, or one that could not be read.
	Lookup(sessionID string) (Task, bool)
	// ApplyPaths re-reads exactly the named task files and updates the serving
	// index in place. A path that is not a `.md` file directly under
	// `25 Tasks/` is skipped; a path whose file is gone removes its entry. An
	// empty path list is a no-op.
	ApplyPaths(ctx context.Context, paths []string)
	// Reconcile re-derives the index from a directory listing plus the stamps of
	// the files already held, reading only what changed, and re-reads the goal
	// and topic rungs. It installs the result only when it actually read
	// `25 Tasks/`, so an unreadable directory leaves the previous index serving.
	Reconcile(ctx context.Context)
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
	// ProductionTouching reports whether this task's own file declares a
	// production-touching step — the operator's authored
	// `- [ ] ⚠️ production-touching — <command>` marker on one of the task's
	// subtask lines. It rides the same read that produces Name and Path; no
	// second file is opened for it.
	ProductionTouching bool
}

// taskIndexBackstopWindow is the safety net under the task-directory watcher.
// The watcher is what keeps the index current: it rebuilds on every change to
// `25 Tasks/`, so a task file written while the process runs resolves on a later
// lookup without a restart. This window picks up a change a watcher event did
// not deliver — a dropped inotify event, or a write that landed before the watch
// was established — so the index converges rather than staying stale. It is
// deliberately slow, because the common path is a map hit and a rebuild here is
// the exception rather than the mechanism.
const taskIndexBackstopWindow = libtime.Duration(5 * time.Minute)

// NewTaskIndex builds the session -> task index from vaultDir's task files.
//
// It fails soft in every direction: an empty vaultDir, a vault that does not
// exist, an unreadable `25 Tasks/`, an unreadable `24 Goals/`, an unreadable
// `23 Topics/` and an unreadable file each yield no entry for the affected
// tasks and never an error. An empty vaultDir is the ordinary case for a host
// with no vault configured, not a fault.
//
// The index is not frozen at construction: a watcher on the task directory calls
// ApplyPaths on every change, and Lookup falls back to a Reconcile once the
// serving index is older than taskIndexBackstopWindow. Either way a task file
// written after this call resolves without a restart. The clock is injected so
// the backstop is testable.
//
// The goal rung is built before the task walk, because each indexed task
// carries the goal it names and the topic that lists it: both maps must exist
// before the first task file is read.
func NewTaskIndex(
	ctx context.Context,
	vaultDir string,
	currentDateTimeGetter libtime.CurrentDateTimeGetter,
) TaskIndex {
	index := &taskIndex{
		ctx:                   ctx,
		vaultDir:              vaultDir,
		currentDateTimeGetter: currentDateTimeGetter,
	}
	// The boot build is installed unconditionally, even when ctx is already
	// cancelled or the task walk read nothing: there is no previous index to
	// preserve, and the fail-soft contract is that a cancelled or empty boot
	// holds what it read and no more rather than failing. Every later reconcile is
	// installed only when it completed AND actually read `25 Tasks/` — see
	// reconcileAndInstall.
	fresh, _ := index.build(ctx)
	index.install(fresh)
	return index
}

type taskIndex struct {
	// ctx is the build context a backstop reconcile runs under. Lookup carries no
	// context of its own, so when it finds the serving index past
	// taskIndexBackstopWindow it passes this construction context to Reconcile —
	// the same context the boot build ran under, which is what lets a cancelled
	// backstop reconcile stop at the same points the boot build does. The
	// watcher's own reconcile passes the watcher's run context instead.
	ctx context.Context
	// vaultDir is the vault the index reads. It is fixed for the process's life;
	// a rebuild re-reads the same directory rather than being re-pointed.
	vaultDir string
	// currentDateTimeGetter stamps each published index and measures the refresh
	// window. Injected, never time.Now(), so the bound is testable.
	currentDateTimeGetter libtime.CurrentDateTimeGetter
	// mu guards the fields below. It is an RWMutex because Lookup is a map hit on
	// the common path and many renders read it concurrently, while only a
	// rebuild's swap takes the write side.
	mu sync.RWMutex
	// refreshing is the token of the rebuild currently in flight, or nil when none
	// is, so a burst of concurrent lookups that all observe a lapsed window does
	// not multiply one full-vault read by the number of renders.
	//
	// ⚠️ A token rather than a bool, mirroring provenanceResolver.refreshing, for
	// the same reason: only the goroutine that set the token clears it, so a
	// rebuild that finishes cannot reopen the single-flight window while another
	// rebuild — started after the first claimed the token — is still reading the
	// vault. The token is claimed in the SAME critical section that observes the
	// window lapsed, so a caller descheduled between a separate stale check and
	// the claim cannot start a second rebuild.
	refreshing chan struct{}
	// rebuilds counts the rebuilds that actually started, so a test can assert
	// the single-flight property directly. Guarded by mu.
	refreshes int
	// builtAt is the clock reading at which the serving index was published.
	builtAt   libtime.DateTime
	bySession map[string]taskEntry
	// byPath holds every indexed task file keyed by its vault-relative path,
	// carrying the session it was indexed under. It is the source of truth the
	// incremental update and the reconcile both work from; bySession is derived
	// from it by the tie-break in recomputeSession.
	byPath map[string]taskCandidate
	// bySessionPaths maps a session id to the paths indexed under it, so an
	// incremental update recomputes a winner from that session's own candidates
	// — measured at most 6 in the live vault — rather than scanning every
	// indexed file.
	bySessionPaths map[string]map[string]struct{}
	// fileStamps records the size and modification time of every `.md` file
	// seen under `25 Tasks/`, indexed or not. It is what the reconcile compares
	// against: a file whose stamp is unchanged is reused rather than re-read,
	// which is what keeps the safety net off the full-vault read path.
	fileStamps map[string]fileStamp
	// appliedSeq counts the incremental updates applied to the serving index. A
	// reconcile snapshots it and refuses to install when it has moved, so an
	// update landing mid-walk is never overwritten by the older listing that
	// walk was building. Guarded by mu.
	appliedSeq int64
	// baseSeq is the appliedSeq the snapshot a fresh index was reconciled from
	// was taken at. It is meaningful only on an index returned by reconcile;
	// install compares it against the serving appliedSeq. Guarded by mu.
	baseSeq int64
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
	// declaredGoals is the task's `goals:` list as wikilink titles, in order and
	// unfiltered by whether each title exists as a goal file. A reconcile that
	// reuses an entry re-resolves its goal and topic against a refreshed rung
	// set from this list rather than re-reading the file — the goal and topic
	// fields are the only ones that depend on anything outside the task file.
	declaredGoals []string
}

// taskCandidate is one indexed task file: its entry plus the session it was
// indexed under, so an incremental update can recompute the winner for every
// session it touches without re-reading the vault.
type taskCandidate struct {
	sessionID string
	entry     taskEntry
}

// fileStamp is the identity a reconcile compares a task file against. Size and
// modification time are read from the directory entry's own stat, so the
// comparison costs no file open — which is the whole point: a vault of 5,800
// task files is re-stamped on every reconcile without a byte of their content
// being read.
//
// ⚠️ The modification time is held as Unix nanoseconds rather than a time
// value: it is only ever compared for equality against another stamp, never
// arithmetic, so the domain's time types would add a conversion without adding
// a meaning.
//
// ⚠️ Size plus modification time is a heuristic, and the case it misses is named
// rather than implied: a same-size edit landing inside one filesystem timestamp
// tick reuses the stale entry. The tick is coarse enough for that to be possible
// and fine enough that the window is tiny, and the failure self-corrects at the
// next edit or reconcile — but it is why this pair is a hint that a file is
// unchanged, never a proof that its content is current.
type fileStamp struct {
	size        int64
	modTimeNano int64
}

// build reads the vault into a fresh index and reports whether the task walk
// actually read `25 Tasks/`. Nothing on the receiver is mutated: the fresh index
// carries its own maps, so a build that is discarded — because it was cancelled
// or because the task walk could not read its directory — leaves the serving
// index untouched.
//
// ⚠️ The boolean is the task-walk half alone, and deliberately not a conjunct
// over the goal and topic rungs. Those two degrade to "no goal" and "no topic"
// by design — a vault with no `24 Goals/` is a legal state that must still
// resolve its tasks — so refusing an install on their failure would refuse a
// perfectly good task walk. Only an unreadable `25 Tasks/` means the rebuild
// learned nothing about the population the index exists to serve.
//
// ⚠️ An empty vaultDir reports true: there is no directory to read and no
// previous index holding entries it could lose, so a rebuild over it is a
// no-op rather than a failed read.
func (t *taskIndex) build(ctx context.Context) (*taskIndex, bool) {
	fresh := &taskIndex{
		bySession:      map[string]taskEntry{},
		byPath:         map[string]taskCandidate{},
		bySessionPaths: map[string]map[string]struct{}{},
		fileStamps:     map[string]fileStamp{},
		goals:          map[string]struct{}{},
		goalTopics:     map[string]goalTopic{},
	}
	if t.vaultDir == "" {
		return fresh, true
	}
	fresh.readGoalTitles(t.vaultDir)
	fresh.readGoalTopics(ctx, t.vaultDir)
	return fresh, fresh.readTasks(ctx, t.vaultDir)
}

// install swaps a freshly built index's maps in as the serving index and stamps
// it at the current clock reading. It takes the write lock, so a reader sees
// either the whole previous index or the whole new one, never a half-built one.
//
// ⚠️ It reports false, and changes nothing, when an incremental update has been
// applied since the snapshot this index was reconciled from. A reconcile
// snapshots the index, walks the directory, and installs the result; an apply
// landing during that walk would otherwise be discarded by the install, leaving
// the task mis-resolved until the next backstop window — the staleness window
// the index's own criteria forbid. The check and the swap share one critical
// section, so an apply cannot slip between them either.
//
// ⚠️ A boot build installs unconditionally in practice: its fresh index carries
// baseSeq 0 and nothing has been applied yet, so the sequences agree.
func (t *taskIndex) install(fresh *taskIndex) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if fresh.baseSeq != t.appliedSeq {
		return false
	}
	t.bySession = fresh.bySession
	t.byPath = fresh.byPath
	t.bySessionPaths = fresh.bySessionPaths
	t.fileStamps = fresh.fileStamps
	t.goals = fresh.goals
	t.goalTopics = fresh.goalTopics
	t.builtAt = t.currentDateTimeGetter.Now()
	return true
}

// ApplyPaths re-reads exactly the named task files and updates the serving
// index in place. It is the ordinary path the watcher drives on a vault change.
//
// ⚠️ It is where the cost moved to. One task-file write costs one file open and
// that file's own bytes; the wholesale rebuild this replaces spent 6,057 opens
// and 61 MB to learn the same fact.
//
// ⚠️ Every read happens BEFORE the lock is taken, and only the map update takes
// it. A burst can name hundreds of task files, and holding the write lock across
// their reads would block every render on the board for the duration — the same
// rule the boot build follows.
//
// ⚠️ It mutates the serving maps rather than swapping in fresh ones, which is
// what makes a single change cheap. The cost is that a concurrent reader must
// never hold a reference to those maps across the lock: `reconcile` takes a real
// copy of them, not the map header, for exactly this reason. Mutating a map that
// another goroutine is reading is a fatal runtime error, not a stale read.
//
// ⚠️ A path that is not a `.md` file directly under `25 Tasks/` is skipped, and
// a path whose file is gone removes its entry — a delete or a rename arrives as
// an event naming a file that no longer opens.
func (t *taskIndex) ApplyPaths(ctx context.Context, paths []string) {
	if t == nil || len(paths) == 0 {
		return
	}
	if t.vaultDir == "" {
		return
	}
	tasksDir := filepath.Join(t.vaultDir, taskDirName)
	root, err := os.OpenRoot(tasksDir)
	if err != nil {
		// Unreadable, so nothing can be applied. The watcher's own reconcile
		// keeps the index converging; failing loud here would take down a
		// watcher that is fail-soft everywhere else.
		glog.V(2).Infof("open vault tasks dir %s failed: %v", tasksDir, err)
		return
	}
	defer root.Close()

	// ⚠️ Every read happens BEFORE the lock is taken. A burst — an obsidian-git
	// autocommit, a `git checkout` — can name hundreds of task files, and holding
	// the write lock across their reads would block every render on the board for
	// the duration. It is the same rule the rebuild follows: do the expensive
	// work outside the lock, and take the lock only to swap what it produced.
	type pendingApply struct {
		path    string
		name    string
		content []byte
		stamp   fileStamp
		stamped bool
		removed bool
	}
	pending := make([]pendingApply, 0, len(paths))
	for _, path := range paths {
		if ctx.Err() != nil {
			return
		}
		name, ok := taskFileName(path)
		if !ok {
			continue
		}
		rel := filepath.Join(taskDirName, name)
		// ⚠️ The stamp is taken BEFORE the content is read, and the order is
		// load-bearing. Read-then-stat can store one version's bytes under
		// another version's stamp — a write landing between the two — and a later
		// reconcile then finds the stamp unchanged and REUSES the stale entry,
		// permanently, because nothing makes that pair disagree again. Stat-then-
		// read can only store older bytes under an older stamp, which the next
		// reconcile re-reads.
		stamp, stamped := rootStat(root, name)
		content, readErr := taskFileReader(root, name)
		if readErr != nil {
			// The file was removed between the event and this read, which is the
			// ordinary shape of a delete or a rename. The removal is applied
			// under the lock with everything else.
			glog.V(3).Infof("applying removal of vault task %s: %v", rel, readErr)
			pending = append(pending, pendingApply{path: rel, name: name, removed: true})
			continue
		}
		pending = append(pending, pendingApply{
			path:    rel,
			name:    name,
			content: content,
			stamp:   stamp,
			stamped: stamped,
		})
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	for _, entry := range pending {
		t.forgetPath(entry.path)
		if entry.removed {
			continue
		}
		if entry.stamped {
			t.fileStamps[entry.path] = entry.stamp
		}
		t.indexContent(entry.path, entry.name, entry.content)
	}
	// ⚠️ The sequence is what stops a reconcile from installing an older listing
	// over this update. A reconcile snapshots the index, walks the directory, and
	// installs the result; an apply landing in between would be silently
	// discarded, leaving the task mis-resolved until the next window. The apply
	// bumps this, and the install refuses when it moved — see reconcileAndInstall.
	t.appliedSeq++
	// ⚠️ builtAt advances here, on the same reasoning install uses: the serving
	// index now reflects the vault, so the Lookup backstop should not fire on the
	// next render. A dropped event is the watcher's own reconcile to catch, not
	// the render path's — a listing walk on the render path would put a stat of
	// every task file in front of a page load.
	//
	// ⚠️ It advances even when nothing was indexed — a burst naming only
	// non-task paths, or removals of files that were never indexed. That
	// postpones the backstop by one window for no gain, which is the accepted
	// direction: the alternative is a per-path notion of "did anything change",
	// and the cost of being wrong that way is a stale index rather than a late
	// reconcile. The watcher's own ticker is unaffected either way.
	t.builtAt = t.currentDateTimeGetter.Now()
}

// claimRefresh claims the single-flight token, reporting false when a refresh is
// already in flight or — when onlyIfLapsed is set — when the serving index is
// still inside taskIndexBackstopWindow.
//
// ⚠️ The window check and the claim are in the SAME critical section, and that
// atomicity is load-bearing for the backstop. A caller that observed a lapsed
// window, was descheduled, and reached the claim only after the winner had
// installed and cleared the token would otherwise start a second walk over the
// vault — exactly the amplification the token exists to prevent. Checking the
// window inside the claim is what stops it: the winner's install advances
// builtAt, so the straggler's (already read) clock reading no longer exceeds the
// window and it declines. This is why the backstop cannot simply check the clock
// and then call Reconcile, which has no window check of its own.
func (t *taskIndex) claimRefresh(onlyIfLapsed bool) (chan struct{}, bool) {
	now := t.currentDateTimeGetter.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if onlyIfLapsed && now.Sub(t.builtAt) < taskIndexBackstopWindow {
		return nil, false
	}
	if t.refreshing != nil {
		// A rebuild is already in flight. Serve the index currently installed
		// instead of starting a second full-vault read alongside it.
		return nil, false
	}
	token := make(chan struct{})
	t.refreshing = token
	t.refreshes++
	return token, true
}

// Reconcile re-derives the index from a directory listing plus the stamps of the
// files already held, reading only what changed. It is the safety net under the
// watcher: a change the watcher never delivered — a dropped inotify event, or a
// write that landed before the watch was established — is caught here, because
// the listing sees the file even when no event named it.
//
// ⚠️ Why a listing rather than the wholesale rebuild it replaces. The rebuild
// answered the same question at 6,057 file opens and 61 MB; a listing answers it
// at one ReadDir, one stat per entry, and a read of only the files whose stamp
// moved. That is also what lets the goal's *"no per-request or timer-driven full
// vault rescan"* hold while a safety net stays — the net no longer has to be
// absent for the criterion to be met.
//
// ⚠️ The goal and topic rungs ARE re-read here, because this is the only path
// that can pick up a goal file or topic page added while the process runs. They
// are small: 268 goal filenames by listing, and 13 topic pages at 2.1 MB,
// against the 66 MB task directory.
func (t *taskIndex) Reconcile(ctx context.Context) {
	if t == nil || t.vaultDir == "" {
		return
	}
	if token, ok := t.claimRefresh(false); ok {
		t.reconcileAndInstall(ctx, token)
	}
}

// reconcileAndInstall runs one reconcile under a token already claimed, and
// installs the result on exactly the terms the rebuild used.
func (t *taskIndex) reconcileAndInstall(ctx context.Context, token chan struct{}) {
	// Deferred rather than inlined after the walk: a panic anywhere in it must
	// still clear the token, or the index stays pinned to a snapshot that will
	// never be replaced.
	defer t.finishRefresh(token)

	fresh, tasksRead := t.reconcile(ctx)
	if ctx.Err() != nil {
		glog.V(3).Infof("task index reconcile cancelled, keeping previous index")
		return
	}
	if !tasksRead {
		glog.V(2).Infof(
			"task index reconcile could not read %s, keeping previous index",
			filepath.Join(t.vaultDir, taskDirName),
		)
		return
	}
	if !t.install(fresh) {
		glog.V(3).Infof(
			"task index reconcile superseded by an incremental update, keeping previous index",
		)
	}
}

// reconcile builds a fresh index from the task directory's listing, reusing the
// serving entry for every file whose size and modification time are unchanged.
//
// ⚠️ It reports whether the task listing was actually read, on the same contract
// the build used: a reconcile that could not read `25 Tasks/` must not replace a
// populated index with an empty one, because an empty result is what a genuinely
// empty vault produces AND what an unreadable directory produces.
func (t *taskIndex) reconcile(ctx context.Context) (*taskIndex, bool) {
	fresh := &taskIndex{
		bySession:      map[string]taskEntry{},
		byPath:         map[string]taskCandidate{},
		bySessionPaths: map[string]map[string]struct{}{},
		fileStamps:     map[string]fileStamp{},
		goals:          map[string]struct{}{},
		goalTopics:     map[string]goalTopic{},
	}
	fresh.readGoalTitles(t.vaultDir)
	fresh.readGoalTopics(ctx, t.vaultDir)

	tasksDir := filepath.Join(t.vaultDir, taskDirName)
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		glog.V(2).Infof("read vault tasks dir %s failed: %v", tasksDir, err)
		return fresh, false
	}
	root, err := os.OpenRoot(tasksDir)
	if err != nil {
		glog.V(2).Infof("open vault tasks dir %s failed: %v", tasksDir, err)
		return fresh, false
	}
	defer root.Close()

	// ⚠️ This is a real COPY of the two maps, not a reference to them, and the
	// distinction is fatal rather than cosmetic. ApplyPaths mutates these maps in
	// place under the write lock; keeping the map header past the unlock and then
	// reading it is a concurrent map read and map write — a Go runtime fatal
	// error, not a stale value. The copy costs one pass over ~5,800 entries, once
	// per backstop window, which is nothing against the 61 MB the rebuild this
	// replaced would have spent.
	//
	// The sequence is read in the same critical section as the copy, so the pair
	// describes one instant: install refuses when the serving sequence has moved
	// past it.
	t.mu.RLock()
	previous := make(map[string]taskCandidate, len(t.byPath))
	for path, candidate := range t.byPath {
		previous[path] = candidate
	}
	previousStamps := make(map[string]fileStamp, len(t.fileStamps))
	for path, stamp := range t.fileStamps {
		previousStamps[path] = stamp
	}
	fresh.baseSeq = t.appliedSeq
	t.mu.RUnlock()

	for _, entry := range entries {
		if ctx.Err() != nil {
			return fresh, false
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(taskDirName, entry.Name())
		stamp, ok := stampOf(entry)
		if !ok {
			// Unstampable, so it cannot be proven unchanged: re-read it rather
			// than reuse an entry that may be stale.
			fresh.addFile(root, entry)
			continue
		}
		fresh.fileStamps[path] = stamp
		held, seen := previous[path]
		if !seen || previousStamps[path] != stamp {
			fresh.addFile(root, entry)
			continue
		}
		// Unchanged by size and modification time, so the file is not re-read.
		// ⚠️ Its goal and topic are still re-derived: those two fields depend on
		// the rungs, which were just re-read, not on the task file's bytes.
		fresh.addCandidate(path, held.sessionID, fresh.reresolve(held.entry))
	}
	return fresh, true
}

// reresolve returns entry with its goal and topic re-derived from the index's
// current rungs.
//
// ⚠️ It is what a reconcile applies to a reused entry, whose file bytes it
// deliberately did not re-read. The task file is unchanged, so the only thing
// that can have moved is whether the goal it declares exists yet, and which
// topic page lists that goal — which is exactly why the declared title list is
// carried on the entry rather than only the title that resolved.
func (t *taskIndex) reresolve(entry taskEntry) taskEntry {
	goalName, goalPath := resolveGoal(entry.declaredGoals, t.goals)
	entry.task.GoalName = goalName
	entry.task.GoalPath = goalPath
	entry.task.TopicName = ""
	entry.task.TopicPath = ""
	if goalName != "" {
		if topic, ok := t.goalTopics[goalName]; ok {
			entry.task.TopicName = topic.name
			entry.task.TopicPath = topic.path
		}
	}
	return entry
}

// taskFileName reduces an event path to the task file it names under
// `25 Tasks/`. It reports false for anything else, so a nested path, a
// directory event or a non-markdown write is skipped rather than misread as a
// task.
//
// ⚠️ The path is compared against the vault-relative task directory rather than
// resolved against it: the watcher reports paths relative to the vault it
// watches, and resolving an absolute path here would make the confinement
// depend on the caller rather than on this check.
func taskFileName(path string) (string, bool) {
	if filepath.Dir(path) != taskDirName {
		return "", false
	}
	name := filepath.Base(path)
	if !strings.HasSuffix(name, ".md") {
		return "", false
	}
	return name, true
}

// finishRefresh clears the in-flight token, but only if it is still the one this
// rebuild set. A rebuild that started afterwards overwrote it, and clearing it
// here would reopen the single-flight window while that later rebuild is still
// reading the vault — the amplification the token exists to prevent.
func (t *taskIndex) finishRefresh(token chan struct{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.refreshing == token {
		t.refreshing = nil
	}
}

// RefreshCount returns how many rebuilds have actually started. It is the
// observable the single-flight property is asserted against: a burst of
// concurrent lookups past the window must leave this at exactly one.
func (t *taskIndex) RefreshCount() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.refreshes
}

// readTasks walks `<vault>/25 Tasks/` and indexes every task file that records
// a session. It reports whether that directory was actually read: false when
// either the listing or the os.Root handle could not be opened, which is the
// signal a reconcile uses to refuse installing itself — see reconcileAndInstall.
//
// A directory that reads but holds no task file reports true: an empty vault is
// a legal state, and an index that read it holds the honest answer rather than
// a failed one.
//
// os.ReadDir returns entries sorted by filename, and the tie-break in
// recomputeSession depends on it: candidates are visited in ascending path
// order, so the later candidate is the lexicographically greater path.
func (t *taskIndex) readTasks(ctx context.Context, vaultDir string) bool {
	tasksDir := filepath.Join(vaultDir, taskDirName)
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		glog.V(2).Infof("read vault tasks dir %s failed: %v", tasksDir, err)
		return false
	}
	// Opened as an os.Root so every read is confined beneath the tasks
	// directory: the file names come from the directory listing, and scoping the
	// handle makes that confinement structural rather than an assumption about
	// the names. Same pattern as the event-log read in readEvents.
	root, err := os.OpenRoot(tasksDir)
	if err != nil {
		glog.V(2).Infof("open vault tasks dir %s failed: %v", tasksDir, err)
		return false
	}
	defer root.Close()
	for _, entry := range entries {
		select {
		case <-ctx.Done():
			glog.V(3).Infof("task index build cancelled")
			return true
		default:
		}
		t.addFile(root, entry)
	}
	return true
}

// reconcileIfBackstopLapsed reconciles the index when the serving one is older
// than taskIndexBackstopWindow, so a task file written after construction
// resolves on a later lookup even when no watcher event delivered it.
//
// ⚠️ This is the no-watcher path. A service whose watcher could not be
// established still serves, and this is what keeps its index converging. When
// the watcher IS running it advances builtAt on every apply, so this rarely
// lapses — which is deliberate: a listing walk is cheap but not free, and the
// render path is not where it belongs.
//
// ⚠️ The window check rides the claim rather than preceding it — see
// claimRefresh. Checking the clock here and then reconciling would let a lookup
// that was descheduled between the two start a second walk after the winner had
// already finished, which is the amplification the token exists to prevent.
func (t *taskIndex) reconcileIfBackstopLapsed() {
	if token, ok := t.claimRefresh(true); ok {
		t.reconcileAndInstall(t.ctx, token)
	}
}

// Lookup returns the task recorded for sessionID.
//
// An unknown session is an ordinary miss, and an empty sessionID is a miss
// too: addFile never indexes a file under "", so there is nothing for it to
// match.
//
// ⚠️ The common path is a map hit and opens no file at all. The vault is walked
// only when the serving index is older than taskIndexBackstopWindow — the
// safety net for a service whose watcher could not be established — and even
// then the walk is a listing compared against the stamps already held, so it
// reads only what changed rather than every task file.
func (t *taskIndex) Lookup(sessionID string) (Task, bool) {
	if t == nil {
		return Task{}, false
	}
	t.reconcileIfBackstopLapsed()
	t.mu.RLock()
	entry, ok := t.bySession[sessionID]
	t.mu.RUnlock()
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
	path := filepath.Join(taskDirName, entry.Name())
	if stamp, ok := stampOf(entry); ok {
		t.fileStamps[path] = stamp
	}
	content, err := taskFileReader(root, entry.Name())
	if err != nil {
		glog.V(3).Infof("read vault task %s failed: %v", entry.Name(), err)
		return
	}
	t.indexContent(path, entry.Name(), content)
}

// taskFileReader reads one task file by name from the tasks-directory root.
//
// ⚠️ It is a package variable rather than a direct os.Root.ReadFile so a spec can
// count the opens and bytes one update performs. The index's own criteria pin a
// bound on exactly that count — one changed file must cost one open and its own
// bytes, against the 6,057 opens and 61 MB a wholesale rebuild spent — and a
// count cannot be asserted against a reader the spec cannot see. The alternative
// is asserting the cost by re-deriving it from this source, which is the claim
// under test rather than evidence for it.
//
// ⚠️ Every task-file read in the package goes through here, including the boot
// walk's: a spec that counts must therefore install its counter AFTER
// construction, or it counts the boot build as if it were the change.
var taskFileReader = func(root *os.Root, name string) ([]byte, error) {
	return root.ReadFile(name)
}

// stampOf reads a directory entry's size and modification time without opening
// the file. It reports false when the entry cannot be stamped at all, which the
// reconcile reads as "changed" and re-reads rather than skipping.
func stampOf(entry os.DirEntry) (fileStamp, bool) {
	info, err := entry.Info()
	if err != nil {
		return fileStamp{}, false
	}
	return fileStamp{size: info.Size(), modTimeNano: info.ModTime().UnixNano()}, true
}

// rootStat stamps one task file by name from the tasks-directory root.
//
// ⚠️ It is a stat, not an open: a path that cannot be stamped is reported false
// and the caller records no stamp for it, which makes the next reconcile re-read
// that file rather than trust a stamp nobody wrote. It is deliberately separate
// from the content read so ApplyPaths can stamp BEFORE reading — see there for
// why the order matters.
func rootStat(root *os.Root, name string) (fileStamp, bool) {
	info, err := root.Stat(name)
	if err != nil {
		return fileStamp{}, false
	}
	return fileStamp{size: info.Size(), modTimeNano: info.ModTime().UnixNano()}, true
}

// indexContent indexes one task file's content under its vault-relative path.
//
// ⚠️ It is the single place a task file's frontmatter is interpreted, so the
// boot walk, the incremental update and the reconcile all derive identical
// entries from identical bytes. Three callers, one reading — a second
// interpretation is how the incremental path and the full path come to
// disagree about the same file.
func (t *taskIndex) indexContent(path, name string, content []byte) {
	sessionID := frontmatterValue(content, taskSessionKey)
	if sessionID == "" {
		// No frontmatter, no key, or an empty value after trimming whitespace:
		// the file contributes no entry rather than an entry under "".
		return
	}
	task := Task{
		Name: strings.TrimSuffix(name, ".md"),
		Path: path,
	}
	// The production-touching fact rides the same read: the whole file is already
	// in memory here, so the marker costs no second ReadFile and no directory
	// scan.
	task.ProductionTouching = hasProductionTouchingMarker(content)
	// The goal and the topic ride with the task: both derive from this file's
	// `goals:` list, so they are resolved here, once, rather than at page load.
	declared := declaredGoalTitles(content)
	goalName, goalPath := resolveGoal(declared, t.goals)
	task.GoalName = goalName
	task.GoalPath = goalPath
	if goalName != "" {
		if topic, ok := t.goalTopics[goalName]; ok {
			task.TopicName = topic.name
			task.TopicPath = topic.path
		}
	}
	t.addCandidate(path, sessionID, taskEntry{
		task:          task,
		terminal:      isTerminalTaskStatus(frontmatterValue(content, taskStatusKey)),
		declaredGoals: declared,
	})
}

// declaredGoalTitles returns every wikilink title in a task's `goals:` list, in
// order and unfiltered by whether the title exists as a goal file.
//
// ⚠️ The filtering is resolveGoal's job, and keeping the raw list is what lets a
// reconcile re-resolve a reused entry against a refreshed goal set without
// re-reading the task file. Storing only the *resolved* title would freeze the
// answer a task gave when a goal file did not yet exist.
//
// ⚠️ The encoding, measured live over 1,619 goal-carrying tasks: every entry is
// a **quoted Obsidian wikilink** — `- "[[Goal Title]]"` — with single-quoted
// (`- '[[Goal Title]]'`) and 4-space-indented variants also occurring, and the
// empty form is inline (`goals: []`). Quotes, `[[ ]]` brackets and any
// `|alias` are stripped by wikilinkTitle before the path is built. A bare title
// is not a valid entry and yields nothing rather than being accepted.
//
// ⚠️ A task carrying several goals resolves the **first** one that exists; the
// rest are unrendered, deliberately.
func declaredGoalTitles(content []byte) []string {
	entries := frontmatterList(content, taskGoalsKey)
	titles := make([]string, 0, len(entries))
	for _, entry := range entries {
		if title, ok := wikilinkTitle(entry); ok {
			titles = append(titles, title)
		}
	}
	return titles
}

// resolveGoal returns the first declared goal title that exists as a goal file,
// with its vault-relative path, or two empty strings when none does.
func resolveGoal(declared []string, goals map[string]struct{}) (string, string) {
	for _, title := range declared {
		if _, exists := goals[title]; !exists {
			continue
		}
		return title, filepath.Join(goalDirName, title+".md")
	}
	return "", ""
}

// addCandidate records one candidate for a session and re-derives that
// session's winner.
func (t *taskIndex) addCandidate(path, sessionID string, entry taskEntry) {
	t.byPath[path] = taskCandidate{sessionID: sessionID, entry: entry}
	paths, ok := t.bySessionPaths[sessionID]
	if !ok {
		paths = map[string]struct{}{}
		t.bySessionPaths[sessionID] = paths
	}
	paths[path] = struct{}{}
	t.recomputeSession(sessionID)
}

// forgetPath removes the file at path from the index and from the stamp map.
//
// ⚠️ A path that is absent from the index is a no-op rather than an error: the
// reconcile sees every `.md` file under `25 Tasks/`, and a file that records no
// session is never indexed, so most paths it names have nothing to forget.
func (t *taskIndex) forgetPath(path string) {
	delete(t.fileStamps, path)
	old, ok := t.byPath[path]
	if !ok {
		return
	}
	delete(t.byPath, path)
	paths := t.bySessionPaths[old.sessionID]
	delete(paths, path)
	if len(paths) == 0 {
		delete(t.bySessionPaths, old.sessionID)
	}
	t.recomputeSession(old.sessionID)
}

// recomputeSession re-derives the winning entry for one session from that
// session's own candidates.
//
// ⚠️ Several files may carry the same claude_session_id — measured in the live
// vault, 33 session ids map to between 2 and 6 task files. The tie-break is:
// prefer the file whose `status` is neither `completed` nor `aborted`, because
// that is the task the session is still anchored to; if every candidate is
// terminal, the lexicographically last path wins.
//
// ⚠️ Candidates are visited in **ascending path order**, which is the order
// os.ReadDir produced them in during the boot walk, so an incremental update
// and a full walk agree on the winner for every session. Iterating the map
// directly would make the winner depend on Go's map order and flip between
// two equally valid answers from one render to the next.
func (t *taskIndex) recomputeSession(sessionID string) {
	paths := t.bySessionPaths[sessionID]
	if len(paths) == 0 {
		delete(t.bySession, sessionID)
		return
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	winner := t.byPath[ordered[0]].entry
	for _, path := range ordered[1:] {
		candidate := t.byPath[path].entry
		if !winner.terminal && candidate.terminal {
			// The winner is in flight and the candidate is not: keep the one
			// the session is still working on.
			continue
		}
		winner = candidate
	}
	t.bySession[sessionID] = winner
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
