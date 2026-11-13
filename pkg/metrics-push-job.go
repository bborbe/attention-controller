// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bytes"
	"context"
	"encoding/json"
	stderrors "errors"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/bborbe/errors"
	"github.com/bborbe/run"
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"
)

const (
	// metricsPushPrefix is the only metric-name prefix forwarded to the remote
	// store.
	//
	// ⚠️ The registry also carries the Go runtime and promhttp instrumentation,
	// which describe this process rather than the attention pipeline. Pushing them
	// would put a Mac's memory profile in the nuke store, and no dashboard panel
	// asks for it.
	metricsPushPrefix = "attention_"

	// metricsPushJobName is the `job=` value on every line this job logs. A probe
	// binds a push to the job by grepping it, so it is a stable identifier rather
	// than a label that happens to read well.
	metricsPushJobName = "attention-metrics-push"

	// metricsPushImportPath and metricsPushExportPath are the two halves of the
	// store this job talks to: the import it writes through, and the export it
	// reads back through. The export URL is derived from the import endpoint, so a
	// job pointed at another store verifies against that store rather than against
	// the production one.
	metricsPushImportPath = "/api/v1/import/prometheus"
	metricsPushExportPath = "/api/v1/export"

	// MetricsPushDefaultEndpoint is the VictoriaMetrics import endpoint the Python
	// job posted to.
	MetricsPushDefaultEndpoint = "https://victoriametrics.prod.nuke.benjamin-borbe.de" +
		metricsPushImportPath

	// MetricsPushDefaultInterval is the cadence the Python job ran at, under
	// `StartInterval 300` in com.bborbe.attention-metrics-push. A daily cadence
	// would leave every rate() window on the dashboard empty and each panel a
	// single isolated point per day.
	MetricsPushDefaultInterval = 300 * time.Second

	// metricsPushAttempts and metricsPushRetryDelay mirror the Python job's import
	// retry budget.
	metricsPushAttempts   = 6
	metricsPushRetryDelay = 45 * time.Second

	// metricsPushReadBackAttempts and metricsPushReadBackDelay mirror the Python
	// job's read-back budget: a 2xx from the import endpoint says the request was
	// accepted, not that the points are queryable, and the store answers the
	// export from a separate read path that lags the write.
	metricsPushReadBackAttempts = 6
	metricsPushReadBackDelay    = 10 * time.Second

	// metricsPushReadBackBodyLimit bounds the export response this job buffers.
	// The query is the whole attention_ family, so the body is a few hundred
	// kilobytes in practice; the limit is what keeps a far end that answers with
	// far more than it was asked for from growing this process's heap against its
	// 112 MiB GOMEMLIMIT.
	metricsPushReadBackBodyLimit = 8 << 20

	// metricsPushCredentialAttempts and metricsPushCredentialRetryDelay mirror the
	// Python job's TeamVault retry budget: a TeamVault outage is transient far more
	// often than it is permanent, and eight attempts cover an eight-minute blip.
	metricsPushCredentialAttempts   = 8
	metricsPushCredentialRetryDelay = 60 * time.Second

	// metricsPushTeamVaultCLI and metricsPushTeamVaultKey are the credential source
	// the Python job used. The key is an identifier, never a secret — the secret is
	// the CLI's stdout, and nothing here reads it into a log line.
	metricsPushTeamVaultCLI = "/opt/homebrew/bin/teamvault-cli"
	metricsPushTeamVaultKey = "zL8nyw"

	// metricsPushCommandTimeout bounds one teamvault-cli invocation, matching the
	// Python job's 60 s subprocess timeout.
	metricsPushCommandTimeout = 60 * time.Second
)

