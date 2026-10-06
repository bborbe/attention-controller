// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"os"
	"path/filepath"

	"github.com/bborbe/errors"
	"github.com/bborbe/run"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

// This file is the probe for the event-log read path's cost.
//
// The read used to re-scan a producer's whole log on every render. The log is
// append-only and unbounded, so the cost of one board render grew every day the
// service ran — measured at 61.30 % of board-render CPU. The resolver now
// remembers a byte offset per producer and reads only what was appended since
// the last render.
//
// ⚠️ The freshness guarantee is preserved, not traded: the offset advances by
// READING the file, never by waiting for a notification or a timer. These specs
// pin both halves — that an unchanged log costs nothing, and that a just-
// appended line still resolves on the very next render.

// countingEventLogReader wraps the real reader and counts every incremental
// read it is asked for, so a spec can assert that a render over an unchanged
// log reads nothing at all.
//
// ⚠️ It counts bytes as well as calls, and both assertions matter: a plain
// time-based cache would pass a "no re-scan" count on its own, so the byte
// counter is what tells a real incremental read apart from a render that still
// reads the whole file but is merely not asked twice.
type countingEventLogReader struct {
	inner     pkg.EventLogReader
	reads     int
	bytesRead int

	// readErr, when set, is returned by ReadFrom instead of reading the file.
	// The read-error branch is driven through this seam rather than with a
	// mode-000 file: a permission bit is not enforced when the suite runs as
	// root, which is the same reason the task-index specs remove the directory
	// instead of chmod'ing it.
	readErr error
}

// Size delegates to the real reader without counting — a stat is not a read.
func (c *countingEventLogReader) Size(producerID pkg.ProducerID) (int64, bool) {
	return c.inner.Size(producerID)
}

// ReadFrom counts the call and the bytes it returned, then delegates.
func (c *countingEventLogReader) ReadFrom(
	ctx context.Context,
	producerID pkg.ProducerID,
	offset int64,
) ([]byte, error) {
	c.reads++
	if c.readErr != nil {
		return nil, c.readErr
	}
	data, err := c.inner.ReadFrom(ctx, producerID, offset)
	c.bytesRead += len(data)
	return data, err
}

var _ = Describe("EventLogReader", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	// An empty state dir, an unopenable root and an absent file are all the
	// ordinary "no log" case, never an error.
	It("reports no log for an empty state dir, a missing root and an absent file", func() {
		empty := pkg.NewEventLogReader("")
		_, ok := empty.Size(pkg.ProducerID("p"))
		Expect(ok).To(BeFalse())
		data, err := empty.ReadFrom(ctx, pkg.ProducerID("p"), 0)
		Expect(err).To(BeNil())
		Expect(data).To(BeEmpty())

		missingRoot := pkg.NewEventLogReader(filepath.Join(GinkgoT().TempDir(), "missing"))
		_, ok = missingRoot.Size(pkg.ProducerID("p"))
		Expect(ok).To(BeFalse())
		data, err = missingRoot.ReadFrom(ctx, pkg.ProducerID("p"), 0)
		Expect(err).To(BeNil())
		Expect(data).To(BeEmpty())

		readable := pkg.NewEventLogReader(GinkgoT().TempDir())
		_, ok = readable.Size(pkg.ProducerID("p"))
		Expect(ok).To(BeFalse())
		data, err = readable.ReadFrom(ctx, pkg.ProducerID("p"), 0)
		Expect(err).To(BeNil())
		Expect(data).To(BeEmpty())
	})

	It("reads the bytes beyond the offset", func() {
		dir := GinkgoT().TempDir()
		Expect(os.WriteFile(
			filepath.Join(dir, "p.events.jsonl"),
			[]byte("abcdef\n"),
			0o600,
		)).To(BeNil())
		reader := pkg.NewEventLogReader(dir)

		size, ok := reader.Size(pkg.ProducerID("p"))
		Expect(ok).To(BeTrue())
		Expect(size).To(Equal(int64(7)))

		data, err := reader.ReadFrom(ctx, pkg.ProducerID("p"), 3)
		Expect(err).To(BeNil())
		Expect(string(data)).To(Equal("def\n"))
	})
})

