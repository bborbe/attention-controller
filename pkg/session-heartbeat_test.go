// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	stdtime "time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

// The heartbeat's load-bearing claims are two, and both are about a BOUNDARY
// rather than a happy path. First, 60 seconds is the line between Live and
// Stale, and it is one whole missed tick of a 30-second timer — so 59 reads
// Live and 61 reads Stale, and a test that only ever checked "a fresh row is
// fresh" would pass on an implementation with no window at all.
//
// Second, an ABSENT row and a STALE row are different answers. They are the
// two that a reader renders differently (`Resume` versus `Stale Nm`), so
// collapsing them is the defect these specs exist to catch.
var _ = Describe("Session heartbeat", func() {
	var ctx context.Context
	// heartbeatWindow is the store's own named constant — deliberately NOT the
	// supervisor's 15-minute HEARTBEAT_WINDOW, which governs a different probe
	// for a different class of producer.
	const heartbeatWindow = libtime.Duration(60 * stdtime.Second)

	// at is a fixed instant, so the boundary cases are placed rather than
	// raced against a wall clock.
	at := libtime.DateTime(stdtime.Date(2026, 10, 8, 9, 0, 0, 0, stdtime.UTC))

	// heartbeat builds a well-formed declaration whose At is `at`. Only the
	// fields a spec is about are varied.
	heartbeat := func() pkg.SessionHeartbeat {
		return pkg.SessionHeartbeat{
			SessionID: "5f2a1c34-0000-4000-8000-000000000001",
			Task:      "Session Liveness Comes From a Heartbeat Store in attention-controller",
			Vault:     "private-personal",
			Location:  pkg.LocalSessionHeartbeatLocation,
			State:     pkg.IdleSessionHeartbeatState,
			Source:    pkg.MCPTimerSessionHeartbeatSource,
			At:        at,
		}
	}

	// after returns `at` moved forward by d. libtime.DateTime.Add takes a
	// libtime.HasDuration, so the stdlib duration is wrapped once here rather
	// than at every call site — which keeps the boundary numbers themselves
	// readable, since they are the whole point of these specs.
	after := func(d stdtime.Duration) libtime.DateTime {
		return at.Add(libtime.Duration(d))
	}

	BeforeEach(func() {
		ctx = context.Background()
	})

	Describe("the Live/Stale boundary", func() {
		// The window is a parameter rather than a constant read inside IsFresh,
		// which is what lets these specs move the clock instead of sleeping.
		It("reads Live one second inside the window", func() {
			Expect(heartbeat().IsFresh(after(59*stdtime.Second), heartbeatWindow)).
				To(BeTrue())
		})

		It("reads Live exactly on the window, since the rule is ≤ and not <", func() {
			Expect(heartbeat().IsFresh(after(60*stdtime.Second), heartbeatWindow)).
				To(BeTrue())
		})

		It("reads Stale one second past the window", func() {
			Expect(heartbeat().IsFresh(after(61*stdtime.Second), heartbeatWindow)).
				To(BeFalse())
		})

		It("reads Stale when the process is alive but has stopped posting", func() {
			// The hung-process case: nothing about the row changed, only the
			// distance from it. A window measured from the row's own stamp is
			// what makes this detectable at all.
			Expect(heartbeat().IsFresh(after(10*stdtime.Minute), heartbeatWindow)).
				To(BeFalse())
		})

		It("reads Stale for a row stamped in the future", func() {
			// ⚠️ A negative age is <= any positive window, so the bare
			// comparison would report a future-dated row Live forever — the one
			// answer a liveness check must never produce from a nonsense input.
			Expect(heartbeat().IsFresh(after(-5*stdtime.Minute), heartbeatWindow)).
				To(BeFalse())
		})
	})

	Describe("the read view", func() {
		It("rounds the age up so it cannot contradict Live", func() {
			// ⚠️ 60.9 s is past the window but truncates to 60. A reader keying
			// on age_seconds would then see an in-window row that the same
			// struct marks Live:false — the disagreement the AgeSeconds doc
			// explicitly promises cannot happen.
			view := pkg.NewSessionHeartbeatView(
				heartbeat(), after(60900*stdtime.Millisecond), heartbeatWindow,
			)
			Expect(view.AgeSeconds).To(Equal(61))
			Expect(view.Live).To(BeFalse())
		})

		It("keeps age and verdict agreeing exactly on the window", func() {
			view := pkg.NewSessionHeartbeatView(
				heartbeat(), after(60*stdtime.Second), heartbeatWindow,
			)
			Expect(view.AgeSeconds).To(Equal(60))
			Expect(view.Live).To(BeTrue())
		})

		It("projects a whole listing in the order it was handed", func() {
			// The plural constructor is what the HTTP surface consumes directly,
			// so its ordering is a contract rather than an implementation detail.
			first := heartbeat()
			second := heartbeat()
			second.SessionID = "00000000-0000-4000-8000-000000000002"
			views := pkg.NewSessionHeartbeatViews(
				pkg.SessionHeartbeats{first, second}, at, heartbeatWindow,
			)
			Expect(views).To(HaveLen(2))
			Expect(views[0].SessionID).To(Equal(first.SessionID))
			Expect(views[1].SessionID).To(Equal(second.SessionID))
		})

		It("renders an empty listing as an empty NON-NIL slice, never null", func() {
			// ⚠️ `null` and `[]` are different answers on the wire. A consumer
			// that counts rows would have to special-case null, and one that
			// iterates would have to guard — so the empty listing must serialise
			// as `[]`, which the preallocated make guarantees.
			views := pkg.NewSessionHeartbeatViews(pkg.SessionHeartbeats{}, at, heartbeatWindow)
			Expect(views).NotTo(BeNil())
			Expect(views).To(BeEmpty())
		})

		It("carries a NEGATIVE age for a future-dated row, with live false", func() {
			// ⚠️ Documents the disagreement rather than pretending it cannot
			// happen: a row stamped ahead of the clock yields a negative age, and
			// `-300 <= window` reads as in-window to anyone testing the age. The
			// rule the field's comment states is that `Live` is authoritative
			// here — clamping to zero would NOT fix it, since `0 <= window` is
			// true as well. Pinned so a future change to either field has to
			// confront the pair.
			view := pkg.NewSessionHeartbeatView(
				heartbeat(), after(-5*stdtime.Minute), heartbeatWindow,
			)
			Expect(view.AgeSeconds).To(BeNumerically("<", 0))
			Expect(view.Live).To(BeFalse())
		})

		It("keeps a SUB-SECOND future stamp visible rather than rounding it to zero", func() {
			// ⚠️ The case a large negative value does not reach, and the one the
			// first version of this rounding got wrong. At -0.3 s, plain Ceil
			// yields -0 and int(-0.0) renders 0 — so the wire would carry
			// `age_seconds: 0` beside `live: false`, and the documented rule that
			// Live is authoritative *when the age is negative* could never fire,
			// because the rendered age is not negative. A reader testing
			// `age <= window` would be told "in window" about a row the verdict
			// already called dead. Rounding away from zero keeps the skew visible.
			view := pkg.NewSessionHeartbeatView(
				heartbeat(), after(-300*stdtime.Millisecond), heartbeatWindow,
			)
			Expect(view.AgeSeconds).To(BeNumerically("<", 0))
			Expect(view.Live).To(BeFalse())
		})
	})

	Describe("validation", func() {
		It("accepts a well-formed declaration", func() {
			Expect(heartbeat().Validate(ctx)).To(BeNil())
		})

		It("rejects a missing session id", func() {
			h := heartbeat()
			h.SessionID = ""
			Expect(h.Validate(ctx)).NotTo(BeNil())
		})

		It("rejects an unknown location", func() {
			h := heartbeat()
			h.Location = pkg.SessionHeartbeatLocation("moon")
			Expect(h.Validate(ctx)).NotTo(BeNil())
		})

		It("rejects an unknown state", func() {
			h := heartbeat()
			h.State = pkg.SessionHeartbeatState("napping")
			Expect(h.Validate(ctx)).NotTo(BeNil())
		})

		It("rejects an unknown source", func() {
			h := heartbeat()
			h.Source = pkg.SessionHeartbeatSource("guessed")
			Expect(h.Validate(ctx)).NotTo(BeNil())
		})

		It("rejects a task declared without its vault", func() {
			// A task name collides across vaults, so half an anchor cannot be
			// resolved later — it is refused rather than stored and guessed at.
			h := heartbeat()
			h.Vault = ""
			Expect(h.Validate(ctx)).NotTo(BeNil())
		})

		It("rejects a vault declared without its task", func() {
			h := heartbeat()
			h.Task = ""
			Expect(h.Validate(ctx)).NotTo(BeNil())
		})

		It("accepts a session with no anchor at all", func() {
			// Unanchored is a real state, not an error.
			h := heartbeat()
			h.Task = ""
			h.Vault = ""
			Expect(h.Validate(ctx)).To(BeNil())
		})

		It("does not judge At, which is the store's to write", func() {
			// A zero At must not reject the caller's declaration — the same
			// split PushRequest and Item carry.
			h := heartbeat()
			h.At = libtime.DateTime{}
			Expect(h.Validate(ctx)).To(BeNil())
		})
	})

	Describe("the source vocabulary", func() {
		// `manual` exists so its absence from the live set can be COUNTED, not
		// so it can be trusted. SC1 asserts `mcp-timer` for this reason.
		It("holds exactly the three sources the mechanism names", func() {
			Expect(pkg.AvailableSessionHeartbeatSources).To(ConsistOf(
				pkg.HookSessionHeartbeatSource,
				pkg.MCPTimerSessionHeartbeatSource,
				pkg.ManualSessionHeartbeatSource,
			))
		})
	})
})
