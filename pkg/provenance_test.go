// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	"github.com/bborbe/errors"
	"github.com/bborbe/run"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

var _ = Describe("ProvenanceResolver", func() {
	var ctx context.Context
	var stateDir string
	var sessionsDir string
	var spawnDir string
	var paneLister *mocks.PaneLister
	var resolver pkg.ProvenanceResolver
	// clock drives the resolver's host-snapshot cache. It is frozen in
	// BeforeEach so the cache window is measured from a fixed instant rather
	// than from the wall clock, and the caching specs advance it with SetNow
	// instead of sleeping.
	var clock libtime.CurrentDateTime

	// eventLine writes one event-log line. Written as raw JSON rather than
	// marshalled from a struct, so the fixture is the *file shape* the watcher
	// actually writes and a field rename in the resolver's own struct cannot
	// make the test agree with itself.
	eventLine := func(itemID, sessionID, host, cwd, tool, pane string) string {
		return `{"item_id":"` + itemID + `","session_id":"` + sessionID + `","host":"` + host +
			`","cwd":"` + cwd + `","tool_name":"` + tool + `","pane":"` + pane + `"}` + "\n"
	}

	writeEvents := func(producerID, content string) {
		Expect(os.WriteFile(
			filepath.Join(stateDir, producerID+".events.jsonl"),
			[]byte(content),
			0o600,
		)).To(BeNil())
	}

	writeSession := func(pid, sessionID, name string) {
		Expect(os.WriteFile(
			filepath.Join(sessionsDir, pid+".json"),
			[]byte(`{"sessionId":"`+sessionID+`","name":"`+name+`"}`),
			0o600,
		)).To(BeNil())
	}

	// writeSessionWithSource is writeSession plus the registry's `nameSource`
	// key. Written as raw JSON text for the same reason writeSession is: the
	// fixture must be the file shape the registry actually holds, so a field
	// rename in the resolver's own struct cannot make the test agree with
	// itself. writeSession is deliberately left sourceless — its records are the
	// fixture for the "absence is not user" case.
	writeSessionWithSource := func(pid, sessionID, name, nameSource string) {
		Expect(os.WriteFile(
			filepath.Join(sessionsDir, pid+".json"),
			[]byte(`{"sessionId":"`+sessionID+`","name":"`+name+`","nameSource":"`+nameSource+`"}`),
			0o600,
		)).To(BeNil())
	}

	// writeSpawn writes one spawn-ledger record as `<spawnDir>/<sessionID>.json`.
	// Written as raw JSON text, never marshalled from spawnRecord, so the fixture
	// is the *file shape* the supervisor actually writes and a field rename in the
	// resolver's own struct cannot make the fixture agree with itself.
	writeSpawn := func(sessionID, mode string) {
		Expect(os.WriteFile(
			filepath.Join(spawnDir, sessionID+".json"),
			[]byte(`{"session_id":"`+sessionID+`","mode":"`+mode+`"}`),
			0o600,
		)).To(BeNil())
	}

	item := func(itemID, producerID, dedupKey string) pkg.Item {
		return pkg.Item{
			ItemID:     pkg.ItemID(itemID),
			ProducerID: pkg.ProducerID(producerID),
			DedupKey:   pkg.DedupKey(dedupKey),
		}
	}

	// sessionItem is `item` plus the liveness ref, which is where the session id
	// actually lives — the item carries no session_id field of its own. The
	// heartbeat form is the store's live shape: the watcher touches one file per
	// session, so the final path segment is the session id.
	sessionItem := func(itemID, producerID, dedupKey, sessionID string) pkg.Item {
		it := item(itemID, producerID, dedupKey)
		it.LivenessRef = pkg.LivenessRef(
			"heartbeat:/w/heartbeat/" + sessionID,
		)
		return it
	}

	BeforeEach(func() {
		ctx = context.Background()
		stateDir = GinkgoT().TempDir()
		sessionsDir = GinkgoT().TempDir()
		spawnDir = GinkgoT().TempDir()
		paneLister = &mocks.PaneLister{}
		paneLister.ListReturns(map[int]pkg.Pane{}, nil)
		clock = libtime.NewCurrentDateTime()
		clock.SetNow(clock.Now())
		// An empty vault: the specs below are about the event log, the registry
		// and the pane listing, so no task resolves and TaskName/TaskPath stay
		// empty. The vault itself is covered by the TaskIndex specs.
		resolver = pkg.NewProvenanceResolver(
			stateDir,
			sessionsDir,
			spawnDir,
			paneLister,
			pkg.NewTaskIndex(ctx, GinkgoT().TempDir(), clock),
			clock,
		)
	})

	It("joins on the item's dedup key, so one producer's items do not share provenance", func() {
		// ⚠️ The correction this test locks in. A producer is a session, which
		// routinely holds several open items at once (measured live: 15 of 26
		// producers held more than one, one held six). Joining on the producer
		// would give both items below the same host and cwd, stamping one
		// session's values onto a row that belongs to a different item.
		writeEvents("producer-a",
			eventLine("key-one", "producer-a", "burn", "/w/one", "", "11")+
				eventLine("key-two", "producer-a", "burn", "/w/two", "", "22"))

		resolved := resolver.Resolve(ctx, pkg.Items{
			item("item-1", "producer-a", "key-one"),
			item("item-2", "producer-a", "key-two"),
		})

		Expect(resolved[pkg.ItemID("item-1")].Cwd).To(Equal("/w/one"))
		Expect(resolved[pkg.ItemID("item-2")].Cwd).To(Equal("/w/two"))
	})

	It("keeps the provenance-bearing line when a later close event shares the item id", func() {
		// The log is append-only and a close event carries no host, cwd or pane.
		// Letting it win would blank the provenance of every item that was ever
		// closed and re-read.
		writeEvents("producer-b",
			eventLine("key-close", "producer-b", "burn", "/w/keep", "AskUserQuestion", "33")+
				`{"item_id":"key-close","session_id":"producer-b","closed_by":"UserPromptSubmit"}`+"\n")

		resolved := resolver.Resolve(ctx, pkg.Items{item("item-3", "producer-b", "key-close")})

		Expect(resolved[pkg.ItemID("item-3")].Host).To(Equal("burn"))
		Expect(resolved[pkg.ItemID("item-3")].Cwd).To(Equal("/w/keep"))
		Expect(resolved[pkg.ItemID("item-3")].Tool).To(Equal("AskUserQuestion"))
	})

	It("shows the pane when it validates against the session's current registry name", func() {
		writeSession("111", "producer-c", "⚙ Deploy Vulnerability Fix Agent to Octopus Dev")
		writeEvents("producer-c", eventLine("key-ok", "producer-c", "burn", "/w/c", "", "928"))
		paneLister.ListReturns(map[int]pkg.Pane{
			928: {PaneID: 928, Title: "◑ Deploy Vulnerability Fix Agent to Octopus Dev"},
		}, nil)

		resolved := resolver.Resolve(ctx, pkg.Items{item("item-4", "producer-c", "key-ok")})
		provenance := resolved[pkg.ItemID("item-4")]

		Expect(provenance.PaneRecorded).To(BeTrue())
		Expect(provenance.Routable).To(BeTrue())
		Expect(provenance.Pane).To(Equal("928"))
	})

	It("withholds the pane when it exists but belongs to another session", func() {
		// § Silence 7's case: the id resolves, so an existence-only check passes,
		// but the pane is somebody else's. This is the headless worker inheriting
		// its spawner's WEZTERM_PANE.
		writeSession("111", "producer-d", "⚙ Deploy Vulnerability Fix Agent")
		writeEvents("producer-d", eventLine("key-bad", "producer-d", "burn", "/w/d", "", "928"))
		paneLister.ListReturns(map[int]pkg.Pane{
			928: {PaneID: 928, Title: "◑ Somebody Else's Session"},
		}, nil)

		resolved := resolver.Resolve(ctx, pkg.Items{item("item-5", "producer-d", "key-bad")})
		provenance := resolved[pkg.ItemID("item-5")]

		Expect(provenance.PaneRecorded).To(BeTrue())
		Expect(provenance.Routable).To(BeFalse())
		// Withheld, not substituted: the id is absent from what the page may render.
		Expect(provenance.Pane).To(BeEmpty())
		// The rest of the row still resolves — a bad pane does not blank the host.
		Expect(provenance.Host).To(Equal("burn"))
	})

	It("makes no pane claim at all when the pane listing cannot be read", func() {
		// ⚠️ The defect the live launchd artifact exposed. WezTerm is not on the
		// plist's PATH, so the listing fails there on every request — and the
		// first implementation flattened that failure into an empty map, which
		// marked every row `unroutable`. That asserts the pane does not resolve
		// to this session, which an unreadable listing cannot establish. The row
		// must instead carry no pane claim, exactly as when a value is absent.
		writeSession("111", "producer-x", "⚙ Some Session")
		writeEvents("producer-x", eventLine("key-x", "producer-x", "burn", "/w/x", "", "928"))
		paneLister.ListReturns(nil, errors.New(ctx, "wezterm not found"))

		provenance := resolver.Resolve(
			ctx,
			pkg.Items{item("item-x", "producer-x", "key-x")},
		)[pkg.ItemID("item-x")]

		// The pane is neither shown nor disowned.
		Expect(provenance.PaneRecorded).To(BeFalse())
		Expect(provenance.Routable).To(BeFalse())
		Expect(provenance.Pane).To(BeEmpty())
		// The rest of the row still resolves — an unreadable listing is not a
		// reason to drop host, cwd or tool.
		Expect(provenance.Host).To(Equal("burn"))
		Expect(provenance.Cwd).To(Equal("/w/x"))
	})

	It(
		"makes no claim for an item whose producer wrote no event log and is not in the registry",
		func() {
			// ⚠️ The retitle is the point. This case used to stand for "no event
			// log", and it no longer does: an item with no event line now resolves
			// through the name-keyed fallback when its session is nameable. What
			// this actually covers is the *unnameable* session — the producer is in
			// neither the log nor the registry — and that is the case that must
			// still render nothing.
			resolved := resolver.Resolve(
				ctx,
				pkg.Items{item("item-6", "producer-absent", "key-absent")},
			)
			provenance := resolved[pkg.ItemID("item-6")]

			Expect(provenance.Resolved()).To(BeFalse())
			Expect(provenance.PaneRecorded).To(BeFalse())
		},
	)

	It("resolves a pane for an item whose producer wrote no line for its dedup key", func() {
		// Cause 1 of the second resolution source. The producer's log exists and
		// carries other items, but nothing for this item's dedup key — the live
		// shape, where the log postdates the push. The registry and the pane
		// listing are the only remaining route to the pane.
		writeEvents("producer-h",
			eventLine("some-other-key", "session-h", "burn", "/w/other", "", "77"))
		writeSession("1", "session-h", "⚙ Session H")
		paneLister.ListReturns(map[int]pkg.Pane{
			12: {PaneID: 12, Title: "✳ ⚙ Session H"},
		}, nil)

		resolved := resolver.Resolve(
			ctx,
			pkg.Items{sessionItem("item-11", "producer-h", "key-h", "session-h")},
		)

		provenance := resolved[pkg.ItemID("item-11")]
		Expect(provenance.Pane).To(Equal("12"))
		Expect(provenance.PaneRecorded).To(BeTrue())
		Expect(provenance.Routable).To(BeTrue())
	})

	It("resolves a pane for a producer id carrying the session: prefix", func() {
		// Cause 2. `session:` belongs on the item's LivenessRef, never on its
		// ProducerID, but ProducerID is validated only as non-empty so a
		// producer can push the marker in the wrong field. readEvents then opens
		// `session:<id>.events.jsonl`, which cannot exist — the log is named for
		// the bare id.
		//
		// The bare log deliberately exists and carries a DIFFERENT key. That is
		// the pair that makes this test about the prefix rather than about a
		// missing log: the prefixed name is absent, the bare name is present, and
		// the item's own key is in neither — so the only route to pane 34 is the
		// session join. Writing `key-j` into the bare log instead would let the
		// event path resolve it and the prefix would never be exercised.
		writeEvents("session-j",
			eventLine("some-other-key", "session-j", "burn", "/w/j", "", "88"))
		writeSession("2", "session-j", "⚙ Session J")
		paneLister.ListReturns(map[int]pkg.Pane{
			34: {PaneID: 34, Title: "◐ ⚙ Session J"},
		}, nil)

		_, err := os.Stat(filepath.Join(stateDir, "session:session-j.events.jsonl"))
		Expect(os.IsNotExist(err)).To(BeTrue(), "the prefixed log must not exist")
		bare, err := os.ReadFile(filepath.Join(stateDir, "session-j.events.jsonl"))
		Expect(err).To(BeNil())
		Expect(string(bare)).To(ContainSubstring("some-other-key"))
		Expect(string(bare)).NotTo(ContainSubstring("key-j"))

		resolved := resolver.Resolve(
			ctx,
			pkg.Items{sessionItem("item-12", "session:session-j", "key-j", "session-j")},
		)

		provenance := resolved[pkg.ItemID("item-12")]
		Expect(provenance.Pane).To(Equal("34"))
		Expect(provenance.PaneRecorded).To(BeTrue())
	})

	It("makes no claim when the session is registered but owns no matching pane", func() {
		// The negative half, and the one that keeps the fallback honest: the
		// session is nameable, so OwnsPane would treat an empty name as
		// unprovable-and-therefore-owned, but there is no pane whose title
		// matches. Nothing is claimed and nothing is marked unroutable — there
		// is no recorded pane to distrust.
		writeSession("3", "session-k", "⚙ Session K")
		paneLister.ListReturns(map[int]pkg.Pane{
			56: {PaneID: 56, Title: "⚙ Some Other Session"},
		}, nil)

		resolved := resolver.Resolve(
			ctx,
			pkg.Items{sessionItem("item-13", "session-k", "key-k", "session-k")},
		)

		provenance := resolved[pkg.ItemID("item-13")]
		Expect(provenance.Resolved()).To(BeFalse())
		Expect(provenance.PaneRecorded).To(BeFalse())
	})

	It(
		"selects only the pane whose glyph-stripped title matches, not the first pane found",
		func() {
			// Positive control (a): a fallback that returned the first pane it saw
			// would pass every other test in this file. The matching pane is
			// deliberately not the first in map order.
			writeSession("4", "session-l", "⚙ Session L")
			paneLister.ListReturns(map[int]pkg.Pane{
				90: {PaneID: 90, Title: "⚙ Unrelated"},
				91: {PaneID: 91, Title: "⚙ Session L"},
				92: {PaneID: 92, Title: "⚙ Also Unrelated"},
			}, nil)

			resolved := resolver.Resolve(
				ctx,
				pkg.Items{sessionItem("item-14", "session-l", "key-l", "session-l")},
			)

			Expect(resolved[pkg.ItemID("item-14")].Pane).To(Equal("91"))
		},
	)

	It("makes no claim when the pane listing cannot be read", func() {
		// Same direction as build: an unreadable listing proves nothing, so the
		// row makes no pane claim at all rather than being marked unroutable.
		writeSession("5", "session-m", "⚙ Session M")
		paneLister.ListReturns(nil, errors.New(ctx, "wezterm unreachable"))

		resolved := resolver.Resolve(
			ctx,
			pkg.Items{sessionItem("item-15", "session-m", "key-m", "session-m")},
		)

		provenance := resolved[pkg.ItemID("item-15")]
		Expect(provenance.Resolved()).To(BeFalse())
		Expect(provenance.PaneRecorded).To(BeFalse())
	})

	It("still prefers the event log when the item has a line for its dedup key", func() {
		// The regression guard: the fallback must not displace the logged path.
		// The log says pane 61 and the registry-name join would say 62; the log
		// wins, and host/cwd still come from the event.
		writeEvents("producer-n", eventLine("key-n", "session-n", "burn", "/w/n", "", "61"))
		writeSession("6", "session-n", "⚙ Session N")
		paneLister.ListReturns(map[int]pkg.Pane{
			61: {PaneID: 61, Title: "⚙ Session N"},
			62: {PaneID: 62, Title: "⚙ Session N"},
		}, nil)

		resolved := resolver.Resolve(ctx, pkg.Items{item("item-16", "producer-n", "key-n")})

		provenance := resolved[pkg.ItemID("item-16")]
		Expect(provenance.Pane).To(Equal("61"))
		Expect(provenance.Host).To(Equal("burn"))
		Expect(provenance.Cwd).To(Equal("/w/n"))
	})

	It("resolves nothing when the state directory is unavailable", func() {
		// The standalone case: a store running for k8s agents, cron jobs or
		// dark-factory runs has no Claude Code state directory at all.
		unavailable := pkg.NewProvenanceResolver(
			filepath.Join(stateDir, "does-not-exist"),
			filepath.Join(sessionsDir, "does-not-exist"),
			spawnDir,
			paneLister,
			pkg.NewTaskIndex(ctx, GinkgoT().TempDir(), clock),
			clock,
		)

		resolved := unavailable.Resolve(ctx, pkg.Items{item("item-7", "producer-e", "key-e")})

		Expect(resolved[pkg.ItemID("item-7")].Resolved()).To(BeFalse())
	})

	It("reads the pane listing once for a whole page rather than once per row", func() {
		writeEvents("producer-f", eventLine("key-f1", "producer-f", "burn", "/w/f", "", "44"))

		resolver.Resolve(ctx, pkg.Items{
			item("item-8", "producer-f", "key-f1"),
			item("item-9", "producer-f", "key-f1"),
			item("item-10", "producer-g", "key-g"),
		})

		Expect(paneLister.ListCallCount()).To(Equal(1))
	})

	// advanceClock moves the resolver's injected clock past the two-second
	// host-snapshot window, so the next Resolve re-reads the pane listing, the
	// session registry and the spawn ledger. The specs drive the clock rather
	// than sleeping, per the repo's time-injection rule.
	advanceClock := func() {
		clock.SetNow(clock.Now().Add(libtime.Duration(3 * 1e9)))
	}

	// releaseOnce returns a closer for ch that closes it exactly once, so it can be
	// called from the happy path AND deferred as a cleanup without panicking on the
	// second call. ⚠️ The defer is the point: without it, a spec that fails before
	// its own close leaks the refresher goroutine parked in the lister stub for the
	// rest of the suite. That is harmless with today's fake — mocks.PaneLister
	// releases its mutex before invoking the stub, and the fake is rebuilt per spec
	// — but the failure mode of a regression test should not depend on the fake
	// happening to be harmless after the fact.
	releaseOnce := func(ch chan struct{}) func() {
		var once sync.Once
		return func() { once.Do(func() { close(ch) }) }
	}

	It("reads the host snapshot once inside the window and again past it", func() {
		// The cache is what stops the live stream's cost scaling with the number
		// of connected clients: every store change wakes every stream, and each
		// one would otherwise re-read the pane listing (a subprocess), the
		// registry and the 1,114-file ledger.
		writeEvents("producer-w", eventLine("key-w", "producer-w", "burn", "/w/w", "", ""))
		items := pkg.Items{item("item-w", "producer-w", "key-w")}

		resolver.Resolve(ctx, items)
		Expect(paneLister.ListCallCount()).To(Equal(1))

		// A second resolve inside the window is served from the cache.
		resolver.Resolve(ctx, items)
		Expect(paneLister.ListCallCount()).To(Equal(1))

		advanceClock()
		resolver.Resolve(ctx, items)
		Expect(paneLister.ListCallCount()).To(Equal(2))
	})

	It(
		"serves the last good snapshot while a refresh is in flight instead of queueing behind it",
		func() {
			// ⚠️ The guard against the 2026-10-03 outage. The resolver used to hold
			// its mutex across the refresh, and the refresh runs a subprocess, so one
			// wedged `wezterm cli list` stalled every Resolve in the process: the board
			// page timed out at 25 s while /healthz and the API answered in 1 ms and
			// 20 ms, because neither of those calls Resolve. Here the listing is held
			// open on purpose, standing in for a mux that has stopped answering, and a
			// concurrent caller must be served the previous snapshot rather than block
			// behind it. The refresh itself is bounded separately, by
			// paneListingTimeout in the lister.
			// ⚠️ The assertion is on SessionName, not Pane. Pane carries the id the
			// event recorded, which is the same string in both snapshots and so cannot
			// tell a stale answer from a fresh one; the registry's name is what changes
			// underneath, and it is therefore what proves which snapshot was served.
			writeSessionWithSource("1", "session-block", "Before Name", "user")
			writeSpawn("session-block", "interactive")
			items := pkg.Items{
				sessionItem("item-block", "producer-block", "key-block", "session-block"),
			}

			first := resolver.Resolve(ctx, items)[pkg.ItemID("item-block")]
			Expect(first.SessionName).To(Equal("Before Name"))

			// Past the window the next caller refreshes — and that refresh is held
			// open for as long as this spec needs, so the lock-free path is the only
			// way a concurrent caller can return at all.
			advanceClock()
			writeSessionWithSource("1", "session-block", "After Name", "user")
			// Buffered send rather than a bare close. The single-flight contract admits
			// exactly one post-stub invocation today, so a close is correct as written —
			// but if that contract ever regresses, a second invocation would panic with
			// "close of closed channel" inside the resolver goroutine instead of failing
			// the assertion below, which is a far harder failure to read.
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			releaseAll := releaseOnce(release)
			DeferCleanup(releaseAll)
			paneLister.ListCalls(func(context.Context) (map[int]pkg.Pane, error) {
				select {
				case entered <- struct{}{}:
				default:
				}
				<-release
				return map[int]pkg.Pane{}, nil
			})

			refresher := make(chan pkg.Provenances, 1)
			go func() { refresher <- resolver.Resolve(ctx, items) }()
			// Waiting for List to be entered is what makes the race deterministic:
			// the refreshing flag is set before the call, so by here the concurrent
			// caller below is guaranteed to take the stale-snapshot path rather than
			// becoming a second refresher.
			Eventually(entered).Should(Receive())

			concurrent := make(chan pkg.Provenances, 1)
			go func() { concurrent <- resolver.Resolve(ctx, items) }()

			// A timeout here IS the regression: it means the caller queued behind the
			// in-flight subprocess instead of being served the last good snapshot.
			var stale pkg.Provenances
			Eventually(concurrent, "2s").Should(Receive(&stale))
			// ⚠️ The stale snapshot, not the refreshed one. That is what proves the
			// caller was served the last good host state rather than queueing behind
			// the in-flight subprocess and then reading the registry that changed
			// while it waited — a fresh read here would satisfy a mere "it returned".
			Expect(stale[pkg.ItemID("item-block")].SessionName).To(Equal("Before Name"))

			releaseAll()
			var fresh pkg.Provenances
			Eventually(refresher, "2s").Should(Receive(&fresh))
			Expect(fresh[pkg.ItemID("item-block")].SessionName).To(Equal("After Name"))
		},
	)

	It(
		"lets concurrent callers each refresh on a cold start, where no snapshot exists to serve",
		func() {
			// ⚠️ The one branch of hostState that is NOT single-flight, pinned so it is
			// a stated contract rather than an accident. The stale-serve branch requires
			// cached != nil, so with no snapshot yet every concurrent caller falls
			// through and refreshes — and a cold start is exactly when many streams
			// wake at once. The exposure is bounded rather than unbounded: each caller
			// runs exactly one exec, itself bounded by paneListingTimeout.
			writeSessionWithSource("1", "session-cold", "Cold Name", "user")
			items := pkg.Items{
				sessionItem("item-cold", "producer-cold", "key-cold", "session-cold"),
			}

			entries := make(chan struct{}, 4)
			release := make(chan struct{})
			releaseAll := releaseOnce(release)
			DeferCleanup(releaseAll)
			paneLister.ListCalls(func(context.Context) (map[int]pkg.Pane, error) {
				entries <- struct{}{}
				<-release
				return map[int]pkg.Pane{}, nil
			})

			first := make(chan pkg.Provenances, 1)
			second := make(chan pkg.Provenances, 1)
			go func() { first <- resolver.Resolve(ctx, items) }()
			go func() { second <- resolver.Resolve(ctx, items) }()

			// Both must reach the lister: neither can be served from a cache that has
			// never been written, so two entries is the assertion that this branch is
			// not single-flight. The call count is read while both are still blocked
			// inside the stub, so it is 2 by construction rather than by timing.
			Eventually(entries, "2s").Should(Receive())
			Eventually(entries, "2s").Should(Receive())
			Expect(paneLister.ListCallCount()).To(Equal(2))

			releaseAll()
			var got pkg.Provenances
			Eventually(first, "2s").Should(Receive(&got))
			Expect(got[pkg.ItemID("item-cold")].SessionName).To(Equal("Cold Name"))
			Eventually(second, "2s").Should(Receive(&got))
			Expect(got[pkg.ItemID("item-cold")].SessionName).To(Equal("Cold Name"))
		},
	)

	It(
		"does not clear the in-flight token for a refresh it did not start",
		func() {
			// ⚠️ The token's whole point, and the defect a bool had. Two cold callers
			// both start a refresh; with a bool the first to finish cleared the marker
			// for the second, still parked in its exec, so a third caller arriving once
			// the window had lapsed found the cache stale AND the marker clear and
			// started a third subprocess alongside it. Under a wedged mux a refresh
			// takes the full paneListingTimeout — longer than provenanceCacheWindow —
			// so that was the ordinary case there, not a race.
			writeSessionWithSource("1", "session-token", "Token Name", "user")
			items := pkg.Items{
				sessionItem("item-token", "producer-token", "key-token", "session-token"),
			}

			// Each invocation gets its own release, so the spec can finish one
			// refresher while deliberately leaving the other parked.
			entered := make(chan chan struct{}, 4)
			paneLister.ListCalls(func(context.Context) (map[int]pkg.Pane, error) {
				release := make(chan struct{})
				entered <- release
				<-release
				return map[int]pkg.Pane{}, nil
			})

			first := make(chan pkg.Provenances, 1)
			go func() { first <- resolver.Resolve(ctx, items) }()
			// ⚠️ The second caller starts only once the first is inside the lister, so
			// the token order is deterministic rather than raced: the second is always
			// the one whose token ends up stored, and the first is always the one whose
			// deferred clear must leave it alone. Launching both at once makes the spec
			// depend on which goroutine is scheduled last — and if the first to finish
			// also happens to be the last to claim the token, clearing it is CORRECT,
			// so the spec would fail on a resolver that is behaving properly. That is
			// how this spec first failed.
			firstRelease := releaseOnce(<-entered)
			DeferCleanup(firstRelease)

			second := make(chan pkg.Provenances, 1)
			go func() { second <- resolver.Resolve(ctx, items) }()
			secondRelease := releaseOnce(<-entered)
			DeferCleanup(secondRelease)

			// Finish the first refresher. It publishes, and its deferred clear must not
			// take the second one's token with it.
			firstRelease()
			var published pkg.Provenances
			Eventually(first, "2s").Should(Receive(&published))
			Expect(published[pkg.ItemID("item-token")].SessionName).To(Equal("Token Name"))

			// Let the window lapse while the second refresher is still parked, then
			// arrive as a third caller. It must be served the published snapshot rather
			// than start a subprocess of its own — which is exactly what a bool let
			// through, and what makes the call count below read 3 instead of 2.
			advanceClock()
			third := make(chan pkg.Provenances, 1)
			go func() { third <- resolver.Resolve(ctx, items) }()
			var served pkg.Provenances
			Eventually(third, "2s").Should(Receive(&served))
			Expect(served[pkg.ItemID("item-token")].SessionName).To(Equal("Token Name"))
			Expect(paneLister.ListCallCount()).To(Equal(2))

			secondRelease()
			Eventually(second, "2s").Should(Receive(&published))
		},
	)

	It("serves the registry and the ledger from the cache inside the window", func() {
		// ⚠️ The pane-lister count alone cannot prove sessionNames and
		// sessionModes are cached. This case changes both on disk and shows the
		// change is not observed until the window lapses, which is what proves
		// those two reads are served from the same snapshot.
		writeSessionWithSource("1", "session-cached", "Before Name", "user")
		writeSpawn("session-cached", "interactive")
		items := pkg.Items{
			sessionItem("item-cached", "producer-cached", "key-cached", "session-cached"),
		}

		first := resolver.Resolve(ctx, items)[pkg.ItemID("item-cached")]
		Expect(first.SessionName).To(Equal("Before Name"))
		Expect(first.Headless).To(BeFalse())

		// Both files change on disk. Inside the window neither change is seen.
		writeSessionWithSource("1", "session-cached", "After Name", "user")
		writeSpawn("session-cached", "headless")

		second := resolver.Resolve(ctx, items)[pkg.ItemID("item-cached")]
		Expect(second.SessionName).To(Equal("Before Name"))
		Expect(second.Headless).To(BeFalse())

		// Past the window the fresh registry and ledger are served.
		advanceClock()
		third := resolver.Resolve(ctx, items)[pkg.ItemID("item-cached")]
		Expect(third.SessionName).To(Equal("After Name"))
		Expect(third.Headless).To(BeTrue())
	})

	It("carries a pane listing error through the cache and never serves it as success", func() {
		// The fail-closed direction requirement 5 pins: an unreadable listing
		// yields no pane claim, and a cached error is not later replaced by a
		// successful listing until the window lapses.
		writeSession("111", "producer-cached-err", "⚙ Session E")
		writeEvents(
			"producer-cached-err",
			eventLine("key-cached-err", "producer-cached-err", "burn", "/w/e", "", "928"),
		)
		items := pkg.Items{item("item-cached-err", "producer-cached-err", "key-cached-err")}

		paneLister.ListReturns(nil, errors.New(ctx, "wezterm unreachable"))
		first := resolver.Resolve(ctx, items)[pkg.ItemID("item-cached-err")]
		Expect(first.PaneRecorded).To(BeFalse())
		Expect(first.Pane).To(BeEmpty())
		// The rest of the row still resolves — an unreadable listing is not a
		// reason to drop host, cwd or tool.
		Expect(first.Host).To(Equal("burn"))

		// A listing that would now succeed is not consulted inside the window:
		// the cached error stands and still makes no pane claim.
		paneLister.ListReturns(map[int]pkg.Pane{
			928: {PaneID: 928, Title: "◑ Session E"},
		}, nil)
		second := resolver.Resolve(ctx, items)[pkg.ItemID("item-cached-err")]
		Expect(second.PaneRecorded).To(BeFalse())
		Expect(second.Pane).To(BeEmpty())

		// Past the window the fresh listing is read and the pane resolves.
		advanceClock()
		third := resolver.Resolve(ctx, items)[pkg.ItemID("item-cached-err")]
		Expect(third.PaneRecorded).To(BeTrue())
		Expect(third.Routable).To(BeTrue())
		Expect(third.Pane).To(Equal("928"))
	})

	It("does not race when many resolves share one resolver", func() {
		// ⚠️ make precommit runs with -race=false, so a data race in the cache
		// guard would not be reported here. This case exists to drive concurrent
		// Resolve calls through the same resolver — the shape the SSE stream
		// produces with many open boards — so the mutex-guarded check-and-refresh
		// is exercised under contention rather than only serially.
		writeEvents("producer-conc", eventLine("key-conc", "producer-conc", "burn", "/w/c", "", ""))
		items := pkg.Items{item("item-conc", "producer-conc", "key-conc")}

		funcs := make([]run.Func, 0, 8)
		for i := 0; i < 8; i++ {
			funcs = append(funcs, func(ctx context.Context) error {
				resolver.Resolve(ctx, items)
				return nil
			})
		}
		Expect(run.CancelOnFirstErrorWait(ctx, funcs...)).To(BeNil())
		Expect(paneLister.ListCallCount()).To(BeNumerically(">=", 1))
	})

	// withVault is the BeforeEach resolver plus a task index over vault. The
	// vault is per-spec rather than shared, so each case's fixture is the only
	// thing its index can see.
	withVault := func(vault string) pkg.ProvenanceResolver {
		return pkg.NewProvenanceResolver(
			stateDir,
			sessionsDir,
			spawnDir,
			paneLister,
			pkg.NewTaskIndex(ctx, vault, clock),
			clock,
		)
	}

	It("resolves the vault task the item's session is anchored to", func() {
		vault := GinkgoT().TempDir()
		writeVaultTask(vault, "Fix the board.md",
			"---\nclaude_session_id: session-task\nstatus: active\n---\n\nbody\n")
		writeEvents(
			"producer-task",
			eventLine("key-task", "session-task", "burn", "/w/task", "", ""),
		)

		provenance := withVault(vault).Resolve(ctx, pkg.Items{
			sessionItem("item-task", "producer-task", "key-task", "session-task"),
		})[pkg.ItemID("item-task")]

		Expect(provenance.TaskName).To(Equal("Fix the board"))
		Expect(provenance.TaskPath).To(Equal("25 Tasks/Fix the board.md"))
		// The event-log branch still resolves everything it did before.
		Expect(provenance.Host).To(Equal("burn"))
	})

	It("resolves no task when no task file records the session", func() {
		vault := GinkgoT().TempDir()
		writeVaultTask(vault, "Someone Else's Task.md",
			"---\nclaude_session_id: some-other-session\n---\n")
		writeEvents("producer-none", eventLine("key-none", "session-none", "burn", "/w/n", "", ""))

		provenance := withVault(vault).Resolve(ctx, pkg.Items{
			sessionItem("item-none", "producer-none", "key-none", "session-none"),
		})[pkg.ItemID("item-none")]

		Expect(provenance.TaskName).To(BeEmpty())
		Expect(provenance.TaskPath).To(BeEmpty())
		Expect(provenance.Host).To(Equal("burn"))
	})

	It("resolves the task for an item whose producer wrote no event log", func() {
		// ⚠️ The branch this test exists for. The producer wrote no event log at
		// all, and resolveByName reports ok=false — the session is in neither
		// the registry nor the pane listing — so the loop would otherwise store
		// no Provenance for this item at all and the task would be lost. The
		// task lookup must not depend on the pane branches.
		vault := GinkgoT().TempDir()
		writeVaultTask(vault, "No Log Task.md",
			"---\nclaude_session_id: session-nolog\n---\n")

		provenance := withVault(vault).Resolve(ctx, pkg.Items{
			sessionItem("item-nolog", "producer-nolog", "key-nolog", "session-nolog"),
		})[pkg.ItemID("item-nolog")]

		// No pane is claimed and no event-log value resolved.
		Expect(provenance.PaneRecorded).To(BeFalse())
		Expect(provenance.Pane).To(BeEmpty())
		// ⚠️ INVERTED from the previous prompt's assertion, which read
		// `Resolved() == false` while the task was carried but not drawn. The task
		// alone now makes the provenance render, and it has to: the template gates
		// the whole line on this method, so a task-carrying row reporting false
		// here would resolve the name correctly, draw it correctly, and never show
		// it. This is the exact row the change exists for — the one whose producer
		// wrote no event log, so the task is the only thing the card can say.
		Expect(provenance.Resolved()).To(BeTrue())
		// The task resolves anyway — it is an independent source.
		Expect(provenance.TaskName).To(Equal("No Log Task"))
		Expect(provenance.TaskPath).To(Equal("25 Tasks/No Log Task.md"))
	})

	It("resolves no task when no index was configured", func() {
		// The standalone host: no vault, so no index. Everything else still
		// resolves exactly as it did before the index existed.
		writeEvents("producer-nil", eventLine("key-nil", "session-nil", "burn", "/w/nil", "", ""))
		nilIndex := pkg.NewProvenanceResolver(
			stateDir,
			sessionsDir,
			spawnDir,
			paneLister,
			nil,
			clock,
		)

		provenance := nilIndex.Resolve(ctx, pkg.Items{
			sessionItem("item-nil", "producer-nil", "key-nil", "session-nil"),
		})[pkg.ItemID("item-nil")]

		Expect(provenance.TaskName).To(BeEmpty())
		Expect(provenance.TaskPath).To(BeEmpty())
		Expect(provenance.Host).To(Equal("burn"))
	})

	It("resolves no task for an item that names no session", func() {
		// ProducerID is the bare `session:` marker with no id after it — the
		// one shape sessionIDFromItem reduces to "". The item names no session,
		// so there is nothing to look up and no task is guessed.
		vault := GinkgoT().TempDir()
		writeVaultTask(vault, "Orphan Task.md", "---\nclaude_session_id: session-orphan\n---\n")

		provenance := withVault(vault).Resolve(ctx, pkg.Items{pkg.Item{
			ItemID:     pkg.ItemID("item-orphan"),
			ProducerID: pkg.ProducerID("session:"),
			DedupKey:   pkg.DedupKey("key-orphan"),
		}})[pkg.ItemID("item-orphan")]

		Expect(provenance.TaskName).To(BeEmpty())
		Expect(provenance.TaskPath).To(BeEmpty())
	})

	It("resolves the production-touching fact from the task's own marker", func() {
		vault := GinkgoT().TempDir()
		writeVaultTask(vault, "Deploy Task.md",
			"---\nclaude_session_id: session-pt\n---\n\n"+
				"- [ ] ⚠️ production-touching — `make install`\n")

		provenance := withVault(vault).Resolve(ctx, pkg.Items{
			sessionItem("item-pt", "producer-pt", "key-pt", "session-pt"),
		})[pkg.ItemID("item-pt")]

		// Positive control: the task resolved, so the boolean below is read from
		// a task that was actually found rather than from a miss.
		Expect(provenance.TaskName).To(Equal("Deploy Task"))
		Expect(provenance.ProductionTouching).To(BeTrue())
	})

	It("resolves not production-touching when the phrase appears only in prose", func() {
		// ⚠️ The false positive the list-item form exists to avoid. Every prose
		// shape the vault writes the phrase in is here, and none of them is the
		// marker: the phrase in a checkbox bullet that is not the marker, the
		// phrase mid-sentence in a paragraph, and the phrase in a non-checkbox
		// bullet. A substring match fires on all three and removes a control the
		// operator needs.
		vault := GinkgoT().TempDir()
		writeVaultTask(vault, "Prose Task.md",
			"---\nclaude_session_id: session-pt-prose\n---\n\n"+
				"# Success Criteria\n\n"+
				"- [ ] SC2: a production-touching park renders the Allow / Deny pair\n\n"+
				"# Progress\n\n"+
				"The production-touching marker is authored by the operator, so the board "+
				"reads the declaration rather than guessing from the command.\n\n"+
				"- The production-touching marker is authored by the operator\n")

		provenance := withVault(vault).Resolve(ctx, pkg.Items{
			sessionItem("item-pt-prose", "producer-pt-prose", "key-pt-prose", "session-pt-prose"),
		})[pkg.ItemID("item-pt-prose")]

		Expect(provenance.TaskName).To(Equal("Prose Task"))
		Expect(provenance.ProductionTouching).To(BeFalse())
	})

	It("resolves production-touching for a marker on a checked box", func() {
		vault := GinkgoT().TempDir()
		writeVaultTask(vault, "Checked Task.md",
			"---\nclaude_session_id: session-pt-checked\n---\n\n"+
				"- [x] ⚠️ production-touching — `make install`\n")

		provenance := withVault(vault).Resolve(ctx, pkg.Items{
			sessionItem(
				"item-pt-checked",
				"producer-pt-checked",
				"key-pt-checked",
				"session-pt-checked",
			),
		})[pkg.ItemID("item-pt-checked")]

		Expect(provenance.TaskName).To(Equal("Checked Task"))
		Expect(provenance.ProductionTouching).To(BeTrue())
	})

	It("resolves production-touching for a marker on a slash box", func() {
		vault := GinkgoT().TempDir()
		writeVaultTask(vault, "Slash Task.md",
			"---\nclaude_session_id: session-pt-slash\n---\n\n"+
				"- [/] ⚠️ production-touching — `make install`\n")

		provenance := withVault(vault).Resolve(ctx, pkg.Items{
			sessionItem("item-pt-slash", "producer-pt-slash", "key-pt-slash", "session-pt-slash"),
		})[pkg.ItemID("item-pt-slash")]

		Expect(provenance.TaskName).To(Equal("Slash Task"))
		Expect(provenance.ProductionTouching).To(BeTrue())
	})

	It("resolves production-touching for a marker written without the warning glyph", func() {
		// ⚠️ The case the `(?:⚠️?)?` outer group exists for. The glyph is two
		// code points, so a bare `⚠️?` would still require the base glyph and
		// silently fail this spelling.
		vault := GinkgoT().TempDir()
		writeVaultTask(vault, "Glyphless Task.md",
			"---\nclaude_session_id: session-pt-glyphless\n---\n\n"+
				"- [ ] production-touching — `make install`\n")

		provenance := withVault(vault).Resolve(ctx, pkg.Items{
			sessionItem(
				"item-pt-glyphless",
				"producer-pt-glyphless",
				"key-pt-glyphless",
				"session-pt-glyphless",
			),
		})[pkg.ItemID("item-pt-glyphless")]

		Expect(provenance.TaskName).To(Equal("Glyphless Task"))
		Expect(provenance.ProductionTouching).To(BeTrue())
	})

	It("resolves not production-touching when the session resolves no task", func() {
		// The vault holds a marker, but for a different session. The fail-open
		// direction: no task for this session leaves the fact false, and false
		// renders the pair.
		vault := GinkgoT().TempDir()
		writeVaultTask(vault, "Other Task.md",
			"---\nclaude_session_id: session-pt-other\n---\n\n"+
				"- [ ] ⚠️ production-touching — `make install`\n")
		writeEvents(
			"producer-pt-none",
			eventLine("key-pt-none", "session-pt-none", "burn", "/w/pt", "", ""),
		)

		provenance := withVault(vault).Resolve(ctx, pkg.Items{
			sessionItem("item-pt-none", "producer-pt-none", "key-pt-none", "session-pt-none"),
		})[pkg.ItemID("item-pt-none")]

		// Positive control: the item's event-log provenance still resolved.
		Expect(provenance.Host).To(Equal("burn"))
		Expect(provenance.ProductionTouching).To(BeFalse())
	})

	It("resolves the goal and the topic a task's goals list names", func() {
		vault := GinkgoT().TempDir()
		writeVaultTask(
			vault,
			"Fix the board.md",
			"---\nclaude_session_id: session-goal\ngoals:\n  - \"[[Fix the Board]]\"\n---\n\nbody\n",
		)
		writeVaultFile(vault, "24 Goals", "Fix the Board.md", "---\ntitle: Fix the Board\n---\n")
		writeVaultFile(vault, "23 Topics", "Attention Board Polish.md",
			"---\ntitle: Attention Board Polish\n---\n\n## Goals\n\n- [[Fix the Board]]\n")
		writeEvents(
			"producer-goal",
			eventLine("key-goal", "session-goal", "burn", "/w/goal", "", ""),
		)

		provenance := withVault(vault).Resolve(ctx, pkg.Items{
			sessionItem("item-goal", "producer-goal", "key-goal", "session-goal"),
		})[pkg.ItemID("item-goal")]

		Expect(provenance.GoalName).To(Equal("Fix the Board"))
		Expect(provenance.GoalPath).To(Equal("24 Goals/Fix the Board.md"))
		Expect(provenance.TopicName).To(Equal("Attention Board Polish"))
		Expect(provenance.TopicPath).To(Equal("23 Topics/Attention Board Polish.md"))
		// The goal and the topic derive from the task, and a resolved task always
		// carries a non-empty name, so the gate is already true wherever a goal
		// exists — which is why Resolved needs no conjunct for either.
		Expect(provenance.TaskName).NotTo(BeEmpty())
		Expect(provenance.Resolved()).To(BeTrue())
	})

	It("resolves a goal no topic lists, and no topic", func() {
		// The dominant live case: 1,148 of 1,619 goal-carrying tasks resolve a
		// goal span with no topic span.
		vault := GinkgoT().TempDir()
		writeVaultTask(vault, "Lonely Goal Task.md",
			"---\nclaude_session_id: session-lonely\ngoals:\n  - \"[[Lonely Goal]]\"\n---\n")
		writeVaultFile(vault, "24 Goals", "Lonely Goal.md", "---\ntitle: Lonely Goal\n---\n")

		provenance := withVault(vault).Resolve(ctx, pkg.Items{
			sessionItem("item-lonely", "producer-lonely", "key-lonely", "session-lonely"),
		})[pkg.ItemID("item-lonely")]

		Expect(provenance.GoalName).To(Equal("Lonely Goal"))
		Expect(provenance.GoalPath).To(Equal("24 Goals/Lonely Goal.md"))
		Expect(provenance.TopicName).To(BeEmpty())
		Expect(provenance.TopicPath).To(BeEmpty())
	})

	It("resolves no goal and no topic for a task carrying goals: []", func() {
		vault := GinkgoT().TempDir()
		writeVaultTask(vault, "Empty Goals Task.md",
			"---\nclaude_session_id: session-empty\ngoals: []\n---\n")
		writeVaultFile(vault, "24 Goals", "Some Goal.md", "---\ntitle: Some Goal\n---\n")

		provenance := withVault(vault).Resolve(ctx, pkg.Items{
			sessionItem("item-empty", "producer-empty", "key-empty", "session-empty"),
		})[pkg.ItemID("item-empty")]

		// The task itself still resolves — the three spans are not one unit.
		Expect(provenance.TaskName).To(Equal("Empty Goals Task"))
		Expect(provenance.TaskPath).To(Equal("25 Tasks/Empty Goals Task.md"))
		Expect(provenance.GoalName).To(BeEmpty())
		Expect(provenance.GoalPath).To(BeEmpty())
		Expect(provenance.TopicName).To(BeEmpty())
		Expect(provenance.TopicPath).To(BeEmpty())
	})

	It("resolves no goal and no topic when no vault was configured", func() {
		writeEvents(
			"producer-novault",
			eventLine("key-novault", "session-novault", "burn", "/w/nv", "", ""),
		)
		nilIndex := pkg.NewProvenanceResolver(
			stateDir,
			sessionsDir,
			spawnDir,
			paneLister,
			nil,
			clock,
		)

		provenance := nilIndex.Resolve(ctx, pkg.Items{
			sessionItem("item-novault", "producer-novault", "key-novault", "session-novault"),
		})[pkg.ItemID("item-novault")]

		Expect(provenance.GoalName).To(BeEmpty())
		Expect(provenance.GoalPath).To(BeEmpty())
		Expect(provenance.TopicName).To(BeEmpty())
		Expect(provenance.TopicPath).To(BeEmpty())
		// Everything else resolves exactly as it did before the goal rung existed.
		Expect(provenance.Host).To(Equal("burn"))
	})

	It("renders the session name the registry records with source user", func() {
		writeSessionWithSource("1", "session-name-user", "Board Polish Session", "user")

		provenance := resolver.Resolve(ctx, pkg.Items{
			sessionItem("item-name-user", "producer-name-user", "key-name-user", "session-name-user"),
		})[pkg.ItemID("item-name-user")]

		Expect(provenance.SessionName).To(Equal("Board Polish Session"))
	})

	It("withholds the session name the registry records with source derived", func() {
		writeSessionWithSource("1", "session-name-derived", "Generated Name", "derived")

		provenance := resolver.Resolve(ctx, pkg.Items{
			sessionItem(
				"item-name-derived",
				"producer-name-derived",
				"key-name-derived",
				"session-name-derived",
			),
		})[pkg.ItemID("item-name-derived")]

		Expect(provenance.SessionName).To(BeEmpty())
	})

	It("withholds the session name the registry records with source peer", func() {
		// `peer` means the name was inherited from the spawning parent, so it
		// belongs to another session and rendering it would attribute this card
		// to a name the operator never chose.
		writeSessionWithSource("1", "session-name-peer", "Inherited Name", "peer")

		provenance := resolver.Resolve(ctx, pkg.Items{
			sessionItem("item-name-peer", "producer-name-peer", "key-name-peer", "session-name-peer"),
		})[pkg.ItemID("item-name-peer")]

		Expect(provenance.SessionName).To(BeEmpty())
	})

	It("withholds the session name of a record carrying no nameSource at all", func() {
		// ⚠️ The absence of the field is not `user`. A record written by
		// writeSession has no `nameSource` key, which is the shape every record
		// predating the field has — reading that absence as permission would
		// render a name whose provenance is simply unknown.
		writeSession("1", "session-name-absent", "Sourceless Name")

		provenance := resolver.Resolve(ctx, pkg.Items{
			sessionItem(
				"item-name-absent",
				"producer-name-absent",
				"key-name-absent",
				"session-name-absent",
			),
		})[pkg.ItemID("item-name-absent")]

		Expect(provenance.SessionName).To(BeEmpty())
	})

	It("withholds the session name of a session the registry does not hold", func() {
		provenance := resolver.Resolve(ctx, pkg.Items{
			sessionItem(
				"item-name-unknown",
				"producer-name-unknown",
				"key-name-unknown",
				"session-name-unknown",
			),
		})[pkg.ItemID("item-name-unknown")]

		Expect(provenance.SessionName).To(BeEmpty())
	})

	It("withholds the session name for an item that names no session", func() {
		// ProducerID is the bare `session:` marker with no id after it — the one
		// shape sessionIDFromItem reduces to "". There is no session to look up,
		// so the registry is not consulted and no name is guessed.
		writeSessionWithSource("1", "session-name-other", "Some Other Session", "user")

		provenance := resolver.Resolve(ctx, pkg.Items{pkg.Item{
			ItemID:     pkg.ItemID("item-name-nosession"),
			ProducerID: pkg.ProducerID("session:"),
			DedupKey:   pkg.DedupKey("key-name-nosession"),
		}})[pkg.ItemID("item-name-nosession")]

		Expect(provenance.SessionName).To(BeEmpty())
	})

	It("withholds every session name when the registry directory does not exist", func() {
		unavailable := pkg.NewProvenanceResolver(
			stateDir,
			filepath.Join(sessionsDir, "does-not-exist"),
			spawnDir,
			paneLister,
			pkg.NewTaskIndex(ctx, GinkgoT().TempDir(), clock),
			clock,
		)

		resolved := unavailable.Resolve(ctx, pkg.Items{
			sessionItem(
				"item-name-nodir",
				"producer-name-nodir",
				"key-name-nodir",
				"session-name-nodir",
			),
		})

		// An unreadable registry degrades to no name, never to an error: the page
		// still returns 200 with the row it would have rendered anyway.
		Expect(resolved[pkg.ItemID("item-name-nodir")].SessionName).To(BeEmpty())
	})

	It("skips a malformed registry record and still resolves the others", func() {
		Expect(os.WriteFile(
			filepath.Join(sessionsDir, "broken.json"),
			[]byte("this is not json"),
			0o600,
		)).To(BeNil())
		writeSessionWithSource("2", "session-name-intact", "Intact Name", "user")

		provenance := resolver.Resolve(ctx, pkg.Items{
			sessionItem(
				"item-name-intact",
				"producer-name-intact",
				"key-name-intact",
				"session-name-intact",
			),
		})[pkg.ItemID("item-name-intact")]

		Expect(provenance.SessionName).To(Equal("Intact Name"))
	})

	It("lets the lexically-last record win when two share a session id", func() {
		// os.ReadDir returns entries sorted by filename and the map write is
		// unconditional, so `b.json` is read after `a.json` and its name is the
		// one left in the map.
		writeSessionWithSource("a", "session-name-dup", "First Name", "user")
		writeSessionWithSource("b", "session-name-dup", "Second Name", "user")

		provenance := resolver.Resolve(ctx, pkg.Items{
			sessionItem("item-name-dup", "producer-name-dup", "key-name-dup", "session-name-dup"),
		})[pkg.ItemID("item-name-dup")]

		Expect(provenance.SessionName).To(Equal("Second Name"))
	})

	It("resolves headless for a session the spawn ledger records as headless", func() {
		// The spawn ledger is the only source that separates a headless worker
		// from a tab worker: nothing on the item does. A headless worker inherits
		// its spawner's WEZTERM_PANE, so its item carries the spawner's pane id.
		writeEvents(
			"producer-headless",
			eventLine("key-headless", "session-headless", "burn", "/w/h", "", ""),
		)
		writeSpawn("session-headless", "headless")

		provenance := resolver.Resolve(ctx, pkg.Items{
			sessionItem("item-headless", "producer-headless", "key-headless", "session-headless"),
		})[pkg.ItemID("item-headless")]

		// Positive control: the item resolved its event-log provenance at all.
		Expect(provenance.Host).To(Equal("burn"))
		Expect(provenance.Headless).To(BeTrue())
	})

	It("resolves not headless for a session the spawn ledger records as interactive", func() {
		writeEvents(
			"producer-interactive",
			eventLine("key-interactive", "session-interactive", "burn", "/w/i", "", ""),
		)
		writeSpawn("session-interactive", "interactive")

		provenance := resolver.Resolve(ctx, pkg.Items{
			sessionItem(
				"item-interactive",
				"producer-interactive",
				"key-interactive",
				"session-interactive",
			),
		})[pkg.ItemID("item-interactive")]

		Expect(provenance.Host).To(Equal("burn"))
		Expect(provenance.Headless).To(BeFalse())
	})

	It("resolves not headless when the spawn ledger holds no record for the session", func() {
		writeEvents(
			"producer-norecord",
			eventLine("key-norecord", "session-norecord", "burn", "/w/nr", "", ""),
		)
		// A record for a *different* session: the map resolves, this item's
		// session is absent from it, and absence is not headless.
		writeSpawn("some-other-session", "headless")

		provenance := resolver.Resolve(ctx, pkg.Items{
			sessionItem("item-norecord", "producer-norecord", "key-norecord", "session-norecord"),
		})[pkg.ItemID("item-norecord")]

		Expect(provenance.Host).To(Equal("burn"))
		Expect(provenance.Headless).To(BeFalse())
	})

	It("resolves not headless when the spawn ledger directory does not exist", func() {
		writeEvents(
			"producer-nospawn",
			eventLine("key-nospawn", "session-nospawn", "burn", "/w/ns", "", ""),
		)
		unavailable := pkg.NewProvenanceResolver(
			stateDir,
			sessionsDir,
			filepath.Join(spawnDir, "does-not-exist"),
			paneLister,
			pkg.NewTaskIndex(ctx, GinkgoT().TempDir(), clock),
			clock,
		)

		provenance := unavailable.Resolve(ctx, pkg.Items{
			sessionItem("item-nospawn", "producer-nospawn", "key-nospawn", "session-nospawn"),
		})[pkg.ItemID("item-nospawn")]

		Expect(provenance.Host).To(Equal("burn"))
		Expect(provenance.Headless).To(BeFalse())
	})

	It("resolves not headless when a spawn ledger record does not parse", func() {
		writeEvents(
			"producer-broken",
			eventLine("key-broken", "session-broken", "burn", "/w/b", "", ""),
		)
		writeEvents(
			"producer-sibling",
			eventLine("key-sibling", "session-sibling", "burn", "/w/s", "", ""),
		)
		// A truncated record for the broken session; a valid headless record for
		// its sibling, so one bad file must not blank the whole map.
		Expect(os.WriteFile(
			filepath.Join(spawnDir, "session-broken.json"),
			[]byte(`{"session_id":"session-broken","mode":`),
			0o600,
		)).To(BeNil())
		writeSpawn("session-sibling", "headless")

		resolved := resolver.Resolve(ctx, pkg.Items{
			sessionItem("item-broken", "producer-broken", "key-broken", "session-broken"),
			sessionItem("item-sibling", "producer-sibling", "key-sibling", "session-sibling"),
		})

		// Positive control: the broken session's item still resolved its event.
		Expect(resolved[pkg.ItemID("item-broken")].Host).To(Equal("burn"))
		Expect(resolved[pkg.ItemID("item-broken")].Headless).To(BeFalse())
		Expect(resolved[pkg.ItemID("item-sibling")].Headless).To(BeTrue())
	})

	It("resolves not headless when a record's mode is outside the known set", func() {
		writeEvents(
			"producer-daemon",
			eventLine("key-daemon", "session-daemon", "burn", "/w/d", "", ""),
		)
		writeSpawn("session-daemon", "daemon")

		provenance := resolver.Resolve(ctx, pkg.Items{
			sessionItem("item-daemon", "producer-daemon", "key-daemon", "session-daemon"),
		})[pkg.ItemID("item-daemon")]

		Expect(provenance.Host).To(Equal("burn"))
		Expect(provenance.Headless).To(BeFalse())
	})

	It("keeps the headless fact out of Resolved", func() {
		// The gate the page's provenance line hangs on. A boolean draws nothing,
		// so a headless-only card must draw no line rather than an empty one.
		Expect((pkg.Provenance{Headless: true}).Resolved()).To(BeFalse())
	})

	It("reports unresolved when ProductionTouching is the only set field", func() {
		// ⚠️ The same rule as the headless case above, and the evidence that the
		// production-touching boolean stayed out of Resolved(). It is a control
		// gate, not a line fact: it draws nothing, so it must not put a
		// non-drawing value in the line's gate.
		Expect((pkg.Provenance{ProductionTouching: true}).Resolved()).To(BeFalse())
	})
})

