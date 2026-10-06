// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"os"
	"path/filepath"
	"time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

// The safety net is two mechanisms that cover different failures: a byte-offset
// tail that picks up an event appended to a producer's log on the next render,
// and a minutes-scale rescan that picks up a task file no watcher event ever
// delivered. These specs drive the real chain over temp directories and show
// each half resolving while the other has not, so neither mechanism can be
// dropped without a spec failing.
var _ = Describe("Provenance safety net", func() {
	var ctx context.Context
	var stateDir string
	var sessionsDir string
	var spawnDir string
	var vault string
	// clock is frozen in BeforeEach so the backstop window is measured from a
	// fixed instant; the specs advance it with SetNow rather than sleeping, per
	// the repo's time-injection rule.
	var clock libtime.CurrentDateTime
	var resolver pkg.ProvenanceResolver

	// eventLine writes one event-log line, as raw JSON rather than marshalled
	// from a struct, so the fixture is the *file shape* the watcher actually
	// writes and a field rename in the resolver's own struct cannot make the
	// test agree with itself. Mirrors the fixture in provenance_test.go.
	eventLine := func(itemID, sessionID, host, cwd string) string {
		return `{"item_id":"` + itemID + `","session_id":"` + sessionID +
			`","host":"` + host + `","cwd":"` + cwd + `"}` + "\n"
	}

	writeEvents := func(producerID, content string) {
		Expect(os.WriteFile(
			filepath.Join(stateDir, producerID+".events.jsonl"),
			[]byte(content),
			0o600,
		)).To(Succeed())
	}

	// healItem is the one item these specs drive: its producer wrote one event
	// line and its session has one task file. The session id lives in the
	// liveness ref — the item carries no session_id field of its own — and the
	// `session:<id>` form is a legal shape sessionIDFromItem handles.
	healItem := func() pkg.Item {
		return pkg.Item{
			ItemID:      pkg.ItemID("item-heal"),
			ProducerID:  pkg.ProducerID("session-heal"),
			DedupKey:    pkg.DedupKey("key-heal"),
			LivenessRef: pkg.LivenessRef("session:session-heal"),
		}
	}

	// heal writes the two inputs the item joins against: the task file whose
	// frontmatter records the session, and one event line whose item_id is the
	// item's DedupKey — the event log is named for the PRODUCER, and the event's
	// item_id is the dedup key, not the store's item id.
	heal := func() {
		writeVaultTask(vault, "Heal.md", "---\nclaude_session_id: session-heal\n---\n")
		writeEvents("session-heal", eventLine("key-heal", "session-heal", "burn", "/w/heal"))
	}

	BeforeEach(func() {
		ctx = context.Background()
		stateDir = GinkgoT().TempDir()
		sessionsDir = GinkgoT().TempDir()
		spawnDir = GinkgoT().TempDir()
		vault = GinkgoT().TempDir()
		clock = libtime.NewCurrentDateTime()
		clock.SetNow(clock.Now())

		panes := &mocks.PaneLister{}
		panes.ListReturns(map[int]pkg.Pane{}, nil)
		// ⚠️ The index is built over the EMPTY vault first, so the boot build
		// resolves nothing and the task file written by heal() is invisible until
		// the backstop window lapses.
		index := pkg.NewTaskIndex(ctx, vault, clock)
		resolver = pkg.NewProvenanceResolver(
			pkg.NewEventLogReader(stateDir),
			sessionsDir,
			spawnDir,
			panes,
			index,
			clock,
		)
	})

	It("resolves a task file no watcher delivered, once the backstop lapses", func() {
		// ⚠️ No watcher runs in this spec — the watcher's absence is what makes the
		// backstop the only possible cause. The task file appears after the index
		// was built and nothing signals the index, so the second render's task
		// resolution can only have come from the minutes-scale rescan inside
		// Lookup. Drop that rescan and the task assertions below fail.
		heal()

		// The event half resolves on this first render, with no clock advance: the
		// byte-offset tail reads the appended line immediately.
		before := resolver.Resolve(ctx, pkg.Items{healItem()})[pkg.ItemID("item-heal")]
		Expect(before.Host).To(Equal("burn"))
		// The task half does not. The serving index is still inside the backstop
		// window, so the task file written above is invisible — which is what
		// distinguishes the backstop from an unconditional re-read.
		Expect(before.TaskName).To(BeEmpty())
		Expect(before.TaskPath).To(BeEmpty())

		// Ten minutes is comfortably past the five-minute backstop.
		clock.SetNow(clock.Now().Add(libtime.Duration(10 * time.Minute)))

		after := resolver.Resolve(ctx, pkg.Items{healItem()})[pkg.ItemID("item-heal")]
		Expect(after.TaskName).To(Equal("Heal"))
		Expect(after.TaskPath).To(Equal("25 Tasks/Heal.md"))
		// The event half is not lost by the rebuild: the tail still holds what it
		// accumulated, because nothing was appended between the two renders.
		Expect(after.Host).To(Equal("burn"))
	})

	It("keeps the task unresolved for every render inside the backstop window", func() {
		// ⚠️ The half that fails if Lookup re-reads the vault unconditionally:
		// many renders with the clock standing still, so the task file must stay
		// invisible throughout. The window is real rather than a no-op.
		heal()

		for i := 0; i < 5; i++ {
			got := resolver.Resolve(ctx, pkg.Items{healItem()})[pkg.ItemID("item-heal")]
			Expect(got.TaskName).To(BeEmpty(),
				"render %d resolved a task inside the backstop window", i)
			Expect(got.TaskPath).To(BeEmpty(),
				"render %d resolved a task inside the backstop window", i)
		}
	})
})