// metricsPushInfof and metricsPushErrorf are package variables so a spec can
// capture the per-cycle job lines.
//
// ⚠️ The line is not decoration: the job's effect is a write to a remote store
// that no in-process assertion can read back, so `job=attention-metrics-push
// action=push` is what binds that effect to the job. A spec that had to read
// glog's file output would be testing glog rather than the job.
var (
	metricsPushInfof = func(format string, args ...any) {
		glog.Infof(format, args...)
	}
	metricsPushErrorf = func(format string, args ...any) {
		glog.Errorf(format, args...)
	}
)

// MetricsPushSample is one series/value pair read from the registry.
type MetricsPushSample struct {
	// Series is the series name with its labels, byte-for-byte as the registry
	// rendered it.
	Series string
	// Value is the sample value as Prometheus exposition text.
	Value string
}

// MetricsPushAuth is the basic-auth pair the import endpoint requires.
//
// ⚠️ It must never reach a log line or an error message. String() is redacted so
// a stray `%v` or `%s` prints no credential, and nothing else formats it.
type MetricsPushAuth struct {
	Username string
	Password string
}

// String renders the pair without its secrets, so an accidental format verb
// cannot leak them.
func (a MetricsPushAuth) String() string {
	return "MetricsPushAuth{<redacted>}"
}

// MetricsPushCredentials resolves the basic-auth pair the import endpoint needs.
//
// It is an interface so a spec can drive a cycle without shelling out to
// TeamVault: the resolution path is a subprocess call whose failure modes belong
// to the CLI, not to this job.
type MetricsPushCredentials interface {
	Credentials(ctx context.Context) (MetricsPushAuth, error)
}

// NewMetricsPushJob pushes the registry's attention_* series to endpoint every
// interval, until ctx is cancelled.
//
// ⚠️ The samples are read from gatherer in-process, never over HTTP. The Python
// job this replaces scraped http://localhost:18080/metrics because it was a
// separate process; this job runs inside the process that serves that endpoint,
// so the registry is the source and the loopback port is not a dependency.
//
// ⚠️ Every failure is fail-soft: a failed push costs one interval of dashboard
// freshness and loses nothing, so the loop logs it and continues. Taking the
// process down for it would drop the board over something the board survives.
//
// The interval is a parameter rather than the constant read directly, so a spec
// can drive the tick without waiting five minutes for it.
func NewMetricsPushJob(
	gatherer prometheus.Gatherer,
	endpoint string,
	interval time.Duration,
	credentials MetricsPushCredentials,
	client *http.Client,
	now libtime.CurrentDateTimeGetter,
) run.Func {
	job := &metricsPushJob{
		gatherer:    gatherer,
		endpoint:    endpoint,
		interval:    interval,
		credentials: credentials,
		client:      client,
		now:         now,
	}
	return job.run
}

type metricsPushJob struct {
	gatherer    prometheus.Gatherer
	endpoint    string
	interval    time.Duration
	credentials MetricsPushCredentials
	client      *http.Client
	now         libtime.CurrentDateTimeGetter
}

// run is the loop shape every in-process job in this repo shares: a ticker and
// ctx.Done, with a failed cycle logged and continued rather than returned.
//
// ⚠️ It waits for the first tick instead of pushing at startup, matching the
// launchd cadence it replaces: the forwarded counters are cumulative, so a push
// at start adds nothing one interval later does not.
func (j *metricsPushJob) run(ctx context.Context) error {
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			j.push(ctx)
		}
	}
}

