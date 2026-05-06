package config

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Config holds the parsed runtime configuration for the kube-state-graph server.
type Config struct {
	PromURL                  string
	ListenAddr               string
	MaxWindow                time.Duration
	MaxSkew                  time.Duration
	MaxPods                  int
	BuildTimeout             time.Duration
	BuildConcurrency         int
	ClusterDiscoveryLookback time.Duration
	ClustersAllowlist        []string
	ExternalNamePattern      string
	APIKeysFile              string
	APIKeys                  string
	APIKeysReloadInterval    time.Duration
	EnableDebug              bool
	LogLevel                 string
}

// LookupEnvFunc matches os.LookupEnv signature so tests can inject env values.
type LookupEnvFunc func(string) (string, bool)

// Defaults returns a Config populated with the documented v1 defaults.
func Defaults() Config {
	return Config{
		PromURL:                  "http://localhost:8428",
		ListenAddr:               ":8080",
		MaxWindow:                24 * time.Hour,
		MaxSkew:                  time.Minute,
		MaxPods:                  5000,
		BuildTimeout:             15 * time.Second,
		BuildConcurrency:         8,
		ClusterDiscoveryLookback: time.Hour,
		ClustersAllowlist:        nil,
		ExternalNamePattern:      "",
		APIKeysFile:              "",
		APIKeys:                  "",
		APIKeysReloadInterval:    30 * time.Second,
		EnableDebug:              false,
		LogLevel:                 "info",
	}
}

// Parse parses CLI args + env vars into a Config and validates it.
// Env vars override defaults; flags override env vars.
func Parse(args []string, lookup LookupEnvFunc) (Config, error) {
	cfg := Defaults()
	applyEnv(&cfg, lookup)

	fs := flag.NewFlagSet("kube-state-graph", flag.ContinueOnError)
	fs.StringVar(&cfg.PromURL, "prom-url", cfg.PromURL, "VictoriaMetrics Prometheus-compatible URL.")
	fs.StringVar(&cfg.ListenAddr, "listen-addr", cfg.ListenAddr, "HTTP listen address.")
	fs.DurationVar(&cfg.MaxWindow, "max-window", cfg.MaxWindow, "Maximum allowed end-start time window.")
	fs.DurationVar(&cfg.MaxSkew, "max-skew", cfg.MaxSkew, "Maximum allowed (end - now) skew.")
	fs.IntVar(&cfg.MaxPods, "max-pods", cfg.MaxPods, "Maximum cluster size (count of distinct kube_pod_info series).")
	fs.DurationVar(&cfg.BuildTimeout, "build-timeout", cfg.BuildTimeout, "Per-build context timeout.")
	fs.IntVar(&cfg.BuildConcurrency, "build-concurrency", cfg.BuildConcurrency, "Maximum concurrent builds.")
	fs.DurationVar(&cfg.ClusterDiscoveryLookback, "cluster-discovery-lookback", cfg.ClusterDiscoveryLookback, "Lookback window for cluster discovery.")
	fs.Func("clusters-allowlist", "Comma-separated list of clusters to expose (empty = all).", func(v string) error {
		cfg.ClustersAllowlist = splitAndTrim(v)
		return nil
	})
	fs.StringVar(&cfg.ExternalNamePattern, "external-name-pattern", cfg.ExternalNamePattern, "Substring; when set and matched against client/server label values, that endpoint becomes an external node.")
	fs.StringVar(&cfg.APIKeysFile, "api-keys-file", cfg.APIKeysFile, "Path to a file holding accepted API keys (one per line, # comments allowed). Reloaded periodically. Takes precedence over --api-keys.")
	fs.StringVar(&cfg.APIKeys, "api-keys", cfg.APIKeys, "Comma-separated list of accepted API keys. Used when --api-keys-file is unset.")
	fs.DurationVar(&cfg.APIKeysReloadInterval, "api-keys-reload-interval", cfg.APIKeysReloadInterval, "How often to re-read --api-keys-file. Set to 0 to disable hot reload.")
	fs.BoolVar(&cfg.EnableDebug, "enable-debug", cfg.EnableDebug, "Enable /debug/* endpoints.")
	fs.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "Log level: debug, info, warn, error.")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func applyEnv(cfg *Config, lookup LookupEnvFunc) {
	getStr := func(env string, dst *string) {
		if v, ok := lookup(env); ok {
			*dst = v
		}
	}
	getDur := func(env string, dst *time.Duration) {
		if v, ok := lookup(env); ok {
			if d, err := time.ParseDuration(v); err == nil {
				*dst = d
			}
		}
	}
	getInt := func(env string, dst *int) {
		if v, ok := lookup(env); ok {
			if n, err := strconv.Atoi(v); err == nil {
				*dst = n
			}
		}
	}
	getBool := func(env string, dst *bool) {
		if v, ok := lookup(env); ok {
			if b, err := strconv.ParseBool(v); err == nil {
				*dst = b
			}
		}
	}

	getStr("KSG_PROM_URL", &cfg.PromURL)
	getStr("KSG_LISTEN_ADDR", &cfg.ListenAddr)
	getDur("KSG_MAX_WINDOW", &cfg.MaxWindow)
	getDur("KSG_MAX_SKEW", &cfg.MaxSkew)
	getInt("KSG_MAX_PODS", &cfg.MaxPods)
	getDur("KSG_BUILD_TIMEOUT", &cfg.BuildTimeout)
	getInt("KSG_BUILD_CONCURRENCY", &cfg.BuildConcurrency)
	getDur("KSG_CLUSTER_DISCOVERY_LOOKBACK", &cfg.ClusterDiscoveryLookback)
	if v, ok := lookup("KSG_CLUSTERS_ALLOWLIST"); ok {
		cfg.ClustersAllowlist = splitAndTrim(v)
	}
	getStr("KSG_EXTERNAL_NAME_PATTERN", &cfg.ExternalNamePattern)
	getStr("KSG_API_KEYS_FILE", &cfg.APIKeysFile)
	getStr("KSG_API_KEYS", &cfg.APIKeys)
	getDur("KSG_API_KEYS_RELOAD_INTERVAL", &cfg.APIKeysReloadInterval)
	getBool("KSG_ENABLE_DEBUG", &cfg.EnableDebug)
	getStr("KSG_LOG_LEVEL", &cfg.LogLevel)
}

// Validate checks Config invariants.
func (c Config) Validate() error {
	if c.PromURL == "" {
		return errors.New("prom-url is required")
	}
	u, err := url.Parse(c.PromURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("prom-url is not a valid URL: %q", c.PromURL)
	}
	if c.ListenAddr == "" {
		return errors.New("listen-addr is required")
	}
	if c.MaxWindow <= 0 {
		return errors.New("max-window must be positive")
	}
	if c.MaxSkew < 0 {
		return errors.New("max-skew must be non-negative")
	}
	if c.MaxPods <= 0 {
		return errors.New("max-pods must be positive")
	}
	if c.BuildTimeout <= 0 {
		return errors.New("build-timeout must be positive")
	}
	if c.BuildConcurrency <= 0 {
		return errors.New("build-concurrency must be positive")
	}
	if c.ClusterDiscoveryLookback <= 0 {
		return errors.New("cluster-discovery-lookback must be positive")
	}
	switch strings.ToLower(c.LogLevel) {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("invalid log-level: %q", c.LogLevel)
	}
	return nil
}

func splitAndTrim(v string) []string {
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
