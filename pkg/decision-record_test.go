// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	libboltkv "github.com/bborbe/boltkv"
	libkv "github.com/bborbe/kv"
	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
)

// storedSpecimenPayload is the card 491aab84 carried, verbatim. It is the
// measured miss this rule was written from: a `pick` whose answer shut two VMs
// down and created 490 GB of volumes.
const storedSpecimenPayload = "Approve migrating sun's 2 k3s VMs to nuke? [production-touching] " +
	"Change: migrate sun-k3s-master-0 (4 GiB) + sun-k3s-worker-0 (48→16 GiB) to nuke; " +
	"shrink nuke-k3s-dev-worker-0 to 24 GiB in the same change. " +
	"Blast radius: nuke is the TRADING hypervisor (runs nuke dev+prod) hosting 10 VMs — " +
	"this lands at ~130 GiB declared on 125 GiB physical (~4% declared overcommit; actual usage far lower). " +
	"Preserves cluster identity (kube-system UID, CA, node names) that SC1 and goal rows 2/3 need; " +
	"a rebuild would not. Reversible by migrating back."

var _ = Describe("Decision Record mint path", func() {
	var ctx context.Context
	var db libkv.DB
	var store pkg.AttentionStore
	var vaultDir string
	var livenessChecker *mocks.SessionLivenessChecker

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		db, err = libboltkv.OpenTemp(ctx)
		Expect(err).To(BeNil())
		livenessChecker = &mocks.SessionLivenessChecker{}
		livenessChecker.IsLiveReturns(true)
		vaultDir = GinkgoT().TempDir()
		store = pkg.NewAttentionStore(
			db,
			pkg.NewItemIDGenerator(),
			livenessChecker,
			libtime.NewCurrentDateTime(),
			libtime.Duration(15*60*1e9),
			pkg.WithDecisionRecordWriter(
				pkg.NewVaultDecisionRecordWriter(vaultDir, libtime.NewCurrentDateTime()),
			),
		)
	})

	AfterEach(func() {
		Expect(db.Close()).To(BeNil())
	})

	// request builds a message card the way a producer would push one.
	request := func(
		dedupKey pkg.DedupKey,
		payload string,
		options ...string,
	) pkg.PushRequest {
		answerOptions := make(pkg.AnswerOptions, 0, len(options))
		for index, label := range options {
			answerOptions = append(answerOptions, pkg.AnswerOption{
				Label:       label,
				Recommended: index == 0,
			})
		}
		return pkg.PushRequest{
			ProducerID:      "session-a",
			ProducerKind:    pkg.SessionProducerKind,
			LivenessRef:     "session:session-a",
			DedupKey:        dedupKey,
			InterruptClass:  pkg.PickInterruptClass,
			Payload:         pkg.Payload(payload),
			AnswerMechanism: pkg.MessageAnswerMechanism,
			Options:         answerOptions,
		}
	}

	// storedSpecimen is the measured card: a pick whose options name the
	// migration.
	storedSpecimen := func() pkg.PushRequest {
		return request(
			"sun-k3s-migration-nuke-2026-11-24",
			storedSpecimenPayload,
			"approve — migrate to nuke",
			"decline — don't migrate; keep sun up as fallback",
		)
	}

	// novelSpecimen is invented at test time and exists nowhere in the store, so
	// a producer that special-cases the one dedup key it has seen fails here.
	novelSpecimen := func() pkg.PushRequest {
		return request(
			"move-vg0-data-hell-to-nuke-dev",
			"move `vg0/data` from `hell` to `nuke-dev`",
			"approve — move the volume",
			"decline — leave it where it is",
		)
	}

	// routinePick is the negative control: a task approval must still mint.
	routinePick := func() pkg.PushRequest {
		return request(
			"task-approval-clean-cache",
			"Approve starting task: clean the stale build cache?",
			"approve — start the task",
			"decline — leave it",
		)
	}

	// selfContainedOneWay is the second negative control: an irreversible but
	// self-contained decision that is legitimately a card must still mint. It
	// carries an irreversible verb, so the RENDER level escalates, but it names
	// no VM, host, cluster or disk, so it is not the infrastructure class.
	selfContainedOneWay := func() pkg.PushRequest {
		return request(
			"delete-branch-old-thing",
			"Approve deleting the stale feature branch feat/old-thing?",
			"approve — delete the branch",
			"decline — keep it",
		)
	}

	Describe("Consequence classification", func() {
		It("classifies the stored specimen as irreversible infrastructure", func() {
			Expect(pkg.RequestConsequence(storedSpecimen())).
				To(Equal(pkg.IrreversibleInfraConsequence))
		})

		It("classifies a novel payload invented at test time", func() {
			Expect(pkg.RequestConsequence(novelSpecimen())).
				To(Equal(pkg.IrreversibleInfraConsequence))
		})

		It("does not classify a routine task approval", func() {
			Expect(pkg.RequestConsequence(routinePick())).To(Equal(pkg.NoConsequence))
		})

		It("does not classify a self-contained one-way card", func() {
			Expect(pkg.RequestConsequence(selfContainedOneWay())).
				To(Equal(pkg.NoConsequence))
		})

		It("does not classify a negated instruction", func() {
			Expect(pkg.RequestConsequence(request(
				"no-migrate",
				"Do not migrate the VM to nuke.",
				"approve — leave it",
			))).To(Equal(pkg.NoConsequence))
		})

		It("states a blast radius for an infrastructure item and none for a routine one", func() {
			Expect(pkg.BlastRadiusOf(pkg.Item{
				Payload: pkg.Payload(storedSpecimenPayload),
			})).ToNot(BeEmpty())
			Expect(pkg.BlastRadiusOf(pkg.Item{
				Payload: pkg.Payload("Approve starting task: clean the stale build cache?"),
			})).To(BeEmpty())
		})
	})

	Describe("Push", func() {
		It("mints no pick for the stored specimen and writes one Decision Record", func() {
			item, err := store.Push(ctx, storedSpecimen())
			Expect(err).To(BeNil())
			Expect(item.InterruptClass).To(Equal(pkg.AckInterruptClass))
			Expect(item.AnswerMechanism).To(Equal(pkg.AckAnswerMechanism))
			Expect(item.Options).To(BeEmpty())
			Expect(item.Payload.String()).To(HavePrefix("decision waiting for ack: "))
			Expect(item.Risk).To(Equal(pkg.ElevatedRisk))

			pages := decisionRecordPages(vaultDir)
			Expect(pages).To(HaveLen(1))
			body := readFile(pages[0])
			Expect(body).To(ContainSubstring("type: Decision Record"))
			Expect(body).To(ContainSubstring("needs_review: true"))
			Expect(body).To(ContainSubstring("status: proposed"))
			Expect(body).To(ContainSubstring("# Question"))
			Expect(body).To(ContainSubstring("# Options"))
			Expect(body).To(ContainSubstring("# Premise"))
			Expect(body).To(ContainSubstring("What breaks today without this change:"))
			Expect(body).To(ContainSubstring("# Blast Radius"))
			Expect(body).ToNot(ContainSubstring("reviewed: true"))
			// The card points at the page that was written.
			Expect(item.Payload.String()).To(ContainSubstring("50 Knowledge Base/"))
		})

		It("mints no pick for a novel payload invented at test time", func() {
			item, err := store.Push(ctx, novelSpecimen())
			Expect(err).To(BeNil())
			Expect(item.InterruptClass).To(Equal(pkg.AckInterruptClass))
			Expect(item.Options).To(BeEmpty())
			Expect(item.Payload.String()).To(HavePrefix("decision waiting for ack: "))

			pages := decisionRecordPages(vaultDir)
			Expect(pages).To(HaveLen(1))
			body := readFile(pages[0])
			Expect(body).To(ContainSubstring("type: Decision Record"))
			Expect(body).To(ContainSubstring("needs_review: true"))
			Expect(body).To(ContainSubstring("vg0/data"))
			Expect(body).To(ContainSubstring("What breaks today without this change:"))
		})

		It("still mints a routine pick, so the rule cannot pass by silencing the board", func() {
			item, err := store.Push(ctx, routinePick())
			Expect(err).To(BeNil())
			Expect(item.InterruptClass).To(Equal(pkg.PickInterruptClass))
			Expect(item.Options).To(HaveLen(2))
			Expect(
				item.Payload.String(),
			).To(Equal("Approve starting task: clean the stale build cache?"))
			Expect(decisionRecordPages(vaultDir)).To(BeEmpty())
		})

		It(
			"still mints a self-contained one-way decision, so the rule cannot route every one-way door to a page",
			func() {
				item, err := store.Push(ctx, selfContainedOneWay())
				Expect(err).To(BeNil())
				Expect(item.InterruptClass).To(Equal(pkg.PickInterruptClass))
				Expect(item.Options).To(HaveLen(2))
				Expect(decisionRecordPages(vaultDir)).To(BeEmpty())
			},
		)

		It("does not rewrite a notification about a completed migration", func() {
			// An `ack` card reports; it does not ask. Converting it to a decision
			// pointer would be a category error and would lose its message.
			payload := "Migration of sun's 2 k3s VMs to nuke completed successfully."
			item, err := store.Push(ctx, pkg.PushRequest{
				ProducerID:      "session-a",
				ProducerKind:    pkg.SessionProducerKind,
				LivenessRef:     "session:session-a",
				DedupKey:        "migration-completed-sun-nuke",
				InterruptClass:  pkg.AckInterruptClass,
				Payload:         pkg.Payload(payload),
				AnswerMechanism: pkg.AckAnswerMechanism,
			})
			Expect(err).To(BeNil())
			Expect(item.InterruptClass).To(Equal(pkg.AckInterruptClass))
			Expect(item.Payload.String()).To(Equal(payload))
			Expect(decisionRecordPages(vaultDir)).To(BeEmpty())
		})

		It("does not rewrite an approve gate, which carries its own answer routing", func() {
			item, err := store.Push(ctx, pkg.PushRequest{
				ProducerID:      "session-a",
				ProducerKind:    pkg.SessionProducerKind,
				LivenessRef:     "session:session-a",
				DedupKey:        "approve-migrate-sun-vms",
				InterruptClass:  pkg.ApproveInterruptClass,
				Payload:         pkg.Payload("Approve migrating sun's 2 k3s VMs to nuke?"),
				AnswerMechanism: pkg.PermissionAnswerMechanism,
			})
			Expect(err).To(BeNil())
			Expect(item.InterruptClass).To(Equal(pkg.ApproveInterruptClass))
			Expect(item.AnswerMechanism).To(Equal(pkg.PermissionAnswerMechanism))
			Expect(decisionRecordPages(vaultDir)).To(BeEmpty())
		})

		It("reuses the page when the same decision is pushed again", func() {
			_, err := store.Push(ctx, storedSpecimen())
			Expect(err).To(BeNil())
			_, err = store.Push(ctx, storedSpecimen())
			Expect(err).To(BeNil())
			Expect(decisionRecordPages(vaultDir)).To(HaveLen(1))
		})

		It("still suppresses the pick when no writer (no vault) is configured", func() {
			noWriterStore := pkg.NewAttentionStore(
				db,
				pkg.NewItemIDGenerator(),
				livenessChecker,
				libtime.NewCurrentDateTime(),
				libtime.Duration(15*60*1e9),
			)
			item, err := noWriterStore.Push(ctx, storedSpecimen())
			Expect(err).To(BeNil())
			Expect(item.InterruptClass).To(Equal(pkg.AckInterruptClass))
			Expect(item.AnswerMechanism).To(Equal(pkg.AckAnswerMechanism))
			Expect(item.Options).To(BeEmpty())
			Expect(item.Risk).To(Equal(pkg.ElevatedRisk))
			// It does not claim a page it cannot write.
			Expect(item.Payload.String()).To(ContainSubstring("no Decision Record vault"))
			Expect(decisionRecordPages(vaultDir)).To(BeEmpty())
		})

		It(
			"fully converts an existing pick when a re-push becomes an irreversible decision",
			func() {
				seeded, err := store.Push(ctx, request(
					"convert-on-repush",
					"Approve starting task: clean the stale build cache?",
					"approve — start the task", "decline — leave it",
				))
				Expect(err).To(BeNil())
				Expect(seeded.InterruptClass).To(Equal(pkg.PickInterruptClass))
				Expect(seeded.AnswerMechanism).To(Equal(pkg.MessageAnswerMechanism))

				// Re-push the SAME dedup key with an irreversible-infra payload. The
				// rule rewrites it to an ack; the stored item must be fully converted,
				// not left class=ack with mechanism=message.
				converted, err := store.Push(ctx, request(
					"convert-on-repush",
					storedSpecimenPayload,
					"approve — migrate to nuke", "decline — don't migrate; keep sun up as fallback",
				))
				Expect(err).To(BeNil())
				Expect(converted.InterruptClass).To(Equal(pkg.AckInterruptClass))
				Expect(converted.AnswerMechanism).To(Equal(pkg.AckAnswerMechanism))
				Expect(converted.Options).To(BeEmpty())
				Expect(converted.Questions).To(BeEmpty())
				Expect(converted.Risk).To(Equal(pkg.ElevatedRisk))
			},
		)
	})

	Describe("Answer", func() {
		It("never releases the Decision Record — only vault-cli decision ack does", func() {
			item, err := store.Push(ctx, storedSpecimen())
			Expect(err).To(BeNil())
			pages := decisionRecordPages(vaultDir)
			Expect(pages).To(HaveLen(1))
			before := readFile(pages[0])

			_, err = store.Answer(ctx, item.ItemID, "attention-board", "", "", nil, nil, nil)
			Expect(err).To(BeNil())

			after := readFile(pages[0])
			Expect(after).To(Equal(before))
			Expect(after).ToNot(ContainSubstring("reviewed: true"))
		})
	})

	Describe("Decision Record writer", func() {
		It("does not reuse a page whose dedup key merely shares a prefix", func() {
			_, err := store.Push(ctx, request(
				"sun", "move `vg0/data` from `hell` to `nuke-dev`",
				"approve — move the volume", "decline — leave it where it is",
			))
			Expect(err).To(BeNil())
			_, err = store.Push(ctx, request(
				"sun-extra", "move `vg1/data` from `hell` to `nuke-dev`",
				"approve — move the volume", "decline — leave it where it is",
			))
			Expect(err).To(BeNil())
			Expect(decisionRecordPages(vaultDir)).To(HaveLen(2))

			// Re-pushing "sun" must reuse its own page, not the "sun-extra" page a
			// substring lookup would have matched.
			_, err = store.Push(ctx, request(
				"sun", "move `vg0/data` from `hell` to `nuke-dev`",
				"approve — move the volume", "decline — leave it where it is",
			))
			Expect(err).To(BeNil())
			Expect(decisionRecordPages(vaultDir)).To(HaveLen(2))
		})

		It("quotes and escapes an unsafe dedup key and still reuses its page", func() {
			key := pkg.DedupKey(`weird "key\with\backslash`)
			_, err := store.Push(ctx, request(
				key, "move `vg0/data` from `hell` to `nuke-dev`",
				"approve — move the volume", "decline — leave it where it is",
			))
			Expect(err).To(BeNil())
			pages := decisionRecordPages(vaultDir)
			Expect(pages).To(HaveLen(1))
			Expect(readFile(pages[0])).To(ContainSubstring(
				`dedup_key: "weird \"key\\with\\backslash"`,
			))
			_, err = store.Push(ctx, request(
				key, "move `vg0/data` from `hell` to `nuke-dev`",
				"approve — move the volume", "decline — leave it where it is",
			))
			Expect(err).To(BeNil())
			Expect(decisionRecordPages(vaultDir)).To(HaveLen(1))
		})
	})
})

// decisionRecordPages returns every Decision Record page written under the
// vault, sorted so assertions are stable.
func decisionRecordPages(vaultDir string) []string {
	directory := filepath.Join(vaultDir, "50 Knowledge Base")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		paths = append(paths, filepath.Join(directory, entry.Name()))
	}
	return paths
}

// readFile reads a page, failing the spec rather than returning an empty string
// so a missing page cannot read as an empty assertion.
func readFile(path string) string {
	body, err := os.ReadFile(path)
	Expect(err).To(BeNil(), "read %s", path)
	return string(body)
}
