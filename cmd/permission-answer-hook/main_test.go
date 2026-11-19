// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	stderrors "errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	libtime "github.com/bborbe/time"
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
	items pkg.Items
	item  *pkg.Item
	// getResults is the sequence Get serves, last entry repeating. It is how a
	// spec distinguishes the catch-up read from an event read.
	getResults     []*pkg.Item
	listErr        error
	getErr         error
	attemptErr     error
	subscribeErr   error
	events         chan StreamEvent
	subscribed     chan struct{}
	listCalls      int
	getCalls       int
	attemptCalls   int
	subscribeCalls int
	attempts       []recordedAttempt
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
	if len(c.getResults) == 0 {
		return c.item, nil
	}
	next := c.getResults[0]
	if len(c.getResults) > 1 {
		c.getResults = c.getResults[1:]
	}
	return next, nil
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

// Subscribe records the call and hands out the spec's event channel.
func (c *recordingClient) Subscribe(_ context.Context) (Subscription, error) {
	c.subscribeCalls++
	if c.subscribed != nil {
		select {
		case c.subscribed <- struct{}{}:
		default:
		}
	}
	if c.subscribeErr != nil {
		return nil, c.subscribeErr
	}
	if c.events == nil {
		c.events = make(chan StreamEvent, 8)
	}
	return &recordingSubscription{events: c.events}, nil
}

// recordingSubscription is the fake stream the settle specs feed.
type recordingSubscription struct {
	events chan StreamEvent
}

func (s *recordingSubscription) Events() <-chan StreamEvent { return s.events }

func (s *recordingSubscription) Close() {}

// fakeTimer is one timer armed on the fake clock.
type fakeTimer struct {
	due time.Time
	ch  chan time.Time
}

// fakeClock is the hook's time source in a spec. Advance moves Now and fires
// every timer that becomes due, including one re-armed during the advance.
type fakeClock struct {
	mu     sync.Mutex
	cond   *sync.Cond
	now    time.Time
	timers []*fakeTimer
	// armed counts every timer ever armed, so a spec can order an Advance after
	// the loop has re-armed without sleeping.
	armed int
}

// newFakeClock creates a fake clock at the given instant.
func newFakeClock(now time.Time) *fakeClock {
	clock := &fakeClock{now: now}
	clock.cond = sync.NewCond(&clock.mu)
	return clock
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
	timer := &fakeTimer{due: c.now.Add(d), ch: make(chan time.Time, 1)}
	c.timers = append(c.timers, timer)
	c.armed++
	c.cond.Broadcast()
	c.mu.Unlock()
	return timer.ch
}

// awaitArms blocks until at least n timers have been armed on this clock.
func (c *fakeClock) awaitArms(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for c.armed < n {
		c.cond.Wait()
	}
}

// armedCount returns how many timers have been armed in total. A loop that
// leaves a closed channel in its select arms one per iteration, so this is how a
// spec sees a busy-spin that still terminates.
func (c *fakeClock) armedCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.armed
}

// ticker drives a loop's one-shot timers one interval at a time, waiting for
// the loop to re-arm between ticks. No sleep, so a re-armed timer is never
// missed by an advance that ran ahead of it.
type ticker struct {
	clock *fakeClock
	step  time.Duration
	armed int
}

// newTicker creates a ticker stepping by step.
func newTicker(clock *fakeClock, step time.Duration) *ticker {
	return &ticker{clock: clock, step: step}
}

// tick advances one interval, after the loop has armed the next timer.
func (t *ticker) tick() {
	t.armed++
	t.clock.awaitArms(t.armed)
	t.clock.Advance(t.step)
}

// awaitNext blocks until the loop has re-armed after the last tick, which is
// the point at which that tick has been fully processed. It is what orders a
// file mutation between two ticks.
func (t *ticker) awaitNext() {
	t.clock.awaitArms(t.armed + 1)
}

