// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	stdtime "time"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	"github.com/gorilla/mux"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/handler"
)

const patchLedgerSessionID = "77777777-7777-4777-8777-777777777777"

// fullLedgerRow is a complete row carrying every field a later stop must not
// disturb. The merge is only interesting against a row that HAS fields to lose,
// so the body is deliberately wider than the minimum `Post` accepts.
func fullLedgerRow() map[string]any {
	return map[string]any{
		"session_id": patchLedgerSessionID,
		"spawned_at": stdtime.Date(2026, stdtime.October, 17, 9, 0, 0, 0, stdtime.UTC).
			Format(stdtime.RFC3339),
		"location":           "local",
		"agent_id":           "agent_7",
		"label":              "a worker another server spawned",
		"mode":               "interactive",
		"cwd":                "/tmp/worker",
		"worker_id":          "worker-abc",
		"target":             "docker",
		"spawner_session_id": "11111111-1111-4111-8111-111111111111",
		"role":               "agent",
		"task":               "some task",
		"vault":              "private-personal",
		"status":             "running",
		"parked_permission":  map[string]any{"item_id": "abc"},
	}
}

var _ = Describe("SessionLedgerPatchHandler", func() {
	var ctx context.Context
	var db libkv.DB
	var server *httptest.Server

	// ⚠️ **TWO clients, and they are the point of the spec rather than plumbing.**
	// The contract is that a supervisor which did NOT write the row can still
	// record a field on it, so the spawn goes out on one client and the patch on
	// another. `DisableKeepAlives` is what makes them genuinely separate: sharing a
	// pooled connection would let a transport-level identity (a session, a
	// connection header) survive between the two calls and make the test weaker
	// than it reads.
	var spawner *http.Client
	var stopper *http.Client

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())
		store := pkg.NewSessionLedgerStore(db)
		router := mux.NewRouter()
		router.Path("/api/1.0/session-ledger").
			Methods(http.MethodPost).
			Handler(handler.NewSessionLedgerPostHandler(store))
		router.Path("/api/1.0/session-ledger/{sessionID}").
			Methods(http.MethodGet).
			Handler(handler.NewSessionLedgerGetHandler(store))
		router.Path("/api/1.0/session-ledger/{sessionID}").
			Methods(http.MethodPatch).
			Handler(handler.NewSessionLedgerPatchHandler(store))
		server = httptest.NewServer(router)
		newClient := func() *http.Client {
			return &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
		}
		spawner = newClient()
		stopper = newClient()
	})

	AfterEach(func() {
		server.Close()
		Expect(db.Close()).To(BeNil())
	})

	// do runs one request through the real HTTP handler and returns the status plus
	// the decoded body when there is one.
	do := func(
		client *http.Client,
		method string,
		path string,
		body any,
	) (int, map[string]any) {
		var reader io.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			Expect(err).To(BeNil())
			reader = bytes.NewReader(raw)
		}
		req, err := http.NewRequestWithContext(ctx, method, server.URL+path, reader)
		Expect(err).To(BeNil())
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		Expect(err).To(BeNil())
		defer func() { Expect(res.Body.Close()).To(BeNil()) }()
		decoded := map[string]any{}
		if res.StatusCode == http.StatusOK {
			Expect(json.NewDecoder(res.Body).Decode(&decoded)).To(BeNil())
		}
		return res.StatusCode, decoded
	}

	ledgerPath := "/api/1.0/session-ledger/" + patchLedgerSessionID

	It(
		"records a stop on a row this caller did not write, leaving every other field intact",
		func() {
			status, _ := do(spawner, http.MethodPost, "/api/1.0/session-ledger", fullLedgerRow())
			Expect(status).To(Equal(http.StatusOK))

			// A DIFFERENT client records the stop, naming only `stopped_at`.
			status, patched := do(stopper, http.MethodPatch, ledgerPath, map[string]any{
				"stopped_at": "2026-10-17T09:30:00Z",
			})
			Expect(status).To(Equal(http.StatusOK))
			Expect(patched["stopped_at"]).To(Equal("2026-10-17T09:30:00Z"))

			// The row as a later read sees it: the stop landed and NOTHING the owner
			// wrote was blanked. This is the assertion the whole endpoint exists for —
			// a replace-shaped write would pass the line above and fail this loop.
			status, read := do(stopper, http.MethodGet, ledgerPath, nil)
			Expect(status).To(Equal(http.StatusOK))
			for key, want := range fullLedgerRow() {
				Expect(read[key]).To(Equal(want), "field %s must survive the merge", key)
			}
			Expect(read["stopped_at"]).To(Equal("2026-10-17T09:30:00Z"))
		},
	)

	It("leaves an absent field alone and clears one sent as null", func() {
		status, _ := do(spawner, http.MethodPost, "/api/1.0/session-ledger", fullLedgerRow())
		Expect(status).To(Equal(http.StatusOK))

		// `label` is NOT named and must survive; `parked_permission` is sent as an
		// explicit null and must be gone.
		status, patched := do(stopper, http.MethodPatch, ledgerPath, map[string]any{
			"stopped_at":        "2026-10-17T09:30:00Z",
			"parked_permission": nil,
		})
		Expect(status).To(Equal(http.StatusOK))
		Expect(patched["label"]).To(Equal("a worker another server spawned"))
		Expect(patched).NotTo(HaveKey("parked_permission"))
		Expect(patched["stopped_at"]).To(Equal("2026-10-17T09:30:00Z"))
	})

	It("answers 404 for a row that was never spawned", func() {
		status, _ := do(stopper, http.MethodPatch, ledgerPath, map[string]any{
			"stopped_at": "2026-10-17T09:30:00Z",
		})
		Expect(status).To(Equal(http.StatusNotFound))
	})

	// ⚠️ The identity is path-owned, so a body copy can only disagree with it.
	// Accepting one would mean either discarding the caller's value or moving the
	// row, and both are silent.
	It("refuses a patch that names the identity", func() {
		status, _ := do(spawner, http.MethodPost, "/api/1.0/session-ledger", fullLedgerRow())
		Expect(status).To(Equal(http.StatusOK))
		status, _ = do(stopper, http.MethodPatch, ledgerPath, map[string]any{
			"session_id": "88888888-8888-4888-8888-888888888888",
		})
		Expect(status).To(Equal(http.StatusBadRequest))
	})

	// ⚠️ A typo must not read as a successful write of an unchanged row.
	It("refuses a patch naming a field the row does not have", func() {
		status, _ := do(spawner, http.MethodPost, "/api/1.0/session-ledger", fullLedgerRow())
		Expect(status).To(Equal(http.StatusOK))
		status, _ = do(stopper, http.MethodPatch, ledgerPath, map[string]any{
			"stoped_at": "2026-10-17T09:30:00Z",
		})
		Expect(status).To(Equal(http.StatusBadRequest))
	})

	// `Post` validates every row it stores, so PATCH must not be the one route able
	// to write a row the store's own readers cannot use.
	It("refuses a patch that would leave the row invalid", func() {
		status, _ := do(spawner, http.MethodPost, "/api/1.0/session-ledger", fullLedgerRow())
		Expect(status).To(Equal(http.StatusOK))
		status, _ = do(stopper, http.MethodPatch, ledgerPath, map[string]any{
			"spawned_at": nil,
		})
		Expect(status).To(Equal(http.StatusBadRequest))
	})

	It("refuses a body that is not a JSON object", func() {
		status, _ := do(spawner, http.MethodPost, "/api/1.0/session-ledger", fullLedgerRow())
		Expect(status).To(Equal(http.StatusOK))
		req, err := http.NewRequestWithContext(
			ctx,
			http.MethodPatch,
			server.URL+ledgerPath,
			bytes.NewReader([]byte("{")),
		)
		Expect(err).To(BeNil())
		res, err := stopper.Do(req)
		Expect(err).To(BeNil())
		defer func() { Expect(res.Body.Close()).To(BeNil()) }()
		Expect(res.StatusCode).To(Equal(http.StatusBadRequest))
	})
})
