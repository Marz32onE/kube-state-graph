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

func TestRender_ServiceGraphInjectsClusterAllowlist(t *testing.T) {
	got := Render(QServiceGraphTotal, time.Minute, "alpha|beta")
	// Service-graph metrics carry only a single `cluster` label (the trace
	// source / client side). Server-side cluster filtering is recovered at
	// build time via the topology pod-UID index, not by PromQL.
	assert.Contains(t, got, `cluster=~"alpha|beta"`)
	assert.NotContains(t, got, "client_cluster")
	assert.NotContains(t, got, "server_cluster")
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

// Hyphens are RE2-special only inside character classes ([...]). The generated
// PromQL uses cluster=~"a|b|c" alternation — no character classes — so hyphens
// are intentionally NOT escaped. This test documents that contract; if a future
// change introduces character-class usage, this test must be revisited.
func TestAllowlistRegex_HyphenNotEscaped(t *testing.T) {
	got := AllowlistRegex([]string{"prod-east", "stg-eu-1"})
	assert.Contains(t, got, "prod-east")
	assert.Contains(t, got, "stg-eu-1")
	assert.NotContains(t, got, `\-`, "hyphen should not be backslash-escaped in alternation context")
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
