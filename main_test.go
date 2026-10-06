// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/bborbe/argument/v2"
	libboltkv "github.com/bborbe/boltkv"
	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	"github.com/bborbe/run"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

// recordingRunner counts Add calls. It is hand-written rather than
// counterfeiter-generated because run.ConcurrentRunner is an interface this
// repo does not own and mocks/ is regenerated from directives beside the
// interfaces in pkg/ — the whole of what this double needs to do is count.
type recordingRunner struct {
	added int
}

func (r *recordingRunner) Add(_ context.Context, _ run.Func) { r.added++ }

func (r *recordingRunner) Run(_ context.Context) error { return nil }

func (r *recordingRunner) Close() error { return nil }

var _ = Describe("runAnsweredSweep", func() {
	var ctx context.Context
	var store *mocks.AttentionStore

	BeforeEach(func() {
		ctx = context.Background()
		store = &mocks.AttentionStore{}
		store.SweepAnsweredReturns(0, nil)
	})

	It("returns on cancellation without sweeping", func() {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()

		// ⚠️ A long interval, not a short one, and the assertion is why: this spec
		// proves that cancellation returns nil WITHOUT sweeping, and a 1 ms ticker
		// races it — ctx.Done() is ready immediately but ticker.C becomes ready a
		// millisecond after NewTicker, so a goroutine descheduled past that boundary
		// leaves Go choosing between two ready cases at random and the zero-call
		// assertion fails on a loaded runner. A second keeps the tick branch
		// unreachable for the whole spec without weakening what it tests.
		sweep := runAnsweredSweep(store, libtime.Duration(time.Hour), time.Second)
		Expect(sweep(cancelled)).To(Succeed())
		Expect(store.SweepAnsweredCallCount()).To(Equal(0))
	})

	It("keeps ticking after a failed sweep instead of returning the error", func() {
		// ⚠️ A failed sweep costs decode work; it does not lose an item. Returning
		// the error would take the process down through the shared runner and drop
		// the board over something the board survives, so the loop must swallow it
		// and try again on the next tick.
		store.SweepAnsweredReturnsOnCall(0, 0, errors.New(ctx, "sweep exploded"))

		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			done <- runAnsweredSweep(store, libtime.Duration(time.Hour), time.Millisecond)(runCtx)
		}()

		// Two calls is the assertion that matters: the first failed and the loop
		// came back for a second, rather than unwinding with the error.
		Eventually(store.SweepAnsweredCallCount, "2s").Should(BeNumerically(">=", 2))
		cancel()
		Eventually(done, "2s").Should(Receive(BeNil()))
	})

	It("hands each sweep its own deadline", func() {
		// ⚠️ The deadline IS the mechanism the per-tick bound installs, and nothing
		// else in this file can notice its absence: the mock ignores the context, so
		// a regression back to a bare `ctx` would leave every spec here green while
		// silently re-disabling the bound the change exists to add.
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			done <- runAnsweredSweep(store, libtime.Duration(time.Hour), time.Millisecond)(runCtx)
		}()

		Eventually(store.SweepAnsweredCallCount, "2s").Should(BeNumerically(">=", 1))
		// The counterfeiter ArgsForCall returns the arguments as a tuple, not a
		// struct, so the context is taken positionally.
		callCtx, _ := store.SweepAnsweredArgsForCall(0)
		_, hasDeadline := callCtx.Deadline()
		Expect(hasDeadline).To(BeTrue(), "every tick must carry its own deadline")
		cancel()
		Eventually(done, "2s").Should(Receive(BeNil()))
	})

	It("keeps ticking after a sweep that closed items", func() {
		// The closed > 0 branch is the one path the other specs never reach — both
		// keep the count at zero — so the line that reports what a sweep did is
		// otherwise uncovered.
		store.SweepAnsweredReturnsOnCall(0, 3, nil)

		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			done <- runAnsweredSweep(store, libtime.Duration(time.Hour), time.Millisecond)(runCtx)
		}()

		Eventually(store.SweepAnsweredCallCount, "2s").Should(BeNumerically(">=", 2))
		cancel()
		Eventually(done, "2s").Should(Receive(BeNil()))
	})
})

