// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package deploy

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bborbe/errors"
)

const (
	// stopAttempts and stopInterval bound the wait for a bootout to take
	// effect. bootout is asynchronous: bootstrapping before the job is gone
	// fails with `5: Input/output error`.
	stopAttempts = 40
	stopInterval = 500 * time.Millisecond
)

// launchctlPidPattern reads the live pid out of `launchctl print`.
var launchctlPidPattern = regexp.MustCompile(`(?m)^\s*pid = (\d+)\s*$`)

// launchctlExitPattern reads launchd's rendering of the last exit code.
var launchctlExitPattern = regexp.MustCompile(`(?m)^\s*last exit code = (.+?)\s*$`)

// CommandRunner runs one command and returns its combined output.
type CommandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// LaunchctlSupervisor drives one launchd user agent.
type LaunchctlSupervisor struct {
	// Label is the launchd job label.
	Label string
	// Plist is the job's plist path.
	Plist string
	// UID is the user's uid; the job lives in gui/<uid>.
	UID int
	// Run executes launchctl. Defaults to the real binary.
	Run CommandRunner
	// Sleep waits between stop polls. Defaults to time.Sleep.
	Sleep func(time.Duration)
}

// Stop boots the job out and waits for launchd to let go of it.
//
// A bootout of a job that is not loaded is not an error: the caller's contract
// is "the job is gone afterwards", and a job that was already gone satisfies it.
func (s *LaunchctlSupervisor) Stop(ctx context.Context) error {
	_, _ = s.run(ctx, "bootout", s.domain()+"/"+s.Label)
	for i := 0; i < stopAttempts; i++ {
		if !s.loaded(ctx) {
			return nil
		}
		s.sleep()
	}
	return errors.Errorf(ctx, "bootout of %s did not finish", s.Label)
}

// Bootstrap starts the job.
func (s *LaunchctlSupervisor) Bootstrap(ctx context.Context) error {
	out, err := s.run(ctx, "bootstrap", s.domain(), s.Plist)
	if err != nil {
		return errors.Wrapf(ctx, err, "launchctl bootstrap failed: %s", trimOutput(out))
	}
	return nil
}

// Status reports what launchd says about the job.
//
// A job launchd cannot find is not an error to the caller: it is the exited
// state, and that is exactly what the classifier reads it as.
func (s *LaunchctlSupervisor) Status(ctx context.Context) (ProcessStatus, error) {
	out, err := s.run(ctx, "print", s.domain()+"/"+s.Label)
	if err != nil {
		return ProcessStatus{}, nil
	}
	return parseLaunchctlStatus(out), nil
}

// loaded reports whether launchd holds the job at all.
func (s *LaunchctlSupervisor) loaded(ctx context.Context) bool {
	_, err := s.run(ctx, "print", s.domain()+"/"+s.Label)
	return err == nil
}

// domain is the launchd domain the job lives in.
func (s *LaunchctlSupervisor) domain() string {
	return "gui/" + strconv.Itoa(s.UID)
}

// run executes launchctl with the configured runner.
func (s *LaunchctlSupervisor) run(ctx context.Context, args ...string) ([]byte, error) {
	runner := s.Run
	if runner == nil {
		runner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		}
	}
	return runner(ctx, "launchctl", args...)
}

// sleep waits the configured interval.
func (s *LaunchctlSupervisor) sleep() {
	sleeper := s.Sleep
	if sleeper == nil {
		sleeper = time.Sleep
	}
	sleeper(stopInterval)
}

// parseLaunchctlStatus reads the pid and the last exit code out of a
// `launchctl print` rendering.
func parseLaunchctlStatus(out []byte) ProcessStatus {
	status := ProcessStatus{}
	if match := launchctlExitPattern.FindSubmatch(out); match != nil {
		status.LastExit = strings.TrimSpace(string(match[1]))
	}
	if match := launchctlPidPattern.FindSubmatch(out); match != nil {
		pid, err := strconv.Atoi(string(match[1]))
		if err == nil {
			status.Pid = pid
			status.Running = true
		}
	}
	return status
}

// trimOutput makes a command's output fit on one log line.
func trimOutput(out []byte) string {
	return strings.TrimSpace(strings.Join(strings.Fields(string(out)), " "))
}
