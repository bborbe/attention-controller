// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	libboltkv "github.com/bborbe/boltkv"
	"github.com/bborbe/errors"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg/factory"
	"github.com/bborbe/attention-controller/pkg/handler"
)

var _ = Describe("runInitStep", func() {
	var (
		ctx  context.Context
		gate handler.StartupGate
	)

	BeforeEach(func() {
		ctx = context.Background()
		gate = factory.CreateStartupGate(factory.CreateHealthzHandler(), http.NotFoundHandler())
	})

	It("names the step on the gate while it runs", func() {
		var seen string
		err := runInitStep(ctx, gate, libtime.NewCurrentDateTime(), "open-db",
			func(context.Context) error {
				_, seen = gate.Status()
				return nil
			})
		Expect(err).NotTo(HaveOccurred())
		Expect(seen).To(Equal("open-db"))
	})

	It("wraps a failure with the step name", func() {
		err := runInitStep(ctx, gate, libtime.NewCurrentDateTime(), "open-db",
			func(ctx context.Context) error {
				return errors.New(ctx, "boom")
			})
		Expect(err).To(MatchError(ContainSubstring("init step open-db failed")))
		Expect(err).To(MatchError(ContainSubstring("boom")))
	})
})

var _ = Describe("startup ordering", func() {
	It("serves the starting response on the bound port before init opens the gate", func() {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())
		addr := listener.Addr().String()

		app := &application{Listen: addr}
		gate := factory.CreateStartupGate(factory.CreateHealthzHandler(), http.NotFoundHandler())
		gate.Step("open-db")

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- app.runBoardListener(listener, gate)(ctx) }()

		var resp *http.Response
		Eventually(func() error {
			resp, err = http.Get("http://" + addr + "/api/1.0/attention")
			return err
		}, 5*time.Second, 50*time.Millisecond).Should(Succeed())
		body, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.Body.Close()).To(Succeed())
		Expect(resp.StatusCode).To(Equal(http.StatusServiceUnavailable))
		Expect(string(body)).To(ContainSubstring(`"status":"starting","step":"open-db"`))

		cancel()
		Eventually(done, 5*time.Second).Should(Receive())
	})

	It("returns only after an in-flight request has finished", func() {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())
		addr := listener.Addr().String()

		entered := make(chan struct{})
		release := make(chan struct{})
		finished := make(chan struct{})
		slow := http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
			close(entered)
			<-release
			close(finished)
		})
		gate := factory.CreateStartupGate(factory.CreateHealthzHandler(), http.NotFoundHandler())
		gate.Open(slow)

		app := &application{Listen: addr}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- app.runBoardListener(listener, gate)(ctx) }()
		go func() {
			defer GinkgoRecover()
			resp, err := http.Get("http://" + addr + "/")
			if err == nil {
				_ = resp.Body.Close()
			}
		}()

		Eventually(entered, 5*time.Second).Should(BeClosed())
		cancel()
		Consistently(done, 200*time.Millisecond).ShouldNot(Receive())
		close(release)
		Eventually(done, 5*time.Second).Should(Receive(BeNil()))
		Expect(finished).To(BeClosed())
	})

	It("fails the process with the step name when the db cannot be opened", func() {
		dir := GinkgoT().TempDir()
		notADir := filepath.Join(dir, "file")
		Expect(os.WriteFile(notADir, []byte("x"), 0o600)).To(Succeed())

		app := &application{DataDir: notADir}
		gate := factory.CreateStartupGate(factory.CreateHealthzHandler(), http.NotFoundHandler())
		var opened libkv.DB
		err := app.initAndServe(nil, gate, func(db libkv.DB) { opened = db })(context.Background())
		Expect(opened).To(BeNil())
		Expect(err).To(MatchError(ContainSubstring("init step open-db failed")))
		isReady, _ := gate.Status()
		Expect(isReady).To(BeFalse())
	})
})

var _ = Describe("shutdown with open board streams", func() {
	// stream stands in for the board's live channel: it answers, flushes, then
	// holds the response open until its request context ends — which
	// http.Server.Shutdown never does on its own.
	newStream := func(entered chan<- struct{}, exited chan<- struct{}) http.Handler {
		return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
			defer close(exited)
			resp.Header().Set("Content-Type", "text/event-stream")
			resp.WriteHeader(http.StatusOK)
			_ = http.NewResponseController(resp).Flush()
			close(entered)
			<-req.Context().Done()
		})
	}
	openStream := func(addr string) {
		go func() {
			defer GinkgoRecover()
			resp, err := http.Get("http://" + addr + "/api/1.0/attention/stream")
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}()
	}

	It("closes a stream the drain cannot end, so no handler outlives serveUntilDone", func() {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		Expect(err).NotTo(HaveOccurred())
		entered := make(chan struct{})
		exited := make(chan struct{})
		server := &http.Server{Handler: newStream(entered, exited), ReadHeaderTimeout: time.Second}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- serveUntilDone(ctx, server, listener, 200*time.Millisecond) }()
		openStream(listener.Addr().String())

		Eventually(entered, 5*time.Second).Should(BeClosed())
		cancel()
		Eventually(done, 3*time.Second).Should(Receive(BeNil()))
		Eventually(exited, time.Second).Should(BeClosed())
	})

	It(
		"ends a shutdown-aware stream at once, so the drain finishes well inside its timeout",
		func() {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			Expect(err).NotTo(HaveOccurred())
			addr := listener.Addr().String()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered := make(chan struct{})
			exited := make(chan struct{})
			gate := factory.CreateStartupGate(
				factory.CreateHealthzHandler(),
				http.NotFoundHandler(),
			)
			gate.Open(handler.NewShutdownAwareHandler(ctx.Done(), newStream(entered, exited)))

			app := &application{Listen: addr}
			done := make(chan error, 1)
			go func() { done <- app.runBoardListener(listener, gate)(ctx) }()
			openStream(addr)

			Eventually(entered, 5*time.Second).Should(BeClosed())
			start := time.Now()
			cancel()
			// The default ShutdownTimeout is 5 s: returning in under 2 s proves the
			// drain finished rather than timed out.
			Eventually(done, 2*time.Second).Should(Receive(BeNil()))
			Expect(time.Since(start)).To(BeNumerically("<", 2*time.Second))
			Expect(exited).To(BeClosed())
		},
	)

	It("does not wait forever on a db close a held read transaction blocks", func() {
		db, err := libboltkv.OpenDir(context.Background(), GinkgoT().TempDir(), boltOpenOptions())
		Expect(err).NotTo(HaveOccurred())

		inView := make(chan struct{})
		release := make(chan struct{})
		viewDone := make(chan struct{})
		go func() {
			defer GinkgoRecover()
			defer close(viewDone)
			_ = db.View(context.Background(), func(context.Context, libkv.Tx) error {
				close(inView)
				<-release
				return nil
			})
		}()
		Eventually(inView, 5*time.Second).Should(BeClosed())

		start := time.Now()
		closeDBBounded(db, 200*time.Millisecond)
		Expect(time.Since(start)).To(BeNumerically("<", time.Second))

		close(release)
		Eventually(viewDone, 5*time.Second).Should(BeClosed())
	})
})
