// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"os"
	"path/filepath"
	"time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// managerTranscriptTestNow is the fixed instant the source's clock reads.
var managerTranscriptTestNow = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

// managerTranscriptTestHeartbeats is a hand-written SessionHeartbeatStore: only
// Get is consulted by the source, and only a listed session reads fresh.
type managerTranscriptTestHeartbeats struct {
	fresh map[string]bool
}

func (h *managerTranscriptTestHeartbeats) Post(
	context.Context,
	SessionHeartbeat,
) error {
	return nil
}

func (h *managerTranscriptTestHeartbeats) Get(
	_ context.Context,
	sessionID string,
) (SessionHeartbeat, bool, error) {
	if !h.fresh[sessionID] {
		return SessionHeartbeat{}, false, nil
	}
	return SessionHeartbeat{
		SessionID: sessionID,
		At:        libtime.DateTime(managerTranscriptTestNow),
	}, true, nil
}

func (h *managerTranscriptTestHeartbeats) List(context.Context) (SessionHeartbeats, error) {
	return nil, nil
}

var _ = Describe("manager transcript source", func() {
	var (
		clock       libtime.CurrentDateTimeGetter
		transcripts *fakeTranscriptReader
		heartbeats  *managerTranscriptTestHeartbeats
	)

	BeforeEach(func() {
		clock = libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
			return libtime.DateTime(managerTranscriptTestNow)
		})
		transcripts = &fakeTranscriptReader{
			lastAssistantText: map[string]string{},
		}
		heartbeats = &managerTranscriptTestHeartbeats{fresh: map[string]bool{}}
	})

	It("FailOpenIsSplitByInput", func() {
		registryDir := GinkgoT().TempDir()
		Expect(os.WriteFile(
			filepath.Join(registryDir, "waiting.json"),
			[]byte(`{"sessionId":"waiting","status":"waiting"}`),
			0o600,
		)).To(Succeed())

		By("(a) an unreadable transcript removes only that session's transcript half")
		source := NewManagerTranscriptSource(registryDir, heartbeats, transcripts, clock)
		states, err := source.GateStates(context.Background(), []string{"waiting", "silent"}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(states).To(HaveLen(2))
		// The waiting session is still gated by the registry half.
		Expect(states[0].Decision).To(Equal(ManagerGateGated))
		Expect(states[0].Reason).To(Equal("registry:waiting"))
		// A session whose only evidence was the transcript contributes no gate.
		Expect(states[1].Decision).NotTo(Equal(ManagerGateGated))

		By("(b) an unreadable registry holds a session without a fresh heartbeat")
		missing := filepath.Join(registryDir, "does-not-exist")
		source = NewManagerTranscriptSource(missing, heartbeats, transcripts, clock)
		states, err = source.GateStates(context.Background(), []string{"dead"}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(states[0].Decision).To(Equal(ManagerGateHeld))
		Expect(states[0].Reason).To(Equal("unregistered"))

		By("(c) the same unreadable registry leaves a headless-live session to the transcript half")
		heartbeats.fresh["live"] = true
		transcripts.lastAssistantText["live"] = "👤 You: nothing"
		states, err = source.GateStates(context.Background(), []string{"live"}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(states[0].Decision).To(Equal(ManagerGateCleared))
		Expect(states[0].Reason).To(Equal("headless:no-ask"))
	})

	It("OpenItemSetGates", func() {
		registryDir := GinkgoT().TempDir()
		Expect(os.WriteFile(
			filepath.Join(registryDir, "idle.json"),
			[]byte(`{"sessionId":"idle","status":"idle"}`),
			0o600,
		)).To(Succeed())
		transcripts.lastAssistantText["idle"] = "👤 You: nothing"
		source := NewManagerTranscriptSource(registryDir, heartbeats, transcripts, clock)

		By("gating an idle session the open set names")
		states, err := source.GateStates(
			context.Background(),
			[]string{"idle"},
			map[string]struct{}{"idle": {}},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(states[0].Decision).To(Equal(ManagerGateGated))
		Expect(states[0].Reason).To(Equal("store:open-item"))

		By("leaving it cleared when the set does not name it")
		states, err = source.GateStates(
			context.Background(),
			[]string{"idle"},
			map[string]struct{}{},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(states[0].Decision).To(Equal(ManagerGateCleared))
		Expect(states[0].Reason).To(Equal("idle:no-ask"))
	})
})
