package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParse_DefaultsValid(t *testing.T) {
	cfg, err := Parse(nil, func(string) (string, bool) { return "", false })
	require.NoError(t, err, "defaults rejected")
	assert.NotZero(t, cfg.BuildTimeout, "expected non-zero BuildTimeout default")
	assert.NotZero(t, cfg.APITimeout, "expected non-zero APITimeout default")
}

func TestParse_FlagsOverrideEnv(t *testing.T) {
	env := map[string]string{
		"KSG_PROM_URL":            "http://env:9090",
		"KSG_LISTEN_ADDR":         ":1111",
		"KSG_BUILD_TIMEOUT":       "5s",
		"KSG_API_TIMEOUT":         "2s",
		"KSG_OTHERS_NAME_PATTERN": "://",
	}
	cfg, err := Parse(
		[]string{"--listen-addr=:2222", "--api-timeout=3s"},
		func(k string) (string, bool) { v, ok := env[k]; return v, ok },
	)
	require.NoError(t, err)
	assert.Equal(t, "http://env:9090", cfg.PromURL, "env not honoured")
	assert.Equal(t, ":2222", cfg.ListenAddr, "flag did not override env")
	assert.Equal(t, 3*time.Second, cfg.APITimeout, "flag did not override env for api-timeout")
	assert.Equal(t, 5*time.Second, cfg.BuildTimeout, "build-timeout env not honoured")
	assert.Equal(t, "://", cfg.OthersNamePattern, "env-only var not bound")
}

func TestValidate_RejectsZeroAPITimeout(t *testing.T) {
	cfg := Defaults()
	cfg.APITimeout = 0
	assert.Error(t, cfg.Validate(), "expected error for zero api-timeout")
}

func TestValidate_RejectsInvalidPromURL(t *testing.T) {
	cfg := Defaults()
	cfg.PromURL = "not-a-url"
	assert.Error(t, cfg.Validate(), "expected error for invalid prom-url")
}

func TestValidate_RejectsBadLogLevel(t *testing.T) {
	cfg := Defaults()
	cfg.LogLevel = "trace"
	assert.Error(t, cfg.Validate(), "expected error for invalid log-level")
}

func TestSplitAndTrim(t *testing.T) {
	assert.Equal(t, []string{"a", "b", "c"}, splitAndTrim(" a, b ,, c "))
}

func TestParse_MetricPrefix_DefaultEmpty(t *testing.T) {
	cfg, err := Parse(nil, func(string) (string, bool) { return "", false })
	require.NoError(t, err)
	assert.Empty(t, cfg.MetricPrefix, "metric-prefix default should be empty")
}

func TestParse_MetricPrefix_EnvAndFlag(t *testing.T) {
	t.Run("env wins over default", func(t *testing.T) {
		cfg, err := Parse(nil, func(k string) (string, bool) {
			if k == "KSG_METRIC_PREFIX" {
				return "o11y_", true
			}
			return "", false
		})
		require.NoError(t, err)
		assert.Equal(t, "o11y_", cfg.MetricPrefix)
	})
	t.Run("flag wins over env", func(t *testing.T) {
		cfg, err := Parse(
			[]string{"--metric-prefix=beta_"},
			func(k string) (string, bool) {
				if k == "KSG_METRIC_PREFIX" {
					return "acme_", true
				}
				return "", false
			},
		)
		require.NoError(t, err)
		assert.Equal(t, "beta_", cfg.MetricPrefix)
	})
}

func TestValidate_MetricPrefix(t *testing.T) {
	cases := map[string]struct {
		prefix  string
		wantErr bool
	}{
		"empty":             {prefix: "", wantErr: false},
		"underscore suffix": {prefix: "o11y_", wantErr: false},
		"colon allowed":     {prefix: "acme:tenant_", wantErr: false},
		"alpha only":        {prefix: "acme", wantErr: false},
		"hyphen rejected":   {prefix: "o11y-bad!", wantErr: true},
		"leading digit":     {prefix: "1starts_with_digit", wantErr: true},
		"trailing space":    {prefix: "o11y_ ", wantErr: true},
		"embedded space":    {prefix: "o 11y_", wantErr: true},
		"unicode rejected":  {prefix: "o11y✓_", wantErr: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := Defaults()
			cfg.MetricPrefix = tc.prefix
			err := cfg.Validate()
			if tc.wantErr {
				require.Error(t, err, "expected error for prefix %q", tc.prefix)
				assert.Contains(t, err.Error(), "metric-prefix", "error should mention metric-prefix")
			} else {
				require.NoError(t, err, "did not expect error for prefix %q", tc.prefix)
			}
		})
	}
}
