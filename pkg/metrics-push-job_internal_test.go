// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	stderrors "errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	libtime "github.com/bborbe/time"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
)

// logCapture collects the job lines a spec would otherwise have to read out of
// glog's file output.
type logCapture struct {
	mutex sync.Mutex
	lines []string
}

func (c *logCapture) info(format string, args ...any) {
	c.record(fmt.Sprintf(format, args...))
}

func (c *logCapture) error(format string, args ...any) {
	c.record(fmt.Sprintf(format, args...))
}

func (c *logCapture) record(line string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.lines = append(c.lines, line)
}

func (c *logCapture) find(substring string) (string, bool) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	for _, line := range c.lines {
		if strings.Contains(line, substring) {
			return line, true
		}
	}
	return "", false
}

// recordingCredentials stands in for the TeamVault subprocess call.
type recordingCredentials struct {
	calls int32
	auth  MetricsPushAuth
	err   error
}

func (r *recordingCredentials) Credentials(_ context.Context) (MetricsPushAuth, error) {
	atomic.AddInt32(&r.calls, 1)
	return r.auth, r.err
}

func (r *recordingCredentials) callCount() int {
	return int(atomic.LoadInt32(&r.calls))
}

// awaitJobDone waits for the job goroutine to return and reports its error.
func awaitJobDone(done <-chan error) error {
	var jobErr error
	EventuallyWithOffset(1, func() bool {
		select {
		case jobErr = <-done:
			return true
		default:
			return false
		}
	}).Should(BeTrue())
	return jobErr
}

var _ = Describe("renderMetricsPushSamples", func() {
	It("stamps every line with one shared millisecond timestamp", func() {
		samples := []MetricsPushSample{
			{Series: "attention_items_total", Value: "3"},
			{Series: `attention_stage_seconds_bucket{stage="watch",le="1"}`, Value: "0.5"},
		}

		body := renderMetricsPushSamples(samples, 1700000000123)

		lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
		Expect(lines).To(HaveLen(2))
		for _, line := range lines {
			Expect(line).To(HaveSuffix(" 1700000000123"))
		}
		Expect(lines[0]).To(Equal("attention_items_total 3 1700000000123"))
		Expect(lines[1]).To(Equal(
			`attention_stage_seconds_bucket{stage="watch",le="1"} 0.5 1700000000123`,
		))
	})

	It("renders an empty body for no samples", func() {
		Expect(renderMetricsPushSamples(nil, 1700000000123)).To(BeEmpty())
	})
})

var _ = Describe("parseMetricsPushSamples", func() {
	It("keeps only the attention_ sample lines", func() {
		text := strings.Join([]string{
			"# HELP attention_items_total items",
			"# TYPE attention_items_total gauge",
			"attention_items_total 3",
			`attention_stage_seconds{stage="watch"} 1.5`,
			`attention_label{note="a b"} 1`,
			"attention_nan NaN",
			"go_goroutines 12",
			"process_resident_memory_bytes 1e+08",
			"attention_broken not-a-number",
			"",
		}, "\n")

		samples := parseMetricsPushSamples(text)

		Expect(samples).To(HaveLen(4))
		Expect(samples[0]).To(Equal(MetricsPushSample{Series: "attention_items_total", Value: "3"}))
		Expect(samples[1]).To(Equal(MetricsPushSample{
			Series: `attention_stage_seconds{stage="watch"}`,
			Value:  "1.5",
		}))
		// A label value may carry spaces; the value is still the last field.
		Expect(samples[2]).To(Equal(MetricsPushSample{
			Series: `attention_label{note="a b"}`,
			Value:  "1",
		}))
		Expect(samples[3].Value).To(Equal("NaN"))
	})
})