var _ = Describe("Event log incremental read", func() {
	var ctx context.Context
	var stateDir string
	var sessionsDir string
	var spawnDir string
	var paneLister *mocks.PaneLister
	var counting *countingEventLogReader
	var resolver pkg.ProvenanceResolver
	var clock libtime.CurrentDateTime

	// eventLine writes one event-log line, as raw JSON so the fixture is the
	// file shape the watcher writes rather than a marshalled struct.
	eventLine := func(itemID, sessionID, host, cwd, tool, pane string) string {
		return `{"item_id":"` + itemID + `","session_id":"` + sessionID + `","host":"` + host +
			`","cwd":"` + cwd + `","tool_name":"` + tool + `","pane":"` + pane + `"}` + "\n"
	}

	// writeEvents replaces a producer's log with content.
	writeEvents := func(producerID, content string) {
		Expect(os.WriteFile(
			filepath.Join(stateDir, producerID+".events.jsonl"),
			[]byte(content),
			0o600,
		)).To(BeNil())
	}

	// appendEvents adds content to a producer's log without truncating it.
	appendEvents := func(producerID, content string) {
		file, err := os.OpenFile(
			filepath.Join(stateDir, producerID+".events.jsonl"),
			os.O_APPEND|os.O_WRONLY|os.O_CREATE,
			0o600,
		)
		Expect(err).To(BeNil())
		defer func() { Expect(file.Close()).To(BeNil()) }()
		_, err = file.WriteString(content)
		Expect(err).To(BeNil())
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
		spawnDir = GinkgoT().TempDir()
		paneLister = &mocks.PaneLister{}
		paneLister.ListReturns(map[int]pkg.Pane{}, nil)
		clock = libtime.NewCurrentDateTime()
		clock.SetNow(clock.Now())
		counting = &countingEventLogReader{inner: pkg.NewEventLogReader(stateDir)}
		resolver = pkg.NewProvenanceResolver(
			counting,
			sessionsDir,
			spawnDir,
			paneLister,
			pkg.NewTaskIndex(ctx, GinkgoT().TempDir(), clock),
			clock,
		)
	})

	// ⚠️ The spec that makes the read-count criterion falsifiable. A second
	// render over an unchanged log must read NOTHING, and must still resolve the
	// item — a plain time-based cache would pass the count but fail the resolve.
	It("reads no bytes on a second render over an unchanged log", func() {
		writeEvents("producer-a", eventLine("key-a", "producer-a", "burn", "/w/a", "", "11"))
		items := pkg.Items{item("item-a", "producer-a", "key-a")}

		first := resolver.Resolve(ctx, items)
		Expect(first[pkg.ItemID("item-a")].Host).To(Equal("burn"))

		counting.reads = 0
		counting.bytesRead = 0
		second := resolver.Resolve(ctx, items)

		Expect(counting.reads).To(Equal(0),
			"a render over an unchanged log must read nothing")
		Expect(counting.bytesRead).To(Equal(0),
			"a render over an unchanged log must read no bytes")
		Expect(second[pkg.ItemID("item-a")].Host).To(Equal("burn"),
			"and it must still resolve the item from the accumulated state")
	})

	It("resolves an item appended after an earlier render", func() {
		writeEvents("producer-b", eventLine("key-one", "producer-b", "burn", "/w/one", "", "1"))
		Expect(resolver.Resolve(ctx, pkg.Items{
			item("item-one", "producer-b", "key-one"),
		})[pkg.ItemID("item-one")].Host).To(Equal("burn"))

		appendEvents("producer-b", eventLine("key-two", "producer-b", "burn", "/w/two", "", "2"))

		resolved := resolver.Resolve(ctx, pkg.Items{
			item("item-two", "producer-b", "key-two"),
		})
		Expect(resolved[pkg.ItemID("item-two")].Host).To(Equal("burn"))
		Expect(resolved[pkg.ItemID("item-two")].Cwd).To(Equal("/w/two"))
	})

	// ⚠️ The boundary case: the first render has already read the log to EOF, so
	// the offset sits at the file's end. The appended line must resolve on the
	// FIRST render after it lands — the offset advanced by reading, not by a
	// notification or a timer.
	It("resolves a line appended after the offset reached EOF", func() {
		writeEvents("producer-c", eventLine("key-c1", "producer-c", "burn", "/w/c1", "", "1"))
		items := pkg.Items{item("item-c1", "producer-c", "key-c1")}
		Expect(resolver.Resolve(ctx, items)[pkg.ItemID("item-c1")].Host).To(Equal("burn"))

		// The offset is at EOF now: an unchanged render reads nothing.
		counting.reads = 0
		Expect(resolver.Resolve(ctx, items)[pkg.ItemID("item-c1")].Host).To(Equal("burn"))
		Expect(counting.reads).To(Equal(0))

		appendEvents("producer-c", eventLine("key-c2", "producer-c", "burn", "/w/c2", "", "2"))

		resolved := resolver.Resolve(ctx, pkg.Items{
			item("item-c2", "producer-c", "key-c2"),
		})
		Expect(resolved[pkg.ItemID("item-c2")].Host).To(Equal("burn"),
			"a just-appended line must resolve on the first render after it lands")
		Expect(resolved[pkg.ItemID("item-c2")].Cwd).To(Equal("/w/c2"))
	})

	// ⚠️ A torn final line — a write caught mid-line — must be left unconsumed
	// until it completes, and then consumed exactly once.
	It("leaves a torn final line unconsumed until it completes", func() {
		writeEvents("producer-torn",
			`{"item_id":"key-torn","session_id":"producer-torn","host":"burn"`)
		items := pkg.Items{item("item-torn", "producer-torn", "key-torn")}

		Expect(resolver.Resolve(ctx, items)[pkg.ItemID("item-torn")].Host).To(BeEmpty(),
			"a partial line must not resolve")

		appendEvents("producer-torn", `,"cwd":"/w/torn","tool_name":"","pane":"44"}`+"\n")

		resolved := resolver.Resolve(ctx, items)[pkg.ItemID("item-torn")]
		Expect(resolved.Host).To(Equal("burn"))
		Expect(resolved.Cwd).To(Equal("/w/torn"))

		// Consumed exactly once: a further render reads nothing and still
		// resolves the same single record.
		counting.reads = 0
		counting.bytesRead = 0
		again := resolver.Resolve(ctx, items)[pkg.ItemID("item-torn")]
		Expect(counting.reads).To(Equal(0))
		Expect(counting.bytesRead).To(Equal(0))
		Expect(again.Host).To(Equal("burn"))
		Expect(again.Cwd).To(Equal("/w/torn"))
	})

	// ⚠️ The second assertion is load-bearing: it pins the reset of the
	// accumulated map, not merely the re-read. A log that shrank below the
	// remembered offset must be re-read from the start, and the content that
	// only the pre-truncation log carried must be gone.
	It("re-reads a log that shrank below the offset", func() {
		writeEvents("producer-s",
			eventLine("key-old", "producer-s", "oldhost", "/w/old", "", "1")+
				eventLine("key-old-2", "producer-s", "oldhost", "/w/old2", "", "2"))
		itemsOld := pkg.Items{item("item-old", "producer-s", "key-old")}
		Expect(resolver.Resolve(ctx, itemsOld)[pkg.ItemID("item-old")].Host).To(Equal("oldhost"))

		// Rewrite with SHORTER, different content.
		writeEvents("producer-s", eventLine("key-new", "producer-s", "newhost", "/w/new", "", "3"))

		Expect(resolver.Resolve(ctx, pkg.Items{
			item("item-new", "producer-s", "key-new"),
		})[pkg.ItemID("item-new")].Host).To(Equal("newhost"),
			"a shrunken log must be re-read from the start")

		Expect(resolver.Resolve(ctx, itemsOld)[pkg.ItemID("item-old")].Host).To(BeEmpty(),
			"the pre-truncation content is gone, so its item no longer resolves")
	})

	// ⚠️ The only spec that drives ReadFrom's error branch rather than its
	// absent-log branch: the log exists and stats fine, but reading it fails, so
	// the render must fail soft — resolve nothing, neither panic nor return an
	// error. The failure is injected through the reader rather than produced
	// with a mode-000 file: a permission bit is not enforced when the suite runs
	// as root, so a chmod-based spec would pass vacuously there.
	It("fails soft when the event log cannot be read", func() {
		writeEvents("producer-e", eventLine("key-e", "producer-e", "burn", "/w/e", "", "5"))
		counting.readErr = errors.New(ctx, "injected read failure")

		items := pkg.Items{item("item-e", "producer-e", "key-e")}
		Expect(func() { resolver.Resolve(ctx, items) }).NotTo(Panic())
		// ⚠️ The stat still succeeds, so the read is attempted and its error is
		// what fails soft — not the absent-log path.
		Expect(counting.reads).To(Equal(1),
			"the unreadable log must be read and its error handled, not skipped as absent")
		Expect(resolver.Resolve(ctx, items)[pkg.ItemID("item-e")].Host).To(BeEmpty(),
			"an unreadable log proves nothing, so nothing resolves")
	})

	// The dedup-key join must survive the incremental rewrite: two items of one
	// producer, read from a single log, each keep their own host and cwd.
	It("gives each of a producer's items its own provenance from one log", func() {
		writeEvents("producer-m",
			eventLine("key-one", "producer-m", "burn", "/w/one", "", "11")+
				eventLine("key-two", "producer-m", "burn", "/w/two", "", "22"))

		resolved := resolver.Resolve(ctx, pkg.Items{
			item("item-1", "producer-m", "key-one"),
			item("item-2", "producer-m", "key-two"),
		})

		Expect(resolved[pkg.ItemID("item-1")].Cwd).To(Equal("/w/one"))
		Expect(resolved[pkg.ItemID("item-2")].Cwd).To(Equal("/w/two"))
	})

	// The decode skips the lines that carry no record, so a malformed or
	// id-less line does not cost the good lines beside it.
	It("skips blank, malformed and id-less lines and resolves the rest", func() {
		writeEvents("producer-skip",
			"\n"+
				"not json at all\n"+
				`{"session_id":"producer-skip","host":"burn"}`+"\n"+
				eventLine("key-good", "producer-skip", "burn", "/w/good", "", "9"))

		resolved := resolver.Resolve(ctx, pkg.Items{
			item("item-good", "producer-skip", "key-good"),
		})
		Expect(resolved[pkg.ItemID("item-good")].Host).To(Equal("burn"))
		Expect(resolved[pkg.ItemID("item-good")].Cwd).To(Equal("/w/good"))
	})

	// ⚠️ make precommit runs with -race=false, so the copy-on-write rule that
	// keeps a published map immutable is otherwise unverified. This drives
	// several concurrent renders through one resolver while a line is appended,
	// so a shared-map mutation would be exercised under contention.
	It("does not race when concurrent renders read a log being appended to", func() {
		writeEvents(
			"producer-race",
			eventLine("key-race", "producer-race", "burn", "/w/r", "", "7"),
		)
		items := pkg.Items{item("item-race", "producer-race", "key-race")}
		Expect(resolver.Resolve(ctx, items)[pkg.ItemID("item-race")].Host).To(Equal("burn"))

		funcs := make([]run.Func, 0, 9)
		for i := 0; i < 8; i++ {
			funcs = append(funcs, func(ctx context.Context) error {
				resolver.Resolve(ctx, items)
				return nil
			})
		}
		// The append returns its error rather than asserting, because a Gomega
		// assertion must run on the spec goroutine, not inside a run.Func.
		funcs = append(funcs, func(ctx context.Context) error {
			file, err := os.OpenFile(
				filepath.Join(stateDir, "producer-race.events.jsonl"),
				os.O_APPEND|os.O_WRONLY|os.O_CREATE,
				0o600,
			)
			if err != nil {
				return err
			}
			defer file.Close()
			_, err = file.WriteString(
				eventLine("key-race-2", "producer-race", "burn", "/w/r2", "", "8"),
			)
			return err
		})

		Expect(run.CancelOnFirstErrorWait(ctx, funcs...)).To(BeNil())
		Expect(resolver.Resolve(ctx, items)[pkg.ItemID("item-race")].Host).To(Equal("burn"))

		// ⚠️ The boundary the freshness guarantee is really about, and the half
		// this spec used to leave unpinned: the append raced renders that had
		// already started. Whichever order the two occurred in, the appended line
		// must be visible on the NEXT render — the offset advances by READING the
		// file, never by waiting for a notification, so a render that began before
		// the append cannot leave the line stranded behind a stale offset.
		Expect(resolver.Resolve(ctx, pkg.Items{
			item("item-race-2", "producer-race", "key-race-2"),
		})[pkg.ItemID("item-race-2")].Host).To(Equal("burn"),
			"a line appended during a concurrent render must resolve on the next render")
	})
})
