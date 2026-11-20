// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/bborbe/errors"
)

// processListTimeout bounds one read of the process table, so a wedged `ps`
// cannot stall the job that asked for it.
const processListTimeout = 10 * time.Second

// Process is one row of the host's process table.
type Process struct {
	// PID is the process id.
	PID int
	// PPID is the parent's process id.
	PPID int
	// UID is the real user id the process runs as.
	UID int
	// Args is the command line split on whitespace. argv[0] is the executable.
	//
	// ⚠️ `ps` joins argv with single spaces, so an argument that itself held a
	// space is split here. Every path the reaper matches lives under
	// `~/.claude/plugins/`, which holds none, so the split cannot change a match.
	Args []string
}

//counterfeiter:generate -o ../mocks/process-lister.go --fake-name ProcessLister . ProcessLister

// ProcessLister reads the host's process table.
type ProcessLister interface {
	// List returns every process the caller may see.
	List(ctx context.Context) ([]Process, error)
}

// NewPsProcessLister creates a ProcessLister backed by `ps`.
func NewPsProcessLister() ProcessLister {
	return &processLister{}
}

type processLister struct{}

// List reads the process table through `ps`.
func (l *processLister) List(ctx context.Context) ([]Process, error) {
	ctx, cancel := context.WithTimeout(ctx, processListTimeout)
	defer cancel()
	// #nosec G204 -- the command and every flag are compile-time literals.
	raw, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,uid=,command=").Output()
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "read process table failed")
	}
	return ParsePsOutput(string(raw)), nil
}

// ParsePsOutput parses `ps -axo pid=,ppid=,uid=,command=` output.
//
// A line that does not parse is skipped rather than failing the read: one
// malformed row (a process exiting mid-read) must not blind the whole table.
func ParsePsOutput(raw string) []Process {
	var result []Process
	for line := range strings.SplitSeq(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		pid, errPID := strconv.Atoi(fields[0])
		ppid, errPPID := strconv.Atoi(fields[1])
		uid, errUID := strconv.Atoi(fields[2])
		if errPID != nil || errPPID != nil || errUID != nil {
			continue
		}
		result = append(result, Process{PID: pid, PPID: ppid, UID: uid, Args: fields[3:]})
	}
	return result
}

// Compile-time guard: the lister must satisfy its own interface.
var _ ProcessLister = (*processLister)(nil)
