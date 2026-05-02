package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParse_DefaultsValid(t *testing.T) {
	cfg, err := Parse(nil, func(string) (string, bool) { return "", false })
	require.NoError(t, err, "defaults rejected")
	assert.NotZero(t, cfg.MaxWindow, "expected non-zero MaxWindow default")
	assert.NotZero(t, cfg.BuildTimeout, "expected non-zero BuildTimeout default")
}

func TestParse_FlagsOverrideEnv(t *testing.T) {
	env := map[string]string{
		"KSG_PROM_URL":              "http://env:9090",
		"KSG_LISTEN_ADDR":           ":1111",
		"KSG_MAX_PODS":              "10",
		"KSG_BUILD_TIMEOUT":         "5s",
		"KSG_EXTERNAL_NAME_PATTERN": "://",
	}
	cfg, err := Parse(
		[]string{"--listen-addr=:2222", "--max-pods=20"},
		func(k string) (string, bool) { v, ok := env[k]; return v, ok },
	)
	require.NoError(t, err)
	assert.Equal(t, "http://env:9090", cfg.PromURL, "env not honoured")
	assert.Equal(t, ":2222", cfg.ListenAddr, "flag did not override env")
	assert.Equal(t, 20, cfg.MaxPods, "flag did not override env for max-pods")
	assert.Equal(t, "://", cfg.ExternalNamePattern, "env-only var not bound")
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
