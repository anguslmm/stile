package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/jsonrpc"
	"github.com/anguslmm/stile/internal/transport"
)

// mockTransport implements transport.Transport with canned responses.
type mockTransport struct {
	tools     []transport.ToolSchema
	roundTrip func(ctx context.Context, req *jsonrpc.Request) (transport.TransportResult, error)
}

func (m *mockTransport) RoundTrip(ctx context.Context, req *jsonrpc.Request) (transport.TransportResult, error) {
	// Handle tools/list internally.
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

func (m *mockTransport) Close() error { return nil }
func (m *mockTransport) Healthy() bool { return true }

// failingTransport always fails on tools/list.
type failingTransport struct{}

func (f *failingTransport) RoundTrip(_ context.Context, _ *jsonrpc.Request) (transport.TransportResult, error) {
	return nil, fmt.Errorf("connection refused")
}
func (f *failingTransport) Close() error { return nil }
func (f *failingTransport) Healthy() bool { return false }

func newTestConfig(names ...string) *config.Config {
	yaml := "upstreams:\n"
	for _, n := range names {
		yaml += fmt.Sprintf("  - name: %s\n    transport: streamable-http\n    url: http://fake/%s\n", n, n)
	}
	cfg, err := config.LoadBytes([]byte(yaml))
	if err != nil {
		panic(err)
	}
	return cfg
}

func TestToolsListMergesUpstreams(t *testing.T) {
	mockA := &mockTransport{
		tools: []transport.ToolSchema{
			{Name: "alpha", Description: "tool alpha"},
		},
	}
	mockB := &mockTransport{
		tools: []transport.ToolSchema{
			{Name: "beta", Description: "tool beta"},
			{Name: "gamma", Description: "tool gamma"},
		},
	}

	cfg := newTestConfig("a", "b")
	idx := 0
	mocks := []transport.Transport{mockA, mockB}

	h, err := NewHandlerWithFactory(cfg, func(_ config.UpstreamConfig) (transport.Transport, error) {
		m := mocks[idx]
		idx++
		return m, nil
	})
	if err != nil {
		t.Fatalf("NewHandlerWithFactory: %v", err)
	}

	resp, err := h.HandleToolsList(jsonrpc.IntID(1))
	if err != nil {
		t.Fatalf("HandleToolsList: %v", err)
	}

	var result struct {
		Tools []transport.ToolSchema `json:"tools"`
	}
	json.Unmarshal(resp.Result, &result)

	if len(result.Tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(result.Tools))
	}

	names := make(map[string]bool)
	for _, tool := range result.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"alpha", "beta", "gamma"} {
		if !names[want] {
			t.Errorf("missing tool %q in merged list", want)
		}
	}
}

