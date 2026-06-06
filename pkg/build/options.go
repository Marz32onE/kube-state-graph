package build

import "time"

// Options configures a Builder. It carries only the build-relevant settings,
// decoupled from any server-side configuration struct, so the package is
// importable by other modules without dragging in internal/config.
type Options struct {
	// MetricPrefix is prepended to kube-state-metrics-shaped metric names
	// (see design.md D26). Empty means no prefix.
	MetricPrefix string
	// APITimeout bounds the cheap up{} probe used for the outside-retention
	// check. Zero means the probe inherits the caller's context deadline.
	APITimeout time.Duration
}