// push runs one cycle: gather, filter, render, resolve credentials, POST.
//
// ⚠️ One millisecond stamp is taken before the gather and reused for every line,
// so the points describe the instant the cumulative counters were read rather
// than the instant each one happened to be rendered.
func (j *metricsPushJob) push(ctx context.Context) {
	stampMS := j.now.Now().Time().UnixMilli()

	samples, err := j.samples(ctx)
	if err != nil {
		metricsPushErrorf("job=%s action=push_error error=%v", metricsPushJobName, err)
		return
	}
	if len(samples) == 0 {
		// ⚠️ The cycle line is logged even when nothing was gathered. It is the
		// evidence the job ran, and an empty set is a fact about the registry that
		// an absent log line would hide — a probe could not tell "pushed nothing"
		// from "the loop is dead".
		metricsPushInfof(
			"job=%s action=push series=0 stamp=%d skipped=no_samples",
			metricsPushJobName,
			stampMS,
		)
		return
	}

	auth, err := j.credentials.Credentials(ctx)
	if err != nil {
		metricsPushErrorf(
			"job=%s action=push_error series=%d stamp=%d error=%v",
			metricsPushJobName,
			len(samples),
			stampMS,
			err,
		)
		return
	}

	body := renderMetricsPushSamples(samples, stampMS)
	if err := pushMetrics(
		ctx,
		j.client,
		j.endpoint,
		[]byte(body),
		auth,
		metricsPushAttempts,
		metricsPushRetryDelay,
	); err != nil {
		metricsPushErrorf(
			"job=%s action=push_error series=%d stamp=%d error=%v",
			metricsPushJobName,
			len(samples),
			stampMS,
			err,
		)
		return
	}
	// ⚠️ A 2xx is NOT the effect. The import endpoint acknowledges a request it
	// has queued, and a queued point that never became queryable leaves every
	// dashboard panel flat while this job reports success every five minutes — the
	// Python job this replaces read the samples back for exactly that reason.
	verified, err := verifyMetricsPush(
		ctx,
		j.client,
		j.endpoint,
		auth,
		stampMS,
		metricsPushReadBackAttempts,
		metricsPushReadBackDelay,
	)
	if err != nil {
		// ⚠️ A distinct action, not push_error: the POST landed. What failed is
		// the proof, and an operator grepping action=push must not see a line
		// claiming an effect this cycle could not confirm.
		metricsPushErrorf(
			"job=%s action=push_unverified series=%d stamp=%d error=%v",
			metricsPushJobName,
			len(samples),
			stampMS,
			err,
		)
		return
	}
	metricsPushInfof(
		"job=%s action=push series=%d stamp=%d verified=%d",
		metricsPushJobName,
		len(samples),
		stampMS,
		verified,
	)
}

// samples gathers the registry and returns its attention_* samples.
func (j *metricsPushJob) samples(ctx context.Context) ([]MetricsPushSample, error) {
	families, err := j.gatherer.Gather()
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "gather metrics failed")
	}
	var buffer bytes.Buffer
	encoder := expfmt.NewEncoder(&buffer, expfmt.FmtText)
	for _, family := range families {
		if err := encoder.Encode(family); err != nil {
			return nil, errors.Wrapf(
				ctx,
				err,
				"encode metric family '%s' failed",
				family.GetName(),
			)
		}
	}
	return parseMetricsPushSamples(buffer.String()), nil
}

// parseMetricsPushSamples keeps the attention_* sample lines of Prometheus
// exposition text, in exposition order.
//
// ⚠️ Comment lines (`# HELP`, `# TYPE`) are dropped: they carry no sample, and
// the import endpoint takes the bare series list. Labels are preserved
// byte-for-byte — a histogram bucket's `le` and a stage delta's `stage` are the
// series identity, and rewriting them would silently create a second series
// rather than update the one the dashboard queries.
func parseMetricsPushSamples(text string) []MetricsPushSample {
	samples := make([]MetricsPushSample, 0, 16)
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// The value is the last space-separated field, not the second one: a label
		// value may contain spaces inside its quotes.
		separator := strings.LastIndexByte(line, ' ')
		if separator < 0 {
			continue
		}
		series, value := line[:separator], line[separator+1:]
		if !strings.HasPrefix(series, metricsPushPrefix) {
			continue
		}
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			// Prometheus exposition permits NaN and ±Inf, and ParseFloat accepts
			// them; anything else means the line is malformed, so it is dropped
			// rather than guessed at.
			glog.Warningf("drop unparseable sample: %s", truncateForLog(line))
			continue
		}
		samples = append(samples, MetricsPushSample{Series: series, Value: value})
	}
	return samples
}

