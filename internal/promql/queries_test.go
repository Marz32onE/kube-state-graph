package promql

import (
	"strings"
	"testing"
	"time"
)

func TestRender_AllowlistInjection(t *testing.T) {
	got := Render(QPodInfo, time.Minute, "alpha|beta")
	if !strings.Contains(got, `cluster=~"alpha|beta"`) {
		t.Errorf("expected cluster regex selector, got %q", got)
	}
}

func TestRender_ServiceGraphInjectsBothEndpoints(t *testing.T) {
	got := Render(QServiceGraphTotal, time.Minute, "alpha|beta")
	if !strings.Contains(got, `client_cluster=~"alpha|beta"`) {
		t.Errorf("missing client_cluster selector: %q", got)
	}
	if !strings.Contains(got, `server_cluster=~"alpha|beta"`) {
		t.Errorf("missing server_cluster selector: %q", got)
	}
}

func TestRender_NoAllowlist(t *testing.T) {
	got := Render(QPodInfo, time.Minute, "")
	if strings.Contains(got, "cluster=~") {
		t.Errorf("did not expect selector for empty allowlist, got %q", got)
	}
}

func TestAllowlistRegex_EscapesMetacharacters(t *testing.T) {
	got := AllowlistRegex([]string{"prod.east", "stg(eu)"})
	if !strings.Contains(got, `prod\.east`) || !strings.Contains(got, `stg\(eu\)`) {
		t.Errorf("escape failed: %q", got)
	}
}

func TestRender_NodeAddressesIncludesExternalIPSelector(t *testing.T) {
	got := Render(QNodeAddresses, time.Minute, "")
	if !strings.Contains(got, `type="ExternalIP"`) {
		t.Errorf("expected type=\"ExternalIP\" selector, got %q", got)
	}
}

func TestFormatDuration(t *testing.T) {
	cases := map[time.Duration]string{
		0:                "0s",
		2 * time.Hour:    "2h",
		15 * time.Minute: "15m",
		90 * time.Second: "90s",
	}
	for in, want := range cases {
		if got := FormatDuration(in); got != want {
			t.Errorf("FormatDuration(%s) = %s, want %s", in, got, want)
		}
	}
}
