package telemetry

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

// Tracer returns the kube-state-graph tracer from the globally registered
// TracerProvider. Safe to call before Init — it falls back to the no-op
// provider until Init wires the SDK.
func Tracer() trace.Tracer {
	return otel.Tracer(ServiceName)
}
