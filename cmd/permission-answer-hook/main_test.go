// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	"runtime"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

// recordedAttempt is one RecordAttempt the fake client saw.
type recordedAttempt struct {
	itemID  pkg.ItemID
	carrier string
	outcome pkg.DeliveryOutcome
}

// recordingClient is a storeClient that records every call, so "no store
// traffic" is a counted assertion rather than a comment.
type recordingClient struct {
	items        pkg.Items
	item         *pkg.Item
	listErr      error
	getErr       error
	attemptErr   error
	listCalls    int
	getCalls     int
	attemptCalls int
	attempts     []recordedAttempt
}

func (c *recordingClient) List(_ context.Context) (pkg.Items, error) {
	c.listCalls++
	if c.listErr != nil {
		return nil, c.listErr
	}
	return c.items, nil
}

func (c *recordingClient) Get(_ context.Context, _ pkg.ItemID) (*pkg.Item, error) {
	c.getCalls++
	if c.getErr != nil {
		return nil, c.getErr
	}
	return c.item, nil
}

func (c *recordingClient) RecordAttempt(
	_ context.Context,
	itemID pkg.ItemID,
	carrier string,
	outcome pkg.DeliveryOutcome,
) error {
	c.attemptCalls++
	c.attempts = append(
		c.attempts,
		recordedAttempt{itemID: itemID, carrier: carrier, outcome: outcome},
	)
	return c.attemptErr
}

// fakeTimer is one timer armed on the fake clock.
type fakeTimer struct {
	due time.Time
	ch  chan time.Time
}

// fakeClock is the hook's time source in a spec. Advance moves Now and fires
// every timer that becomes due, including one re-armed during the advance.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

// newFakeClock creates a fake clock at the given instant.
func newFakeClock(now time.Time) *fakeClock {
	return &fakeClock{now: now}
}

// Now returns the fake clock's current instant.
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Timer arms a one-shot timer that fires once d has elapsed on this clock.
func (c *fakeClock) Timer(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &fakeTimer{due: c.now.Add(d), ch: make(chan time.Time, 1)}
	c.timers = append(c.timers, timer)
	return timer.ch
}

// Advance moves time forward by d, firing every timer that becomes due. A
// receiver that re-arms a timer inside the same advance is picked up by the next
// scan.
func (c *fakeClock) Advance(d time.Duration) {
	target := c.Now().Add(d)
	for {
		timer := c.takeDue(target)
		if timer == nil {
			break
		}
		select {
		case timer.ch <- timer.due:
		default:
		}
		runtime.Gosched()
	}
	c.mu.Lock()
	if c.now.Before(target) {
		c.now = target
	}
	c.mu.Unlock()
}

// takeDue removes and returns the earliest timer due at or before target.
func (c *fakeClock) takeDue(target time.Time) *fakeTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, timer := range c.timers {
		if timer.due.After(target) {
			continue
		}
		c.timers = append(c.timers[:i], c.timers[i+1:]...)
		if c.now.Before(timer.due) {
			c.now = timer.due
		}
		return timer
	}
	return nil
}

// newHookOptions builds the injectable options one spec drives.
func newHookOptions(
	args []string,
	stdin string,
	env map[string]string,
	client *recordingClient,
) (hookOptions, *bytes.Buffer, *bytes.Buffer) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	opts := hookOptions{
		Args:   args,
		Stdin:  strings.NewReader(stdin),
		Stdout: stdout,
		Stderr: stderr,
		Getenv: func(key string) string { return env[key] },
		Client: client,
		Clock:  newFakeClock(time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)),
		// The deadline is set so a later step's waiting path has one; this
		// prompt's run never reaches it.
		Deadline: HookDeadline,
	}
	return opts, stdout, stderr
}

// noStoreCalls asserts the recording client saw no traffic.
func noStoreCalls(client *recordingClient) {
	Expect(client.listCalls).To(Equal(0))
	Expect(client.getCalls).To(Equal(0))
	Expect(client.attemptCalls).To(Equal(0))
	Expect(client.attempts).To(BeEmpty())
}

var _ = Describe("TestHookPayloadReadFromStdin", func() {
	It("TestHookPayloadReadFromStdin parses the payload and derives the expect prefix", func() {
		stdin := `{"session_id":"S","tool_name":"Write","tool_input":{"file_path":"/tmp/w.txt"}}`
		input, ok, _ := parseHookInput(strings.NewReader(stdin), nil)
		Expect(ok).To(BeTrue())
		Expect(input.SessionID).To(Equal("S"))
		Expect(input.Expect).To(Equal("Write: /tmp/w.txt"))
	})

	It("TestHookPayloadReadFromStdin leaves the prefix empty for --session-id", func() {
		input, ok, _ := parseHookInput(strings.NewReader(""), []string{"--session-id", "S"})
		Expect(ok).To(BeTrue())
		Expect(input.SessionID).To(Equal("S"))
		Expect(input.Expect).To(BeEmpty())
	})

	It(
		"TestHookPayloadReadFromStdin exits without touching the store when no session is named",
		func() {
			client := &recordingClient{}
			opts, stdout, stderr := newHookOptions(nil, "", nil, client)

			input, ok, reason := parseHookInput(strings.NewReader(""), nil)
			Expect(ok).To(BeFalse())
			Expect(input.SessionID).To(BeEmpty())
			Expect(reason).ToNot(BeEmpty())

			Expect(run(context.Background(), opts)).To(Succeed())
			Expect(stdout.String()).To(BeEmpty())
			Expect(stderr.String()).To(ContainSubstring("no session id"))
			noStoreCalls(client)
		},
	)
})

