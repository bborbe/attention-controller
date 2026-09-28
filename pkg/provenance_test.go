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
		paneLister = &mocks.PaneLister{}
		paneLister.ListReturns(map[int]pkg.Pane{}, nil)
		// An empty vault: the specs below are about the event log, the registry
		// and the pane listing, so no task resolves and TaskName/TaskPath stay
		// empty. The vault itself is covered by the TaskIndex specs.
		resolver = pkg.NewProvenanceResolver(
			stateDir,
			sessionsDir,
			paneLister,
			pkg.NewTaskIndex(ctx, GinkgoT().TempDir()),
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
			paneLister,
			pkg.NewTaskIndex(ctx, GinkgoT().TempDir()),
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

	// withVault is the BeforeEach resolver plus a task index over vault. The
	// vault is per-spec rather than shared, so each case's fixture is the only
	// thing its index can see.
	withVault := func(vault string) pkg.ProvenanceResolver {
		return pkg.NewProvenanceResolver(
			stateDir,
			sessionsDir,
			paneLister,
			pkg.NewTaskIndex(ctx, vault),
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
		nilIndex := pkg.NewProvenanceResolver(stateDir, sessionsDir, paneLister, nil)

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
})

// writeVaultTask writes one task file under <vault>/25 Tasks/, the directory the
// index reads. Written as raw text rather than through a parser, so the fixture
// is the *file shape* the vault actually holds.
func writeVaultTask(vault, name, content string) {
	dir := filepath.Join(vault, "25 Tasks")
	Expect(os.MkdirAll(dir, 0o750)).To(BeNil())
	Expect(os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600)).To(BeNil())
}

var _ = Describe("TaskIndex", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	// Each entry builds its own vault and returns the directory the index is
	// built from, so the "does not exist" case can point the index at a path
	// nothing ever created.
	DescribeTable("resolves the task recorded for a session",
		func(build func(root string) string, sessionID, wantName, wantPath string, wantOK bool) {
			vault := build(GinkgoT().TempDir())

			task, ok := pkg.NewTaskIndex(ctx, vault).Lookup(sessionID)

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

	It("is false for the zero value", func() {
		// The degradation the page must keep: an item with no provenance source
		// renders exactly what it rendered before the line existed.
		Expect((pkg.Provenance{}).Resolved()).To(BeFalse())
	})
})
