// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command deploy restarts the attention board's launchd job and refuses to call
// the deploy done until the board serves the installed build — restoring the
// previous binary when it does not.
//
// It is the gate `make install`, `make launchd-reload` and
// `ac-deploy-attention-controller.sh` all run, so the three entry points cannot
// drift: the readiness proof, the two failure classes and the rollback are
// implemented once, here.
//
// Exit codes, because the callers act on them:
//
//	0  the installed build serves
//	1  the start failed and the previous binary serves again
//	2  the start failed and the board is dark (no .prev, or .prev failed too)
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/bborbe/errors"

	"github.com/bborbe/attention-controller/pkg/deploy"
)

// defaultLabel is the launchd job the Mac board runs as.
const defaultLabel = "com.bborbe.attention-controller"

func main() {
	// ⚠️ A signal must reach the driver, not kill it. An interrupt between
	// `launchctl bootout` and the readiness proof would otherwise leave the job
	// booted out with no rollback and no verdict — the board dark and nobody
	// told. The driver's own cancel path then stops polling and returns.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	outcome, err := run(ctx, os.Args[1:])
	if err != nil {
		// ⚠️ Exactly one line: for a dark board this line plus the exit code is
		// the whole signal, because no store exists to post a card to.
		_, _ = fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(exitCode(outcome))
}

// exitCode maps an outcome to the process exit code.
//
// ⚠️ 2 means the board is DARK and 3 means nothing was touched. Collapsing the
// two would fire a dark-board alarm for a deploy refused before it restarted
// anything (an unstamped build, an unreadable identity, a flag error).
func exitCode(outcome deploy.Outcome) int {
	switch outcome {
	case deploy.Healthy:
		return 0
	case deploy.RolledBack:
		return 1
	case deploy.Aborted:
		return 3
	default:
		return 2
	}
}

// run parses the flags, wires the real collaborators and runs the driver.
func run(ctx context.Context, args []string) (deploy.Outcome, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return deploy.Aborted, errors.Wrap(ctx, err, "resolve home directory failed")
	}
	flags := flag.NewFlagSet("deploy", flag.ContinueOnError)
	mode := flags.String("mode", string(deploy.InstallMode), "entry point: install or reload")
	bin := flags.String(
		"bin", filepath.Join(home, ".local", "bin", "attention-controller"),
		"installed binary path",
	)
	prev := flags.String("prev", "", "previous binary path (default <bin>.prev)")
	label := flags.String("label", defaultLabel, "launchd job label")
	plist := flags.String(
		"plist", filepath.Join(home, "Library", "LaunchAgents", defaultLabel+".plist"),
		"launchd plist path",
	)
	board := flags.String("board", "http://localhost:18080", "board base URL")
	readinessTimeout := flags.Duration(
		"readiness-timeout", deploy.ReadinessTimeout, "how long a start may take to serve",
	)
	rollbackBudget := flags.Duration(
		"rollback-budget", deploy.RollbackBudget, "how long a rollback may take to serve",
	)
	poll := flags.Duration("poll", deploy.PollInterval, "how often the board is probed")
	if err := flags.Parse(args); err != nil {
		return deploy.Aborted, err
	}
	if *prev == "" {
		*prev = *bin + ".prev"
	}
	driver := &deploy.Driver{
		Mode: deploy.Mode(*mode),
		Supervisor: &deploy.LaunchctlSupervisor{
			Label: *label,
			Plist: *plist,
			UID:   os.Getuid(),
		},
		Prober: newProber(*board),
		Binary: &deploy.FileBinary{Bin: *bin, Prev: *prev},
		Board: &deploy.HTTPCardPoster{
			URL: strings.TrimSuffix(*board, "/") + "/api/1.0/attention",
		},
		ReadinessTimeout: *readinessTimeout,
		RollbackBudget:   *rollbackBudget,
		PollInterval:     *poll,
		Now:              time.Now,
		Sleep:            time.Sleep,
		Log:              os.Stdout,
	}
	outcome, err := driver.Run(ctx)
	if err != nil {
		return deploy.OutcomeOf(err), err
	}
	return outcome, nil
}

// newProber builds the board prober for a base URL.
func newProber(board string) *deploy.HTTPProber {
	base := strings.TrimSuffix(board, "/")
	return &deploy.HTTPProber{
		ReadinessURL: base + "/readiness",
		HealthURL:    base + "/healthz",
		BoardURL:     base + "/",
	}
}
