// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package boardmetrics_test

import (
	"context"
	"sort"
	"sync"
	stdtime "time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/bborbe/attention-controller/mocks"
	"github.com/bborbe/attention-controller/pkg"
	"github.com/bborbe/attention-controller/pkg/boardmetrics"
)

// stageDeltaFamilyFor gathers the registry and returns the family, failing the
// spec when it is absent. Every call is one scrape, which is why the specs use
// it as the scrape trigger rather than gathering separately.
func stageDeltaFamilyFor(registry *prometheus.Registry) *dto.MetricFamily {
	families, err := registry.Gather()
	Expect(err).Should(BeNil())
	for _, family := range families {
		if family.GetName() == "attention_stage_delta_seconds" {
			return family
		}
	}
	return nil
}

// stageDeltaFor returns the single histogram carrying the given label pair,
// scraping once.
func stageDeltaFor(registry *prometheus.Registry, from, to string) *dto.Histogram {
	family := stageDeltaFamilyFor(registry)
	Expect(family).ShouldNot(BeNil())
	for _, metric := range family.GetMetric() {
		labels := map[string]string{}
		for _, label := range metric.GetLabel() {
			labels[label.GetName()] = label.GetValue()
		}
		if labels["from"] == from && labels["to"] == to {
			return metric.GetHistogram()
		}
	}
	return nil
}

// stageDeltaPairsOf returns every observed from→to pair, sorted.
func stageDeltaPairsOf(registry *prometheus.Registry) []string {
	family := stageDeltaFamilyFor(registry)
	Expect(family).ShouldNot(BeNil())
	pairs := make([]string, 0, len(family.GetMetric()))
	for _, metric := range family.GetMetric() {
		labels := map[string]string{}
		for _, label := range metric.GetLabel() {
			labels[label.GetName()] = label.GetValue()
		}
		pairs = append(pairs, labels["from"]+"→"+labels["to"])
	}
	sort.Strings(pairs)
	return pairs
}

// finiteBounds returns a histogram's upper bounds, excluding +Inf.
func finiteBounds(histogram *dto.Histogram) []float64 {
	bounds := make([]float64, 0, len(histogram.GetBucket()))
	for _, bucket := range histogram.GetBucket() {
		bounds = append(bounds, bucket.GetUpperBound())
	}
	sort.Float64s(bounds)
	return bounds
}

// itemAt builds an item whose stages are the given instants. A nil instant
// leaves that stage absent.
func itemAt(
	id string,
	created stdtime.Time,
	asked *stdtime.Time,
	displayed *stdtime.Time,
	answered *stdtime.Time,
) pkg.Item {
	toDateTime := func(at *stdtime.Time) *libtime.DateTime {
		if at == nil {
			return nil
		}
		value := libtime.DateTime(*at)
		return &value
	}
	return pkg.Item{
		ItemID:      pkg.ItemID(id),
		CreatedAt:   libtime.DateTime(created),
		AskedAt:     toDateTime(asked),
		DisplayedAt: toDateTime(displayed),
		AnsweredAt:  toDateTime(answered),
	}
}

// pointerTo returns a pointer to a copy, so each item owns its instants.
func pointerTo(at stdtime.Time) *stdtime.Time {
	value := at
	return &value
}

