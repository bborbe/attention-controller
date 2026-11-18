// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

const bashSessionID = "11111111-2222-3333-4444-555555555555"

// bashQuestion builds the wrapper question for a Bash gate.
func bashQuestion(requestID string, command string) []byte {
	raw, err := json.Marshal(map[string]any{
		"request_id": requestID,
		"input": map[string]any{
			"arguments": map[string]any{
				"tool_name": "Bash",
				"input":     map[string]any{"command": command},
			},
		},
	})
	Expect(err).NotTo(HaveOccurred())
	return raw
}

// writeFakeClassifier writes a /bin/sh fake classifier that appends one line to
// marker per run, then runs body.
func writeFakeClassifier(dir string, marker string, body string) string {
	script := filepath.Join(dir, "permission-tier.sh")
	content := fmt.Sprintf("#!/bin/sh\necho run >> '%s'\n%s\n", marker, body)
	Expect(os.WriteFile(script, []byte(content), 0o600)).To(Succeed())
	return script
}

// markerLines counts the fake's runs; an absent marker is zero runs.
func markerLines(marker string) int {
	content, err := os.ReadFile(marker)
	if os.IsNotExist(err) {
		return 0
	}
	Expect(err).NotTo(HaveOccurred())
	return strings.Count(string(content), "\n")
}

// captureGlog runs fn with glog writing every level to a captured stderr.
func captureGlog(fn func()) string {
	toStderr := flag.Lookup("logtostderr").Value.String()
	verbosity := flag.Lookup("v").Value.String()
	Expect(flag.Set("logtostderr", "true")).To(Succeed())
	Expect(flag.Set("v", "2")).To(Succeed())
	reader, writer, err := os.Pipe()
	Expect(err).NotTo(HaveOccurred())
	original := os.Stderr
	os.Stderr = writer
	done := make(chan string)
	go func() {
		content, _ := io.ReadAll(reader)
		done <- string(content)
	}()
	defer func() {
		os.Stderr = original
		Expect(flag.Set("logtostderr", toStderr)).To(Succeed())
		Expect(flag.Set("v", verbosity)).To(Succeed())
	}()
	fn()
	os.Stderr = original
	Expect(writer.Close()).To(Succeed())
	return <-done
}