var _ = Describe("pushMetrics", func() {
	var ctx context.Context
	var auth MetricsPushAuth

	BeforeEach(func() {
		ctx = context.Background()
		auth = MetricsPushAuth{Username: "user", Password: "pass"}
	})

	It("does not retry a 4xx", func() {
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusBadRequest)
			},
		))
		defer server.Close()

		err := pushMetrics(
			ctx,
			server.Client(),
			server.URL,
			[]byte("attention_x 1 1\n"),
			auth,
			3,
			0,
		)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("HTTP 400"))
		Expect(requests.Load()).To(Equal(int32(1)))
	})

	It("retries a 5xx until it succeeds", func() {
		var requests atomic.Int32
		var receivedUser string
		server := httptest.NewServer(http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				receivedUser, _, _ = r.BasicAuth()
				if requests.Add(1) < 3 {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			},
		))
		defer server.Close()

		err := pushMetrics(
			ctx,
			server.Client(),
			server.URL,
			[]byte("attention_x 1 1\n"),
			auth,
			3,
			0,
		)

		Expect(err).To(BeNil())
		Expect(requests.Load()).To(Equal(int32(3)))
		Expect(receivedUser).To(Equal("user"))
	})

	It("gives up after the last attempt", func() {
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusServiceUnavailable)
			},
		))
		defer server.Close()

		err := pushMetrics(
			ctx,
			server.Client(),
			server.URL,
			[]byte("attention_x 1 1\n"),
			auth,
			2,
			0,
		)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("after 2 attempts"))
		Expect(requests.Load()).To(Equal(int32(2)))
	})
})

var _ = Describe("metrics push job", func() {
	var ctx context.Context
	var cancel context.CancelFunc
	var capture *logCapture
	var done chan error

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		done = make(chan error, 1)
		capture = &logCapture{}

		originalInfo, originalError := metricsPushInfof, metricsPushErrorf
		metricsPushInfof = capture.info
		metricsPushErrorf = capture.error
		DeferCleanup(func() {
			metricsPushInfof = originalInfo
			metricsPushErrorf = originalError
		})
	})

	AfterEach(func() {
		cancel()
		Expect(awaitJobDone(done)).To(BeNil())
	})

	start := func(registry *prometheus.Registry, credentials MetricsPushCredentials, url string) {
		job := NewMetricsPushJob(
			registry,
			url,
			10*time.Millisecond,
			credentials,
			&http.Client{Timeout: time.Second},
			libtime.NewCurrentDateTime(),
		)
		go func() { done <- job(ctx) }()
	}

	It("logs the job line for a cycle with no samples", func() {
		credentials := &recordingCredentials{}
		start(prometheus.NewRegistry(), credentials, "http://127.0.0.1:1/import")

		Eventually(func() bool {
			_, found := capture.find("job=attention-metrics-push action=push")
			return found
		}).Should(BeTrue())

		line, found := capture.find("job=attention-metrics-push action=push")
		Expect(found).To(BeTrue())
		Expect(line).To(ContainSubstring("series=0"))
		Expect(line).To(ContainSubstring("skipped=no_samples"))
		// No samples means nothing to push, so the credential path is never hit.
		Expect(credentials.callCount()).To(Equal(0))
	})

	It("logs a push_error line when the credentials cannot be resolved", func() {
		registry := prometheus.NewRegistry()
		counter := prometheus.NewCounter(prometheus.CounterOpts{
			Name: "attention_test_total",
			Help: "test",
		})
		registry.MustRegister(counter)
		counter.Inc()

		start(
			registry,
			&recordingCredentials{err: stderrors.New("teamvault down")},
			"http://127.0.0.1:1/import",
		)

		Eventually(func() bool {
			_, found := capture.find("action=push_error")
			return found
		}).Should(BeTrue())
		line, _ := capture.find("action=push_error")
		Expect(line).To(ContainSubstring("job=attention-metrics-push"))
		Expect(line).To(ContainSubstring("teamvault down"))
	})

	It("pushes the gathered attention series with a shared stamp", func() {
		registry := prometheus.NewRegistry()
		counter := prometheus.NewCounter(prometheus.CounterOpts{
			Name: "attention_test_total",
			Help: "test",
		})
		registry.MustRegister(counter)
		counter.Inc()

		var mutex sync.Mutex
		var bodies []string
		// The import endpoint acknowledges; the export answers with the series
		// carrying the stamp the import was given, which is what the read-back
		// asserts. A store that acknowledges and never makes the points queryable
		// is the failure this pair of endpoints reproduces.
		server := httptest.NewServer(http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					// One series per captured push, so a read-back always finds the
					// stamp of the push it is verifying even when the loop has
					// already pushed again.
					mutex.Lock()
					var response strings.Builder
					for _, pushed := range bodies {
						lines := strings.Split(strings.TrimSuffix(pushed, "\n"), "\n")
						fields := strings.Fields(lines[0])
						response.WriteString(
							`{"metric":{"__name__":"attention_test_total"},"timestamps":[` +
								fields[len(fields)-1] + `],"values":[1]}` + "\n",
						)
					}
					mutex.Unlock()
					_, _ = w.Write([]byte(response.String()))
					return
				}
				body, err := io.ReadAll(r.Body)
				Expect(err).To(BeNil())
				mutex.Lock()
				bodies = append(bodies, string(body))
				mutex.Unlock()
				w.WriteHeader(http.StatusNoContent)
			},
		))
		defer server.Close()

		credentials := &recordingCredentials{auth: MetricsPushAuth{Username: "u", Password: "p"}}
		start(registry, credentials, server.URL)

		Eventually(func() int {
			mutex.Lock()
			defer mutex.Unlock()
			return len(bodies)
		}).Should(BeNumerically(">", 0))

		mutex.Lock()
		body := bodies[0]
		mutex.Unlock()

		Expect(body).To(ContainSubstring("attention_test_total 1 "))
		Expect(body).NotTo(ContainSubstring("go_goroutines"))
		Expect(body).NotTo(ContainSubstring("# HELP"))
		for _, line := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
			Expect(line).To(MatchRegexp(`^attention_\S+ \S+ \d{13}$`))
		}
		Eventually(func() bool {
			_, found := capture.find("job=attention-metrics-push action=push series=")
			return found
		}).Should(BeTrue())
	})
})

