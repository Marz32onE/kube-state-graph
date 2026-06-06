// Package integration contains testcontainers-driven integration tests.
//
// Each test package starts exactly one VictoriaMetrics container in
// SetupSuite, shares it across all tests, and tears it down on completion.
// Series are injected via VM's /api/v1/import/prometheus endpoint with
// absolute timestamps so time-bucket alignment is deterministic.
//
// These tests require Docker on the host. They are skipped when the
// `DOCKER_HOST` environment variable is unset and Docker isn't reachable —
// see vmsuite.go.
package integration
