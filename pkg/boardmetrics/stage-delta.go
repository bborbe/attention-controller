// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package-level note: this file holds the stage-delta histogram — how long each
// leg of a card's journey took, derived from the timestamps the store already
// holds. It is a scrape-time collector rather than a set of write-time hooks,
// and the reasoning is in NewStageDeltaCollector's doc comment.
package boardmetrics

import (
	"context"
	"sort"
	"sync"
	"time"

	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bborbe/attention-controller/pkg"
)

// stageDeltaFamily is the frozen metric name. Namespace and Subsystem stay
// empty for the reason metrics.go records on attention_board_renders_total:
// prometheus builds the exposed name from all three, so a namespace here would
// expose the family under a name nothing greps for.
const stageDeltaFamily = "attention_stage_delta_seconds"

// stageDeltaRefreshInterval bounds how often the population is read.
//
// It is 300s because the local metrics pusher
// (com.bborbe.attention-metrics-push) scrapes :18080 on StartInterval 300: a
// finer interval buys no fresher reading at the store, and a full scan of the
// history is not free — the store's own comments record that the unbounded
// History form decodes every row.
const stageDeltaRefreshInterval = 300 * time.Second

// stageDeltaBuckets are the histogram's upper bounds in seconds.
//
// Chosen in the same spirit as attention_watcher_cycle_seconds — edges around
// what the measured thing actually costs — but wider, because a forwarding lag
// and a delivery wait span minutes to hours where a watcher cycle spans a
// fraction of a second to a few minutes. prometheus.DefBuckets would top out at
// 10s, which would put every leg of this pipeline in the +Inf bucket and make
// histogram_quantile return the largest bound for all of them.
var stageDeltaBuckets = []float64{
	0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600, 1800, 3600, 7200, 21600,
}

// StageDeltaSource is the narrow read surface the collector needs.
//
// It is deliberately not pkg.AttentionStore: the collector only reads, and
// taking the whole interface would let a future edit reach a write method
// without anything objecting. pkg.AttentionStore satisfies it.
//
// ⚠️ It carries no //counterfeiter:generate directive, and the specs use a
// hand-written fake instead. The fake is not a substitute for a generated one:
// the refresh and concurrency specs need to count calls and to hold a read open
// while a second scrape arrives, and neither is expressible through a
// Counterfeiter stub. A generated fake here would be dead code beside it, which
// is a worse drift risk than the one the directive prevents.
type StageDeltaSource interface {
	// History returns the store's items, newest first. limit <= 0 is the
	// unbounded form, which is the population this collector measures. It is
	// used rather than Read/ReadBoard because those remove dead items as a side
	// effect, and a metrics scrape must not mutate what it measures.
	History(
		ctx context.Context,
		limit int,
		offset int,
		filters ...pkg.HistoryFilter,
	) (pkg.Items, error)

	// DeliveryAttempts returns every recorded delivery attempt in one read.
	DeliveryAttempts(ctx context.Context) (map[pkg.ItemID]pkg.DeliveryAttempt, error)
}

// stageDeltaPair is one legal delta: the two stage names it spans, and how to
// extract its value from an item.
//
// The four pairs are stated once, here, and both Describe and Collect derive
// from them — so a fifth cannot be added by editing one and forgetting the
// other. See docs/attention-item-timeline.md § The delta vocabulary: there are
// exactly four, and any document naming a different set is wrong.
type stageDeltaPair struct {
	from string
	to   string
	// seconds returns the delta's value in seconds and whether the item
	// carries both of the pair's stages. An absent stage yields ok=false, and
	// the item contributes no observation to this pair — never a zero, and
	// never a substitute field.
	seconds func(item pkg.Item, attempts map[pkg.ItemID]pkg.DeliveryAttempt) (float64, bool)
}

