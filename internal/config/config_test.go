package config

import "testing"

func TestParse_DefaultsValid(t *testing.T) {
	cfg, err := Parse(nil, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("defaults rejected: %v", err)
	}
	if cfg.MaxWindow == 0 || cfg.BuildTimeout == 0 {
		t.Errorf("expected non-zero defaults")
	}
}

func TestParse_FlagsOverrideEnv(t *testing.T) {
	env := map[string]string{
		"KSG_PROM_URL":       "http://env:9090",
		"KSG_LISTEN_ADDR":    ":1111",
		"KSG_MAX_PODS":       "10",
		"KSG_BUILD_TIMEOUT":  "5s",
		"KSG_EXTERNAL_NAME_PATTERN": "://",
	}
	cfg, err := Parse(
		[]string{"--listen-addr=:2222", "--max-pods=20"},
		func(k string) (string, bool) { v, ok := env[k]; return v, ok },
	)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.PromURL != "http://env:9090" {
		t.Errorf("env not honoured: %q", cfg.PromURL)
	}
	if cfg.ListenAddr != ":2222" {
		t.Errorf("flag did not override env: %q", cfg.ListenAddr)
	}
	if cfg.MaxPods != 20 {
		t.Errorf("flag did not override env for max-pods: %d", cfg.MaxPods)
	}
	if cfg.ExternalNamePattern != "://" {
		t.Errorf("env-only var not bound: %q", cfg.ExternalNamePattern)
	}
}

func TestValidate_RejectsInvalidPromURL(t *testing.T) {
	cfg := Defaults()
	cfg.PromURL = "not-a-url"
	if err := cfg.Validate(); err == nil {
		t.Errorf("expected error for invalid prom-url")
	}
}

func TestValidate_RejectsBadLogLevel(t *testing.T) {
	cfg := Defaults()
	cfg.LogLevel = "trace"
	if err := cfg.Validate(); err == nil {
		t.Errorf("expected error for invalid log-level")
	}
}

func TestSplitAndTrim(t *testing.T) {
	got := splitAndTrim(" a, b ,, c ")
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("splitAndTrim got %v", got)
	}
}