var _ = Describe("verifyMetricsPush", func() {
	It("fails when no series serves the pushed stamp", func() {
		// The import is acknowledged and the export answers with a DIFFERENT
		// stamp, so the push landed nowhere queryable.
		server := httptest.NewServer(http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(
					`{"metric":{"__name__":"attention_test_total"},"timestamps":[1],"values":[1]}` + "\n",
				))
			},
		))
		defer server.Close()

		count, err := verifyMetricsPush(
			context.Background(),
			server.Client(),
			server.URL,
			MetricsPushAuth{Username: "u", Password: "p"},
			1700000000123,
			2,
			0,
		)

		Expect(count).To(Equal(0))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("no series carried stamp 1700000000123"))
	})

	It("counts the series carrying the stamp and ignores a malformed line", func() {
		server := httptest.NewServer(http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(
					`{"metric":{"__name__":"attention_a"},"timestamps":[1700000000123],"values":[1]}` +
						"\n" +
						`not json` + "\n" +
						`{"metric":{"__name__":"attention_b"},"timestamps":[1,1700000000123],"values":[1,2]}` +
						"\n" +
						`{"metric":{"__name__":"attention_c"},"timestamps":[2],"values":[1]}` + "\n",
				))
			},
		))
		defer server.Close()

		count, err := verifyMetricsPush(
			context.Background(),
			server.Client(),
			server.URL,
			MetricsPushAuth{Username: "u", Password: "p"},
			1700000000123,
			1,
			0,
		)

		Expect(err).To(BeNil())
		Expect(count).To(Equal(2))
	})
})
