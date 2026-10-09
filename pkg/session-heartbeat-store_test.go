// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	stdtime "time"

	"github.com/bborbe/errors"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

// The store's load-bearing claims are three, and each is about the fact that it
// is a SHARED directory rather than a private one: a row another writer left
// must still parse, a file that is not a row must not break the listing, and an
// unreadable row for a NAMED session must never be answered as `absent` —
// because `absent` is what renders a live session's card Resume.
var _ = Describe("Session heartbeat store", func() {
	var ctx context.Context
	var dir string
	var store pkg.SessionHeartbeatStore

	// now is the store's clock, and it is MOVABLE so the stamp can be checked
	// rather than raced against a wall clock.
	var now stdtime.Time
	clock := func() libtime.CurrentDateTimeGetter {
		return libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
			return libtime.DateTime(now)
		})
	}

	declaration := func() pkg.SessionHeartbeat {
		return pkg.SessionHeartbeat{
			SessionID: "5f2a1c34-0000-4000-8000-000000000001",
			Task:      "Session Liveness Comes From a Heartbeat Store in attention-controller",
			Vault:     "private-personal",
			Location:  pkg.LocalSessionHeartbeatLocation,
			State:     pkg.IdleSessionHeartbeatState,
			Source:    pkg.MCPTimerSessionHeartbeatSource,
		}
	}

	BeforeEach(func() {
		ctx = context.Background()
		now = stdtime.Date(2026, 10, 8, 9, 0, 0, 0, stdtime.UTC)
		var err error
		dir, err = os.MkdirTemp("", "session-heartbeat-store-*")
		Expect(err).To(BeNil())
		store = pkg.NewSessionHeartbeatStore(dir, clock())
	})

	AfterEach(func() {
		Expect(os.RemoveAll(dir)).To(BeNil())
	})

	Describe("Post and Get", func() {
		It("round-trips a declaration", func() {
			Expect(store.Post(ctx, declaration())).To(BeNil())
			got, found, err := store.Get(ctx, "5f2a1c34-0000-4000-8000-000000000001")
			Expect(err).To(BeNil())
			Expect(found).To(BeTrue())
			Expect(got.SessionID).To(Equal("5f2a1c34-0000-4000-8000-000000000001"))
			Expect(got.Task).To(Equal(declaration().Task))
			Expect(got.Location).To(Equal(pkg.LocalSessionHeartbeatLocation))
			Expect(got.Source).To(Equal(pkg.MCPTimerSessionHeartbeatSource))
		})

		It("stamps At from its OWN clock, ignoring anything the caller set", func() {
			// A session that could set its own timestamp could declare itself
			// alive indefinitely — the liveness check deleted by the party it
			// checks.
			caller := declaration()
			caller.At = libtime.DateTime(stdtime.Date(2099, 1, 1, 0, 0, 0, 0, stdtime.UTC))
			Expect(store.Post(ctx, caller)).To(BeNil())
			got, _, err := store.Get(ctx, caller.SessionID)
			Expect(err).To(BeNil())
			Expect(stdtime.Time(got.At).UTC()).To(Equal(now))
		})

		It("reports an unknown session as not found, with NO error", func() {
			// The distinction the read path needs: `absent` is a real answer,
			// not a failure.
			_, found, err := store.Get(ctx, "never-posted")
			Expect(err).To(BeNil())
			Expect(found).To(BeFalse())
		})

		It("keeps ONE row per session, overwriting the previous declaration", func() {
			Expect(store.Post(ctx, declaration())).To(BeNil())
			next := declaration()
			next.State = pkg.BusySessionHeartbeatState
			Expect(store.Post(ctx, next)).To(BeNil())

			listed, err := store.List(ctx)
			Expect(err).To(BeNil())
			Expect(listed).To(HaveLen(1))
			Expect(listed[0].State).To(Equal(pkg.BusySessionHeartbeatState))
		})

		It("writes the activity under `activity`, never under the readers' `state` key", func() {
			// ⚠️ THE GUARD. This directory is shared with four readers that own
			// the word `state` for a LIVENESS VERDICT and gate on
			// `stamp.get("state", "live") != "live"` — `worker-sessions.py`,
			// `fleet-board.py`, `adopt-orphans.py` and `session-liveness.py` —
			// where a MISSING key must read as `live` rather than vanish.
			//
			// ⚠️ **This comment first claimed a `state: "busy"` row "reads as
			// NOT-live" and frees a duplicate auto-resume. That was wrong** — no
			// reader consumes a raw stamp's `state`; they all read a dict that the
			// readers' own chokepoint synthesizes. The key is write-only today,
			// which is exactly what makes the rename cheap now and expensive once a
			// raw-stamp reader exists. The WIRE keeps the name `state` (its contract
			// has no such collision), so only the file's key is asserted here.
			Expect(store.Post(ctx, declaration())).To(BeNil())
			entries, err := os.ReadDir(dir)
			Expect(err).To(BeNil())
			Expect(entries).To(HaveLen(1))
			raw, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
			Expect(err).To(BeNil())
			var onDisk map[string]any
			Expect(json.Unmarshal(raw, &onDisk)).To(BeNil())
			Expect(onDisk["activity"]).To(Equal("idle"))
			Expect(onDisk).ToNot(HaveKey("state"))
		})

		It(
			"still parses a row written before the rename, and surfaces it with an empty State",
			func() {
				// ⚠️ The rename is ONE-DIRECTIONAL, and this pins the direction it does
				// not cover. A row written by the PREVIOUS RELEASE of this store carries
				// `state` and no `activity`; it parses, but `toHeartbeat` reads
				// `r.Activity`, so the activity is gone rather than repaired by a
				// fallback. That is deliberate — a `state` fallback would keep the
				// collision key alive on the READ path, which is exactly what this
				// rename removes. Pinned so a later fallback refactor is a visible
				// change rather than a silent one.
				legacy := `{"sessionId":"5f2a1c34-0000-4000-8000-000000000001","pid":4242,` +
					`"mode":"local","at":"2026-10-08T09:00:00Z","source":"mcp-timer",` +
					`"location":"local","state":"busy"}`
				Expect(os.WriteFile(
					filepath.Join(dir, "5f2a1c34-0000-4000-8000-000000000001.json"),
					[]byte(legacy), 0600,
				)).To(BeNil())

				got, found, err := store.Get(ctx, "5f2a1c34-0000-4000-8000-000000000001")
				Expect(err).To(BeNil())
				Expect(found).To(BeTrue(), "a legacy row must still parse, not vanish")
				// ⚠️ The advertised claim is that the row still PARSES, so the fields that
				// were never renamed have to be asserted too — `found` alone passes on a
				// row that parsed into a zero-valued struct.
				Expect(got.SessionID).To(Equal("5f2a1c34-0000-4000-8000-000000000001"))
				Expect(got.Location).To(Equal(pkg.LocalSessionHeartbeatLocation))
				Expect(got.Source.String()).To(Equal("mcp-timer"))
				Expect(
					stdtime.Time(got.At).UTC(),
				).To(Equal(stdtime.Date(2026, 10, 8, 9, 0, 0, 0, stdtime.UTC)))
				Expect(string(got.State)).To(BeEmpty())
			},
		)

		It("lists a legacy row alongside a new-format one", func() {
			// ⚠️ Mixed-format directories are the REALISTIC mid-deploy state, not a
			// corner case: the rename is one-directional and the writer-side half
			// ships from another repo, so both shapes coexist until a session posts
			// again. `List` funnels through the same projection `Get` does, so this is
			// correct by construction — which is exactly why it is pinned rather than
			// assumed: the directory scan is the path a future format change breaks
			// silently.
			//
			// ⚠️ A DIFFERENT session id from `declaration()`. Reusing it would make
			// `Post` overwrite the legacy row, and the spec would pass on one row.
			legacy := `{"sessionId":"5f2a1c34-0000-4000-8000-000000000002","pid":4242,` +
				`"mode":"local","at":"2026-10-08T09:00:00Z","source":"mcp-timer",` +
				`"location":"local","state":"busy"}`
			Expect(os.WriteFile(
				filepath.Join(dir, "5f2a1c34-0000-4000-8000-000000000002.json"),
				[]byte(legacy), 0600,
			)).To(BeNil())
			Expect(store.Post(ctx, declaration())).To(BeNil())

			listed, err := store.List(ctx)
			Expect(err).To(BeNil())
			Expect(listed).To(HaveLen(2), "both shapes must survive the same scan")

			byID := map[string]pkg.SessionHeartbeat{}
			for _, row := range listed {
				byID[row.SessionID] = row
			}
			Expect(byID).To(HaveKey("5f2a1c34-0000-4000-8000-000000000001"))
			legacyRow, ok := byID["5f2a1c34-0000-4000-8000-000000000002"]
			Expect(ok).To(BeTrue(), "the legacy row must survive a directory scan")
			Expect(string(legacyRow.State)).To(BeEmpty())
			Expect(legacyRow.Location).To(Equal(pkg.LocalSessionHeartbeatLocation))
		})

		It("leaves no temp file behind after a write", func() {
			// The rename is what makes a row either wholly old or wholly new; a
			// leftover temp would be a file the listing has to skip forever.
			Expect(store.Post(ctx, declaration())).To(BeNil())
			entries, err := os.ReadDir(dir)
			Expect(err).To(BeNil())
			for _, entry := range entries {
				Expect(entry.Name()).NotTo(HavePrefix("."), entry.Name())
			}
		})
	})

	Describe("session id validation", func() {
		// ⚠️ The id arrives from the network and becomes a FILENAME, so this is
		// the one input that could escape the store directory.
		for _, bad := range []string{"..", ".", "a/b", `a\b`} {
			bad := bad
			It("rejects the id "+bad+" on write", func() {
				h := declaration()
				h.SessionID = bad
				Expect(store.Post(ctx, h)).NotTo(BeNil())
			})

			It("rejects the id "+bad+" on read, as a CLIENT error", func() {
				_, _, err := store.Get(ctx, bad)
				Expect(err).NotTo(BeNil())
				Expect(errors.Is(err, pkg.ErrInvalidSessionID)).To(BeTrue())
			})
		}

		It("writes nothing outside the store directory", func() {
			h := declaration()
			h.SessionID = "../escaped"
			Expect(store.Post(ctx, h)).NotTo(BeNil())
			parent, err := os.ReadDir(filepath.Dir(dir))
			Expect(err).To(BeNil())
			for _, entry := range parent {
				Expect(entry.Name()).NotTo(HaveSuffix("escaped.json"))
			}
		})
	})

	Describe("reading the SHARED directory", func() {
		// ⚠️ This is the claim the whole "extend the existing store, do not build
		// a parallel one" decision rests on: a row the legacy Node writer left
		// must still parse and surface, or the endpoint is blind to exactly the
		// rows it exists to show.
		It("reads a legacy row written by the cluster writer", func() {
			legacy := map[string]any{
				"sessionId": "296786d2-ce1a-43f4-9647-8c5368f7f7fe",
				"pid":       nil,
				"mode":      "cluster",
				"at":        "2026-10-05T20:19:18.090Z",
				"source":    "cluster",
			}
			raw, err := json.Marshal(legacy)
			Expect(err).To(BeNil())
			Expect(os.WriteFile(
				filepath.Join(dir, "296786d2-ce1a-43f4-9647-8c5368f7f7fe.json"), raw, 0600,
			)).To(BeNil())

			got, found, err := store.Get(ctx, "296786d2-ce1a-43f4-9647-8c5368f7f7fe")
			Expect(err).To(BeNil())
			Expect(found).To(BeTrue())
			// ⚠️ Passed through VERBATIM and deliberately not validated against
			// the write vocabulary: `cluster` is not a value this task's
			// producers post, and rejecting it here would hide the row.
			Expect(got.Source.String()).To(Equal("cluster"))
			Expect(got.Task).To(BeEmpty())
		})

		It("skips a non-heartbeat file without failing the listing", func() {
			// The shared directory legitimately carries `_cluster-reachability.json`.
			Expect(os.WriteFile(
				filepath.Join(dir, "_cluster-reachability.json"),
				[]byte(`{"at":"2026-10-08T06:50:01.061Z","stamped":0}`), 0600,
			)).To(BeNil())
			Expect(store.Post(ctx, declaration())).To(BeNil())

			listed, err := store.List(ctx)
			Expect(err).To(BeNil())
			Expect(listed).To(HaveLen(1))
		})

		It("skips an unparseable file without failing the listing", func() {
			Expect(os.WriteFile(
				filepath.Join(dir, "broken.json"), []byte("{"), 0600,
			)).To(BeNil())
			Expect(store.Post(ctx, declaration())).To(BeNil())

			listed, err := store.List(ctx)
			Expect(err).To(BeNil())
			Expect(listed).To(HaveLen(1))
		})

		It("lists rows ordered by session id", func() {
			second := declaration()
			second.SessionID = "00000000-0000-4000-8000-000000000002"
			Expect(store.Post(ctx, declaration())).To(BeNil())
			Expect(store.Post(ctx, second)).To(BeNil())

			listed, err := store.List(ctx)
			Expect(err).To(BeNil())
			Expect(listed).To(HaveLen(2))
			Expect(listed[0].SessionID).To(Equal(second.SessionID))
		})

		It("lists nothing for a directory that does not exist yet", func() {
			missing := pkg.NewSessionHeartbeatStore(
				filepath.Join(dir, "not-created"), clock(),
			)
			listed, err := missing.List(ctx)
			Expect(err).To(BeNil())
			Expect(listed).To(BeEmpty())
		})
	})
})
