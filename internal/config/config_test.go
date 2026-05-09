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
		"KSG_PROM_URL":              "http://env:9090",
		"KSG_LISTEN_ADDR":           ":1111",
		"KSG_BUILD_TIMEOUT":         "5s",
		"KSG_API_TIMEOUT":           "2s",
		"KSG_EXTERNAL_NAME_PATTERN": "://",
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
	assert.Equal(t, "://", cfg.ExternalNamePattern, "env-only var not bound")
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
