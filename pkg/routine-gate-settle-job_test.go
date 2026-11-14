// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"encoding/json"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

// fakeWrapper is a scripted ClaudeWrapperClient.
type fakeWrapper struct {
	state      pkg.WrapperProbeState
	session    *pkg.WrapperSession
	verdict    pkg.DeliveryVerdict
	answered   []string
	probeCalls int
}

func (f *fakeWrapper) Probe(
	_ context.Context,
	_ string,
) (pkg.WrapperProbeState, *pkg.WrapperSession) {
	f.probeCalls++
	return f.state, f.session
}

func (f *fakeWrapper) AnswerViaWrapper(
	_ context.Context,
	sessionID string,
	decision string,
	_ *string,
) (pkg.DeliveryVerdict, string) {
	f.answered = append(f.answered, sessionID+":"+decision)
	return f.verdict, "scripted"
}

// fakePolicy is a scripted RoutinePolicy.
type fakePolicy struct {
	rule    string
	allowed bool
}

func (f *fakePolicy) RoutineRule(_ json.RawMessage, _ string) (string, bool) {
	return f.rule, f.allowed
}

// fakeSessions is a scripted RoutineGateSessionReader.
type fakeSessions struct {
	entry pkg.SessionEntry
	has   bool
	tty   string
}

func (f *fakeSessions) SessionEntry(_ context.Context, _ string) (pkg.SessionEntry, bool) {
	return f.entry, f.has
}

func (f *fakeSessions) SessionTTY(_ context.Context, _ string) string { return f.tty }

// permissionItem builds an open permission gate for a wrapper child.
func permissionItem(id string) pkg.Item {
	return pkg.Item{
		ItemID:          pkg.ItemID(id),
		LivenessRef:     pkg.LivenessRef("session:11111111-2222-3333-4444-555555555555"),
		State:           pkg.OpenState,
		AnswerMechanism: pkg.PermissionAnswerMechanism,
	}
}

var _ = Describe("routine gate settle job", func() {
	var (
		store    *mocks.AttentionStore
		wrapper  *fakeWrapper
		policy   *fakePolicy
		sessions *fakeSessions
		job      pkg.RoutineGateSettleJob
	)

	BeforeEach(func() {
		store = &mocks.AttentionStore{}
		store.RecordAttemptReturns(&pkg.DeliveryAttempt{}, nil)
		wrapper = &fakeWrapper{
			state: pkg.WrapperProbeOK,
			session: &pkg.WrapperSession{
				Question: &pkg.WrapperQuestion{
					RequestID: "req-1",
					Raw: []byte(
						`{"request_id":"req-1","input":{"arguments":{"tool_name":"Read","input":{"file_path":"/tmp/ws/a.go"}}}}`,
					),
				},
			},
			verdict: pkg.DeliveryOK,
		}
		policy = &fakePolicy{rule: "routine-tool:Read", allowed: true}
		sessions = &fakeSessions{
			entry: pkg.SessionEntry{PID: os.Getpid(), Entrypoint: "sdk-cli", CWD: "/tmp/ws"},
			has:   true,
		}
		job = pkg.NewRoutineGateSettleJob(store, wrapper, policy, sessions, time.Hour)
	})

	It("allows and delivers a routine Read gate", func() {
		store.ReadReturns(pkg.Items{permissionItem("item-1")}, nil)
		Expect(job.SettleOnce(context.Background())).To(Succeed())
		Expect(wrapper.answered).To(ConsistOf(
			"11111111-2222-3333-4444-555555555555:allow"))
		Expect(store.RecordAttemptCallCount()).To(Equal(1))
		_, _, _, outcome := store.RecordAttemptArgsForCall(0)
		Expect(outcome).To(Equal(pkg.DeliveredOutcome))
	})

	It("holds a gate the policy refuses, recording the rule", func() {
		policy = &fakePolicy{rule: "path-outside-workspace:Read", allowed: false}
		job = pkg.NewRoutineGateSettleJob(store, wrapper, policy, sessions, time.Hour)
		store.ReadReturns(pkg.Items{permissionItem("item-1")}, nil)
		Expect(job.SettleOnce(context.Background())).To(Succeed())
		Expect(wrapper.answered).To(BeEmpty())
		Expect(store.RecordAttemptCallCount()).To(Equal(1))
		_, _, carrier, outcome := store.RecordAttemptArgsForCall(0)
		Expect(carrier).To(Equal("path-outside-workspace:Read"))
		Expect(outcome).To(Equal(pkg.HeldOutcome))
	})

	It("skips a session that has a pane", func() {
		sessions.tty = "/dev/ttys004"
		store.ReadReturns(pkg.Items{permissionItem("item-1")}, nil)
		Expect(job.SettleOnce(context.Background())).To(Succeed())
		Expect(wrapper.probeCalls).To(Equal(0))
		Expect(wrapper.answered).To(BeEmpty())
	})

	It("skips a dead session", func() {
		sessions.entry = pkg.SessionEntry{PID: 0, Entrypoint: "sdk-cli", CWD: "/tmp/ws"}
		store.ReadReturns(pkg.Items{permissionItem("item-1")}, nil)
		Expect(job.SettleOnce(context.Background())).To(Succeed())
		Expect(wrapper.probeCalls).To(Equal(0))
	})

	It("skips a non-permission item", func() {
		item := permissionItem("item-1")
		item.AnswerMechanism = pkg.MessageAnswerMechanism
		store.ReadReturns(pkg.Items{item}, nil)
		Expect(job.SettleOnce(context.Background())).To(Succeed())
		Expect(wrapper.probeCalls).To(Equal(0))
	})

	It("records a failed attempt when the wrapper refuses", func() {
		wrapper.verdict = pkg.DeliveryRefuse
		store.ReadReturns(pkg.Items{permissionItem("item-1")}, nil)
		Expect(job.SettleOnce(context.Background())).To(Succeed())
		Expect(store.RecordAttemptCallCount()).To(Equal(1))
		_, _, _, outcome := store.RecordAttemptArgsForCall(0)
		Expect(outcome).To(Equal(pkg.FailedOutcome))
	})
})
