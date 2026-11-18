// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

const (
	// RoutineBashClassifierDefault is the classifier the Bash rule runs when the
	// setting is empty: claude-supervisor's own permission classifier in the
	// marketplace clone. ⚠️ The clone, not the versioned plugin cache: a fixed
	// cache path pins the release it was written against, and the clone moves
	// the script and the allowlist it loads together.
	RoutineBashClassifierDefault = "~/.claude/plugins/marketplaces/claude-supervisor/scripts/permission-tier.py"

	// RoutineBashClassifierOff disables the Bash rule — the rollback.
	RoutineBashClassifierOff = "off"

	// RoutineBashClassifierInterpreter runs the classifier. The script is not
	// executable, so it is run through the interpreter the sibling jobs use.
	RoutineBashClassifierInterpreter = "/usr/bin/python3"

	// RoutineBashClassifierTimeout is the most one gate can delay a settle cycle.
	RoutineBashClassifierTimeout = 5 * time.Second

	// routineBashClassifierOutputCap bounds what the classifier's stdout may
	// occupy. A larger answer is not a verdict.
	routineBashClassifierOutputCap = 64 * 1024

	// routineBashClassifierWaitDelay bounds how long a killed classifier's pipes
	// may keep Wait blocked.
	routineBashClassifierWaitDelay = 250 * time.Millisecond
)

// The classifier's tiers. Only allowlist is ever an allow.
const (
	RoutineBashTierAllowlist = "allowlist"
	RoutineBashTierManager   = "manager"
	RoutineBashTierOperator  = "operator"
)

// RoutineBashClassifier rates a Bash command with claude-supervisor's
// permission classifier.
type RoutineBashClassifier interface {
	// Classify returns the tier the classifier assigned to command.
	//
	// ⚠️ Every failure is an error, never a tier: a non-zero exit, a deadline, a
	// launch failure, oversized or unparseable output, and a missing or unknown
	// tier. The error text never carries the command or the classifier's output.
	Classify(ctx context.Context, command string) (string, error)
}

// NewRoutineBashClassifier creates the exec client.
//
// The interpreter and the script are injected so a spec can drive the real exec
// path against a fake classifier.
func NewRoutineBashClassifier(
	interpreter string,
	script string,
	timeout time.Duration,
) RoutineBashClassifier {
	return &routineBashClassifier{
		interpreter: interpreter,
		script:      script,
		timeout:     timeout,
	}
}

type routineBashClassifier struct {
	interpreter string
	script      string
	timeout     time.Duration
}

// Classify runs `<interpreter> <script> --tool Bash --input -` with the command
// on stdin.
//
// ⚠️ The command is a headless child's text, so it reaches the classifier only
// on stdin — never in argv, never through a shell.
func (c *routineBashClassifier) Classify(ctx context.Context, command string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	// #nosec G204 -- the interpreter and the script are operator configuration;
	// the gate-supplied command is stdin, never an argv word.
	cmd := exec.CommandContext(
		commandCtx,
		c.interpreter,
		c.script,
		"--tool",
		"Bash",
		"--input",
		"-",
	)
	cmd.Env = childEnv()
	cmd.Stdin = strings.NewReader(command)
	stdout := &cappedBuffer{limit: routineBashClassifierOutputCap}
	cmd.Stdout = stdout
	// ⚠️ Its own process group, killed as a group: a classifier that forked a
	// child would otherwise leave it holding stdout open past the deadline.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = routineBashClassifierWaitDelay
	if err := cmd.Run(); err != nil {
		// ⚠️ A process killed by the deadline is NOT a verdict.
		if commandCtx.Err() != nil {
			return "", errors.Errorf(ctx, "classifier class=timeout did not complete")
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", errors.Errorf(ctx, "classifier class=exit code=%d", exitErr.ExitCode())
		}
		return "", errors.Errorf(ctx, "classifier class=launch failed")
	}
	if stdout.overflow {
		return "", errors.Errorf(
			ctx,
			"classifier class=oversize output over %d bytes",
			stdout.limit,
		)
	}
	var verdict struct {
		Tier *string `json:"tier"`
	}
	// ⚠️ The parse error is not wrapped: its text quotes the classifier's output.
	if err := json.Unmarshal(bytes.TrimSpace(stdout.buf.Bytes()), &verdict); err != nil {
		return "", errors.Errorf(ctx, "classifier class=parse output is not one JSON object")
	}
	if verdict.Tier == nil {
		return "", errors.Errorf(ctx, "classifier class=parse output has no tier")
	}
	switch *verdict.Tier {
	case RoutineBashTierAllowlist, RoutineBashTierManager, RoutineBashTierOperator:
		return *verdict.Tier, nil
	default:
		return "", errors.Errorf(ctx, "classifier class=tier unknown tier")
	}
}

// cappedBuffer keeps at most limit bytes and records that more arrived.
//
// ⚠️ It reports every write as accepted, so a chatty child is never blocked on
// a full pipe while the deadline runs out.
type cappedBuffer struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	room := b.limit - b.buf.Len()
	if len(p) > room {
		b.overflow = true
		if room > 0 {
			b.buf.Write(p[:room])
		}
		return len(p), nil
	}
	b.buf.Write(p)
	return len(p), nil
}

// ResolveRoutineBashClassifier turns the setting into the classifier path, or ""
// when the Bash rule is disabled.
//
// Empty is the default (the marketplace clone); `off` disables the rule; a path
// that does not exist disables it too and logs one startup warning, because a
// host without claude-supervisor installed has nothing to classify with and
// refusing to boot over it would take the board down.
func ResolveRoutineBashClassifier(setting string) string {
	if setting == RoutineBashClassifierOff {
		return ""
	}
	if setting == "" {
		setting = RoutineBashClassifierDefault
	}
	path := expandHome(setting)
	if !filepath.IsAbs(path) {
		glog.Warningf(
			"job=attention-watcher action=routine_bash_classifier_disabled reason=not-absolute path=%q",
			path,
		)
		return ""
	}
	if _, err := os.Stat(path); err != nil {
		glog.Warningf(
			"job=attention-watcher action=routine_bash_classifier_disabled reason=missing path=%q",
			path,
		)
		return ""
	}
	return path
}

// NewDefaultRoutinePolicy builds the settle arm's production policy from the
// classifier setting: the workspace Read rule always, the Bash rule when the
// setting resolves to an existing classifier.
func NewDefaultRoutinePolicy(
	setting string,
	currentDateTimeGetter libtime.CurrentDateTimeGetter,
) RoutinePolicy {
	path := ResolveRoutineBashClassifier(setting)
	if path == "" {
		return NewRoutinePolicy(nil)
	}
	return NewRoutinePolicy(
		nil,
		WithRoutineBashClassifier(
			NewRoutineBashClassifier(
				RoutineBashClassifierInterpreter,
				path,
				RoutineBashClassifierTimeout,
			),
			currentDateTimeGetter,
		),
	)
}

// Compile-time guard.
var _ RoutineBashClassifier = (*routineBashClassifier)(nil)