var _ = Describe("routine gate settle job Bash rule", func() {
	var (
		ctx       context.Context
		dir       string
		workspace string
		marker    string
		store     *mocks.AttentionStore
		wrapper   *fakeWrapper
		sessions  *fakeSessions
		clock     libtime.CurrentDateTime
		timeout   time.Duration
	)

	newJob := func(script string) pkg.RoutineGateSettleJob {
		policy := pkg.NewRoutinePolicy(nil, pkg.WithRoutineBashClassifier(
			pkg.NewRoutineBashClassifier("/bin/sh", script, timeout),
			clock,
		))
		return pkg.NewRoutineGateSettleJob(store, wrapper, policy, sessions, time.Hour, clock)
	}
	setCommand := func(requestID string, command string) {
		wrapper.session = &pkg.WrapperSession{Question: &pkg.WrapperQuestion{
			RequestID: requestID,
			Raw:       bashQuestion(requestID, command),
		}}
	}
	heldCarrier := func() string {
		Expect(store.RecordAttemptCallCount()).To(Equal(1))
		_, _, carrier, outcome := store.RecordAttemptArgsForCall(0)
		Expect(outcome).To(Equal(pkg.HeldOutcome))
		return carrier
	}

	BeforeEach(func() {
		ctx = context.Background()
		dir = GinkgoT().TempDir()
		workspace = GinkgoT().TempDir()
		marker = filepath.Join(dir, "runs")
		timeout = 5 * time.Second
		store = &mocks.AttentionStore{}
		store.RecordAttemptReturns(&pkg.DeliveryAttempt{}, nil)
		store.ReadReturns(pkg.Items{permissionItem("item-1")}, nil)
		wrapper = &fakeWrapper{state: pkg.WrapperProbeOK, verdict: pkg.DeliveryOK}
		setCommand("req-1", "ls -la")
		sessions = &fakeSessions{
			entry: pkg.SessionEntry{PID: os.Getpid(), Entrypoint: "sdk-cli", CWD: workspace},
			has:   true,
		}
		clock = libtime.NewCurrentDateTime()
		clock.SetNow(libtime.DateTime(time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)))
	})

	// AC1
	It("allows and delivers an allowlist-tier Bash gate", func() {
		job := newJob(writeFakeClassifier(dir, marker,
			`cat >/dev/null; echo '{"tier":"allowlist","form":null,"reason":"x"}'`))
		Expect(job.SettleOnce(ctx)).To(Succeed())
		Expect(wrapper.answered).To(Equal([]string{bashSessionID + ":allow"}))
		Expect(store.RecordAttemptCallCount()).To(Equal(1))
		_, id, _, outcome := store.RecordAttemptArgsForCall(0)
		Expect(id).To(Equal(pkg.ItemID("item-1")))
		Expect(outcome).To(Equal(pkg.DeliveredOutcome))
	})

	// AC2
	DescribeTable("holds a non-allowlist tier",
		func(tier string, carrier string) {
			job := newJob(writeFakeClassifier(dir, marker,
				fmt.Sprintf(`cat >/dev/null; echo '{"tier":"%s","form":null,"reason":"x"}'`, tier)))
			Expect(job.SettleOnce(ctx)).To(Succeed())
			Expect(wrapper.answered).To(BeEmpty())
			Expect(heldCarrier()).To(Equal(carrier))
		},
		Entry("manager", "manager", "tier-manager"),
		Entry("operator", "operator", "tier-operator"),
	)

	// AC3
	DescribeTable(
		"fails closed as tier-error",
		func(body string) {
			job := newJob(writeFakeClassifier(dir, marker, body))
			Expect(job.SettleOnce(ctx)).To(Succeed())
			Expect(wrapper.answered).To(BeEmpty())
			Expect(heldCarrier()).To(Equal("tier-error"))
		},
		Entry("non-zero exit", `cat >/dev/null; echo '{"tier":"allowlist"}'; exit 3`),
		Entry("empty stdout", `cat >/dev/null`),
		Entry("non-JSON stdout", `cat >/dev/null; echo 'allowlist'`),
		Entry("unknown tier", `cat >/dev/null; echo '{"tier":"allow"}'`),
		Entry("missing tier", `cat >/dev/null; echo '{"form":null}'`),
		Entry(
			"two JSON objects",
			`cat >/dev/null; echo '{"tier":"allowlist"}{"tier":"allowlist"}'`,
		),
		Entry(
			"output over the 64 KiB cap",
			`cat >/dev/null; printf '{"tier":"allowlist","reason":"'; head -c 70000 /dev/zero | tr '\0' 'a'; printf '"}'`,
		),
	)

	It("fails closed as tier-error when the script is deleted after construction", func() {
		script := writeFakeClassifier(dir, marker, `echo '{"tier":"allowlist"}'`)
		job := newJob(script)
		Expect(os.Remove(script)).To(Succeed())
		Expect(job.SettleOnce(ctx)).To(Succeed())
		Expect(wrapper.answered).To(BeEmpty())
		Expect(heldCarrier()).To(Equal("tier-error"))
	})

	It("fails closed as tier-error within the deadline when the classifier hangs", func() {
		timeout = 500 * time.Millisecond
		job := newJob(writeFakeClassifier(dir, marker,
			`sleep 10; echo '{"tier":"allowlist"}'`))
		started := time.Now()
		Expect(job.SettleOnce(ctx)).To(Succeed())
		Expect(time.Since(started)).To(BeNumerically("<", timeout+time.Second))
		Expect(wrapper.answered).To(BeEmpty())
		Expect(heldCarrier()).To(Equal("tier-error"))
	})

	// AC4
	Context("guards run before the classifier", func() {
		var job pkg.RoutineGateSettleJob

		BeforeEach(func() {
			job = newJob(writeFakeClassifier(dir, marker,
				`cat >/dev/null; echo '{"tier":"allowlist"}'`))
		})

		It("skips a session with a tty", func() {
			sessions.tty = "/dev/ttys004"
			Expect(job.SettleOnce(ctx)).To(Succeed())
			Expect(marker).NotTo(BeAnExistingFile())
		})

		It("skips a dead session", func() {
			sessions.entry.PID = 0
			Expect(job.SettleOnce(ctx)).To(Succeed())
			Expect(marker).NotTo(BeAnExistingFile())
		})

		It("skips a session absent from the registry", func() {
			sessions.has = false
			Expect(job.SettleOnce(ctx)).To(Succeed())
			Expect(marker).NotTo(BeAnExistingFile())
		})

		It("skips a non-permission item", func() {
			item := permissionItem("item-1")
			item.AnswerMechanism = pkg.MessageAnswerMechanism
			store.ReadReturns(pkg.Items{item}, nil)
			Expect(job.SettleOnce(ctx)).To(Succeed())
			Expect(marker).NotTo(BeAnExistingFile())
		})

		DescribeTable("holds a cwd that is not a workspace",
			func(cwd func() string) {
				sessions.entry.CWD = cwd()
				Expect(job.SettleOnce(ctx)).To(Succeed())
				Expect(marker).NotTo(BeAnExistingFile())
				Expect(wrapper.answered).To(BeEmpty())
				Expect(heldCarrier()).To(Equal("bash-cwd-not-workspace"))
			},
			Entry("root", func() string { return "/" }),
			Entry("relative", func() string { return "relative/ws" }),
			Entry("empty", func() string { return "" }),
			Entry("home tree", func() string {
				home, err := os.UserHomeDir()
				Expect(err).NotTo(HaveOccurred())
				return home
			}),
		)
	})

	// AC6
	It("classifies a gate once per hold interval, per command, while it is open", func() {
		job := newJob(writeFakeClassifier(dir, marker,
			`cat >/dev/null; echo '{"tier":"manager"}'`))
		for range 3 {
			Expect(job.SettleOnce(ctx)).To(Succeed())
		}
		Expect(markerLines(marker)).To(Equal(1))

		clock.SetNow(
			clock.Now().Add(libtime.Duration(pkg.RoutineGateHoldRepeatInterval + time.Second)),
		)
		Expect(job.SettleOnce(ctx)).To(Succeed())
		Expect(markerLines(marker)).To(Equal(2))

		setCommand("req-2", "grep -rn foo .")
		Expect(job.SettleOnce(ctx)).To(Succeed())
		Expect(markerLines(marker)).To(Equal(3))
		Expect(job.SettleOnce(ctx)).To(Succeed())
		Expect(markerLines(marker)).To(Equal(3))

		store.ReadReturns(pkg.Items{}, nil)
		Expect(job.SettleOnce(ctx)).To(Succeed())
		store.ReadReturns(pkg.Items{permissionItem("item-1")}, nil)
		Expect(job.SettleOnce(ctx)).To(Succeed())
		Expect(markerLines(marker)).To(Equal(4))
	})

	// AC7
	Context("the exec contract", func() {
		var (
			argvFile  string
			stdinFile string
			envFile   string
			job       pkg.RoutineGateSettleJob
		)

		BeforeEach(func() {
			argvFile = filepath.Join(dir, "argv")
			stdinFile = filepath.Join(dir, "stdin")
			envFile = filepath.Join(dir, "env")
			job = newJob(writeFakeClassifier(dir, marker, fmt.Sprintf(
				`printf '%%s\n' "$0" "$@" > '%s'; cat > '%s'; env > '%s'; echo '{"tier":"allowlist"}'`,
				argvFile,
				stdinFile,
				envFile,
			)))
			GinkgoT().Setenv("ATTENTION_STORE_TOKEN", "store-token-marker")
		})

		It("passes the script and fixed flags in argv, never the command", func() {
			Expect(job.SettleOnce(ctx)).To(Succeed())
			argv, err := os.ReadFile(argvFile)
			Expect(err).NotTo(HaveOccurred())
			script := filepath.Join(dir, "permission-tier.sh")
			Expect(string(argv)).To(Equal(script + "\n--tool\nBash\n--input\n-\n"))
		})

		DescribeTable("passes the command byte-identical on stdin",
			func(command func() string) {
				setCommand("req-1", command())
				Expect(job.SettleOnce(ctx)).To(Succeed())
				stdin, err := os.ReadFile(stdinFile)
				Expect(err).NotTo(HaveOccurred())
				Expect(string(stdin) == command()).To(BeTrue())
			},
			Entry("one line", func() string { return "ls -la" }),
			Entry("with a newline", func() string { return "ls\ncat README.md" }),
			Entry("a 200 KiB heredoc", func() string {
				return "cat <<'EOF'\n" + strings.Repeat("x", 200*1024) + "\nEOF"
			}),
			Entry("a substitution", func() string { return "ls $(echo /) && rm -rf x" }),
		)

		It("does not hand the child the service's credentials", func() {
			Expect(job.SettleOnce(ctx)).To(Succeed())
			env, err := os.ReadFile(envFile)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(env)).NotTo(ContainSubstring("ATTENTION_STORE_TOKEN"))
			Expect(string(env)).NotTo(ContainSubstring("store-token-marker"))
		})
	})

	// AC3 / security: a compound or substituted command is the classifier's to
	// rate, and a non-allowlist rating is never allowed.
	It("holds a compound $(…) command the classifier rates operator", func() {
		job := newJob(writeFakeClassifier(
			dir,
			marker,
			`case "$(cat)" in *'$('*) echo '{"tier":"operator"}';; *) echo '{"tier":"allowlist"}';; esac`,
		))
		setCommand("req-1", "ls $(cat ~/.netrc) && rm -rf /")
		Expect(job.SettleOnce(ctx)).To(Succeed())
		Expect(wrapper.answered).To(BeEmpty())
		Expect(heldCarrier()).To(Equal("tier-operator"))
	})

	// AC10
	It("writes neither the command nor the classifier's reason to the log or the store", func() {
		const reasonMarker = "REASON-MARKER-7f3a9c"
		const commandMarker = "COMMAND-MARKER-41be02"
		for _, tier := range []string{"allowlist", "manager", "bogus"} {
			store = &mocks.AttentionStore{}
			store.RecordAttemptReturns(&pkg.DeliveryAttempt{}, nil)
			store.ReadReturns(pkg.Items{permissionItem("item-1")}, nil)
			wrapper.answered = nil
			setCommand("req-1", "ls "+commandMarker)
			job := newJob(writeFakeClassifier(dir, marker, fmt.Sprintf(
				`cat >/dev/null; echo '{"tier":"%s","form":"%s","reason":"%s"}'`,
				tier, reasonMarker, reasonMarker,
			)))
			output := captureGlog(func() {
				Expect(job.SettleOnce(ctx)).To(Succeed())
			})
			Expect(output).To(ContainSubstring("item=item-1"))
			Expect(strings.Count(output, reasonMarker)).To(Equal(0))
			Expect(strings.Count(output, commandMarker)).To(Equal(0))
			Expect(store.RecordAttemptCallCount()).To(Equal(1))
			_, _, carrier, _ := store.RecordAttemptArgsForCall(0)
			Expect(strings.Count(carrier, reasonMarker)).To(Equal(0))
			Expect(strings.Count(carrier, commandMarker)).To(Equal(0))
		}
	})
})

