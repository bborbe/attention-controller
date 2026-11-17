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

	"github.com/bborbe/errors"
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
		Expect(listener.Close()).To(Succeed())

		app := &application{Listen: addr}
		gate := factory.CreateStartupGate(factory.CreateHealthzHandler(), http.NotFoundHandler())
		gate.Step("open-db")

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- app.runBoardListener(gate)(ctx) }()

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

	It("fails the process with the step name when the db cannot be opened", func() {
		dir := GinkgoT().TempDir()
		notADir := filepath.Join(dir, "file")
		Expect(os.WriteFile(notADir, []byte("x"), 0o600)).To(Succeed())

		app := &application{DataDir: notADir}
		gate := factory.CreateStartupGate(factory.CreateHealthzHandler(), http.NotFoundHandler())
		err := app.initAndServe(nil, gate)(context.Background())
		Expect(err).To(MatchError(ContainSubstring("init step open-db failed")))
		isReady, _ := gate.Status()
		Expect(isReady).To(BeFalse())
	})
})