var stageDeltaPairs = []stageDeltaPair{
	{
		from: "asked",
		to:   "created",
		seconds: func(item pkg.Item, _ map[pkg.ItemID]pkg.DeliveryAttempt) (float64, bool) {
			return secondsBetween(item.AskedAt, &item.CreatedAt)
		},
	},
	{
		from: "created",
		to:   "displayed",
		seconds: func(item pkg.Item, _ map[pkg.ItemID]pkg.DeliveryAttempt) (float64, bool) {
			return secondsBetween(&item.CreatedAt, item.DisplayedAt)
		},
	},
	{
		from: "displayed",
		to:   "answered",
		seconds: func(item pkg.Item, _ map[pkg.ItemID]pkg.DeliveryAttempt) (float64, bool) {
			return secondsBetween(item.DisplayedAt, item.AnsweredAt)
		},
	},
	{
		from: "answered",
		to:   "delivered",
		seconds: func(item pkg.Item, attempts map[pkg.ItemID]pkg.DeliveryAttempt) (float64, bool) {
			attempt, ok := attempts[item.ItemID]
			if !ok {
				return 0, false
			}
			// ⚠️ Only a delivered attempt is the delivery. A failed or held
			// attempt carries an AttemptedAt of its own, but the contract says
			// the release IS the delivery — counting either would report a
			// delivery time for an answer that never arrived, which is 245 of
			// the 736 answered cards measured on 2026-11-11.
			if attempt.Outcome != pkg.DeliveredOutcome {
				return 0, false
			}
			return secondsBetween(item.AnsweredAt, &attempt.AttemptedAt)
		},
	},
}

// secondsBetween returns the signed seconds from one stage to the next, and
// whether both stages carry a timestamp.
//
// ⚠️ Signed and unfloored. A producer whose clock runs ahead of the store's
// makes a delta negative, and the honest reading of skew is the signed value:
// clamping asserts the leg took no time, which is the opposite of what a
// negative says. Not flooring keeps the sub-second resolution a percentile
// needs, where the card's text renderer deliberately floors to whole seconds.
func secondsBetween(from, to *libtime.DateTime) (float64, bool) {
	if from == nil || to == nil {
		return 0, false
	}
	return to.Sub(*from).Duration().Seconds(), true
}

// stageDeltaHistogram is one pair's computed reading for a population.
type stageDeltaHistogram struct {
	count      uint64
	sum        float64
	cumulative map[float64]uint64
}

// NewStageDeltaCollector builds the collector and registers it on the given
// registry.
//
// It is a scrape-time collector, and the choice is deliberate:
//
//   - one read path instead of four write-path hooks, each of which would need
//     its own metrics dependency and would re-observe on a re-attempt (which
//     overwrites rather than appends);
//   - the pre-existing backlog supplies observations immediately, so nothing
//     has to be driven through the pipeline before the family reads non-zero;
//   - the missing-stage rule is one per-item predicate rather than four
//     independent ones;
//   - it reads the resulting displayed_at value rather than instrumenting the
//     seen endpoint's write path, so it cannot inflate displayed→answered.
//
// The observation set is recomputed and emitted as constant histograms, never
// accumulated with Observe: a scrape-time collector that called Observe would
// report a count that grows with the process's uptime rather than with the
// population.
//
// It panics on a duplicate registration for the same reason NewMetrics does:
// registration is a startup invariant, and two packages claiming one series is a
// programming error rather than a condition to recover from.
func NewStageDeltaCollector(
	ctx context.Context,
	registry prometheus.Registerer,
	source StageDeltaSource,
	now libtime.CurrentDateTimeGetter,
) prometheus.Collector {
	collector := &stageDeltaCollector{
		desc: prometheus.NewDesc(
			stageDeltaFamily,
			"Seconds between two consecutive stages of an attention card's journey, "+
				"over the four deltas named in docs/attention-item-timeline.md.",
			[]string{"from", "to"},
			nil,
		),
		ctx:    ctx,
		source: source,
		now:    now,
	}
	registry.MustRegister(collector)
	return collector
}

type stageDeltaCollector struct {
	desc   *prometheus.Desc
	ctx    context.Context
	source StageDeltaSource
	now    libtime.CurrentDateTimeGetter

	// mu guards the cached reading AND is held across the population read, so
	// two scrapes arriving together share one read instead of starting one
	// each — the same shape the federating store's peer cache uses.
	mu          sync.Mutex
	snapshot    []stageDeltaHistogram
	refreshedAt libtime.DateTime
	haveReading bool
}

// Describe sends the single descriptor every observation shares.
func (c *stageDeltaCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.desc
}