var _ = Describe("StageDeltaCollector", func() {
	var (
		registry *prometheus.Registry
		source   *mocks.StageDeltaSource
		clock    stdtime.Time
	)

	BeforeEach(func() {
		registry = prometheus.NewRegistry()
		source = &mocks.StageDeltaSource{}
		source.HistoryReturns(pkg.Items{}, nil)
		source.DeliveryAttemptsReturns(map[pkg.ItemID]pkg.DeliveryAttempt{}, nil)
		clock = stdtime.Date(2026, 11, 13, 10, 0, 0, 0, stdtime.UTC)
	})

	// register builds the collector once per spec. Registration panics on a
	// duplicate, so every scrape below goes through registry.Gather rather than
	// a second construction.
	register := func() {
		boardmetrics.NewStageDeltaCollector(
			context.Background(),
			registry,
			source,
			libtime.CurrentDateTimeGetterFunc(func() libtime.DateTime {
				return libtime.DateTime(clock)
			}),
		)
	}

	Context("the exposed label set", func() {
		It("carries exactly from and to, and exactly the four contract pairs", func() {
			base := clock
			source.HistoryReturns(pkg.Items{
				itemAt(
					"a",
					base,
					pointerTo(base.Add(-5*stdtime.Second)),
					pointerTo(base.Add(2*stdtime.Second)),
					pointerTo(base.Add(9*stdtime.Second)),
				),
			}, nil)
			source.DeliveryAttemptsReturns(map[pkg.ItemID]pkg.DeliveryAttempt{
				"a": {
					ItemID:      pkg.ItemID("a"),
					Outcome:     pkg.DeliveredOutcome,
					AttemptedAt: libtime.DateTime(base.Add(stdtime.Minute)),
				},
			}, nil)

			register()

			Expect(stageDeltaPairsOf(registry)).To(Equal([]string{
				"answered→delivered",
				"asked→created",
				"created→displayed",
				"displayed→answered",
			}))
			for _, metric := range stageDeltaFamilyFor(registry).GetMetric() {
				names := make([]string, 0, len(metric.GetLabel()))
				for _, label := range metric.GetLabel() {
					names = append(names, label.GetName())
				}
				sort.Strings(names)
				Expect(names).To(Equal([]string{"from", "to"}))
			}
		})

		It("exposes all four pairs over an empty population", func() {
			register()

			Expect(stageDeltaPairsOf(registry)).To(Equal([]string{
				"answered→delivered",
				"asked→created",
				"created→displayed",
				"displayed→answered",
			}))
			for _, metric := range stageDeltaFamilyFor(registry).GetMetric() {
				Expect(metric.GetHistogram().GetSampleCount()).To(BeNumerically("==", 0))
			}
		})
	})

	It("uses the frozen bucket edges, not prometheus.DefBuckets", func() {
		register()

		histogram := stageDeltaFor(registry, "asked", "created")
		Expect(histogram).ShouldNot(BeNil())
		Expect(finiteBounds(histogram)).To(Equal([]float64{
			0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600, 1800, 3600, 7200, 21600,
		}))
	})

	It("suppresses only the deltas touching a missing stage", func() {
		base := clock
		source.HistoryReturns(pkg.Items{
			itemAt(
				"no-asked",
				base,
				nil,
				pointerTo(base.Add(2*stdtime.Second)),
				pointerTo(base.Add(9*stdtime.Second)),
			),
		}, nil)
		source.DeliveryAttemptsReturns(map[pkg.ItemID]pkg.DeliveryAttempt{
			"no-asked": {
				ItemID:      pkg.ItemID("no-asked"),
				Outcome:     pkg.DeliveredOutcome,
				AttemptedAt: libtime.DateTime(base.Add(10 * stdtime.Second)),
			},
		}, nil)

		register()

		Expect(
			stageDeltaFor(registry, "asked", "created").GetSampleCount(),
		).To(BeNumerically("==", 0))
		Expect(
			stageDeltaFor(registry, "created", "displayed").GetSampleCount(),
		).To(BeNumerically("==", 1))
		Expect(
			stageDeltaFor(registry, "displayed", "answered").GetSampleCount(),
		).To(BeNumerically("==", 1))
		Expect(
			stageDeltaFor(registry, "answered", "delivered").GetSampleCount(),
		).To(BeNumerically("==", 1))
	})

	It("observes a negative delta unclamped", func() {
		base := clock
		source.HistoryReturns(pkg.Items{
			itemAt("skewed", base, pointerTo(base.Add(30*stdtime.Second)), nil, nil),
		}, nil)

		register()

		histogram := stageDeltaFor(registry, "asked", "created")
		Expect(histogram.GetSampleCount()).To(BeNumerically("==", 1))
		Expect(histogram.GetSampleSum()).To(BeNumerically("<", 0))
	})

	// ⚠️ An observation above the last finite bound belongs in +Inf alone. It
	// must still reach the sample count: a count derived from the finite buckets
	// would drop it while keeping it in the sum, and _sum/_count would then
	// describe two different populations.
	It(
		"counts an observation above the top bucket, and keeps it out of the finite buckets",
		func() {
			base := clock
			source.HistoryReturns(pkg.Items{
				itemAt("slow", base, nil, pointerTo(base.Add(7*stdtime.Hour)), nil),
			}, nil)

			register()

			histogram := stageDeltaFor(registry, "created", "displayed")
			Expect(histogram.GetSampleCount()).To(BeNumerically("==", 1))
			Expect(histogram.GetSampleSum()).To(BeNumerically("==", (7 * stdtime.Hour).Seconds()))
			for _, bucket := range histogram.GetBucket() {
				if bucket.GetUpperBound() <= 21600 {
					Expect(bucket.GetCumulativeCount()).To(BeNumerically("==", 0))
				}
			}
		},
	)

	It("counts only a delivered attempt as the delivery", func() {
		base := clock
		source.HistoryReturns(pkg.Items{
			itemAt("ok", base, nil, pointerTo(base), pointerTo(base.Add(stdtime.Second))),
			itemAt("failed", base, nil, pointerTo(base), pointerTo(base.Add(stdtime.Second))),
			itemAt("held", base, nil, pointerTo(base), pointerTo(base.Add(stdtime.Second))),
		}, nil)
		attempted := libtime.DateTime(base.Add(10 * stdtime.Second))
		source.DeliveryAttemptsReturns(map[pkg.ItemID]pkg.DeliveryAttempt{
			"ok": {
				ItemID:      pkg.ItemID("ok"),
				Outcome:     pkg.DeliveredOutcome,
				AttemptedAt: attempted,
			},
			"failed": {
				ItemID:      pkg.ItemID("failed"),
				Outcome:     pkg.FailedOutcome,
				AttemptedAt: attempted,
			},
			"held": {
				ItemID:      pkg.ItemID("held"),
				Outcome:     pkg.HeldOutcome,
				AttemptedAt: attempted,
			},
		}, nil)

		register()

		Expect(
			stageDeltaFor(registry, "answered", "delivered").GetSampleCount(),
		).To(BeNumerically("==", 1))
	})

	It("reports a snapshot of the population, not an accumulation", func() {
		base := clock
		source.HistoryReturns(pkg.Items{
			itemAt("a", base, nil, pointerTo(base.Add(2*stdtime.Second)), nil),
		}, nil)
		register()

		Expect(
			stageDeltaFor(registry, "created", "displayed").GetSampleCount(),
		).To(BeNumerically("==", 1))
		// Same population, still inside the refresh window: the cached reading
		// is served, so the count does not double.
		Expect(
			stageDeltaFor(registry, "created", "displayed").GetSampleCount(),
		).To(BeNumerically("==", 1))

		// One more item, and the clock past the bound: recomputed, not added to.
		source.HistoryReturns(pkg.Items{
			itemAt("a", base, nil, pointerTo(base.Add(2*stdtime.Second)), nil),
			itemAt("b", base, nil, pointerTo(base.Add(3*stdtime.Second)), nil),
		}, nil)
		clock = clock.Add(301 * stdtime.Second)
		Expect(
			stageDeltaFor(registry, "created", "displayed").GetSampleCount(),
		).To(BeNumerically("==", 2))
	})

	It("reads the population at most once per refresh window", func() {
		register()

		stageDeltaPairsOf(registry)
		stageDeltaPairsOf(registry)
		stageDeltaPairsOf(registry)
		Expect(source.HistoryCallCount()).To(Equal(1))

		clock = clock.Add(301 * stdtime.Second)
		stageDeltaPairsOf(registry)
		Expect(source.HistoryCallCount()).To(Equal(2))
	})

	It("shares one read between concurrent scrapes", func() {
		block := make(chan struct{})
		entered := make(chan struct{}, 1)
		source.HistoryCalls(func(
			_ context.Context,
			_ int,
			_ int,
			_ ...pkg.HistoryFilter,
		) (pkg.Items, error) {
			entered <- struct{}{}
			<-block
			return pkg.Items{}, nil
		})
		register()

		done := make(chan struct{}, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer GinkgoRecover()
			defer wg.Done()
			stageDeltaPairsOf(registry)
			done <- struct{}{}
		}()
		// Wait until the first scrape is inside the population read, so the
		// second is guaranteed to arrive while the collector holds its lock.
		<-entered
		go func() {
			defer GinkgoRecover()
			defer wg.Done()
			stageDeltaPairsOf(registry)
			done <- struct{}{}
		}()

		close(block)
		wg.Wait()
		close(done)

		Expect(source.HistoryCallCount()).To(Equal(1))
	})
})
