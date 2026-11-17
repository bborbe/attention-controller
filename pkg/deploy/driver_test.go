// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package deploy_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/buildidentity"
	"github.com/bborbe/attention-controller/pkg/deploy"
)

const (
	failedCommit = "baddc0de1234"
	prevCommit   = "091b67b59bf5"
	prevVersion  = "091b67b59bf5"
	failedVer    = "baddc0de1234"
)

// fakeSupervisor records what the driver asked launchd to do.
type fakeSupervisor struct {
	bootstraps int
	stops      int
	status     deploy.ProcessStatus
	bootstrap  error
}

func (s *fakeSupervisor) Stop(ctx context.Context) error {
	s.stops++
	return nil
}

func (s *fakeSupervisor) Bootstrap(ctx context.Context) error {
	s.bootstraps++
	return s.bootstrap
}

func (s *fakeSupervisor) Status(ctx context.Context) (deploy.ProcessStatus, error) {
	return s.status, nil
}

// fakeProber answers from a fixed result until the binary is restored.
type fakeProber struct {
	calls        int
	result       deploy.ProbeResult
	afterRestore *deploy.ProbeResult
}

func (p *fakeProber) Probe(ctx context.Context, expectedCommit string) deploy.ProbeResult {
	p.calls++
	if p.afterRestore != nil && restored {
		result := *p.afterRestore
		if result.Ready {
			result.ServingCommit = expectedCommit
		}
		return result
	}
	return p.result
}

// fakeBinary stands in for the installed artifact and its retained copy.
type fakeBinary struct {
	installed   buildidentity.Identity
	previous    buildidentity.Identity
	hasPrev     bool
	restoreFail error
}

var restored bool

func (b *fakeBinary) Path() string {
	return "/Users/test/.local/bin/attention-controller"
}

func (b *fakeBinary) Identity(ctx context.Context) (buildidentity.Identity, error) {
	return b.installed, nil
}

func (b *fakeBinary) HasPrev() bool {
	return b.hasPrev
}

func (b *fakeBinary) PrevIdentity(ctx context.Context) (buildidentity.Identity, error) {
	return b.previous, nil
}

func (b *fakeBinary) RestorePrev(ctx context.Context) error {
	if b.restoreFail != nil {
		return b.restoreFail
	}
	restored = true
	return nil
}

// cardBoard is the board's push route, recording every posted card so a spec
// can read the payload the operator would have seen.
type cardBoard struct {
	mu     sync.Mutex
	cards  []pkg.PushRequest
	server *httptest.Server
	status int
}

func newCardBoard() *cardBoard {
	board := &cardBoard{status: http.StatusCreated}
	board.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request pkg.PushRequest
		Expect(json.NewDecoder(r.Body).Decode(&request)).To(Succeed())
		board.mu.Lock()
		board.cards = append(board.cards, request)
		board.mu.Unlock()
		w.WriteHeader(board.status)
		_, _ = w.Write([]byte(`{"item_id":"test"}`))
	}))
	return board
}

func (b *cardBoard) posted() []pkg.PushRequest {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]pkg.PushRequest(nil), b.cards...)
}

func (b *cardBoard) close() {
	b.server.Close()
}

// virtualClock advances only when the driver sleeps, so a spec exercises the
// real poll loop without waiting two real minutes.
type virtualClock struct {
	now time.Time
}

func newVirtualClock() *virtualClock {
	return &virtualClock{now: time.Date(2026, 11, 17, 14, 0, 0, 0, time.UTC)}
}

func (c *virtualClock) Now() time.Time {
	return c.now
}

func (c *virtualClock) Sleep(d time.Duration) {
	c.now = c.now.Add(d)
}

// harness is one driver with its fakes wired.
type harness struct {
	driver     *deploy.Driver
	supervisor *fakeSupervisor
	prober     *fakeProber
	binary     *fakeBinary
	board      *cardBoard
	clock      *virtualClock
	log        *bytes.Buffer
}

func newHarness() *harness {
	restored = false
	board := newCardBoard()
	clock := newVirtualClock()
	log := &bytes.Buffer{}
	h := &harness{
		supervisor: &fakeSupervisor{status: deploy.ProcessStatus{Running: true, Pid: 4242}},
		prober:     &fakeProber{result: deploy.ProbeResult{Detail: "no listener"}},
		binary: &fakeBinary{
			installed: buildidentity.Identity{Commit: failedCommit, Version: failedVer},
			previous:  buildidentity.Identity{Commit: prevCommit, Version: prevVersion},
			hasPrev:   true,
		},
		board: board,
		clock: clock,
		log:   log,
	}
	h.driver = &deploy.Driver{
		Mode:       deploy.InstallMode,
		Supervisor: h.supervisor,
		Prober:     h.prober,
		Binary:     h.binary,
		Board:      &deploy.HTTPCardPoster{URL: board.server.URL},
		Now:        clock.Now,
		Sleep:      clock.Sleep,
		Log:        log,
	}
	return h
}

func (h *harness) close() {
	h.board.close()
}

// delta reads the rollback's elapsed time out of the driver log.
func (h *harness) delta() float64 {
	match := regexp.MustCompile(`delta_s=([0-9.]+)`).FindStringSubmatch(h.log.String())
	ExpectWithOffset(1, match).NotTo(BeNil(), "no t_ready_after_rollback delta in log:\n%s", h.log)
	value, err := strconv.ParseFloat(match[1], 64)
	ExpectWithOffset(1, err).NotTo(HaveOccurred())
	return value
}

