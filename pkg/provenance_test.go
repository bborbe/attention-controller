// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"os"
	"path/filepath"

	"github.com/bborbe/errors"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

var _ = Describe("ProvenanceResolver", func() {
	var ctx context.Context
	var stateDir string
	var sessionsDir string
	var paneLister *mocks.PaneLister
	var resolver pkg.ProvenanceResolver

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

	item := func(itemID, producerID, dedupKey string) pkg.Item {
		return pkg.Item{
			ItemID:     pkg.ItemID(itemID),
			ProducerID: pkg.ProducerID(producerID),
			DedupKey:   pkg.DedupKey(dedupKey),
		}
	}

	BeforeEach(func() {
		ctx = context.Background()
		stateDir = GinkgoT().TempDir()
		sessionsDir = GinkgoT().TempDir()
		paneLister = &mocks.PaneLister{}
		paneLister.ListReturns(map[int]pkg.Pane{}, nil)
		resolver = pkg.NewProvenanceResolver(stateDir, sessionsDir, paneLister)
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

	It("resolves nothing for an item whose producer wrote no event log", func() {
		resolved := resolver.Resolve(
			ctx,
			pkg.Items{item("item-6", "producer-absent", "key-absent")},
		)
		provenance := resolved[pkg.ItemID("item-6")]

		Expect(provenance.Resolved()).To(BeFalse())
		Expect(provenance.PaneRecorded).To(BeFalse())
	})

	It("resolves nothing when the state directory is unavailable", func() {
		// The standalone case: a store running for k8s agents, cron jobs or
		// dark-factory runs has no Claude Code state directory at all.
		unavailable := pkg.NewProvenanceResolver(
			filepath.Join(stateDir, "does-not-exist"),
			filepath.Join(sessionsDir, "does-not-exist"),
			paneLister,
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
})