// renderMetricsPushSamples renders Prometheus exposition text with one explicit
// millisecond stamp on every line.
//
// ⚠️ One stamp for the whole cycle is deliberate: it is the instant the
// cumulative counters were read, and a per-line stamp would make a histogram's
// `_count` and `_bucket` lines disagree about which instant they describe. The
// explicit stamp is also what makes a retry safe — a second POST rewrites the
// same points instead of duplicating them.
func renderMetricsPushSamples(samples []MetricsPushSample, stampMS int64) string {
	stamp := strconv.FormatInt(stampMS, 10)
	var builder strings.Builder
	for _, sample := range samples {
		builder.WriteString(sample.Series)
		builder.WriteByte(' ')
		builder.WriteString(sample.Value)
		builder.WriteByte(' ')
		builder.WriteString(stamp)
		builder.WriteByte('\n')
	}
	return builder.String()
}

// pushMetrics POSTs body to endpoint, retrying a transient failure.
//
// ⚠️ A 4xx is raised at once: it is a real rejection (bad auth, bad format), and
// retrying it only delays the log line that names it. A 5xx and a transport
// error are retried, because both describe the far end being briefly
// unavailable. Retrying is safe: every line carries the same explicit timestamp
// and value, so a second POST rewrites the same points.
func pushMetrics(
	ctx context.Context,
	client *http.Client,
	endpoint string,
	body []byte,
	auth MetricsPushAuth,
	attempts int,
	delay time.Duration,
) error {
	if attempts < 1 {
		attempts = 1
	}
	var last error
	for attempt := 1; attempt <= attempts; attempt++ {
		err := pushMetricsOnce(ctx, client, endpoint, body, auth)
		if err == nil {
			return nil
		}
		var rejected *metricsPushRejectedError
		if stderrors.As(err, &rejected) {
			return errors.Wrapf(ctx, err, "push metrics to %s rejected", endpoint)
		}
		last = err
		if attempt < attempts {
			if waitErr := waitMetricsPushRetry(ctx, delay); waitErr != nil {
				return errors.Wrapf(ctx, waitErr, "wait before metrics push retry failed")
			}
		}
	}
	return errors.Wrapf(
		ctx,
		last,
		"push metrics to %s failed after %d attempts",
		endpoint,
		attempts,
	)
}

// metricsPushRejectedError is a 4xx from the import endpoint — a rejection that
// retrying cannot fix.
type metricsPushRejectedError struct {
	statusCode int
}

func (e *metricsPushRejectedError) Error() string {
	return "import rejected: HTTP " + strconv.Itoa(e.statusCode)
}

// pushMetricsOnce performs one import POST.
func pushMetricsOnce(
	ctx context.Context,
	client *http.Client,
	endpoint string,
	body []byte,
	auth MetricsPushAuth,
) error {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		endpoint,
		bytes.NewReader(body),
	)
	if err != nil {
		return errors.Wrapf(ctx, err, "create metrics import request failed")
	}
	req.Header.Set("Content-Type", "text/plain")
	// ⚠️ SetBasicAuth writes the Authorization header from auth. Neither the
	// header nor auth is ever logged: MetricsPushAuth redacts on format, and this
	// function formats neither.
	req.SetBasicAuth(auth.Username, auth.Password)

	resp, err := client.Do(req)
	if err != nil {
		return errors.Wrapf(ctx, err, "post metrics to %s failed", endpoint)
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return errors.Wrapf(ctx, err, "read metrics import response failed")
	}

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return &metricsPushRejectedError{statusCode: resp.StatusCode}
	default:
		return errors.Errorf(ctx, "import failed: HTTP %d", resp.StatusCode)
	}
}