// writeVaultFile writes one file under <vault>/<dir>/. Written as raw text
// rather than through a parser, so the fixture is the *file shape* the vault
// actually holds and a rename in the reader cannot make a fixture agree with
// itself.
func writeVaultFile(vault, dir, name, content string) {
	path := filepath.Join(vault, dir)
	Expect(os.MkdirAll(path, 0o750)).To(BeNil())
	Expect(os.WriteFile(filepath.Join(path, name), []byte(content), 0o600)).To(BeNil())
}

// writeVaultTask writes one task file under <vault>/25 Tasks/, the directory the
// index reads.
func writeVaultTask(vault, name, content string) {
	writeVaultFile(vault, "25 Tasks", name, content)
}

var _ = Describe("TaskIndex", func() {
	var ctx context.Context
	// clock is frozen in BeforeEach so the refresh window is measured from a
	// fixed instant; the refresh specs advance it with SetNow rather than
	// sleeping, per the repo's time-injection rule.
	var clock libtime.CurrentDateTime

	BeforeEach(func() {
		ctx = context.Background()
		clock = libtime.NewCurrentDateTime()
		clock.SetNow(clock.Now())
	})

	// Each entry builds its own vault and returns the directory the index is
	// built from, so the "does not exist" case can point the index at a path
	// nothing ever created.
	DescribeTable("resolves the task recorded for a session",
		func(build func(root string) string, sessionID, wantName, wantPath string, wantOK bool) {
			vault := build(GinkgoT().TempDir())

			task, ok := pkg.NewTaskIndex(ctx, vault, clock).Lookup(sessionID)

			Expect(ok).To(Equal(wantOK))
			Expect(task.Name).To(Equal(wantName))
			Expect(task.Path).To(Equal(wantPath))
		},
		Entry("a file with a valid claude_session_id",
			func(root string) string {
				writeVaultTask(root, "Fix the board.md",
					"---\nclaude_session_id: session-a\nstatus: active\n---\nbody\n")
				return root
			},
			"session-a", "Fix the board", "25 Tasks/Fix the board.md", true),
		Entry("a file with no claude_session_id",
			func(root string) string {
				writeVaultTask(root, "No Session.md", "---\ntitle: No Session\n---\n")
				return root
			},
			"session-a", "", "", false),
		Entry("a file with an empty claude_session_id",
			func(root string) string {
				writeVaultTask(root, "Empty.md", "---\nclaude_session_id:\n---\n")
				return root
			},
			"", "", "", false),
		Entry("a file whose claude_session_id is only whitespace",
			func(root string) string {
				writeVaultTask(root, "Blank.md", "---\nclaude_session_id:    \n---\n")
				return root
			},
			"", "", "", false),
		Entry("a file with no frontmatter block",
			func(root string) string {
				writeVaultTask(root, "Plain.md", "# Plain\n\nclaude_session_id: session-a\n")
				return root
			},
			"session-a", "", "", false),
		Entry("a file whose frontmatter block is never closed",
			func(root string) string {
				writeVaultTask(root, "Unclosed.md", "---\nclaude_session_id: session-a\n")
				return root
			},
			"session-a", "", "", false),
		Entry("a non-markdown file",
			func(root string) string {
				writeVaultTask(root, "Notes.txt", "---\nclaude_session_id: session-a\n---\n")
				return root
			},
			"session-a", "", "", false),
		Entry("two files sharing one id, one still in flight",
			func(root string) string {
				// The in-flight file sorts *first*, so a tie-break that simply
				// took the last file read would pick the completed one.
				writeVaultTask(root, "Newer Task.md",
					"---\nclaude_session_id: session-b\nstatus: active\n---\n")
				writeVaultTask(root, "Older Task.md",
					"---\nclaude_session_id: session-b\nstatus: completed\n---\n")
				return root
			},
			"session-b", "Newer Task", "25 Tasks/Newer Task.md", true),
		Entry("two files sharing one id, both terminal",
			func(root string) string {
				writeVaultTask(root, "Alpha Task.md",
					"---\nclaude_session_id: session-c\nstatus: completed\n---\n")
				writeVaultTask(root, "Beta Task.md",
					"---\nclaude_session_id: session-c\nstatus: aborted\n---\n")
				return root
			},
			"session-c", "Beta Task", "25 Tasks/Beta Task.md", true),
		Entry("a vault directory that does not exist",
			func(root string) string {
				return filepath.Join(root, "does-not-exist")
			},
			"session-a", "", "", false),
	)

	// The goal rung, driven through the same exported constructor. The fixtures
	// write the *file shape* the vault actually holds — raw frontmatter text,
	// never a value marshalled from a struct — so a rename in the reader cannot
	// make a fixture agree with itself.
	DescribeTable(
		"resolves the goal and the topic a task's goals name",
		func(build func(root string) string, sessionID, wantGoalName, wantGoalPath, wantTopicName, wantTopicPath string) {
			vault := build(GinkgoT().TempDir())

			task, _ := pkg.NewTaskIndex(ctx, vault, clock).Lookup(sessionID)

			Expect(task.GoalName).To(Equal(wantGoalName))
			Expect(task.GoalPath).To(Equal(wantGoalPath))
			Expect(task.TopicName).To(Equal(wantTopicName))
			Expect(task.TopicPath).To(Equal(wantTopicPath))
		},
		Entry("a task whose goals list names a goal a topic lists",
			func(root string) string {
				writeVaultTask(
					root,
					"Fix the board.md",
					"---\nclaude_session_id: session-a\ngoals:\n  - \"[[Fix the Board]]\"\n---\nbody\n",
				)
				writeVaultFile(root, "24 Goals", "Fix the Board.md",
					"---\ntitle: Fix the Board\n---\n")
				writeVaultFile(root, "23 Topics", "Attention Board Polish.md",
					"---\ntitle: Attention Board Polish\n---\n\n## Goals\n\n- [[Fix the Board]]\n")
				return root
			},
			"session-a",
			"Fix the Board", "24 Goals/Fix the Board.md",
			"Attention Board Polish", "23 Topics/Attention Board Polish.md"),
		Entry("a task whose goals is the inline empty form",
			func(root string) string {
				writeVaultTask(root, "Empty Goals.md",
					"---\nclaude_session_id: session-a\ngoals: []\n---\n")
				writeVaultFile(root, "24 Goals", "Fix the Board.md",
					"---\ntitle: Fix the Board\n---\n")
				return root
			},
			"session-a", "", "", "", ""),
		Entry("a task whose goals list names two entries",
			func(root string) string {
				// The first is the one rendered; the second must appear in no
				// field, so its goal file and its topic both exist and are both
				// reachable only if the wrong entry won.
				writeVaultTask(
					root,
					"Two Goals.md",
					"---\nclaude_session_id: session-a\ngoals:\n  - \"[[First Goal]]\"\n  - \"[[Second Goal]]\"\n---\n",
				)
				writeVaultFile(root, "24 Goals", "First Goal.md", "---\ntitle: First Goal\n---\n")
				writeVaultFile(root, "24 Goals", "Second Goal.md", "---\ntitle: Second Goal\n---\n")
				writeVaultFile(root, "23 Topics", "First Topic.md",
					"---\ntitle: First Topic\n---\n\n## Goals\n\n- [[First Goal]]\n")
				writeVaultFile(root, "23 Topics", "Second Topic.md",
					"---\ntitle: Second Topic\n---\n\n## Goals\n\n- [[Second Goal]]\n")
				return root
			},
			"session-a",
			"First Goal", "24 Goals/First Goal.md",
			"First Topic", "23 Topics/First Topic.md"),
		Entry("a task whose goals names a title with no file under 24 Goals",
			func(root string) string {
				writeVaultTask(root, "Missing Goal.md",
					"---\nclaude_session_id: session-a\ngoals:\n  - \"[[Missing Goal]]\"\n---\n")
				writeVaultFile(root, "23 Topics", "Missing Topic.md",
					"---\ntitle: Missing Topic\n---\n\n## Goals\n\n- [[Missing Goal]]\n")
				return root
			},
			"session-a", "", "", "", ""),
		Entry("a goal file that no topic lists",
			func(root string) string {
				// The dominant live case: a goal resolves and no topic does.
				writeVaultTask(root, "Lonely Goal.md",
					"---\nclaude_session_id: session-a\ngoals:\n  - \"[[Lonely Goal]]\"\n---\n")
				writeVaultFile(root, "24 Goals", "Lonely Goal.md",
					"---\ntitle: Lonely Goal\n---\n")
				return root
			},
			"session-a",
			"Lonely Goal", "24 Goals/Lonely Goal.md", "", ""),
		Entry("a goal named in a topic page's prose but not under its Goals heading",
			func(root string) string {
				writeVaultTask(root, "Prose Goal.md",
					"---\nclaude_session_id: session-a\ngoals:\n  - \"[[Prose Goal]]\"\n---\n")
				writeVaultFile(root, "24 Goals", "Prose Goal.md",
					"---\ntitle: Prose Goal\n---\n")
				writeVaultFile(
					root,
					"23 Topics",
					"Prose Topic.md",
					"---\ntitle: Prose Topic\n---\n\nWe should work on [[Prose Goal]] soon.\n\n## Goals\n\n- [[Some Other Goal]]\n\n## Notes\n\n- [[Prose Goal]]\n",
				)
				return root
			},
			"session-a",
			"Prose Goal", "24 Goals/Prose Goal.md", "", ""),
		Entry("a topic Goals section listing a title that exists only under 25 Tasks",
			func(root string) string {
				// ⚠️ The existence guard, and the only case that asserts it. The
				// section mixes goals and tasks, so an implementation that trusted
				// it would resolve a task title as a goal.
				writeVaultTask(root, "Shared Title.md", "---\nclaude_session_id: some-other\n---\n")
				writeVaultTask(root, "Goal Carrier.md",
					"---\nclaude_session_id: session-a\ngoals:\n  - \"[[Shared Title]]\"\n---\n")
				writeVaultFile(root, "23 Topics", "Mixer.md",
					"---\ntitle: Mixer\n---\n\n## Goals\n\n- [[Shared Title]]\n")
				return root
			},
			"session-a", "", "", "", ""),
		Entry("a single-quoted goals entry",
			func(root string) string {
				writeVaultTask(root, "Single Quoted.md",
					"---\nclaude_session_id: session-a\ngoals:\n  - '[[Quote Goal]]'\n---\n")
				writeVaultFile(root, "24 Goals", "Quote Goal.md", "---\ntitle: Quote Goal\n---\n")
				writeVaultFile(root, "23 Topics", "Quote Topic.md",
					"---\ntitle: Quote Topic\n---\n\n## Goals\n\n- [[Quote Goal]]\n")
				return root
			},
			"session-a",
			"Quote Goal", "24 Goals/Quote Goal.md",
			"Quote Topic", "23 Topics/Quote Topic.md"),
		Entry("a 4-space-indented goals entry",
			func(root string) string {
				writeVaultTask(root, "Indented.md",
					"---\nclaude_session_id: session-a\ngoals:\n    - \"[[Indent Goal]]\"\n---\n")
				writeVaultFile(root, "24 Goals", "Indent Goal.md",
					"---\ntitle: Indent Goal\n---\n")
				writeVaultFile(root, "23 Topics", "Indent Topic.md",
					"---\ntitle: Indent Topic\n---\n\n## Goals\n\n- [[Indent Goal]]\n")
				return root
			},
			"session-a",
			"Indent Goal", "24 Goals/Indent Goal.md",
			"Indent Topic", "23 Topics/Indent Topic.md"),
		Entry("a goals entry carrying an alias",
			func(root string) string {
				writeVaultTask(
					root,
					"Aliased.md",
					"---\nclaude_session_id: session-a\ngoals:\n  - \"[[Alias Goal|display]]\"\n---\n",
				)
				writeVaultFile(root, "24 Goals", "Alias Goal.md", "---\ntitle: Alias Goal\n---\n")
				writeVaultFile(root, "23 Topics", "Alias Topic.md",
					"---\ntitle: Alias Topic\n---\n\n## Goals\n\n- [[Alias Goal]]\n")
				return root
			},
			"session-a",
			"Alias Goal", "24 Goals/Alias Goal.md",
			"Alias Topic", "23 Topics/Alias Topic.md"),
		Entry("a bare-title goals entry with no wikilink brackets",
			func(root string) string {
				writeVaultTask(root, "Bare Title.md",
					"---\nclaude_session_id: session-a\ngoals:\n  - Bare Goal\n---\n")
				writeVaultFile(root, "24 Goals", "Bare Goal.md", "---\ntitle: Bare Goal\n---\n")
				return root
			},
			"session-a", "", "", "", ""),
		Entry("a topic page carrying no Goals heading",
			func(root string) string {
				writeVaultTask(root, "No Heading.md",
					"---\nclaude_session_id: session-a\ngoals:\n  - \"[[Heading Goal]]\"\n---\n")
				writeVaultFile(root, "24 Goals", "Heading Goal.md",
					"---\ntitle: Heading Goal\n---\n")
				writeVaultFile(root, "23 Topics", "No Heading Topic.md",
					"---\ntitle: No Heading Topic\n---\n\n- [[Heading Goal]]\n")
				return root
			},
			"session-a",
			"Heading Goal", "24 Goals/Heading Goal.md", "", ""),
		Entry("a topic page carrying a Goals subheading instead",
			func(root string) string {
				writeVaultTask(root, "Subheading.md",
					"---\nclaude_session_id: session-a\ngoals:\n  - \"[[Subheading Goal]]\"\n---\n")
				writeVaultFile(root, "24 Goals", "Subheading Goal.md",
					"---\ntitle: Subheading Goal\n---\n")
				writeVaultFile(root, "23 Topics", "Subheading Topic.md",
					"---\ntitle: Subheading Topic\n---\n\n### Goals\n\n- [[Subheading Goal]]\n")
				return root
			},
			"session-a",
			"Subheading Goal", "24 Goals/Subheading Goal.md", "", ""),
		Entry("two topic pages listing one goal, with a subdirectory and a non-markdown file",
			func(root string) string {
				// The first topic found wins — with os.ReadDir's sorted order that
				// is the lexicographically first topic path. The subdirectory and
				// the `.txt` file must be skipped rather than read.
				writeVaultTask(root, "Shared Goal.md",
					"---\nclaude_session_id: session-a\ngoals:\n  - \"[[Shared Goal]]\"\n---\n")
				writeVaultFile(root, "24 Goals", "Shared Goal.md",
					"---\ntitle: Shared Goal\n---\n")
				writeVaultFile(root, "23 Topics", "B Topic.md",
					"---\ntitle: B Topic\n---\n\n## Goals\n\n- [[Shared Goal]]\n")
				writeVaultFile(root, "23 Topics", "A Topic.md",
					"---\ntitle: A Topic\n---\n\n## Goals\n\n- [[Shared Goal]]\n")
				writeVaultFile(root, "23 Topics", "notes.txt", "not markdown\n")
				writeVaultFile(
					root,
					"23 Topics/Sub",
					"nested.md",
					"## Goals\n\n- [[Shared Goal]]\n",
				)
				return root
			},
			"session-a",
			"Shared Goal", "24 Goals/Shared Goal.md",
			"A Topic", "23 Topics/A Topic.md"),
		Entry("a topic directory holding an unreadable .md entry",
			func(root string) string {
				writeVaultTask(root, "Broken Link.md",
					"---\nclaude_session_id: session-a\ngoals:\n  - \"[[Broken Goal]]\"\n---\n")
				writeVaultFile(root, "24 Goals", "Broken Goal.md",
					"---\ntitle: Broken Goal\n---\n")
				writeVaultFile(root, "23 Topics", "Intact Topic.md",
					"---\ntitle: Intact Topic\n---\n\n## Goals\n\n- [[Broken Goal]]\n")
				Expect(os.Symlink(
					"does-not-exist.md",
					filepath.Join(root, "23 Topics", "Dangling.md"),
				)).To(BeNil())
				return root
			},
			"session-a",
			"Broken Goal", "24 Goals/Broken Goal.md",
			"Intact Topic", "23 Topics/Intact Topic.md"),
		Entry("a vault with no 24 Goals directory at all",
			func(root string) string {
				writeVaultTask(root, "No Goals Dir.md",
					"---\nclaude_session_id: session-a\ngoals:\n  - \"[[Any Goal]]\"\n---\n")
				return root
			},
			"session-a", "", "", "", ""),
		Entry("a task using the singular goal key",
			func(root string) string {
				writeVaultTask(root, "Singular.md",
					"---\nclaude_session_id: session-a\ngoal:\n  - \"[[Fix the Board]]\"\n---\n")
				writeVaultFile(root, "24 Goals", "Fix the Board.md",
					"---\ntitle: Fix the Board\n---\n")
				writeVaultFile(root, "23 Topics", "Attention Board Polish.md",
					"---\ntitle: Attention Board Polish\n---\n\n## Goals\n\n- [[Fix the Board]]\n")
				return root
			},
			"session-a", "", "", "", ""),
	)

	It("resolves no goal and no topic when the index build is cancelled", func() {
		vault := GinkgoT().TempDir()
		writeVaultTask(vault, "Cancelled.md",
			"---\nclaude_session_id: session-a\ngoals:\n  - \"[[Cancelled Goal]]\"\n---\n")
		writeVaultFile(vault, "24 Goals", "Cancelled Goal.md",
			"---\ntitle: Cancelled Goal\n---\n")
		writeVaultFile(vault, "23 Topics", "Cancelled Topic.md",
			"---\ntitle: Cancelled Topic\n---\n\n## Goals\n\n- [[Cancelled Goal]]\n")
		cancelled, cancel := context.WithCancel(ctx)
		cancel()

		task, _ := pkg.NewTaskIndex(cancelled, vault, clock).Lookup("session-a")

		// A cancelled build holds what it read before cancellation and no more,
		// so the goal rung — which reads before the task walk — contributes
		// nothing rather than failing.
		Expect(task.GoalName).To(BeEmpty())
		Expect(task.TopicName).To(BeEmpty())
	})
})