// The watcher's lifecycle is a shutdown contract: it must return when the
// service's context is cancelled, leaving neither a goroutine nor an inotify
// watch behind, and it must fail soft in every direction so a vault it cannot
// watch never takes the process down.
var _ = Describe("TaskIndexWatcher lifecycle", func() {
	var ctx context.Context
	var index pkg.TaskIndex

	BeforeEach(func() {
		ctx = context.Background()
		clock := libtime.NewCurrentDateTime()
		clock.SetNow(clock.Now())
		index = pkg.NewTaskIndex(ctx, "", clock)
	})

	It("returns when the service context is cancelled", func() {
		vault := GinkgoT().TempDir()
		// ⚠️ The tasks directory has to exist for the watch to be established. An
		// absent directory takes the fail-soft path instead and would return
		// before the cancel path was ever reached, so this spec would be
		// asserting the wrong branch.
		Expect(os.MkdirAll(filepath.Join(vault, "25 Tasks"), 0o750)).To(Succeed())
		watcher := pkg.NewTaskIndexWatcher(index, vault)

		watchCtx, cancel := context.WithCancel(ctx)
		DeferCleanup(cancel)

		// Buffered, so the watcher's goroutine never blocks on the send and the
		// result is readable after the spec returns.
		done := make(chan error, 1)
		go func() { done <- watcher.Run(watchCtx) }()

		// ⚠️ It must still be watching rather than already returned: a watcher
		// that returned here never established the watch, and the assertion below
		// would then pass for the wrong reason.
		Consistently(done, "100ms").ShouldNot(Receive())

		cancel()
		// A watcher that did not return would leak both the goroutine and the
		// inotify watch — the shutdown contract this spec pins.
		Eventually(done, "5s").Should(Receive(BeNil()))
	})

	It("returns immediately when no vault is configured", func() {
		// There is nothing to watch and no error to report: an empty vaultDir is
		// the ordinary case for a host with no vault configured.
		watcher := pkg.NewTaskIndexWatcher(index, "")
		Expect(watcher.Run(ctx)).To(Succeed())
	})

	It("fails soft when the tasks directory cannot be watched", func() {
		// A vault holding no `25 Tasks/`: the watch cannot be established, the
		// failure is logged as a warning, and the service still serves — the
		// index's backstop keeps it converging meanwhile.
		watcher := pkg.NewTaskIndexWatcher(index, GinkgoT().TempDir())
		Expect(watcher.Run(ctx)).To(Succeed())
	})
})
