package build

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
)

// syncBuffer is a goroutine-safe writer for capturing log output.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogs swaps the default slog logger for the test's duration and
// returns the buffer collecting its output.
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

func TestParseTopology_WarnsOnMissingClusterLabel(t *testing.T) {
	buf := captureLogs(t)
	parseTopology(topologyVectors{
		Pod: sampleVec(model.Sample{
			Metric: model.Metric{"namespace": "shop", "pod": "checkout", "uid": "uid-1", "node": "worker-0"},
			Value:  1,
		}),
		Node: sampleVec(model.Sample{
			Metric: model.Metric{"node": "worker-0"},
			Value:  1,
		}),
	})
	out := buf.String()
	assert.Contains(t, out, "level=WARN")
	assert.Contains(t, out, "missing cluster label")
	assert.Contains(t, out, "metric=kube_pod_info")
	assert.Contains(t, out, "metric=kube_node_info")
}

func TestParseTopology_NoWarnWhenClusterPresent(t *testing.T) {
	buf := captureLogs(t)
	parseTopology(topologyVectors{
		Pod: sampleVec(model.Sample{
			Metric: model.Metric{"cluster": "alpha", "namespace": "shop", "pod": "checkout", "uid": "uid-1", "node": "worker-0"},
			Value:  1,
		}),
	})
	assert.NotContains(t, buf.String(), "missing cluster label")
}

func TestParseTopology_AggregatesMissingClusterSamples(t *testing.T) {
	buf := captureLogs(t)
	parseTopology(topologyVectors{
		Pod: sampleVec(
			model.Sample{
				Metric: model.Metric{"namespace": "shop", "pod": "checkout", "uid": "uid-1", "node": "worker-0"},
				Value:  1,
			},
			model.Sample{
				Metric: model.Metric{"namespace": "shop", "pod": "billing", "uid": "uid-2", "node": "worker-0"},
				Value:  1,
			},
		),
	})
	out := buf.String()
	assert.Equal(t, 1, strings.Count(out, "metric=kube_pod_info"),
		"one aggregated warn line per metric, not one per sample")
	assert.Contains(t, out, "samples=2")
}

func TestParseServiceGraph_WarnsOnMissingClusterLabel(t *testing.T) {
	buf := captureLogs(t)
	vec := sampleVec(model.Sample{
		Metric: model.Metric{
			"client":             "checkout",
			"server":             "billing",
			"client_k8s_pod_uid": "uid-1",
			"server_k8s_pod_uid": "uid-2",
		},
		Value: 1,
	})
	parseServiceGraph(vec, parseTopology(topologyVectors{}))
	out := buf.String()
	assert.Contains(t, out, "level=WARN")
	assert.Contains(t, out, "missing cluster label")
	assert.Contains(t, out, "metric=traces_service_graph_request_total")
	assert.Contains(t, out, "samples=1")
}