var _ = Describe("parseAnsweredMaxAge", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("accepts a positive value inside the ceiling", func() {
		got, err := parseAnsweredMaxAge(ctx, "1h")
		Expect(err).To(BeNil())
		Expect(time.Duration(got)).To(Equal(time.Hour))
	})

	It("rejects zero", func() {
		// ⚠️ The highest-consequence branch in this change. The sweep computes its
		// cutoff as now minus maxAge, so a zero makes EVERY answered item due on the
		// first tick and closes the whole backlog at once — destroying every verdict
		// a consumer had not read yet, which is the outcome the bound exists to
		// prevent.
		_, err := parseAnsweredMaxAge(ctx, "0s")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("must be positive"))
	})

	It("rejects a negative value", func() {
		_, err := parseAnsweredMaxAge(ctx, "-1h")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("must be positive"))
	})

	It("rejects a value above the ceiling, which would reinstate the unbounded index", func() {
		_, err := parseAnsweredMaxAge(ctx, "8760h")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("exceeds"))
	})

	It("rejects an unparseable value", func() {
		_, err := parseAnsweredMaxAge(ctx, "not-a-duration")
		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("addAttentionStoreAPIListener", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var runner *recordingRunner

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

		sessionLivenessChecker := &mocks.SessionLivenessChecker{}
		sessionLivenessChecker.IsLiveReturns(true)

		store = pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			sessionLivenessChecker,
			libtime.NewCurrentDateTime(),
			libtime.Duration(15*60*1e9),
		)
		runner = &recordingRunner{}
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	It("disables the listener and adds nothing when the address is empty", func() {
		app := &application{AttentionStoreListen: "", AttentionStoreToken: "s3cret-token"}
		Expect(app.addAttentionStoreAPIListener(ctx, runner, store)).To(BeNil())
		Expect(runner.added).To(Equal(0))
	})

	It("disables the listener and adds nothing when the token is empty", func() {
		app := &application{AttentionStoreListen: "127.0.0.1:0", AttentionStoreToken: ""}
		Expect(app.addAttentionStoreAPIListener(ctx, runner, store)).To(BeNil())
		Expect(runner.added).To(Equal(0))
	})

	It("disables the listener and adds nothing when both are empty", func() {
		app := &application{AttentionStoreListen: "", AttentionStoreToken: ""}
		Expect(app.addAttentionStoreAPIListener(ctx, runner, store)).To(BeNil())
		Expect(runner.added).To(Equal(0))
	})

	It("adds exactly one listener when the address and token are set", func() {
		app := &application{
			AttentionStoreListen: "127.0.0.1:0",
			AttentionStoreToken:  "s3cret-token",
		}
		Expect(app.addAttentionStoreAPIListener(ctx, runner, store)).To(BeNil())
		Expect(runner.added).To(Equal(1))
	})
})

var _ = Describe("createAttentionStoreAPIHandler", func() {
	var ctx context.Context
	var db libkv.DB
	var httpHandler http.Handler

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())

		sessionLivenessChecker := &mocks.SessionLivenessChecker{}
		sessionLivenessChecker.IsLiveReturns(true)

		store := pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			sessionLivenessChecker,
			libtime.NewCurrentDateTime(),
			libtime.Duration(15*60*1e9),
		)
		httpHandler = (&application{}).createAttentionStoreAPIHandler(store, "s3cret-token")
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	It("rejects a business route without the token", func() {
		req := httptest.NewRequest(http.MethodGet, "/api/1.0/attention", nil)
		rec := httptest.NewRecorder()
		httpHandler.ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusUnauthorized))
	})

	It("serves a business route with the token", func() {
		req := httptest.NewRequest(http.MethodGet, "/api/1.0/attention", nil)
		req.Header.Set("Authorization", "Bearer s3cret-token")
		rec := httptest.NewRecorder()
		httpHandler.ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusOK))
	})

	It("does not serve the board page", func() {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		httpHandler.ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusNotFound))
	})

	It("does not serve the metrics endpoint even to an authenticated caller", func() {
		// ⚠️ Asserted WITH the header: unauthenticated this path would also be
		// 404, but only the authenticated probe distinguishes "not mounted" from
		// "mounted behind the middleware".
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		req.Header.Set("Authorization", "Bearer s3cret-token")
		rec := httptest.NewRecorder()
		httpHandler.ServeHTTP(rec, req)
		Expect(rec.Code).To(Equal(http.StatusNotFound))
	})
})

var _ = Describe("attention store token configuration", func() {
	It("renders the token by length and never by value", func() {
		ctx := context.Background()

		var buf bytes.Buffer
		original := log.Writer()
		log.SetOutput(&buf)
		defer log.SetOutput(original)

		cfg := &application{AttentionStoreToken: "s3cret-token"}
		Expect(argument.Print(ctx, cfg)).To(BeNil())

		out := buf.String()
		Expect(out).To(ContainSubstring("AttentionStoreToken length 12"))
		Expect(out).NotTo(ContainSubstring("s3cret-token"))
	})
})