// The index used to read the vault once, at construction, so every task created
// afterwards was invisible for the process's lifetime. These specs pin the
// bounded re-read that fixes it: a task file written after the build resolves on
// a later lookup, and only once the injected clock has passed the window.
var _ = Describe("TaskIndex refresh", func() {
	var ctx context.Context
	var clock libtime.CurrentDateTime

	BeforeEach(func() {
		ctx = context.Background()
		// Frozen, so the refresh window is measured from a fixed instant and the
		// specs advance it with SetNow rather than sleeping.
		clock = libtime.NewCurrentDateTime()
		clock.SetNow(clock.Now())
	})

	// advanceClock moves the injected clock past the two-second refresh window,
	// so the next Lookup rebuilds the index from the vault.
	advanceClock := func() {
		clock.SetNow(clock.Now().Add(libtime.Duration(3 * 1e9)))
	}

	It("resolves a task written after the index was built, once the window lapses", func() {
		vault := GinkgoT().TempDir()
		index := pkg.NewTaskIndex(ctx, vault, clock)

		// The vault is empty at construction, so the session resolves nothing.
		_, ok := index.Lookup("session-late")
		Expect(ok).To(BeFalse())

		writeVaultTask(vault, "Late Task.md",
			"---\nclaude_session_id: session-late\n---\n")

		// ⚠️ Inside the window the new file is not observed. This is the assertion
		// that makes the bound real rather than a no-op: a Lookup that re-read the
		// vault unconditionally would resolve here and fail this line.
		_, ok = index.Lookup("session-late")
		Expect(ok).To(BeFalse())

		advanceClock()
		task, ok := index.Lookup("session-late")
		Expect(ok).To(BeTrue())
		Expect(task.Name).To(Equal("Late Task"))
		Expect(task.Path).To(Equal("25 Tasks/Late Task.md"))
	})

	It("resolves the goal and the topic for a task written after the build", func() {
		// ⚠️ The whole reason the refresh re-runs all three index steps rather than
		// the task walk alone: the goal rung and the topic rung are built before the
		// walk, and a task naming a goal that did not exist at construction would
		// resolve no goal if only the walk were re-run.
		vault := GinkgoT().TempDir()
		index := pkg.NewTaskIndex(ctx, vault, clock)

		writeVaultTask(vault, "New Goal Task.md",
			"---\nclaude_session_id: session-late-goal\ngoals:\n  - \"[[New Goal]]\"\n---\n")
		writeVaultFile(vault, "24 Goals", "New Goal.md", "---\ntitle: New Goal\n---\n")
		writeVaultFile(vault, "23 Topics", "New Topic.md",
			"---\ntitle: New Topic\n---\n\n## Goals\n\n- [[New Goal]]\n")

		advanceClock()
		task, ok := index.Lookup("session-late-goal")
		Expect(ok).To(BeTrue())
		Expect(task.GoalName).To(Equal("New Goal"))
		Expect(task.GoalPath).To(Equal("24 Goals/New Goal.md"))
		Expect(task.TopicName).To(Equal("New Topic"))
		Expect(task.TopicPath).To(Equal("23 Topics/New Topic.md"))
	})

	It("yields no entry and no error when the tasks directory is unreadable", func() {
		// The soft-failure rule survives the refresh: a vault with no `25 Tasks/`
		// resolves nothing rather than failing, and a rebuild that reads no tasks
		// installs an empty index rather than returning an error.
		vault := GinkgoT().TempDir()
		index := pkg.NewTaskIndex(ctx, vault, clock)

		advanceClock()
		task, ok := index.Lookup("session-none")
		Expect(ok).To(BeFalse())
		Expect(task.Name).To(BeEmpty())
	})

	It("keeps serving earlier entries when a refresh is cancelled", func() {
		vault := GinkgoT().TempDir()
		writeVaultTask(vault, "Kept.md", "---\nclaude_session_id: session-kept\n---\n")
		buildCtx, cancel := context.WithCancel(ctx)
		index := pkg.NewTaskIndex(buildCtx, vault, clock)

		task, ok := index.Lookup("session-kept")
		Expect(ok).To(BeTrue())
		Expect(task.Name).To(Equal("Kept"))

		// The build context is cancelled and the vault changes before the window
		// lapses. The refresh the next Lookup triggers stops early, and its partial
		// index must be discarded rather than installed — so the earlier entry
		// keeps resolving instead of being replaced by an empty index.
		cancel()
		writeVaultTask(vault, "New.md", "---\nclaude_session_id: session-new\n---\n")
		advanceClock()

		task, ok = index.Lookup("session-kept")
		Expect(ok).To(BeTrue())
		Expect(task.Name).To(Equal("Kept"))
		_, ok = index.Lookup("session-new")
		Expect(ok).To(BeFalse())
	})

	It("keeps serving earlier entries when a refresh cannot read the tasks directory", func() {
		// ⚠️ The data-loss window this spec closes. The build fails soft on I/O, so
		// an unreadable `25 Tasks/` yields no entries rather than an error — and
		// before the read report existed, that empty rebuild was installed anyway,
		// replacing a fully resolved index with an empty one. The directory is
		// removed rather than chmod 000: absence is how the repo's other specs
		// produce an unreadable `25 Tasks/`, and a permission bit is not enforced
		// when the suite runs as root.
		vault := GinkgoT().TempDir()
		writeVaultTask(vault, "Kept.md", "---\nclaude_session_id: session-kept\n---\n")
		index := pkg.NewTaskIndex(ctx, vault, clock)

		task, ok := index.Lookup("session-kept")
		Expect(ok).To(BeTrue())
		Expect(task.Name).To(Equal("Kept"))

		// The tasks directory is gone before the window lapses, so the next
		// Lookup's rebuild reads no tasks. Its result must be discarded rather
		// than installed — the earlier entry keeps resolving instead of being
		// replaced by an empty index.
		Expect(os.RemoveAll(filepath.Join(vault, "25 Tasks"))).To(Succeed())
		advanceClock()

		task, ok = index.Lookup("session-kept")
		Expect(ok).To(BeTrue(),
			"a failed task walk must not wipe the previously resolved index")
		Expect(task.Name).To(Equal("Kept"))
		Expect(task.Path).To(Equal("25 Tasks/Kept.md"))
	})

	It("does not race when lookups run against a refresh", func() {
		// ⚠️ make precommit runs with -race=false, so a data race between a
		// refresh's map swap and a concurrent Lookup would not be reported here.
		// This case drives many lookups through one index while the window has
		// already lapsed, so the winner's rebuild and its swap race the reads — the
		// RWMutex guard is exercised under contention rather than only serially.
		vault := GinkgoT().TempDir()
		writeVaultTask(vault, "Conc.md", "---\nclaude_session_id: session-conc\n---\n")
		index := pkg.NewTaskIndex(ctx, vault, clock)

		// Lapse the window once before the fan-out, so every goroutine's first
		// lookup finds the index stale and observes the same lapsed window. Only
		// the first to claim the token rebuilds; the rest are served the index
		// already installed.
		advanceClock()

		// Results are collected per goroutine and asserted after the fan-out:
		// Ginkgo assertions are not safe to make from the runner's goroutines.
		// Each goroutine writes its own slot, so the slice needs no lock.
		const workers = 8
		names := make([]string, workers)
		oks := make([]bool, workers)

		funcs := make([]run.Func, 0, workers)
		for i := 0; i < workers; i++ {
			funcs = append(funcs, func(ctx context.Context) error {
				task, ok := index.Lookup("session-conc")
				names[i] = task.Name
				oks[i] = ok
				return nil
			})
		}
		Expect(run.CancelOnFirstErrorWait(ctx, funcs...)).To(BeNil())
		for i := 0; i < workers; i++ {
			Expect(oks[i]).To(BeTrue())
			Expect(names[i]).To(Equal("Conc"))
		}
	})

	It("runs exactly one rebuild when concurrent lookups observe a lapsed window", func() {
		// ⚠️ The guard the render loop depends on. Lookup is reached once per item
		// from the resolver's render loop, so when the window lapses every
		// concurrent render would otherwise start its own full-vault rebuild —
		// over 8,000 task files read synchronously inside each request. The
		// staleness check and the token claim happen in ONE critical section, so a
		// burst of lookups that all observe the lapsed window triggers exactly one
		// rebuild: the winner rebuilds synchronously, and the rest are served the
		// index already installed rather than queueing behind the read.
		//
		// ⚠️ The count is read off the index directly, not through the injected
		// clock: both the staleness check and the install read the clock and the
		// interleaving is nondeterministic, so counting clock reads would be flaky.
		vault := GinkgoT().TempDir()
		writeVaultTask(vault, "Conc.md", "---\nclaude_session_id: session-conc\n---\n")
		index := pkg.NewTaskIndex(ctx, vault, clock)

		counter, ok := index.(interface{ RebuildCount() int })
		Expect(ok).To(BeTrue(), "the task index must expose its rebuild count")
		Expect(counter.RebuildCount()).To(Equal(0), "the boot build is not a rebuild")

		// Written after the boot build, so a lookup that resolves it proves the
		// winner's rebuild was installed rather than merely started.
		writeVaultTask(vault, "Late.md", "---\nclaude_session_id: session-late\n---\n")

		// Lapse the window once before the fan-out, so every goroutine's first
		// lookup observes the same lapsed window.
		advanceClock()

		// Results are collected per goroutine and asserted after the fan-out:
		// Ginkgo assertions are not safe to make from the runner's goroutines.
		const workers = 8
		oks := make([]bool, workers)
		funcs := make([]run.Func, 0, workers)
		for i := 0; i < workers; i++ {
			funcs = append(funcs, func(ctx context.Context) error {
				_, ok := index.Lookup("session-conc")
				oks[i] = ok
				return nil
			})
		}
		Expect(run.CancelOnFirstErrorWait(ctx, funcs...)).To(BeNil())

		Expect(counter.RebuildCount()).To(Equal(1),
			"concurrent lookups past the window started more than one rebuild")
		for i := 0; i < workers; i++ {
			Expect(oks[i]).To(BeTrue(),
				"a lookup served no index while a rebuild was in flight")
		}

		// The winner's rebuild is installed, so a task written after the boot build
		// resolves on a later lookup.
		task, ok := index.Lookup("session-late")
		Expect(ok).To(BeTrue())
		Expect(task.Name).To(Equal("Late"))
		Expect(counter.RebuildCount()).To(Equal(1),
			"a later lookup inside the fresh window must not rebuild again")
	})
})

