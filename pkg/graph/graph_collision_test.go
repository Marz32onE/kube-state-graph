package graph

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// syncBuffer is a goroutine-safe writer for capturing log output.
// (Local copy of the pkg/build test helper — it lives in another package.)
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

// A Service and a PVC sharing (cluster, namespace, name) mint byte-identical
// IDs (ServiceID mirrors PVCID keying). NewGraph must keep the FIRST node for
// a duplicate ID — assemble order puts authoritative topology nodes (PVCs)
// before on-demand service-graph nodes — and warn once per collision.
func TestNewGraph_DuplicateNodeID_KeepsFirstAndWarns(t *testing.T) {
	buf := captureLogs(t)

	id := PVCID("alpha", "db", "postgres") // == ServiceID("alpha", "db", "postgres")
	pvc := &PVCNode{
		IDValue:     id,
		NameValue:   "postgres",
		LabelsValue: map[string]string{"cluster": "alpha", "namespace": "db"},
	}
	svc := &ServiceNode{
		IDValue:     id,
		NameValue:   "postgres",
		LabelsValue: map[string]string{"cluster": "alpha", "namespace": "db"},
	}

	g := NewGraph([]GraphNode{pvc, svc}, nil, time.Unix(0, 0))

	got, ok := g.NodesByID[id]
	require.True(t, ok, "colliding ID must still resolve")
	assert.Equal(t, NodeTypePVC, got.Type(), "first node (PVC) must win, not the later service")
	assert.Same(t, GraphNode(pvc), got)

	out := buf.String()
	assert.Contains(t, out, "level=WARN")
	assert.Contains(t, out, "duplicate node ID")
	assert.Contains(t, out, "id="+id)
	assert.Contains(t, out, "kept_type=pvc")
	assert.Contains(t, out, "dropped_type=service")
	assert.Equal(t, 1, strings.Count(out, "duplicate node ID"),
		"exactly one warn per collision")
}

// The dropped node must be unreachable through projection — the view that
// feeds serialisation derives its node set from NodesByID, so the colliding
// ID must surface exactly once, typed pvc.
func TestNewGraph_DuplicateNodeID_ProjectionEmitsOnlyFirst(t *testing.T) {
	captureLogs(t) // silence the expected warn

	id := PVCID("alpha", "db", "postgres")
	pvc := &PVCNode{
		IDValue:     id,
		NameValue:   "postgres",
		LabelsValue: map[string]string{"cluster": "alpha", "namespace": "db"},
	}
	svc := &ServiceNode{
		IDValue:     id,
		NameValue:   "postgres",
		LabelsValue: map[string]string{"cluster": "alpha", "namespace": "db"},
	}

	g := NewGraph([]GraphNode{pvc, svc}, nil, time.Unix(0, 0))
	view := Project(g, Scope{})

	var matches []GraphNode
	for _, n := range view.Nodes {
		if n.ID() == id {
			matches = append(matches, n)
		}
	}
	require.Len(t, matches, 1, "the colliding ID must be emitted exactly once")
	assert.Equal(t, NodeTypePVC, matches[0].Type())
}

func TestNewGraph_NoWarnWithoutCollision(t *testing.T) {
	buf := captureLogs(t)

	g := NewGraph([]GraphNode{
		&PVCNode{IDValue: PVCID("alpha", "db", "data"), NameValue: "data",
			LabelsValue: map[string]string{"cluster": "alpha", "namespace": "db"}},
		&ServiceNode{IDValue: ServiceID("alpha", "db", "postgres"), NameValue: "postgres",
			LabelsValue: map[string]string{"cluster": "alpha", "namespace": "db"}},
	}, nil, time.Unix(0, 0))

	assert.Len(t, g.NodesByID, 2)
	assert.NotContains(t, buf.String(), "duplicate node ID")
}
