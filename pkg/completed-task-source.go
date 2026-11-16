// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/golang/glog"
)

// completedDateKey is the frontmatter key a task records the moment it finished
// in. The vault writes both a bare date (`2026-10-18`) and an instant
// (`2026-10-18T17:00:00Z`), and completionTime treats them differently.
const completedDateKey = "completed_date"

// NewCompletedTaskSource returns the vault's finished tasks, read from
// vaultDir's task files.
//
// It is the port of the Python watcher's `completed_tasks()`, and it carries
// that function's two rules unchanged:
//
//   - Only a task whose `status` is `completed` or `aborted` is finished.
//     `in_progress`, `next`, `backlog`, `hold` and an absent status are not.
//   - Only a task the vault can DATE is finished. A terminal task with no
//     `completed_date` is left out on purpose: it could suppress a card written
//     before it finished, and only a task with a known completion moment may
//     suppress anything.
//
// ⚠️ **Fails OPEN, in every direction.** An unconfigured vault, an unreadable
// task directory, an unreadable file and a cancelled context each yield no
// entry rather than an error, so the record is forwarded. A missed suppression
// costs one low-value card; a wrongly suppressed card costs a decision nobody
// ever sees.
//
// ⚠️ **No cross-vault veto.** The Python source dropped a title any vault held
// OPEN; this service is configured with ONE vault root (`VAULT_DIR`), so there
// is no second vault to consult and the rule would be inert. It returns if the
// service is ever given a second vault root.
func NewCompletedTaskSource(vaultDir string) CompletedTaskSource {
	return func(ctx context.Context) []CompletedTask {
		return readCompletedTasks(ctx, vaultDir)
	}
}

// readCompletedTasks walks `<vault>/25 Tasks/` and returns one CompletedTask per
// finished, dated task file.
//
// Every read goes through an os.Root opened on the task directory, so a name
// taken from the listing can never walk out of it — the same confinement the
// task index uses.
func readCompletedTasks(ctx context.Context, vaultDir string) []CompletedTask {
	if vaultDir == "" {
		return nil
	}
	tasksDir := filepath.Join(vaultDir, taskDirName)
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		glog.V(2).Infof("read vault tasks dir %s failed: %v", tasksDir, err)
		return nil
	}
	root, err := os.OpenRoot(tasksDir)
	if err != nil {
		glog.V(2).Infof("open vault tasks dir %s failed: %v", tasksDir, err)
		return nil
	}
	defer root.Close()
	tasks := make([]CompletedTask, 0, len(entries))
	for _, entry := range entries {
		if ctx.Err() != nil {
			// A cancelled walk is not a partial answer: half the population would
			// under-suppress, and returning nothing is the same safe direction as an
			// unreadable directory.
			return nil
		}
		task, ok := completedTaskOf(root, entry)
		if !ok {
			continue
		}
		tasks = append(tasks, task)
	}
	return tasks
}

// completedTaskOf reports the finished task one directory entry holds, or false
// when the entry is not a task file, is not terminal, or carries no usable
// completion date.
func completedTaskOf(root *os.Root, entry os.DirEntry) (CompletedTask, bool) {
	if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
		return CompletedTask{}, false
	}
	content, err := root.ReadFile(entry.Name())
	if err != nil {
		glog.V(3).Infof("read vault task %s failed: %v", entry.Name(), err)
		return CompletedTask{}, false
	}
	if !isTerminalTaskStatus(frontmatterValue(content, taskStatusKey)) {
		return CompletedTask{}, false
	}
	completedAt, ok := completionTime(frontmatterValue(content, completedDateKey))
	if !ok {
		return CompletedTask{}, false
	}
	return CompletedTask{
		Name:        strings.TrimSuffix(entry.Name(), ".md"),
		CompletedAt: completedAt,
	}, true
}

// completionTime resolves a `completed_date` value to the moment the task is
// finished.
//
// ⚠️ **A date-only value resolves to END of that day, never to its midnight.**
// The vault writes both forms, and a card created at 09:00 on the day a task was
// really finished at 17:00 would otherwise be suppressed as though the task had
// finished first. End-of-day can only push a card, never drop one.
func completionTime(raw string) (time.Time, bool) {
	value := strings.Trim(strings.TrimSpace(raw), `"'`)
	if value == "" {
		return time.Time{}, false
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed, true
	}
	if len(value) <= 10 {
		day, err := time.ParseInLocation("2006-01-02", value, time.Local)
		if err != nil {
			return time.Time{}, false
		}
		return time.Date(day.Year(), day.Month(), day.Day(), 23, 59, 59, 0, time.Local), true
	}
	// A zone-less instant is read in the machine's own zone, the way the Python
	// source's `astimezone()` read it.
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if parsed, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}