var _ = Describe("default routine policy", func() {
	var (
		ctx       context.Context
		home      string
		workspace string
		marker    string
		store     *mocks.AttentionStore
		wrapper   *fakeWrapper
		sessions  *fakeSessions
		clock     libtime.CurrentDateTime
	)

	defaultPath := func() string {
		return filepath.Join(
			home,
			".claude",
			"plugins",
			"marketplaces",
			"claude-supervisor",
			"scripts",
			"permission-tier.py",
		)
	}
	// writeDefaultClassifier installs a python fake at the default path, because
	// the production factory runs the classifier through /usr/bin/python3.
	writeDefaultClassifier := func(tier string) {
		Expect(os.MkdirAll(filepath.Dir(defaultPath()), 0o700)).To(Succeed())
		content := fmt.Sprintf(
			"import sys\nsys.stdin.read()\nopen(%q, 'a').write('run\\n')\nprint('{\"tier\":\"%s\"}')\n",
			marker,
			tier,
		)
		Expect(os.WriteFile(defaultPath(), []byte(content), 0o600)).To(Succeed())
	}
	settle := func(policy pkg.RoutinePolicy) {
		job := pkg.NewRoutineGateSettleJob(store, wrapper, policy, sessions, time.Hour, clock)
		Expect(job.SettleOnce(ctx)).To(Succeed())
	}

	BeforeEach(func() {
		ctx = context.Background()
		home = GinkgoT().TempDir()
		workspace = GinkgoT().TempDir()
		marker = filepath.Join(GinkgoT().TempDir(), "runs")
		GinkgoT().Setenv("HOME", home)
		store = &mocks.AttentionStore{}
		store.RecordAttemptReturns(&pkg.DeliveryAttempt{}, nil)
		store.ReadReturns(pkg.Items{permissionItem("item-1")}, nil)
		wrapper = &fakeWrapper{
			state:   pkg.WrapperProbeOK,
			verdict: pkg.DeliveryOK,
			session: &pkg.WrapperSession{Question: &pkg.WrapperQuestion{
				RequestID: "req-1",
				Raw:       bashQuestion("req-1", "ls -la"),
			}},
		}
		sessions = &fakeSessions{
			entry: pkg.SessionEntry{PID: os.Getpid(), Entrypoint: "sdk-cli", CWD: workspace},
			has:   true,
		}
		clock = libtime.NewCurrentDateTime()
	})

	// AC9
	It("resolves an empty setting to the marketplace clone under HOME", func() {
		writeDefaultClassifier("allowlist")
		Expect(pkg.ResolveRoutineBashClassifier("")).To(Equal(defaultPath()))
	})

	It("disables the Bash rule with one startup warning when the default is missing", func() {
		var policy pkg.RoutinePolicy
		output := captureGlog(func() {
			policy = pkg.NewDefaultRoutinePolicy("", clock)
		})
		Expect(strings.Count(output, "action=routine_bash_classifier_disabled")).To(Equal(1))
		settle(policy)
		Expect(wrapper.answered).To(BeEmpty())
		_, _, carrier, outcome := store.RecordAttemptArgsForCall(0)
		Expect(carrier).To(Equal("no-routine-rule"))
		Expect(outcome).To(Equal(pkg.HeldOutcome))
	})

	It("keeps the Read rule when the Bash rule is disabled", func() {
		target := filepath.Join(workspace, "a.go")
		Expect(os.WriteFile(target, []byte("package a\n"), 0o600)).To(Succeed())
		raw, err := json.Marshal(map[string]any{"input": map[string]any{"arguments": map[string]any{
			"tool_name": "Read", "input": map[string]any{"file_path": target},
		}}})
		Expect(err).NotTo(HaveOccurred())
		wrapper.session.Question.Raw = raw
		settle(pkg.NewDefaultRoutinePolicy("", clock))
		Expect(wrapper.answered).To(Equal([]string{bashSessionID + ":allow"}))
	})

	It("answers an allowlist Bash gate through the default path", func() {
		writeDefaultClassifier("allowlist")
		settle(pkg.NewDefaultRoutinePolicy("", clock))
		Expect(markerLines(marker)).To(Equal(1))
		Expect(wrapper.answered).To(Equal([]string{bashSessionID + ":allow"}))
		_, _, _, outcome := store.RecordAttemptArgsForCall(0)
		Expect(outcome).To(Equal(pkg.DeliveredOutcome))
	})

	// AC5
	DescribeTable("rolls back to today's record without starting a process",
		func(setting func() string) {
			writeDefaultClassifier("allowlist")
			settle(pkg.NewDefaultRoutinePolicy(setting(), clock))
			Expect(marker).NotTo(BeAnExistingFile())
			Expect(wrapper.answered).To(BeEmpty())
			Expect(store.RecordAttemptCallCount()).To(Equal(1))
			_, _, carrier, outcome := store.RecordAttemptArgsForCall(0)
			Expect(carrier).To(Equal("no-routine-rule"))
			Expect(outcome).To(Equal(pkg.HeldOutcome))
		},
		Entry("off", func() string { return pkg.RoutineBashClassifierOff }),
		Entry("a path that does not exist", func() string {
			return filepath.Join(home, "missing", "permission-tier.py")
		}),
	)
})