// metricsPushExportURL derives the export endpoint from the import endpoint, so a
// job pointed at another store reads its samples back from that same store.
func metricsPushExportURL(endpoint string) string {
	return strings.TrimSuffix(endpoint, metricsPushImportPath) + metricsPushExportPath
}

// metricsPushExportSeries is the part of one export line this job reads.
//
// ⚠️ The timestamps are json.Number rather than int64: the field carries
// millisecond integers, and a store that renders them as a float or a string
// would otherwise fail to decode the whole line and report a landed push as
// unverified.
type metricsPushExportSeries struct {
	Timestamps []json.Number `json:"timestamps"`
}

// verifyMetricsPush counts the series carrying exactly this push's stamp,
// retrying a read-back that has not caught up yet.
//
// ⚠️ The assertion is "did MY push land", not "does the family exist": a family
// left over from an earlier push answers the second question and not the first,
// so the stamp is matched rather than the series name. A zero count after the
// last attempt is an error — the caller logs it as push_unverified rather than as
// a push.
func verifyMetricsPush(
	ctx context.Context,
	client *http.Client,
	endpoint string,
	auth MetricsPushAuth,
	stampMS int64,
	attempts int,
	delay time.Duration,
) (int, error) {
	if attempts < 1 {
		attempts = 1
	}
	var last error
	for attempt := 1; attempt <= attempts; attempt++ {
		count, err := countMetricsPushStamp(ctx, client, endpoint, auth, stampMS)
		if err == nil && count > 0 {
			return count, nil
		}
		if err != nil {
			last = err
		} else {
			last = errors.Errorf(ctx, "no series carried stamp %d", stampMS)
		}
		if attempt < attempts {
			if waitErr := waitMetricsPushRetry(ctx, delay); waitErr != nil {
				return 0, errors.Wrapf(ctx, waitErr, "wait before metrics read-back retry failed")
			}
		}
	}
	return 0, errors.Wrapf(
		ctx,
		last,
		"read back metrics from %s failed after %d attempts",
		endpoint,
		attempts,
	)
}

// countMetricsPushStamp performs one export query and counts the series whose
// timestamps carry stampMS.
func countMetricsPushStamp(
	ctx context.Context,
	client *http.Client,
	endpoint string,
	auth MetricsPushAuth,
	stampMS int64,
) (int, error) {
	selector := `{__name__=~"` + metricsPushPrefix + `.+"}`
	requestURL := metricsPushExportURL(endpoint) + "?match[]=" + url.QueryEscape(selector)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return 0, errors.Wrapf(ctx, err, "create metrics export request failed")
	}
	req.SetBasicAuth(auth.Username, auth.Password)

	resp, err := client.Do(req)
	if err != nil {
		return 0, errors.Wrapf(ctx, err, "read metrics back from %s failed", requestURL)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// The body is drained before the status is acted on so the connection can
		// be reused; nothing in it is read, since an error body is not a sample.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, metricsPushReadBackBodyLimit))
		return 0, errors.Errorf(ctx, "metrics export failed: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, metricsPushReadBackBodyLimit))
	if err != nil {
		return 0, errors.Wrapf(ctx, err, "read metrics export response failed")
	}
	return countMetricsPushStampInExport(body, stampMS), nil
}

// countMetricsPushStampInExport counts the export lines carrying stampMS.
//
// ⚠️ The export is NDJSON — one series object per line — and a line that does not
// parse is skipped rather than failing the count: one malformed series says
// nothing about whether this push landed.
func countMetricsPushStampInExport(body []byte, stampMS int64) int {
	stamp := strconv.FormatInt(stampMS, 10)
	count := 0
	for _, line := range bytes.Split(body, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var series metricsPushExportSeries
		if err := json.Unmarshal(line, &series); err != nil {
			continue
		}
		for _, timestamp := range series.Timestamps {
			if timestamp.String() == stamp {
				count++
				break
			}
		}
	}
	return count
}

// waitMetricsPushRetry waits delay, or returns as soon as ctx is cancelled.
func waitMetricsPushRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return errors.Wrap(ctx, ctx.Err(), "metrics push retry wait cancelled")
	case <-timer.C:
		return nil
	}
}