// Resolved is the gate the page's provenance line hangs on: the template renders
// that line only when it is true, so a fact that does not set it is a fact the
// page can never show however correctly it was resolved and drawn.
var _ = Describe("Provenance.Resolved", func() {
	It("is true for a provenance carrying only a task name", func() {
		// The shape an item resolves to when its producer wrote no event log but
		// its session's task file was found: no host, cwd, tool or pane, and a
		// task. Without the task in the gate this row renders no provenance line
		// at all, so the name is resolved correctly, drawn correctly, and never
		// appears.
		Expect((pkg.Provenance{TaskName: "Fix the board"}).Resolved()).To(BeTrue())
	})

	It("is true for a provenance carrying only a session name", func() {
		// The second member of the set that comes from outside the event log,
		// and the same trap as the task name: a session resolving a `user` name
		// and nothing else about its origin is what 186 of the live board's
		// 1,095 rows looked like on 2026-09-30 — and without this conjunct the
		// name is resolved correctly, drawn correctly, and never appears because
		// the line that would carry it is suppressed.
		Expect((pkg.Provenance{SessionName: "Board Polish Session"}).Resolved()).To(BeTrue())
	})

	It("is false for the zero value", func() {
		// The degradation the page must keep: an item with no provenance source
		// renders exactly what it rendered before the line existed.
		Expect((pkg.Provenance{}).Resolved()).To(BeFalse())
	})
})
