package promql

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRender_PodInfoNoClusterFilter(t *testing.T) {
	got := Render(QPodInfo, time.Minute)
	assert.Contains(t, got, "kube_pod_info")
	assert.Contains(t, got, "[1m]")
	assert.NotContains(t, got, "cluster=~", "PromQL must not push cluster filtering")
}

func TestRender_ServiceGraphTotal(t *testing.T) {
	got := Render(QServiceGraphTotal, time.Minute)
	assert.Contains(t, got, "traces_service_graph_request_total")
	assert.NotContains(t, got, "client_cluster")
	assert.NotContains(t, got, "server_cluster")
}

func TestRender_NodeAddressesIncludesExternalIPSelector(t *testing.T) {
	got := Render(QNodeAddresses, time.Minute)
	assert.Contains(t, got, `type="ExternalIP"`)
}

func TestFormatDuration(t *testing.T) {
	cases := map[time.Duration]string{
		0:                "0s",
		2 * time.Hour:    "2h",
		15 * time.Minute: "15m",
		90 * time.Second: "90s",
	}
	for in, want := range cases {
		assert.Equal(t, want, FormatDuration(in), "FormatDuration(%s)", in)
	}
}