var _ = Describe("TestPermissionAnswerRequiresAnsweredStateDecisionResolvedBy", func() {
	type verdictCase struct {
		item       *pkg.Item
		decision   pkg.Decision
		wantReason string
	}

	DescribeTable(
		"TestPermissionAnswerRequiresAnsweredStateDecisionResolvedBy checks the three conditions in order",
		func(c verdictCase) {
			decision, reason := permissionVerdict(c.item)
			Expect(decision).To(Equal(c.decision))
			if c.wantReason == "" {
				Expect(reason).To(BeEmpty())
			} else {
				Expect(reason).To(ContainSubstring(c.wantReason))
			}
		},
		Entry("passes when answered with allow and a resolver", verdictCase{
			item: &pkg.Item{
				State:      pkg.AnsweredState,
				Decision:   pkg.AllowDecision,
				ResolvedBy: "session-1",
			},
			decision: pkg.AllowDecision,
		}),
		Entry("fails on state", verdictCase{
			item: &pkg.Item{
				State:      pkg.OpenState,
				Decision:   pkg.AllowDecision,
				ResolvedBy: "session-1",
			},
			wantReason: "state",
		}),
		Entry("fails on decision", verdictCase{
			item: &pkg.Item{
				State:      pkg.AnsweredState,
				ResolvedBy: "session-1",
			},
			wantReason: "decision",
		}),
		Entry("fails on resolved_by", verdictCase{
			item: &pkg.Item{
				State:    pkg.AnsweredState,
				Decision: pkg.AllowDecision,
			},
			wantReason: "resolved_by",
		}),
	)
})

var _ = Describe("TestDenyNeverInferred", func() {
	It("TestDenyNeverInferred returns no decision for an empty decision", func() {
		decision, reason := permissionVerdict(&pkg.Item{
			State:      pkg.AnsweredState,
			ResolvedBy: "session-1",
		})
		Expect(decision).To(BeEmpty())
		Expect(decision).ToNot(Equal(pkg.DenyDecision))
		Expect(reason).To(ContainSubstring("decision"))
	})

	It("TestDenyNeverInferred returns no decision for an unknown decision", func() {
		decision, _ := permissionVerdict(&pkg.Item{
			State:      pkg.AnsweredState,
			Decision:   pkg.Decision("maybe"),
			ResolvedBy: "session-1",
		})
		Expect(decision).To(BeEmpty())
		Expect(decision).ToNot(Equal(pkg.DenyDecision))
	})
})

var _ = Describe("TestKillSwitchSupervisorPermissionPollOff", func() {
	for _, value := range []string{"off", "OFF", "Off"} {
		It("TestKillSwitchSupervisorPermissionPollOff exits for "+value, func() {
			client := &recordingClient{}
			opts, stdout, _ := newHookOptions(
				nil,
				`{"session_id":"S"}`,
				map[string]string{"SUPERVISOR_PERMISSION_POLL": value},
				client,
			)
			Expect(run(context.Background(), opts)).To(Succeed())
			Expect(stdout.String()).To(BeEmpty())
			noStoreCalls(client)
		})
	}

	It(
		"TestKillSwitchSupervisorPermissionPollOff leaves the hook running for other values",
		func() {
			for _, value := range []string{"0", "false", "no", ""} {
				skip, _ := skipBeforeConnect(func(key string) string {
					if key == "SUPERVISOR_PERMISSION_POLL" {
						return value
					}
					return ""
				})
				Expect(skip).To(BeFalse(), "value %q must not be off", value)
			}
		},
	)
})

var _ = Describe("TestHeadlessWorkerSkipped", func() {
	It("TestHeadlessWorkerSkipped exits before any connection", func() {
		client := &recordingClient{}
		opts, stdout, _ := newHookOptions(
			nil,
			`{"session_id":"S"}`,
			map[string]string{"SUPERVISOR_WORKER_MODE": "headless"},
			client,
		)
		Expect(run(context.Background(), opts)).To(Succeed())
		Expect(stdout.String()).To(BeEmpty())
		noStoreCalls(client)
	})

	It("TestHeadlessWorkerSkipped leaves the hook running for other values", func() {
		for _, value := range []string{"interactive", "HEADLESS", ""} {
			skip, _ := skipBeforeConnect(func(key string) string {
				if key == "SUPERVISOR_WORKER_MODE" {
					return value
				}
				return ""
			})
			Expect(skip).To(BeFalse(), "value %q must not be headless", value)
		}
	})
})

var _ = Describe("fakeClock", func() {
	It("advances Now and fires a due timer", func() {
		start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		clock := newFakeClock(start)
		ch := clock.Timer(5 * time.Second)

		clock.Advance(4 * time.Second)
		select {
		case <-ch:
			Fail("the timer fired before it was due")
		default:
		}

		clock.Advance(time.Second)
		select {
		case <-ch:
		default:
			Fail("the timer did not fire once it was due")
		}
		Expect(clock.Now()).To(Equal(start.Add(5 * time.Second)))
	})
})
