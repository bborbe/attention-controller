// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The parity spec: this file's coverage rule against claude-supervisor's own,
// over the LIVE fleet, session by session.
//
// ⚠️ It is the evidence the worker-coverage rule is correct, and the only one.
// A rule with two homes drifts silently — one side is amended, the other keeps
// the old behaviour, and a card shows or hides wrongly with no error. Fixture
// specs cannot catch that: a fixture encodes the author's reading of the rule
// on both sides of the assertion. This spec takes the reference verdict from
// `fleet-board.py --json`, the published output of the grouping the rule
// mirrors, and fails on any session where the two disagree.
//
// Opt-in, because it needs a live host: set ATTENTION_COVERAGE_PARITY=1.
// ATTENTION_COVERAGE_VAULT names the vault the store serves (default the
// private-personal vault) and ATTENTION_COVERAGE_FLEET_BOARD the script.
var _ = Describe("worker coverage parity with fleet-board.py", func() {
	It("classifies every live session exactly as claude-supervisor does", func() {
		if os.Getenv("ATTENTION_COVERAGE_PARITY") != "1" {
			Skip("live-fleet parity: set ATTENTION_COVERAGE_PARITY=1 on a host running the fleet")
		}
		ctx := context.Background()
		home, err := os.UserHomeDir()
		Expect(err).NotTo(HaveOccurred())
		vaultDir := envOr("ATTENTION_COVERAGE_VAULT",
			filepath.Join(home, "Documents", "Obsidian", "private-personal"))
		script := envOr(
			"ATTENTION_COVERAGE_FLEET_BOARD",
			filepath.Join(
				home,
				".claude",
				"plugins",
				"marketplaces",
				"claude-supervisor",
				"scripts",
				"fleet-board.py",
			),
		)
		sessionsDir := filepath.Join(home, ".claude", "sessions")

		reference := readFleetBoardVerdicts(script)
		Expect(reference).NotTo(BeEmpty(), "fleet-board returned no sessions — the probe is blind")

		tasks := NewTaskIndex(ctx, vaultDir, libtime.NewCurrentDateTime())
		liveness := NewSessionLivenessChecker(sessionsDir)
		names := readSessionNames(ctx, sessionsDir)
		Expect(names).NotTo(BeEmpty(), "registry unreadable — the probe is blind")
		index := newSubjectIndex(tasks.SubjectTitles())
		managers := liveManagerSubjects(
			names,
			index,
			func(id string) bool { return liveness.IsLive(ctx, id) },
		)

		var disagreements []string
		compared, uncovered := 0, 0
		ids := make([]string, 0, len(reference))
		for id := range reference {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			want := reference[id]
			task, hasTask := tasks.Lookup(id)
			got := workerUncovered(id, task, hasTask, names, index, managers)
			compared++
			if want.uncovered {
				uncovered++
			}
			if got != want.uncovered {
				disagreements = append(disagreements, fmt.Sprintf(
					"%s %-9s fleet-board uncovered=%-5v go uncovered=%-5v task=%q goals=%q | %s",
					id[:8], want.role, want.uncovered, got, task.Name, task.Goals, want.label))
			}
		}
		// ⚠️ The negative control. A zero above is evidence only if this
		// comparison can fire, so it is re-run with the manager set emptied — a
		// matcher that resolves no manager at all. That must disagree on exactly
		// the sessions fleet-board reads as covered; if it does not, the spec is
		// blind and its zero proves nothing.
		covered := compared - uncovered
		blind := 0
		for _, id := range ids {
			task, hasTask := tasks.Lookup(id)
			if workerUncovered(id, task, hasTask, names, index, nil) != reference[id].uncovered {
				blind++
			}
		}
		GinkgoWriter.Printf(
			"negative control: covered(fleet-board)=%d disagreements-with-no-managers=%d\n",
			covered,
			blind,
		)
		Expect(covered).To(BeNumerically(">", 0),
			"no covered session on the live fleet — parity is unproven in the covered direction; arrange a covered worker")
		Expect(
			blind,
		).To(Equal(covered), "the negative control did not fire on every covered session")
		AddReportEntry("worker coverage parity", fmt.Sprintf(
			"compared=%d uncovered(fleet-board)=%d disagreements=%d\n%s",
			compared, uncovered, len(disagreements), strings.Join(disagreements, "\n")))
		GinkgoWriter.Printf(
			"compared=%d uncovered=%d disagreements=%d\n",
			compared,
			uncovered,
			len(disagreements),
		)
		for _, line := range disagreements {
			GinkgoWriter.Println(line)
		}
		Expect(disagreements).To(BeEmpty())
	})
})

// fleetBoardVerdict is one session's coverage as claude-supervisor groups it.
type fleetBoardVerdict struct {
	role      string
	label     string
	uncovered bool
}

// readFleetBoardVerdicts runs fleet-board.py and derives, per session, whether
// it is parented to a manager other than the root — the reference verdict.
func readFleetBoardVerdicts(script string) map[string]fleetBoardVerdict {
	out, err := exec.Command("python3", script, "--json").
		Output()
		// #nosec G204 -- test-only, path from the operator's env
	Expect(err).NotTo(HaveOccurred(), "run %s", script)
	var doc struct {
		Sessions []struct {
			SessionID string  `json:"session_id"`
			Role      string  `json:"role"`
			Parent    *string `json:"parent"`
			Label     string  `json:"label"`
		} `json:"sessions"`
	}
	Expect(json.Unmarshal(out, &doc)).To(Succeed())
	root := ""
	for _, s := range doc.Sessions {
		if s.Role == "manager" && s.Parent == nil {
			root = s.SessionID
		}
	}
	verdicts := make(map[string]fleetBoardVerdict, len(doc.Sessions))
	for _, s := range doc.Sessions {
		parent := ""
		if s.Parent != nil {
			parent = *s.Parent
		}
		covered := s.Role == "worker" && parent != "" && parent != root && parent != "unmanaged"
		verdicts[s.SessionID] = fleetBoardVerdict{role: s.Role, label: s.Label, uncovered: !covered}
	}
	return verdicts
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