// ticks advances n intervals.
func (t *ticker) ticks(n int) {
	for i := 0; i < n; i++ {
		t.tick()
	}
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
		Clock:  newFakeClock(selectionNotBefore),
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

// selectionNotBefore is the fixed instant every selection spec treats as the
// hook's start. Fixed, never a live clock, so each case names its own gap.
var selectionNotBefore = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

// newItemFixture builds a pkg.Item with every field Item.Validate requires
// populated, so a case overrides only the fields it cares about.
func newItemFixture(mutate func(*pkg.Item)) pkg.Item {
	item := pkg.Item{
		ItemID:          pkg.ItemID("item-1"),
		ProducerID:      pkg.ProducerID("session-1"),
		ProducerKind:    pkg.SessionProducerKind,
		LivenessRef:     pkg.LivenessRef("session:session-1"),
		DedupKey:        pkg.DedupKey("dedup-1"),
		InterruptClass:  pkg.InterruptClass("approve"),
		Payload:         pkg.Payload("Write: /tmp/w.txt — create the file"),
		AnswerMechanism: pkg.PermissionAnswerMechanism,
		State:           pkg.OpenState,
		CreatedAt:       libtime.DateTime(selectionNotBefore),
	}
	if mutate != nil {
		mutate(&item)
	}
	return item
}

var _ = Describe("TestPermissionClassOnly", func() {
	It("TestPermissionClassOnly refuses a message-class card for the session", func() {
		item := newItemFixture(func(i *pkg.Item) {
			i.AnswerMechanism = pkg.MessageAnswerMechanism
		})
		selected, reason := selectItem(
			pkg.Items{item}, "session-1", "Write: /tmp/w.txt", selectionNotBefore,
		)
		Expect(selected.ItemID).To(BeEmpty())
		Expect(reason).ToNot(BeEmpty())
	})

	It("TestPermissionClassOnly selects the same card once it is permission-class", func() {
		item := newItemFixture(nil)
		selected, reason := selectItem(
			pkg.Items{item}, "session-1", "Write: /tmp/w.txt", selectionNotBefore,
		)
		Expect(reason).To(BeEmpty())
		Expect(selected.ItemID).To(Equal(pkg.ItemID("item-1")))
	})
})

var _ = Describe("TestTwoPermissionCardsAreAmbiguous", func() {
	twoCards := func() pkg.Items {
		return pkg.Items{
			newItemFixture(func(i *pkg.Item) { i.DedupKey = pkg.DedupKey("dedup-1") }),
			newItemFixture(func(i *pkg.Item) {
				i.ItemID = pkg.ItemID("item-2")
				i.DedupKey = pkg.DedupKey("dedup-2")
			}),
		}
	}

	It("TestTwoPermissionCardsAreAmbiguous settles neither card", func() {
		selected, reason := selectItem(
			twoCards(), "session-1", "Write: /tmp/w.txt", selectionNotBefore,
		)
		Expect(selected.ItemID).To(BeEmpty())
		Expect(reason).To(ContainSubstring("ambiguous"))
		Expect(reason).To(ContainSubstring("2"))
	})

	It("TestTwoPermissionCardsAreAmbiguous selects the survivor once one is dropped", func() {
		selected, reason := selectItem(
			twoCards()[:1], "session-1", "Write: /tmp/w.txt", selectionNotBefore,
		)
		Expect(reason).To(BeEmpty())
		Expect(selected.ItemID).To(Equal(pkg.ItemID("item-1")))
	})
})

var _ = Describe("TestStaleCardForAnotherGateIsRefused", func() {
	It("TestStaleCardForAnotherGateIsRefused refuses a payload from another gate", func() {
		item := newItemFixture(func(i *pkg.Item) {
			i.Payload = pkg.Payload("Bash: rm -rf /tmp/x")
		})
		selected, reason := selectItem(
			pkg.Items{item}, "session-1", "Write: /tmp/w.txt", selectionNotBefore,
		)
		Expect(selected.ItemID).To(BeEmpty())
		Expect(reason).ToNot(BeEmpty())
	})

	It("TestStaleCardForAnotherGateIsRefused selects a payload carrying the prefix", func() {
		item := newItemFixture(nil)
		selected, reason := selectItem(
			pkg.Items{item}, "session-1", "Write: /tmp/w.txt", selectionNotBefore,
		)
		Expect(reason).To(BeEmpty())
		Expect(selected.ItemID).To(Equal(pkg.ItemID("item-1")))
	})

	It("TestStaleCardForAnotherGateIsRefused is inactive when no prefix was carried", func() {
		item := newItemFixture(func(i *pkg.Item) {
			i.Payload = pkg.Payload("Bash: rm -rf /tmp/x")
		})
		selected, reason := selectItem(pkg.Items{item}, "session-1", "", selectionNotBefore)
		Expect(reason).To(BeEmpty())
		Expect(selected.ItemID).To(Equal(pkg.ItemID("item-1")))
	})
})

var _ = Describe("TestCardOlderThanTheHookIsRefused", func() {
	It("TestCardOlderThanTheHookIsRefused refuses a card from 30s before the hook", func() {
		item := newItemFixture(func(i *pkg.Item) {
			i.CreatedAt = libtime.DateTime(selectionNotBefore.Add(-30 * time.Second))
		})
		selected, reason := selectItem(
			pkg.Items{item}, "session-1", "Write: /tmp/w.txt", selectionNotBefore,
		)
		Expect(selected.ItemID).To(BeEmpty())
		Expect(reason).ToNot(BeEmpty())
	})

	It("TestCardOlderThanTheHookIsRefused selects a card exactly at the tolerance", func() {
		item := newItemFixture(func(i *pkg.Item) {
			i.CreatedAt = libtime.DateTime(selectionNotBefore.Add(-notBeforeTolerance))
		})
		selected, reason := selectItem(
			pkg.Items{item}, "session-1", "Write: /tmp/w.txt", selectionNotBefore,
		)
		Expect(reason).To(BeEmpty())
		Expect(selected.ItemID).To(Equal(pkg.ItemID("item-1")))
	})

	It("TestCardOlderThanTheHookIsRefused refuses a card one second past the tolerance", func() {
		item := newItemFixture(func(i *pkg.Item) {
			i.CreatedAt = libtime.DateTime(
				selectionNotBefore.Add(-notBeforeTolerance - time.Second),
			)
		})
		selected, reason := selectItem(
			pkg.Items{item}, "session-1", "Write: /tmp/w.txt", selectionNotBefore,
		)
		Expect(selected.ItemID).To(BeEmpty())
		Expect(reason).ToNot(BeEmpty())
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

// settleFixture is everything one settle spec drives.
type settleFixture struct {
	opts   hookOptions
	clock  *fakeClock
	dir    string
	stdout *bytes.Buffer
	stderr *bytes.Buffer
}

// newSettleFixture builds options for a spec that drives the settle loop: a
// temp ATTENTION_STATE_DIR (without one the feed-grace branch exits before the
// store can settle), a fake clock and a short real deadline so a spec that never
// settles fails in seconds instead of at the hook's real 540s.
func newSettleFixture(client *recordingClient) settleFixture {
	dir := GinkgoT().TempDir()
	opts, stdout, stderr := newHookOptions(nil, "", map[string]string{
		"ATTENTION_STATE_DIR": dir,
	}, client)
	clock, ok := opts.Clock.(*fakeClock)
	Expect(ok).To(BeTrue())
	opts.Deadline = 2 * time.Second
	return settleFixture{opts: opts, clock: clock, dir: dir, stdout: stdout, stderr: stderr}
}

// answeredCard is the card as settled with allow.
func answeredCard() *pkg.Item {
	item := newItemFixture(func(i *pkg.Item) {
		i.State = pkg.AnsweredState
		i.Decision = pkg.AllowDecision
		i.ResolvedBy = "session-1"
	})
	return &item
}

// openCard is the card as still open.
func openCard() *pkg.Item {
	item := newItemFixture(nil)
	return &item
}

// writeOpenMarker writes the producer's open-gate marker for a session and
// returns its path.
func writeOpenMarker(dir, sessionID, content string) string {
	path := filepath.Join(dir, sessionID+openItemSuffix)
	Expect(os.WriteFile(path, []byte(content), 0o600)).To(Succeed())
	return path
}

// startSettle runs settle in the background and returns its result channel.
func startSettle(
	ctx context.Context,
	opts hookOptions,
	item pkg.Item,
	started time.Time,
) <-chan settleResult {
	done := make(chan settleResult, 1)
	go func() { done <- settle(ctx, opts, item, started) }()
	return done
}

var _ = Describe("TestOneSubscriptionNoListRepoll", func() {
	It("TestOneSubscriptionNoListRepoll decides from one matching event", func() {
		client := &recordingClient{
			getResults: []*pkg.Item{openCard(), answeredCard()},
			events:     make(chan StreamEvent, 8),
		}
		client.events <- StreamEvent{Type: "upsert", ItemID: "item-1"}
		fixture := newSettleFixture(client)

		result := settle(
			context.Background(),
			fixture.opts,
			newItemFixture(nil),
			fixture.clock.Now(),
		)
		Expect(result.Decision).To(Equal(pkg.AllowDecision))
		Expect(result.ItemID).To(Equal(pkg.ItemID("item-1")))
		Expect(client.subscribeCalls).To(Equal(1))
		// Zero list reads inside settle: only the resolve reads the list.
		Expect(client.listCalls).To(Equal(0))
		// One catch-up read, then exactly one read for the event.
		Expect(client.getCalls).To(Equal(2))
	})

	It("TestOneSubscriptionNoListRepoll reads the list only in the resolve", func() {
		client := &recordingClient{
			items:      pkg.Items{newItemFixture(nil)},
			getResults: []*pkg.Item{openCard(), answeredCard()},
			events:     make(chan StreamEvent, 8),
		}
		client.events <- StreamEvent{Type: "upsert", ItemID: "item-1"}
		fixture := newSettleFixture(client)
		fixture.opts.Stdin = strings.NewReader(`{"session_id":"session-1"}`)

		Expect(run(context.Background(), fixture.opts)).To(Succeed())
		// The whole invocation read the list once — the resolve — and never again.
		Expect(client.listCalls).To(Equal(1))
		Expect(client.subscribeCalls).To(Equal(1))
		Expect(client.getCalls).To(Equal(2))
		Expect(fixture.stderr.String()).To(BeEmpty())
	})

	It("TestOneSubscriptionNoListRepoll does not re-subscribe when the stream closes", func() {
		client := &recordingClient{
			items:  pkg.Items{newItemFixture(nil)},
			item:   openCard(),
			events: make(chan StreamEvent),
		}
		close(client.events)
		fixture := newSettleFixture(client)
		fixture.opts.Stdin = strings.NewReader(`{"session_id":"session-1"}`)
		fixture.opts.Deadline = 50 * time.Millisecond

		Expect(run(context.Background(), fixture.opts)).To(Succeed())
		Expect(client.subscribeCalls).To(Equal(1))
		Expect(fixture.stderr.String()).To(ContainSubstring("deadline after"))
	})
})

var _ = Describe("TestCatchUpReadDecidesWhenAnswerLandedInBaselineGap", func() {
	It(
		"TestCatchUpReadDecidesWhenAnswerLandedInBaselineGap decides from the catch-up read",
		func() {
			// The card is answered before the subscription is established and NO
			// stream event is ever emitted for it.
			client := &recordingClient{
				getResults: []*pkg.Item{answeredCard()},
				events:     make(chan StreamEvent),
			}
			fixture := newSettleFixture(client)
			writeOpenMarker(fixture.dir, "session-1", `{"item_id":"item-1"}`)

			result := settle(
				context.Background(),
				fixture.opts,
				newItemFixture(nil),
				fixture.clock.Now(),
			)
			// The OUTCOME, not a call count: a discarded read would pass a counter.
			Expect(result.Decision).To(Equal(pkg.AllowDecision))
			Expect(result.ItemID).To(Equal(pkg.ItemID("item-1")))
			Expect(result.Reason).To(BeEmpty())
			Expect(result.Deadline).To(BeFalse())
			Expect(client.getCalls).To(Equal(1))
		},
	)
})

var _ = Describe("TestResolveWindowExpiresQuietly", func() {
	It("TestResolveWindowExpiresQuietly returns a reason naming the producer", func() {
		client := &recordingClient{items: pkg.Items{}}
		fixture := newSettleFixture(client)

		type outcome struct {
			item   pkg.Item
			reason string
		}
		done := make(chan outcome, 1)
		go func() {
			item, reason := resolveItem(
				context.Background(),
				fixture.opts.Client,
				fixture.clock,
				"session-1",
				"",
				fixture.clock.Now(),
			)
			done <- outcome{item: item, reason: reason}
		}()
		newTicker(fixture.clock, resolveRetryInterval).
			ticks(int(ResolveGrace / resolveRetryInterval))

		var got outcome
		Eventually(done).Should(Receive(&got))
		Expect(got.item.ItemID).To(BeEmpty())
		Expect(got.reason).To(ContainSubstring("session-1"))
		// Exactly ResolveGrace elapsed, and nothing was subscribed.
		Expect(fixture.clock.Now()).To(Equal(selectionNotBefore.Add(ResolveGrace)))
		Expect(client.subscribeCalls).To(Equal(0))
		Expect(client.getCalls).To(Equal(0))
	})
})

var _ = Describe("TestFirstAnswerWinsOverInPane", func() {
	It("TestFirstAnswerWinsOverInPane returns a local close when the gate goes away", func() {
		client := &recordingClient{item: openCard(), events: make(chan StreamEvent, 8)}
		fixture := newSettleFixture(client)
		marker := writeOpenMarker(fixture.dir, "session-1", `{"item_id":"item-1"}`)
		done := startSettle(
			context.Background(),
			fixture.opts,
			newItemFixture(nil),
			fixture.clock.Now(),
		)
		ticker := newTicker(fixture.clock, stateCheckInterval)
		ticker.tick()      // the file is present: seenOpen becomes true
		ticker.awaitNext() // that tick is fully processed before the file moves
		Expect(os.Remove(marker)).To(Succeed())
		ticker.tick() // the file is gone: the gate closed locally

		var result settleResult
		Eventually(done).Should(Receive(&result))
		Expect(result.Decision).To(BeEmpty())
		Expect(result.Reason).To(ContainSubstring("closed locally"))
		Expect(result.Deadline).To(BeFalse())
		Expect(client.attemptCalls).To(Equal(0))
	})

	It(
		"TestFirstAnswerWinsOverInPane still decides from an event while the file is present",
		func() {
			client := &recordingClient{
				getResults: []*pkg.Item{openCard(), answeredCard()},
				events:     make(chan StreamEvent, 8),
			}
			fixture := newSettleFixture(client)
			writeOpenMarker(fixture.dir, "session-1", `{"item_id":"item-1"}`)
			done := startSettle(
				context.Background(),
				fixture.opts,
				newItemFixture(nil),
				fixture.clock.Now(),
			)
			ticker := newTicker(fixture.clock, stateCheckInterval)
			ticker.tick()
			ticker.awaitNext() // seenOpen is true before the event lands
			client.events <- StreamEvent{Type: "upsert", ItemID: "item-1"}

			var result settleResult
			Eventually(done).Should(Receive(&result))
			Expect(result.Decision).To(Equal(pkg.AllowDecision))
			Expect(result.Reason).To(BeEmpty())
		},
	)

	It(
		"TestFirstAnswerWinsOverInPane suppresses a store decision when the gate went away",
		func() {
			// The store event and the in-pane close land close together: the card
			// was seen open, the file is gone, and the store still carries an answer.
			client := &recordingClient{
				getResults: []*pkg.Item{openCard(), answeredCard()},
				events:     make(chan StreamEvent, 8),
			}
			fixture := newSettleFixture(client)
			marker := writeOpenMarker(fixture.dir, "session-1", `{"item_id":"item-1"}`)
			done := startSettle(
				context.Background(),
				fixture.opts,
				newItemFixture(nil),
				fixture.clock.Now(),
			)
			ticker := newTicker(fixture.clock, stateCheckInterval)
			ticker.tick()
			ticker.awaitNext()
			Expect(os.Remove(marker)).To(Succeed())
			client.events <- StreamEvent{Type: "upsert", ItemID: "item-1"}

			var result settleResult
			Eventually(done).Should(Receive(&result))
			Expect(result.Decision).To(BeEmpty())
			Expect(result.Reason).To(Equal(localCloseReason))
		},
	)
})

var _ = Describe("TestUntrackedGateAbandonedAfterGrace", func() {
	It("TestUntrackedGateAbandonedAfterGrace abandons a gate the feed never tracks", func() {
		client := &recordingClient{item: openCard(), events: make(chan StreamEvent, 8)}
		fixture := newSettleFixture(client)
		started := fixture.clock.Now()
		done := startSettle(context.Background(), fixture.opts, newItemFixture(nil), started)

		newTicker(fixture.clock, stateCheckInterval).
			ticks(int(FeedGrace / stateCheckInterval))

		var result settleResult
		Eventually(done).Should(Receive(&result))
		Expect(result.Decision).To(BeEmpty())
		Expect(result.Reason).To(ContainSubstring("feed"))
		Expect(result.Reason).ToNot(ContainSubstring("deadline"))
		Expect(result.Deadline).To(BeFalse())
		// Exactly FeedGrace, not the deadline.
		Expect(fixture.clock.Now()).To(Equal(started.Add(FeedGrace)))
	})
})

var _ = Describe("TestLocalCloseReadsProducerStateFile", func() {
	It(
		"TestLocalCloseReadsProducerStateFile sees a local close through the real state dir",
		func() {
			client := &recordingClient{item: openCard(), events: make(chan StreamEvent, 8)}
			fixture := newSettleFixture(client)
			Expect(fixture.dir).ToNot(BeEmpty())
			marker := writeOpenMarker(
				fixture.dir,
				"session-1",
				`{"item_id":"item-1","session_id":"session-1"}`,
			)
			done := startSettle(
				context.Background(),
				fixture.opts,
				newItemFixture(nil),
				fixture.clock.Now(),
			)
			ticker := newTicker(fixture.clock, stateCheckInterval)
			ticker.tick()
			ticker.awaitNext()
			Expect(os.Remove(marker)).To(Succeed())
			ticker.tick()

			var result settleResult
			Eventually(done).Should(Receive(&result))
			Expect(result.Decision).To(BeEmpty())
			Expect(result.Reason).To(Equal(localCloseReason))
		},
	)

	It("TestLocalCloseReadsProducerStateFile keeps waiting on an unreadable state file", func() {
		client := &recordingClient{item: openCard(), events: make(chan StreamEvent, 8)}
		fixture := newSettleFixture(client)
		writeOpenMarker(fixture.dir, "session-1", "not json at all")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := startSettle(ctx, fixture.opts, newItemFixture(nil), fixture.clock.Now())

		newTicker(fixture.clock, stateCheckInterval).ticks(3)
		// Fail open: an unreadable file is not a close, so the loop keeps waiting.
		Consistently(done, 100*time.Millisecond).ShouldNot(Receive())

		cancel()
		var result settleResult
		Eventually(done).Should(Receive(&result))
		Expect(result.Decision).To(BeEmpty())
		Expect(result.Reason).ToNot(Equal(localCloseReason))
	})
})

var _ = Describe("TestFailOpenOnErrorMalformedDeadline", func() {
	It("TestFailOpenOnErrorMalformedDeadline reports a subscribe error and exits zero", func() {
		client := &recordingClient{
			items:        pkg.Items{newItemFixture(nil)},
			subscribeErr: stderrors.New("board down"),
		}
		fixture := newSettleFixture(client)
		fixture.opts.Stdin = strings.NewReader(`{"session_id":"session-1"}`)

		Expect(run(context.Background(), fixture.opts)).To(Succeed())
		Expect(fixture.stdout.String()).To(BeEmpty())
		Expect(fixture.stderr.String()).To(ContainSubstring("subscribe failed"))
		Expect(fixture.stderr.String()).To(ContainSubstring("board down"))
	})

	It("TestFailOpenOnErrorMalformedDeadline names the failed condition for an empty item", func() {
		client := &recordingClient{
			items:      pkg.Items{newItemFixture(nil)},
			getResults: []*pkg.Item{{}},
			events:     make(chan StreamEvent, 8),
		}
		fixture := newSettleFixture(client)
		fixture.opts.Stdin = strings.NewReader(`{"session_id":"session-1"}`)
		fixture.opts.Deadline = 50 * time.Millisecond

		Expect(run(context.Background(), fixture.opts)).To(Succeed())
		Expect(fixture.stdout.String()).To(BeEmpty())
		Expect(fixture.stderr.String()).To(ContainSubstring("state"))
		Expect(fixture.stderr.String()).To(ContainSubstring("deadline after"))
	})

	It("TestFailOpenOnErrorMalformedDeadline prints the terminal line at the deadline", func() {
		client := &recordingClient{
			items:  pkg.Items{newItemFixture(nil)},
			item:   openCard(),
			events: make(chan StreamEvent, 8),
		}
		fixture := newSettleFixture(client)
		fixture.opts.Stdin = strings.NewReader(`{"session_id":"session-1"}`)
		// A REAL 50ms deadline: the fake clock is frozen, so only a real context
		// timeout can end this run.
		fixture.opts.Deadline = 50 * time.Millisecond

		Expect(run(context.Background(), fixture.opts)).To(Succeed())
		Expect(fixture.stdout.String()).To(BeEmpty())
		Expect(fixture.stderr.String()).
			To(ContainSubstring("permission-answer-hook: deadline after"))
	})

	It("TestFailOpenOnErrorMalformedDeadline renders 540s in seconds, not minutes", func() {
		Expect(deadlineLine(HookDeadline, "no answer")).
			To(Equal("permission-answer-hook: deadline after 540s — last: no answer"))
	})

	It(
		"TestFailOpenOnErrorMalformedDeadline settles an event arriving before the deadline",
		func() {
			client := &recordingClient{
				items:      pkg.Items{newItemFixture(nil)},
				getResults: []*pkg.Item{openCard(), answeredCard()},
				events:     make(chan StreamEvent, 8),
			}
			client.events <- StreamEvent{Type: "upsert", ItemID: "item-1"}
			fixture := newSettleFixture(client)
			fixture.opts.Stdin = strings.NewReader(`{"session_id":"session-1"}`)

			Expect(run(context.Background(), fixture.opts)).To(Succeed())
			Expect(fixture.stderr.String()).To(BeEmpty())
			Expect(client.getCalls).To(Equal(2))
		},
	)
})

var _ = Describe("TestStreamFrameParsed", func() {
	It("TestStreamFrameParsed decodes the wire frame into a StreamEvent", func() {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`data: {"type":"upsert","item_id":"X"}` + "\n\n"))
			http.NewResponseController(w).Flush()
		}))
		defer server.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		subscription, err := newHTTPStoreClient(server.URL).Subscribe(ctx)
		Expect(err).ToNot(HaveOccurred())
		defer subscription.Close()

		var event StreamEvent
		Eventually(subscription.Events()).Should(Receive(&event))
		Expect(event).To(Equal(StreamEvent{Type: "upsert", ItemID: "X"}))
	})
})