// Collect emits one constant histogram per legal pair.
//
// ⚠️ All four are emitted every time, including a pair with no observations
// (count 0, sum 0). A zero-count series is a true statement — no card has that
// leg — and it keeps the exposed label set at exactly four regardless of the
// population, which is what the dashboard's queries depend on.
func (c *stageDeltaCollector) Collect(ch chan<- prometheus.Metric) {
	reading := c.reading()
	for i, pair := range stageDeltaPairs {
		histogram := reading[i]
		ch <- prometheus.MustNewConstHistogram(
			c.desc,
			histogram.count,
			histogram.sum,
			histogram.cumulative,
			pair.from,
			pair.to,
		)
	}
}

// reading returns the current snapshot, recomputing it when the refresh
// interval has elapsed.
//
// A failed read keeps the last good snapshot rather than emitting a zeroed
// histogram: zeroes read as "no card has any delta", which is a very different
// claim from "this scrape could not measure".
func (c *stageDeltaCollector) reading() []stageDeltaHistogram {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.haveReading &&
		c.now.Now().Time().Sub(c.refreshedAt.Time()) < stageDeltaRefreshInterval {
		return c.snapshot
	}

	snapshot, err := c.compute()
	if err != nil {
		glog.Warningf("stage delta refresh failed, serving the previous reading: %v", err)
		if c.haveReading {
			return c.snapshot
		}
		return emptyStageDeltaHistograms()
	}

	c.snapshot = snapshot
	c.refreshedAt = c.now.Now()
	c.haveReading = true
	return snapshot
}

// compute reads the population once and folds every item into the four pairs.
func (c *stageDeltaCollector) compute() ([]stageDeltaHistogram, error) {
	// ⚠️ History, never Read or ReadBoard: both remove dead items from the
	// store as a side effect, and a metrics scrape must not mutate the
	// population it measures. limit <= 0 is the unbounded form.
	items, err := c.source.History(c.ctx, 0, 0)
	if err != nil {
		return nil, err
	}
	attempts, err := c.source.DeliveryAttempts(c.ctx)
	if err != nil {
		return nil, err
	}

	// Per-bucket counts first, accumulated to cumulative below, which is what
	// MustNewConstHistogram expects and what Histogram.Observe would have
	// produced.
	counts := make([][]uint64, len(stageDeltaPairs))
	sums := make([]float64, len(stageDeltaPairs))
	for i := range counts {
		counts[i] = make([]uint64, len(stageDeltaBuckets))
	}

	for _, item := range items {
		// The population is in the tens of thousands, so a cancelled process
		// should stop the scan rather than finish it: this loop is the only
		// long-running one here, and the caller of a scrape is a process
		// shutting down as often as it is a Prometheus server.
		select {
		case <-c.ctx.Done():
			return nil, c.ctx.Err()
		default:
		}
		for i, pair := range stageDeltaPairs {
			seconds, ok := pair.seconds(item, attempts)
			if !ok {
				continue
			}
			sums[i] += seconds
			// sort.SearchFloat64s returns the first index whose bound is >= the
			// value, which is exactly the bucket Observe would pick. A negative
			// value lands at index 0 and therefore in every cumulative bucket,
			// as Observe places it.
			if index := sort.SearchFloat64s(stageDeltaBuckets, seconds); index < len(
				stageDeltaBuckets,
			) {
				counts[i][index]++
			}
		}
	}

	return foldToHistograms(counts, sums), nil
}

// foldToHistograms accumulates per-bucket counts into the cumulative form and
// carries the total count and sum.
func foldToHistograms(counts [][]uint64, sums []float64) []stageDeltaHistogram {
	histograms := make([]stageDeltaHistogram, len(counts))
	for i := range counts {
		cumulative := make(map[float64]uint64, len(stageDeltaBuckets))
		var running uint64
		for j, bound := range stageDeltaBuckets {
			running += counts[i][j]
			cumulative[bound] = running
		}
		histograms[i] = stageDeltaHistogram{
			count:      running,
			sum:        sums[i],
			cumulative: cumulative,
		}
	}
	return histograms
}

// emptyStageDeltaHistograms is the reading served before the first successful
// refresh and after one that failed with nothing cached: four present pairs,
// each with count 0.
func emptyStageDeltaHistograms() []stageDeltaHistogram {
	counts := make([][]uint64, len(stageDeltaPairs))
	for i := range counts {
		counts[i] = make([]uint64, len(stageDeltaBuckets))
	}
	return foldToHistograms(counts, make([]float64, len(stageDeltaPairs)))
}
