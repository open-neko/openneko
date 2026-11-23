package gjhost

import (
	"context"
	"net/http"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// TelemetryEnabled follows the worker's rule: OTEL_SDK_DISABLED wins, then
// OPENNEKO_OTEL_ENABLED, then an OTLP endpoint turns export on.
func TelemetryEnabled(getenv func(string) string) bool {
	if truthy(getenv("OTEL_SDK_DISABLED")) {
		return false
	}
	if v := getenv("OPENNEKO_OTEL_ENABLED"); v != "" {
		return truthy(v)
	}
	return getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != "" || getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != ""
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// startTelemetry sets the global tracer that GraphJin uses to export over
// OTLP/HTTP. The exporter reads the standard OTEL_EXPORTER_OTLP_* variables.
// serv.InitTelemetry in the pinned release fails on this SDK's resource
// schema, so serve builds the provider itself.
func startTelemetry(ctx context.Context) (func(context.Context), error) {
	if !TelemetryEnabled(os.Getenv) {
		return func(context.Context) {}, nil
	}
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	name := os.Getenv("OTEL_SERVICE_NAME")
	if name == "" {
		name = "openneko-graphjin"
	}
	host, _ := os.Hostname()
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		attribute.String("service.name", name),
		attribute.String("service.instance.id", host),
	))
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(metadataOnly{exporter}), sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return func(ctx context.Context) { _ = tp.Shutdown(ctx) }, nil
}

// withTraceContext gives every route the caller's trace. GraphJin extracts it
// only on its GraphQL, REST and agent routes; MCP tool calls need it too.
func withTraceContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// metadataOnly drops error text, exception events and URL query strings,
// which can hold database values, before spans leave serve.
type metadataOnly struct{ sdktrace.SpanExporter }

func (m metadataOnly) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	out := make([]sdktrace.ReadOnlySpan, len(spans))
	for i, s := range spans {
		out[i] = redactedSpan{s}
	}
	return m.SpanExporter.ExportSpans(ctx, out)
}

type redactedSpan struct{ sdktrace.ReadOnlySpan }

func (s redactedSpan) Status() sdktrace.Status {
	st := s.ReadOnlySpan.Status()
	st.Description = ""
	return st
}

func (s redactedSpan) Events() []sdktrace.Event { return nil }

func (s redactedSpan) Attributes() []attribute.KeyValue {
	attrs := s.ReadOnlySpan.Attributes()
	out := make([]attribute.KeyValue, 0, len(attrs))
	for _, kv := range attrs {
		switch {
		case kv.Key == "http.path" || kv.Key == "url.full" || kv.Key == "http.url":
			path, _, _ := strings.Cut(kv.Value.AsString(), "?")
			out = append(out, attribute.String(string(kv.Key), path))
		case strings.HasPrefix(string(kv.Key), "exception."), strings.HasPrefix(string(kv.Key), "db.statement"):
		default:
			out = append(out, kv)
		}
	}
	return out
}