var _ = Describe("TestDroppedStreamRunsToDeadline", func() {
	It("TestDroppedStreamRunsToDeadline runs to the deadline without re-subscribing", func() {
		client := &recordingClient{
			items:      pkg.Items{newItemFixture(nil)},
			item:       openCard(),
			events:     make(chan StreamEvent),
			subscribed: make(chan struct{}, 1),
		}
		fixture := newSettleFixture(client)
		fixture.opts.Stdin = strings.NewReader(`{"session_id":"session-1"}`)
		fixture.opts.Deadline = 50 * time.Millisecond

		done := make(chan error, 1)
		go func() { done <- run(context.Background(), fixture.opts) }()
		Eventually(client.subscribed).Should(Receive())
		close(client.events)
		Eventually(done).Should(Receive(BeNil()))

		// Exactly one Subscribe for the whole run: a re-subscribe loop would
		// climb, and it is the poll under another name.
		Expect(client.subscribeCalls).To(Equal(1))
		// Only the catch-up read; the dropped stream never read again.
		Expect(client.getCalls).To(Equal(1))
		Expect(fixture.stderr.String()).To(ContainSubstring("deadline after"))
		// Two timers: one armed before the close was seen, one after. A loop
		// that left the closed channel in its select arms one per iteration and
		// spins until the deadline, which is the busy-wait this guards.
		Expect(fixture.clock.armedCount()).To(BeNumerically("<=", 5))
	})
})
