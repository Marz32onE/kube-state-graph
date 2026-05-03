package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/marz32one/kube-state-graph/internal/api"
	"github.com/marz32one/kube-state-graph/internal/build"
	"github.com/marz32one/kube-state-graph/internal/cache"
	"github.com/marz32one/kube-state-graph/internal/config"
	"github.com/marz32one/kube-state-graph/internal/observability"
	"github.com/marz32one/kube-state-graph/internal/promql"
)

// VMImage is the pinned VictoriaMetrics container image used across the
// integration suite. Pinned by tag — never `:latest` — per D20.
const VMImage = "victoriametrics/victoria-metrics:v1.107.0"

// VMSuite is the base suite type embedded by every integration suite that
// needs a real VictoriaMetrics backend. It starts one container per suite,
// exposes helpers for series ingestion + readiness, and tears the container
// down at the end.
type VMSuite struct {
	suite.Suite

	ctx       context.Context
	cancel    context.CancelFunc
	container testcontainers.Container
	vmURL     string
}

// SkipIfDockerUnavailable short-circuits the suite when Docker isn't usable.
// Used by `go test ./...` runs on developer machines without Docker so the
// rest of the test tree still runs.
func SkipIfDockerUnavailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker binary not in PATH; skipping integration suite")
	}
	cmd := exec.Command("docker", "info")
	if err := cmd.Run(); err != nil {
		t.Skip("docker daemon unreachable; skipping integration suite")
	}
}

// SetupSuite starts the VictoriaMetrics container and waits for readiness.
func (s *VMSuite) SetupSuite() {
	SkipIfDockerUnavailable(s.T())
	s.ctx, s.cancel = context.WithCancel(context.Background())

	req := testcontainers.ContainerRequest{
		Image:        VMImage,
		ExposedPorts: []string{"8428/tcp"},
		// `-search.latencyOffset=0` disables VM's default 30s ingestion-latency
		// rewind so queries at time=T can immediately see samples ingested at T.
		// Without this, fixtures pinned to fixedNow are invisible until 30s pass.
		Cmd:        []string{"-search.latencyOffset=0s"},
		WaitingFor: wait.ForHTTP("/health").WithPort("8428/tcp").WithStartupTimeout(60 * time.Second),
	}
	c, err := testcontainers.GenericContainer(s.ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	s.Require().NoError(err, "start VictoriaMetrics container")
	s.container = c

	host, err := c.Host(s.ctx)
	s.Require().NoError(err)
	port, err := c.MappedPort(s.ctx, "8428/tcp")
	s.Require().NoError(err)
	s.vmURL = fmt.Sprintf("http://%s:%s", host, port.Port())

	s.WaitForReady(10 * time.Second)
}

// TearDownSuite stops and removes the container.
func (s *VMSuite) TearDownSuite() {
	if s.container != nil {
		_ = s.container.Terminate(s.ctx)
	}
	if s.cancel != nil {
		s.cancel()
	}
}

// VMURL returns the base URL of the running VictoriaMetrics instance.
func (s *VMSuite) VMURL() string { return s.vmURL }

// WaitForReady polls VM's `up{}` (effectively, /-/ready) until it answers or
// the budget is exhausted.
func (s *VMSuite) WaitForReady(budget time.Duration) {
	s.T().Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		resp, err := http.Get(s.vmURL + "/-/ready") //nolint:noctx // best-effort readiness probe
		if err == nil && resp.StatusCode == http.StatusOK {
			_ = resp.Body.Close()
			return
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
		time.Sleep(200 * time.Millisecond)
	}
	s.Require().FailNowf("vm_not_ready", "VictoriaMetrics did not become ready within %s", budget)
}

// IngestExpFmt POSTs Prometheus exposition-format text to VM's
// /api/v1/import/prometheus endpoint. Each line is one sample.
func (s *VMSuite) IngestExpFmt(exposition string) {
	s.T().Helper()
	req, err := http.NewRequestWithContext(s.ctx, http.MethodPost,
		s.vmURL+"/api/v1/import/prometheus", strings.NewReader(exposition))
	s.Require().NoError(err)
	resp, err := http.DefaultClient.Do(req)
	s.Require().NoError(err)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	s.Require().Truef(resp.StatusCode >= 200 && resp.StatusCode < 300,
		"VM ingest returned %d: %s", resp.StatusCode, body)
}

// WaitForSeries polls VM until the supplied PromQL returns a non-empty
// vector at the given evaluation time or the budget is exhausted. evalTime
// is forwarded as the `time=` parameter; pass time.Time{} to evaluate at
// the server's current time.
func (s *VMSuite) WaitForSeries(query string, evalTime time.Time, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		v := url.Values{"query": []string{query}}
		if !evalTime.IsZero() {
			v.Set("time", strconv.FormatInt(evalTime.Unix(), 10))
		}
		resp, err := http.Get(s.vmURL + "/api/v1/query?" + v.Encode()) //nolint:noctx // poll loop
		if err == nil && resp.StatusCode == http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			// Crude check: response contains a non-empty result array.
			if !bytes.Contains(body, []byte(`"result":[]`)) {
				return true
			}
		} else if resp != nil {
			_ = resp.Body.Close()
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// StartAPIServer constructs an in-process API server pointed at the running
// VictoriaMetrics container, wraps it in httptest.NewServer, and returns the
// server's base URL. Caller-supplied configure func may tweak the Config.
func (s *VMSuite) StartAPIServer(configure func(*config.Config)) *httptest.Server {
	s.T().Helper()
	cfg := config.Defaults()
	cfg.PromURL = s.vmURL
	cfg.LogLevel = "error"
	if configure != nil {
		configure(&cfg)
	}
	s.Require().NoError(cfg.Validate())

	logger := observability.NewLogger(cfg.LogLevel)
	metrics := observability.NewMetrics()
	prom, err := promql.New(cfg.PromURL, metrics)
	s.Require().NoError(err)
	c, err := cache.New(cfg.CacheMaxCostBytes, metrics)
	s.Require().NoError(err)
	s.T().Cleanup(c.Close)

	builder := build.New(prom, cfg, metrics)
	srv := api.New(cfg, builder, c, prom, metrics, logger)

	httpSrv := httptest.NewServer(srv.Handler())
	s.T().Cleanup(httpSrv.Close)
	return httpSrv
}