// truncateForLog bounds a line quoted in a log message, so one malformed sample
// cannot fill the log file.
func truncateForLog(line string) string {
	const maxLength = 160
	if len(line) <= maxLength {
		return line
	}
	return line[:maxLength]
}

// NewTeamVaultMetricsPushCredentials resolves the import endpoint's basic-auth
// pair from TeamVault, exactly as the Python job did.
func NewTeamVaultMetricsPushCredentials() MetricsPushCredentials {
	return &teamVaultMetricsPushCredentials{}
}

type teamVaultMetricsPushCredentials struct{}

// Credentials reads the username and password for the configured key from
// teamvault-cli, retrying a transient TeamVault outage.
//
// ⚠️ Nothing here is logged. The CLI's stdout IS the credential, so a failure is
// reported by attempt count only and neither value enters an error message.
func (c *teamVaultMetricsPushCredentials) Credentials(
	ctx context.Context,
) (MetricsPushAuth, error) {
	var last error
	for attempt := 1; attempt <= metricsPushCredentialAttempts; attempt++ {
		auth, err := c.read(ctx)
		if err == nil {
			return auth, nil
		}
		last = err
		if attempt < metricsPushCredentialAttempts {
			if waitErr := waitMetricsPushRetry(ctx, metricsPushCredentialRetryDelay); waitErr != nil {
				return MetricsPushAuth{}, errors.Wrapf(
					ctx,
					waitErr,
					"wait before credentials retry failed",
				)
			}
		}
	}
	return MetricsPushAuth{}, errors.Wrapf(
		ctx,
		last,
		"resolve credentials from teamvault failed after %d attempts",
		metricsPushCredentialAttempts,
	)
}

// read resolves both halves of the pair in one attempt.
func (c *teamVaultMetricsPushCredentials) read(ctx context.Context) (MetricsPushAuth, error) {
	username, err := readTeamVaultField(ctx, "username")
	if err != nil {
		return MetricsPushAuth{}, errors.Wrapf(ctx, err, "read teamvault username failed")
	}
	password, err := readTeamVaultField(ctx, "password")
	if err != nil {
		return MetricsPushAuth{}, errors.Wrapf(ctx, err, "read teamvault password failed")
	}
	if username == "" || password == "" {
		return MetricsPushAuth{}, errors.New(ctx, "teamvault returned an empty credential")
	}
	return MetricsPushAuth{Username: username, Password: password}, nil
}

// readTeamVaultField runs one teamvault-cli field read and returns its stdout.
//
// ⚠️ stdout is captured into a buffer and returned, never written to a log or
// wrapped into an error: it is the credential. stderr is discarded for the same
// reason — the CLI's diagnostics are not needed to act on a failure, and
// capturing them adds one more string that might carry a secret into the error
// path.
func readTeamVaultField(ctx context.Context, field string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, metricsPushCommandTimeout)
	defer cancel()
	// #nosec G204 -- the binary is a compile-time constant and field is one of
	// two literals this package passes; no caller input reaches the command.
	cmd := exec.CommandContext(
		commandCtx,
		metricsPushTeamVaultCLI,
		field,
		"--teamvault-key",
		metricsPushTeamVaultKey,
	)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	// ⚠️ An allowlisted environment, not the inherited one: this service holds
	// ATTENTION_STORE_TOKEN and ATTENTION_UPSTREAM_TOKEN, and teamvault-cli needs
	// neither. HOME is what it does need — its config lives under it — so a child
	// that cannot find its credentials without HOME still works.
	cmd.Env = childEnv()
	if err := cmd.Run(); err != nil {
		return "", errors.Wrapf(ctx, err, "run teamvault-cli %s failed", field)
	}
	return strings.TrimSpace(stdout.String()), nil
}
