package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/anguslmm/stile/internal/auth"
	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/proxy"
	"github.com/anguslmm/stile/internal/router"
	"github.com/anguslmm/stile/internal/transport"
)

func newTracedTestServer(t *testing.T, mock *mockTransport) (*httptest.Server, *tracetest.InMemoryExporter) {
	t.Helper()

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	tracer := tp.Tracer("stile-test")

	// Register the W3C propagator so Extract/Inject work.
	otel.SetTextMapPropagator(propagation.TraceContext{})

	yamlCfg := `upstreams:
  - name: test
    transport: streamable-http
    url: http://fake/test
`
	cfg, err := config.LoadBytes([]byte(yamlCfg))
	if err != nil {
		t.Fatal(err)
	}

	rt, err := router.New(
		map[string]transport.Transport{"test": mock},
		cfg.Upstreams(),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close() })

	h := proxy.NewHandler(rt, nil, nil, nil, proxy.WithTracer(tracer))
	srv := New(cfg, h, rt, nil, &Options{Tracer: tracer})
	return httptest.NewServer(srv.Handler()), exporter
}

func TestInboundTraceparentCreatesChildSpan(t *testing.T) {
	mock := &mockTransport{tools: []transport.ToolSchema{{Name: "test-tool"}}}
	ts, exporter := newTracedTestServer(t, mock)
	defer ts.Close()

	// A valid traceparent header: version 00, known trace ID, parent span ID, sampled.
	traceID := "4bf92f3577b34da6a3ce929d0e0e4736"
	parentSpanID := "00f067aa0ba902b7"
	traceparent := "00-" + traceID + "-" + parentSpanID + "-01"

	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  "ping",
		"id":      1,
	}
	data, _ := json.Marshal(req)

	httpReq, err := http.NewRequest("POST", ts.URL+"/mcp", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("traceparent", traceparent)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body)

	spans := exporter.GetSpans()
	if len(spans) == 0 {
		t.Fatal("expected spans to be recorded, got none")
	}

	// The handleMCP span should have the same trace ID as the inbound traceparent.
	var found bool
	for _, s := range spans {
		if s.Name == "handleMCP" {
			found = true
			gotTraceID := s.SpanContext.TraceID().String()
			if gotTraceID != traceID {
				t.Errorf("handleMCP trace ID = %s, want %s", gotTraceID, traceID)
			}
			gotParentSpanID := s.Parent.SpanID().String()
			if gotParentSpanID != parentSpanID {
				t.Errorf("handleMCP parent span ID = %s, want %s", gotParentSpanID, parentSpanID)
			}
		}
	}
	if !found {
		names := make([]string, len(spans))
		for i, s := range spans {
			names[i] = s.Name
		}
		t.Errorf("missing handleMCP span; got spans: %v", names)
	}
}

func TestNoTraceparentCreatesNewRootTrace(t *testing.T) {
	mock := &mockTransport{tools: []transport.ToolSchema{{Name: "test-tool"}}}
	ts, exporter := newTracedTestServer(t, mock)
	defer ts.Close()

	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  "ping",
		"id":      1,
	}
	data, _ := json.Marshal(req)

	resp, err := http.Post(ts.URL+"/mcp", "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body)

	spans := exporter.GetSpans()

	for _, s := range spans {
		if s.Name == "handleMCP" {
			// Without an inbound traceparent, the span should be a root span (no valid parent).
			if s.Parent.IsValid() {
				t.Errorf("expected root span (no valid parent), but parent is valid: %s", s.Parent.SpanID())
			}
			return
		}
	}
	t.Error("missing handleMCP span")
}

func TestTraceparentWithAuthMiddleware(t *testing.T) {
	mock := &mockTransport{tools: []transport.ToolSchema{{Name: "test-tool"}}}

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	tracer := tp.Tracer("stile-test")
	otel.SetTextMapPropagator(propagation.TraceContext{})

	// Set up auth with a real caller store.
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := auth.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	if err := store.AddCaller("test-user"); err != nil {
		t.Fatal(err)
	}
	apiKey := "sk-test-key"
	hash := sha256.Sum256([]byte(apiKey))
	if err := store.AddKey("test-user", hash, ""); err != nil {
		t.Fatal(err)
	}

	authenticator := auth.NewAuthenticator(store, nil)

	yamlCfg := `upstreams:
  - name: test
    transport: streamable-http
    url: http://fake/test
`
	cfg, err := config.LoadBytes([]byte(yamlCfg))
	if err != nil {
		t.Fatal(err)
	}

	rt, err := router.New(
		map[string]transport.Transport{"test": mock},
		cfg.Upstreams(),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close() })

	h := proxy.NewHandler(rt, nil, nil, nil, proxy.WithTracer(tracer))
	srv := New(cfg, h, rt, nil, &Options{
		Tracer:        tracer,
		Authenticator: authenticator,
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	traceID := "4bf92f3577b34da6a3ce929d0e0e4736"
	parentSpanID := "00f067aa0ba902b7"
	traceparent := "00-" + traceID + "-" + parentSpanID + "-01"

	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  "ping",
		"id":      1,
	}
	data, _ := json.Marshal(req)

	httpReq, err := http.NewRequest("POST", ts.URL+"/mcp", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("traceparent", traceparent)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body)

	spans := exporter.GetSpans()

	// All spans should share the same trace ID from the inbound traceparent.
	for _, s := range spans {
		gotTraceID := s.SpanContext.TraceID().String()
		if gotTraceID != traceID {
			t.Errorf("span %q trace ID = %s, want %s", s.Name, gotTraceID, traceID)
		}
	}

	// Verify both auth and handleMCP spans exist.
	spanNames := make(map[string]bool)
	for _, s := range spans {
		spanNames[s.Name] = true
	}
	if !spanNames["auth"] {
		t.Error("missing auth span")
	}
	if !spanNames["handleMCP"] {
		t.Error("missing handleMCP span")
	}
}
