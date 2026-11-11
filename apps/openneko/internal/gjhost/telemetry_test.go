package gjhost

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

const (
	callerTrace = "4bf92f3577b34da6a3ce929d0e0e4736"
	traceparent = "00-" + callerTrace + "-00f067aa0ba902b7-01"
)

func TestTelemetryEnabledFollowsTheWorkerRule(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want bool
	}{
		{map[string]string{}, false},
		{map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "http://c:4318/v1/traces"}, true},
		{map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://c:4318"}, true},
		{map[string]string{"OPENNEKO_OTEL_ENABLED": "on"}, true},
		{map[string]string{"OPENNEKO_OTEL_ENABLED": "false", "OTEL_EXPORTER_OTLP_ENDPOINT": "http://c:4318"}, false},
		{map[string]string{"OTEL_SDK_DISABLED": "true", "OPENNEKO_OTEL_ENABLED": "true"}, false},
	}
	for _, c := range cases {
		if got := TelemetryEnabled(func(k string) string { return c.env[k] }); got != c.want {
			t.Errorf("%v: got %v, want %v", c.env, got, c.want)
		}
	}
}

func TestChildEnvPassesTheTraceExporterSettings(t *testing.T) {
	env := ChildEnv([]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://c", "OTEL_SERVICE_NAME=s", "OPENNEKO_OTEL_ENABLED=true", "NEKO_PG_PASSWORD=p"})
	if got := strings.Join(env, "|"); got != "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://c|OTEL_SERVICE_NAME=s|OPENNEKO_OTEL_ENABLED=true" {
		t.Fatalf("child env = %s", got)
	}
}

func TestEveryRouteJoinsTheCallersTrace(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	var got trace.SpanContext
	h := withTraceContext(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = trace.SpanContextFromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcp", nil)
	req.Header.Set("traceparent", traceparent)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got.TraceID().String() != callerTrace || !got.IsRemote() {
		t.Fatalf("span context = %v", got)
	}
}

type collectorStub struct {
	mu   sync.Mutex
	reqs []*collector.ExportTraceServiceRequest
}

func (c *collectorStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req collector.ExportTraceServiceRequest
	if err := proto.Unmarshal(body, &req); err == nil {
		c.mu.Lock()
		c.reqs = append(c.reqs, &req)
		c.mu.Unlock()
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	b, _ := proto.Marshal(&collector.ExportTraceServiceResponse{})
	_, _ = w.Write(b)
}

func TestServeExportsGraphJinSpansInTheCallersTraceWithoutErrorText(t *testing.T) {
	stub := &collectorStub{}
	col := httptest.NewServer(stub)
	defer col.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", col.URL+"/v1/traces")
	t.Setenv("OTEL_SERVICE_NAME", "gjhost-test")
	t.Setenv("GO_ENV", "agentic")

	file := seedConfig(t, t.TempDir())
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, file, addr, io.Discard) }()
	waitFor(t, "graphjin", func() bool {
		select {
		case err := <-done:
			t.Fatalf("serve stopped: %v", err)
		default:
		}
		return answers(addr)
	})

	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/api/v1/graphql?planted=secret-value", strings.NewReader(`{"query":"{ no_such_table_planted { id } }"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("traceparent", traceparent)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop")
	}

	stub.mu.Lock()
	defer stub.mu.Unlock()
	joined := 0
	for _, r := range stub.reqs {
		raw, _ := proto.Marshal(r)
		if strings.Contains(string(raw), "planted") {
			t.Fatalf("an exported span holds request content: %s", raw)
		}
		for _, rs := range r.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				for _, s := range ss.Spans {
					if trace.TraceID(s.TraceId).String() == callerTrace && s.Name == "GraphQL Request" {
						joined++
						if s.Status != nil && s.Status.Message != "" {
							t.Fatalf("status message exported: %q", s.Status.Message)
						}
					}
				}
			}
		}
	}
	if joined != 1 {
		t.Fatalf("GraphQL spans in the caller's trace = %d, requests = %d", joined, len(stub.reqs))
	}
}
