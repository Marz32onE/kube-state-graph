package promql

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRender_AllowlistInjection(t *testing.T) {
	got := Render(QPodInfo, time.Minute, "alpha|beta")
	assert.Contains(t, got, `cluster=~"alpha|beta"`)
}

func TestRender_ServiceGraphInjectsBothEndpoints(t *testing.T) {
	got := Render(QServiceGraphTotal, time.Minute, "alpha|beta")
	assert.Contains(t, got, `client_cluster=~"alpha|beta"`)
	assert.Contains(t, got, `server_cluster=~"alpha|beta"`)
}

func TestRender_NoAllowlist(t *testing.T) {
	got := Render(QPodInfo, time.Minute, "")
	assert.NotContains(t, got, "cluster=~")
}

func TestAllowlistRegex_EscapesMetacharacters(t *testing.T) {
	got := AllowlistRegex([]string{"prod.east", "stg(eu)"})
	assert.Contains(t, got, `prod\.east`)
	assert.Contains(t, got, `stg\(eu\)`)
}

func TestRender_NodeAddressesIncludesExternalIPSelector(t *testing.T) {
	got := Render(QNodeAddresses, time.Minute, "")
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
