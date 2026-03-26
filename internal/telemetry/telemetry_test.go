package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/anguslmm/stile/internal/testutil"
	"testing"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/jsonrpc"
	"github.com/anguslmm/stile/internal/proxy"
	"github.com/anguslmm/stile/internal/router"
	"github.com/anguslmm/stile/internal/transport"
)

// mockTransport implements transport.Transport for tests.
type mockTransport struct {
	tools     []transport.ToolSchema
	roundTrip func(ctx context.Context, req *jsonrpc.Request) (transport.TransportResult, error)
}

func (m *mockTransport) RoundTrip(ctx context.Context, req *jsonrpc.Request) (transport.TransportResult, error) {
	if req.Method == "tools/list" {
		result := struct {
			Tools []transport.ToolSchema `json:"tools"`
		}{Tools: m.tools}
		resp, _ := jsonrpc.NewResponse(req.ID, result)
		return transport.NewJSONResult(resp), nil
	}
	if m.roundTrip != nil {
		return m.roundTrip(ctx, req)
	}
	resp, _ := jsonrpc.NewResponse(req.ID, map[string]any{"ok": true})
	return transport.NewJSONResult(resp), nil
}
func (m *mockTransport) Close() error  { return nil }
func (m *mockTransport) Healthy() bool { return true }

func newTestRouter(t *testing.T, transports map[string]transport.Transport) *router.RouteTable {
	t.Helper()
	yaml := "upstreams:\n"
	for name := range transports {
		yaml += "  - name: " + name + "\n    transport: streamable-http\n    url: http://fake/" + name + "\n"
	}
	cfg, err := config.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	rt, err := router.New(transports, cfg.Upstreams(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

func setupTracer() (*tracetest.InMemoryExporter, trace.Tracer) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	return exporter, tp.Tracer(tracerName)
}

func TestSpansCreatedForToolsCall(t *testing.T) {
	exporter, tracer := setupTracer()

	mock := &mockTransport{
		tools: []transport.ToolSchema{{Name: "alpha"}},
	}
	rt := newTestRouter(t, map[string]transport.Transport{"a": mock})
	defer rt.Close()

	h := proxy.NewHandler(rt, nil, nil, nil, proxy.WithTracer(tracer))

	// Create a root span to act as the parent (simulates handleMCP + dispatch).
	ctx, rootSpan := tracer.Start(context.Background(), "dispatch")

	params, _ := json.Marshal(map[string]any{"name": "a__alpha"})
	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "tools/call",
		Params:  params,
		ID:      jsonrpc.IntID(1),
	}

	w := httptest.NewRecorder()
	h.HandleToolsCall(ctx, w, req)
	rootSpan.End()

	spans := exporter.GetSpans()
	if len(spans) == 0 {
		t.Fatal("expected spans to be recorded, got none")
	}

	// Check that we have the expected span names.
	spanNames := make(map[string]bool)
	for _, s := range spans {
		spanNames[s.Name] = true
	}

	if !spanNames["route + rate limit"] {
		t.Error("missing 'route + rate limit' span")
	}
	if !spanNames["upstream.RoundTrip"] {
		t.Error("missing 'upstream.RoundTrip' span")
	}
	if !spanNames["dispatch"] {
		t.Error("missing 'dispatch' span")
	}

	// Check that the dispatch span has the expected attributes.
	for _, s := range spans {
		if s.Name == "dispatch" {
			attrs := make(map[string]string)
			for _, a := range s.Attributes {
				attrs[string(a.Key)] = a.Value.AsString()
			}
			if attrs["mcp.tool"] != "a__alpha" {
				t.Errorf("dispatch span mcp.tool = %q, want a__alpha", attrs["mcp.tool"])
			}
			if attrs["mcp.upstream"] != "a" {
				t.Errorf("dispatch span mcp.upstream = %q, want a", attrs["mcp.upstream"])
			}
			if attrs["mcp.status"] != "ok" {
				t.Errorf("dispatch span mcp.status = %q, want ok", attrs["mcp.status"])
			}
		}
	}
}

// errReader is an io.ReadCloser that returns an error on the first read.
type errReader struct{ err error }

func (r *errReader) Read([]byte) (int, error) { return 0, r.err }
func (r *errReader) Close() error             { return nil }

func TestSSEStreamErrorProducesErrorSpan(t *testing.T) {
	exporter, tracer := setupTracer()

	streamErr := errors.New("connection reset")
	mock := &mockTransport{
		tools: []transport.ToolSchema{{Name: "streamy"}},
		roundTrip: func(_ context.Context, _ *jsonrpc.Request) (transport.TransportResult, error) {
			return transport.NewStreamResult(&errReader{err: streamErr}), nil
		},
	}

	rt := newTestRouter(t, map[string]transport.Transport{"a": mock})
	defer rt.Close()

	h := proxy.NewHandler(rt, nil, nil, nil, proxy.WithTracer(tracer))

	ctx, rootSpan := tracer.Start(context.Background(), "dispatch")

	params, _ := json.Marshal(map[string]any{"name": "a__streamy"})
	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "tools/call",
		Params:  params,
		ID:      jsonrpc.IntID(1),
	}

	w := httptest.NewRecorder()
	h.HandleToolsCall(ctx, w, req)
	rootSpan.End()

	spans := exporter.GetSpans()

	// Find the WriteResponse span and verify it has error status.
	var found bool
	for _, s := range spans {
		if s.Name == "StreamResult.WriteResponse" {
			found = true
			if s.Status.Code != codes.Error {
				t.Errorf("expected error status on WriteResponse span, got %v", s.Status)
			}
			// Check that the error was recorded as an event.
			var hasErrorEvent bool
			for _, e := range s.Events {
				if e.Name == "exception" {
					hasErrorEvent = true
				}
			}
			if !hasErrorEvent {
				t.Error("expected error event on WriteResponse span")
			}
		}
	}
	if !found {
		names := make([]string, len(spans))
		for i, s := range spans {
			names[i] = s.Name
		}
		t.Errorf("missing StreamResult.WriteResponse span; got spans: %v", names)
	}
}

func TestNoOpTracerDisabled(t *testing.T) {
	cfg, err := config.LoadBytes([]byte(`
upstreams:
  - name: a
    transport: streamable-http
    url: http://fake/a
`))
	if err != nil {
		t.Fatal(err)
	}

	p, err := Init(context.Background(), cfg.Telemetry())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Shutdown(context.Background())

	// The tracer should be a no-op; creating a span should not panic.
	_, span := p.Tracer().Start(context.Background(), "test")
	span.End()

	// Verify it's a no-op by checking the span context is invalid.
	if span.SpanContext().IsValid() {
		t.Error("expected no-op tracer to produce invalid span context")
	}
}

func TestInitReturnsProviderWhenEnabled(t *testing.T) {
	// Start a dummy HTTP server to act as OTLP receiver (just 200 everything).
	srv := testutil.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	endpoint := strings.TrimPrefix(srv.URL, "http://")

	yaml := `
telemetry:
  traces:
    enabled: true
    endpoint: "` + endpoint + `"
    sample_rate: 1.0
upstreams:
  - name: a
    transport: streamable-http
    url: http://fake/a
`
	cfg, err := config.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}

	p, err := Init(context.Background(), cfg.Telemetry())
	if err != nil {
		t.Fatal(err)
	}

	// The tracer should produce valid spans.
	_, span := p.Tracer().Start(context.Background(), "test")
	if !span.SpanContext().IsValid() {
		t.Error("expected valid span context when tracing is enabled")
	}
	span.End()

	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
