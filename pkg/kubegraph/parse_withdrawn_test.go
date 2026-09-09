package kubegraph_test

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/akira-core/kube-state-graph/pkg/kubegraph"
)

// `edge_type` is withdrawn. Like `name` / `root` / `depth` / `direction` it is
// an unknown parameter now: ignored without error, and its VALUE is never
// inspected — so a value the old registry-backed validation rejected
// ("pod-calls-pods") is no longer a 400. The parsed Request must be identical
// to the one the same query without the key produces, which is what makes the
// response bodies byte-identical.
func TestParseValues_WithdrawnEdgeTypeIgnored(t *testing.T) {
	base := url.Values{}
	base.Set("start", "2026-05-01T11:00:00Z")
	base.Set("end", "2026-05-01T12:00:00Z")
	base.Set("namespace", "shop")

	want, err := kubegraph.ParseValues(base)
	require.NoError(t, err)

	for _, value := range []string{"pod-calls-pod", "pod-calls-pods", "storage-flow", ""} {
		t.Run(value, func(t *testing.T) {
			v := url.Values{}
			for k, vs := range base {
				v[k] = vs
			}
			v.Set("edge_type", value)

			got, err := kubegraph.ParseValues(v)
			require.NoError(t, err, "a withdrawn parameter must never fail the request")
			assert.Equal(t, want, got, "edge_type must not change the parsed request")
		})
	}
}

// Several values, one of them unregistered, is still not an error.
func TestParseValues_RepeatedEdgeTypeIgnored(t *testing.T) {
	v := url.Values{}
	v.Set("start", "2026-05-01T11:00:00Z")
	v.Set("end", "2026-05-01T12:00:00Z")
	v["edge_type"] = []string{"pod-calls-pod", "pod-calls-pods"}

	_, err := kubegraph.ParseValues(v)
	require.NoError(t, err)
}
