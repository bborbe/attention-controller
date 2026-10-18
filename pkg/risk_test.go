// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/attention-controller/pkg"
)

var _ = Describe("Risk", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	Describe("Validate", func() {
		It("accepts every declared level", func() {
			for _, risk := range pkg.AvailableRisks {
				Expect(risk.Validate(ctx)).To(BeNil())
			}
		})

		It("accepts an absent level, which is what a pre-change item reads as", func() {
			Expect(pkg.Risk("").Validate(ctx)).To(BeNil())
		})

		It("refuses a level that is not declared", func() {
			Expect(pkg.Risk("urgent").Validate(ctx)).ToNot(BeNil())
		})
	})

	Describe("OrDefault", func() {
		It("returns the declared level when there is one", func() {
			Expect(pkg.RoutineRisk.OrDefault()).To(Equal(pkg.RoutineRisk))
			Expect(pkg.ElevatedRisk.OrDefault()).To(Equal(pkg.ElevatedRisk))
		})

		It("returns decision when none was declared", func() {
			Expect(pkg.Risk("").OrDefault()).To(Equal(pkg.DecisionRisk))
		})
	})

	Describe("RiskOf", func() {
		// item builds an item carrying only the two fields the derivation reads,
		// so a spec states its own premise rather than inheriting one.
		item := func(declared pkg.Risk, payload string, itemContext string) pkg.Item {
			return pkg.Item{
				Risk:    declared,
				Payload: pkg.Payload(payload),
				Context: pkg.ItemContext(itemContext),
			}
		}

		It("renders the declared level when nothing escalates it", func() {
			Expect(pkg.RiskOf(item(pkg.RoutineRisk, "Compact this session", ""), false)).
				To(Equal(pkg.RoutineRisk))
		})

		It("renders decision for an item that declared nothing", func() {
			Expect(pkg.RiskOf(item("", "Which surface?", ""), false)).
				To(Equal(pkg.DecisionRisk))
		})

		It("escalates a routine declaration whose payload names an irreversible verb", func() {
			Expect(pkg.RiskOf(item(pkg.RoutineRisk, "Delete everything under /tmp", ""), false)).
				To(Equal(pkg.ElevatedRisk))
		})

		It("escalates on the context as well as the payload", func() {
			Expect(
				pkg.RiskOf(item(pkg.RoutineRisk, "Proceed?", "This will deploy to prod"), false),
			).
				To(Equal(pkg.ElevatedRisk))
		})

		It("escalates on the resolved production-touching marker alone", func() {
			Expect(pkg.RiskOf(item(pkg.RoutineRisk, "Compact this session", ""), true)).
				To(Equal(pkg.ElevatedRisk))
		})

		It("keeps a producer's own higher declaration", func() {
			Expect(pkg.RiskOf(item(pkg.ElevatedRisk, "Which surface?", ""), false)).
				To(Equal(pkg.ElevatedRisk))
		})

		It("never renders below the declaration, which is the point of the derivation", func() {
			// The whole safety property in one spec: a producer that under-declares
			// gains nothing, and one that over-declares is not talked down.
			Expect(pkg.RiskOf(item(pkg.RoutineRisk, "rm -rf the build dir", ""), false)).
				To(Equal(pkg.ElevatedRisk))
			Expect(pkg.RiskOf(item(pkg.ElevatedRisk, "Compact this session", ""), false)).
				To(Equal(pkg.ElevatedRisk))
		})

		It("requires the verb at the start of a word", func() {
			// Each of these contains a verb as a mid-word fragment. Without the
			// leading boundary they would escalate a card that asks for none of it.
			for _, innocent := range []string{
				"Enforce the existing limit",
				"The former approach was considered",
				"Reload the configuration",
			} {
				Expect(pkg.RiskOf(item(pkg.RoutineRisk, innocent, ""), false)).
					To(Equal(pkg.RoutineRisk), innocent)
			}
		})

		It("matches verb stems, because that is how a real card words them", func() {
			// A closing boundary would leave every one of these rendering routine,
			// which is the failure the rule exists to prevent.
			for _, destructive := range []string{
				"Deleted the stale branch",
				"Deploying to prod now",
				"Merged the PR",
				"Dropping the table",
			} {
				Expect(pkg.RiskOf(item(pkg.RoutineRisk, destructive, ""), false)).
					To(Equal(pkg.ElevatedRisk), destructive)
			}
		})

		It("matches a verb whatever its case", func() {
			Expect(pkg.RiskOf(item(pkg.RoutineRisk, "DELETE the bucket", ""), false)).
				To(Equal(pkg.ElevatedRisk))
		})
	})
})