var _ = Describe("Driver", func() {
	var h *harness

	BeforeEach(func() {
		h = newHarness()
	})

	AfterEach(func() {
		h.close()
	})

	Context("a start that serves the installed build", func() {
		BeforeEach(func() {
			h.prober.result = deploy.ProbeResult{Ready: true, ServingCommit: failedCommit}
		})

		It("reports healthy, logs t_deploy_start and posts no card", func() {
			outcome, err := h.driver.Run(context.Background())

			Expect(err).To(BeNil())
			Expect(outcome).To(Equal(deploy.Healthy))
			Expect(h.log.String()).To(ContainSubstring("t_deploy_start="))
			Expect(h.log.String()).To(ContainSubstring("commit=" + failedCommit))
			Expect(h.board.posted()).To(BeEmpty())
			Expect(h.supervisor.bootstraps).To(Equal(1))
			Expect(restored).To(BeFalse())
		})
	})

	Context("a start alive with no listener", func() {
		It("rolls back inside the budget and posts the consent card", func() {
			h.prober.afterRestore = &deploy.ProbeResult{Ready: true}
			outcome, err := h.driver.Run(context.Background())

			Expect(err).NotTo(BeNil())
			Expect(outcome).To(Equal(deploy.RolledBack))
			Expect(h.supervisor.bootstraps).To(Equal(2), "the previous binary is started again")
			Expect(restored).To(BeTrue())
			Expect(h.delta()).To(BeNumerically("<=", 120))

			cards := h.board.posted()
			Expect(cards).To(HaveLen(1))
			payload := cards[0].Payload.String()
			Expect(payload).To(ContainSubstring(failedVer))
			Expect(payload).To(ContainSubstring(deploy.ConsentAction))
			Expect(cards[0].Context.String()).To(ContainSubstring(failedCommit))
			Expect(cards[0].Context.String()).To(ContainSubstring(prevCommit))
			Expect(cards[0].DedupKey.String()).To(ContainSubstring(failedCommit))
			Expect(cards[0].AnswerMechanism).To(Equal(pkg.AckAnswerMechanism))
		})
	})

	Context("a start that exits non-zero", func() {
		BeforeEach(func() {
			h.supervisor.status = deploy.ProcessStatus{Running: false, LastExit: "1"}
		})

		It("posts the same version and commit with no consent literal", func() {
			h.prober.afterRestore = &deploy.ProbeResult{Ready: true}
			outcome, err := h.driver.Run(context.Background())

			Expect(err).NotTo(BeNil())
			Expect(outcome).To(Equal(deploy.RolledBack))

			cards := h.board.posted()
			Expect(cards).To(HaveLen(1))
			payload := cards[0].Payload.String()
			Expect(payload).To(ContainSubstring(failedVer))
			Expect(payload).NotTo(ContainSubstring(deploy.ConsentAction))
			Expect(payload).NotTo(ContainSubstring("Grant Documents access"))
			Expect(payload).To(ContainSubstring("status 1"))
			Expect(cards[0].Context.String()).To(ContainSubstring(failedCommit))
		})
	})

	Context("a rollback with no previous binary", func() {
		BeforeEach(func() {
			h.binary.hasPrev = false
		})

		It("exits naming the failed commit and prev=unknown, and claims no card", func() {
			outcome, err := h.driver.Run(context.Background())

			Expect(err).NotTo(BeNil())
			Expect(outcome).To(Equal(deploy.RollbackFailed))
			Expect(err.Error()).To(ContainSubstring(failedCommit))
			Expect(err.Error()).To(ContainSubstring(deploy.PrevUnknown))
			Expect(err.Error()).NotTo(ContainSubstring("\n"))
			Expect(h.board.posted()).To(BeEmpty())
			Expect(h.supervisor.bootstraps).To(Equal(1), "nothing to restore, nothing to restart")
		})
	})

	Context("a rollback whose previous binary also fails", func() {
		It("exits naming both commits and claims no card", func() {
			outcome, err := h.driver.Run(context.Background())

			Expect(err).NotTo(BeNil())
			Expect(outcome).To(Equal(deploy.RollbackFailed))
			Expect(err.Error()).To(ContainSubstring(failedCommit))
			Expect(err.Error()).To(ContainSubstring(prevCommit))
			Expect(err.Error()).NotTo(ContainSubstring("\n"))
			Expect(h.board.posted()).To(BeEmpty())
			Expect(h.supervisor.bootstraps).To(Equal(2))
		})
	})

	Context("an installed binary with no VCS stamp", func() {
		BeforeEach(func() {
			h.binary.installed = buildidentity.Identity{}
		})

		It("refuses to start it at all", func() {
			outcome, err := h.driver.Run(context.Background())

			Expect(err).NotTo(BeNil())
			Expect(outcome).To(Equal(deploy.RollbackFailed))
			Expect(err.Error()).To(ContainSubstring(deploy.UnstampedCommit))
			Expect(h.supervisor.bootstraps).To(Equal(0))
			Expect(h.board.posted()).To(BeEmpty())
		})
	})
})
