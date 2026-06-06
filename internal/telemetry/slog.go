package telemetry

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/trace"
)

// NewSlogHandler returns a slog.Handler that writes records to the supplied
// local handler and, when telemetry is enabled, mirrors them to the global
// OTLP LoggerProvider via otelslog. The local handler's output is enriched
// with trace_id / span_id keys whenever a record is emitted from a context
// carrying an active span.
func NewSlogHandler(local slog.Handler) slog.Handler {
	return &fanoutHandler{
		local:  &spanCorrelatedHandler{inner: local},
		bridge: otelslog.NewHandler(ServiceName),
	}
}

// fanoutHandler delivers each record to two underlying handlers. The bridge
// is a no-op when the global LoggerProvider is no-op (i.e. tracing disabled).
type fanoutHandler struct {
	local  slog.Handler
	bridge slog.Handler
}

func (h *fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.local.Enabled(ctx, level) || h.bridge.Enabled(ctx, level)
}

func (h *fanoutHandler) Handle(ctx context.Context, record slog.Record) error {
	if h.local.Enabled(ctx, record.Level) {
		if err := h.local.Handle(ctx, record.Clone()); err != nil {
			return err
		}
	}
	if h.bridge.Enabled(ctx, record.Level) {
		if err := h.bridge.Handle(ctx, record); err != nil {
			return err
		}
	}
	return nil
}

func (h *fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &fanoutHandler{
		local:  h.local.WithAttrs(attrs),
		bridge: h.bridge.WithAttrs(attrs),
	}
}

func (h *fanoutHandler) WithGroup(name string) slog.Handler {
	return &fanoutHandler{
		local:  h.local.WithGroup(name),
		bridge: h.bridge.WithGroup(name),
	}
}

// spanCorrelatedHandler injects trace_id and span_id into every record whose
// context carries a recording span. The OTLP bridge already pulls span context
// directly from ctx, so this handler only enriches the local stderr stream.
type spanCorrelatedHandler struct {
	inner slog.Handler
}

func (h *spanCorrelatedHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *spanCorrelatedHandler) Handle(ctx context.Context, record slog.Record) error {
	sc := trace.SpanContextFromContext(ctx)
	if sc.IsValid() {
		record.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.inner.Handle(ctx, record)
}

func (h *spanCorrelatedHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &spanCorrelatedHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *spanCorrelatedHandler) WithGroup(name string) slog.Handler {
	return &spanCorrelatedHandler{inner: h.inner.WithGroup(name)}
}
