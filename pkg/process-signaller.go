// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"
	"syscall"

	"github.com/bborbe/errors"
)

// ErrProcessGone reports that the process exited before the signal reached it.
var ErrProcessGone = stderrors.New("process gone")

//counterfeiter:generate -o ../mocks/process-signaller.go --fake-name ProcessSignaller . ProcessSignaller

// ProcessSignaller asks a process to stop.
type ProcessSignaller interface {
	// Terminate sends SIGTERM to pid. A process that already exited returns
	// ErrProcessGone, which a caller may treat as success.
	Terminate(ctx context.Context, pid int) error
}

// NewSyscallProcessSignaller creates a ProcessSignaller backed by kill(2).
func NewSyscallProcessSignaller() ProcessSignaller {
	return &syscallProcessSignaller{}
}

type syscallProcessSignaller struct{}

// Terminate sends SIGTERM, never SIGKILL: a watcher gets the chance to flush
// its state file and exit through its own handler.
func (s *syscallProcessSignaller) Terminate(ctx context.Context, pid int) error {
	if pid <= 1 {
		return errors.Errorf(ctx, "refusing to signal pid %d", pid)
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		if stderrors.Is(err, syscall.ESRCH) {
			return ErrProcessGone
		}
		return errors.Wrapf(ctx, err, "terminate pid %d failed", pid)
	}
	return nil
}

// Compile-time guard: the signaller must satisfy its own interface.
var _ ProcessSignaller = (*syscallProcessSignaller)(nil)