func TestToolsCallDispatchesCorrectly(t *testing.T) {
	called := ""

	mockA := &mockTransport{
		tools: []transport.ToolSchema{{Name: "alpha"}},
		roundTrip: func(_ context.Context, req *jsonrpc.Request) (transport.TransportResult, error) {
			called = "a"
			resp, _ := jsonrpc.NewResponse(req.ID, map[string]any{"from": "a"})
			return transport.NewJSONResult(resp), nil
		},
	}
	mockB := &mockTransport{
		tools: []transport.ToolSchema{{Name: "beta"}},
		roundTrip: func(_ context.Context, req *jsonrpc.Request) (transport.TransportResult, error) {
			called = "b"
			resp, _ := jsonrpc.NewResponse(req.ID, map[string]any{"from": "b"})
			return transport.NewJSONResult(resp), nil
		},
	}

	cfg := newTestConfig("a", "b")
	idx := 0
	mocks := []transport.Transport{mockA, mockB}

	h, err := NewHandlerWithFactory(cfg, func(_ config.UpstreamConfig) (transport.Transport, error) {
		m := mocks[idx]
		idx++
		return m, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	params, _ := json.Marshal(map[string]any{"name": "beta", "arguments": map[string]any{}})
	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "tools/call",
		Params:  params,
		ID:      jsonrpc.IntID(1),
	}

	w := httptest.NewRecorder()
	h.HandleToolsCall(context.Background(), w, req)

	if called != "b" {
		t.Errorf("expected upstream b to be called, got %q", called)
	}

	var resp jsonrpc.Response
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
}

func TestToolsCallUnknownTool(t *testing.T) {
	mockA := &mockTransport{
		tools: []transport.ToolSchema{{Name: "alpha"}},
	}

	cfg := newTestConfig("a")
	h, err := NewHandlerWithFactory(cfg, func(_ config.UpstreamConfig) (transport.Transport, error) {
		return mockA, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	params, _ := json.Marshal(map[string]any{"name": "nonexistent"})
	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "tools/call",
		Params:  params,
		ID:      jsonrpc.IntID(1),
	}

	w := httptest.NewRecorder()
	h.HandleToolsCall(context.Background(), w, req)

	var resp jsonrpc.Response
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp.Error == nil {
		t.Fatal("expected error response for unknown tool")
	}
	if resp.Error.Code != jsonrpc.CodeInvalidParams {
		t.Errorf("expected code %d, got %d", jsonrpc.CodeInvalidParams, resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "unknown tool") {
		t.Errorf("expected 'unknown tool' in message, got %q", resp.Error.Message)
	}
}

func TestUpstreamDownAtStartup(t *testing.T) {
	healthyMock := &mockTransport{
		tools: []transport.ToolSchema{{Name: "alpha"}},
	}

	cfg := newTestConfig("healthy", "broken")
	idx := 0

	h, err := NewHandlerWithFactory(cfg, func(_ config.UpstreamConfig) (transport.Transport, error) {
		idx++
		if idx == 1 {
			return healthyMock, nil
		}
		return &failingTransport{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := h.HandleToolsList(jsonrpc.IntID(1))
	if err != nil {
		t.Fatal(err)
	}

	var result struct {
		Tools []transport.ToolSchema `json:"tools"`
	}
	json.Unmarshal(resp.Result, &result)

	if len(result.Tools) != 1 {
		t.Fatalf("expected 1 tool from healthy upstream, got %d", len(result.Tools))
	}
	if result.Tools[0].Name != "alpha" {
		t.Errorf("expected tool alpha, got %q", result.Tools[0].Name)
	}
}

func TestToolsCallSSEPassthrough(t *testing.T) {
	sseData := "event: message\ndata: {\"jsonrpc\":\"2.0\",\"result\":{\"done\":true},\"id\":1}\n\n"

	mockA := &mockTransport{
		tools: []transport.ToolSchema{{Name: "streamy"}},
		roundTrip: func(_ context.Context, _ *jsonrpc.Request) (transport.TransportResult, error) {
			return transport.NewStreamResult(io.NopCloser(strings.NewReader(sseData))), nil
		},
	}

	cfg := newTestConfig("a")
	h, err := NewHandlerWithFactory(cfg, func(_ config.UpstreamConfig) (transport.Transport, error) {
		return mockA, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	params, _ := json.Marshal(map[string]any{"name": "streamy"})
	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "tools/call",
		Params:  params,
		ID:      jsonrpc.IntID(1),
	}

	w := httptest.NewRecorder()
	h.HandleToolsCall(context.Background(), w, req)

	result := w.Result()
	if ct := result.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("expected Content-Type text/event-stream, got %q", ct)
	}

	body := w.Body.String()
	if !strings.Contains(body, `"done":true`) {
		t.Errorf("SSE body does not contain expected data: %q", body)
	}
}

func TestToolsCallWritesDirectResponse(t *testing.T) {
	mockA := &mockTransport{
		tools: []transport.ToolSchema{{Name: "direct"}},
		roundTrip: func(_ context.Context, req *jsonrpc.Request) (transport.TransportResult, error) {
			resp, _ := jsonrpc.NewResponse(req.ID, map[string]any{"value": 42})
			return transport.NewJSONResult(resp), nil
		},
	}

	cfg := newTestConfig("a")
	h, err := NewHandlerWithFactory(cfg, func(_ config.UpstreamConfig) (transport.Transport, error) {
		return mockA, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	params, _ := json.Marshal(map[string]any{"name": "direct"})
	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "tools/call",
		Params:  params,
		ID:      jsonrpc.IntID(1),
	}

	w := httptest.NewRecorder()
	h.HandleToolsCall(context.Background(), w, req)

	result := w.Result()
	if ct := result.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}

	var resp jsonrpc.Response
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	var data map[string]any
	json.Unmarshal(resp.Result, &data)
	if v, ok := data["value"]; !ok || v != float64(42) {
		t.Errorf("unexpected result: %v", data)
	}
}

// Verify writeJSONResponse writes correct Content-Type header.
func TestWriteJSONResponse(t *testing.T) {
	w := httptest.NewRecorder()
	resp, _ := jsonrpc.NewResponse(jsonrpc.IntID(1), "ok")
	writeJSONResponse(w, resp)

	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}

	var got jsonrpc.Response
	json.Unmarshal(w.Body.Bytes(), &got)
	if got.Error != nil {
		t.Errorf("unexpected error in response")
	}
}

// suppress log output during tests
func init() {
	// Tests produce expected log warnings for failing upstreams.
	_ = http.StatusOK // ensure net/http import is used
}
