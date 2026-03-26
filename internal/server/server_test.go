package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/testutil"
	"github.com/anguslmm/stile/internal/jsonrpc"
	"github.com/anguslmm/stile/internal/proxy"
	"github.com/anguslmm/stile/internal/router"
	"github.com/anguslmm/stile/internal/transport"
)

// mockTransport implements transport.Transport with canned responses.
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

func newTestServer(t *testing.T, mock *mockTransport) *httptest.Server {
	t.Helper()

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

	h := proxy.NewHandler(rt, nil, nil, nil)
	srv := New(cfg, h, rt, nil, nil)
	return testutil.NewServer(srv.Handler())
}

func postMCP(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	data, _ := json.Marshal(body)
	resp, err := http.Post(url+"/mcp", "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("POST /mcp: %v", err)
	}
	return resp
}

func readResponse(t *testing.T, resp *http.Response) jsonrpc.Response {
	t.Helper()
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var r jsonrpc.Response
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("unmarshal response: %v (body: %s)", err, string(data))
	}
	return r
}

func TestInitializeHandshake(t *testing.T) {
	mock := &mockTransport{tools: []transport.ToolSchema{{Name: "test-tool"}}}
	ts := newTestServer(t, mock)
	defer ts.Close()

	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-11-25",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "1.0"},
		},
		"id": 1,
	}

	resp := postMCP(t, ts.URL, req)
	r := readResponse(t, resp)

	if r.Error != nil {
		t.Fatalf("unexpected error: %v", r.Error)
	}

	var result map[string]any
	json.Unmarshal(r.Result, &result)

	serverInfo, ok := result["serverInfo"].(map[string]any)
	if !ok {
		t.Fatalf("missing serverInfo in result: %v", result)
	}
	if serverInfo["name"] != "stile" {
		t.Errorf("expected serverInfo.name = stile, got %v", serverInfo["name"])
	}
	if result["protocolVersion"] != "2025-11-25" {
		t.Errorf("expected protocolVersion 2025-11-25, got %v", result["protocolVersion"])
	}
}

func TestInitializeUnsupportedVersion(t *testing.T) {
	mock := &mockTransport{tools: []transport.ToolSchema{{Name: "test-tool"}}}
	ts := newTestServer(t, mock)
	defer ts.Close()

	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "1999-01-01",
		},
		"id": 1,
	}

	resp := postMCP(t, ts.URL, req)
	r := readResponse(t, resp)

	if r.Error == nil {
		t.Fatal("expected error for unsupported protocol version")
	}
	if !strings.Contains(r.Error.Message, "unsupported protocol version") {
		t.Errorf("unexpected error message: %s", r.Error.Message)
	}
}

func TestPing(t *testing.T) {
	mock := &mockTransport{tools: []transport.ToolSchema{{Name: "test-tool"}}}
	ts := newTestServer(t, mock)
	defer ts.Close()

	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  "ping",
		"id":      1,
	}

	resp := postMCP(t, ts.URL, req)
	r := readResponse(t, resp)

	if r.Error != nil {
		t.Fatalf("unexpected error: %v", r.Error)
	}

	// Result should be an empty object {}.
	var result map[string]any
	json.Unmarshal(r.Result, &result)
	if len(result) != 0 {
		t.Errorf("expected empty result, got %v", result)
	}
}

func TestUnknownMethod(t *testing.T) {
	mock := &mockTransport{tools: []transport.ToolSchema{{Name: "test-tool"}}}
	ts := newTestServer(t, mock)
	defer ts.Close()

	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  "foo/bar",
		"id":      1,
	}

	resp := postMCP(t, ts.URL, req)
	r := readResponse(t, resp)

	if r.Error == nil {
		t.Fatal("expected MethodNotFound error")
	}
	if r.Error.Code != jsonrpc.CodeMethodNotFound {
		t.Errorf("expected code %d, got %d", jsonrpc.CodeMethodNotFound, r.Error.Code)
	}
}

func TestNotificationNoResponseBody(t *testing.T) {
	mock := &mockTransport{tools: []transport.ToolSchema{{Name: "test-tool"}}}
	ts := newTestServer(t, mock)
	defer ts.Close()

	// A notification has no "id" field.
	notif := map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	}

	resp := postMCP(t, ts.URL, notif)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("expected 202 Accepted, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if len(body) != 0 {
		t.Errorf("expected empty body for notification, got %q", string(body))
	}
}

func TestToolsListEndToEnd(t *testing.T) {
	mock := &mockTransport{
		tools: []transport.ToolSchema{
			{Name: "alpha", Description: "first tool"},
			{Name: "beta", Description: "second tool"},
		},
	}
	ts := newTestServer(t, mock)
	defer ts.Close()

	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  "tools/list",
		"id":      1,
	}

	resp := postMCP(t, ts.URL, req)
	r := readResponse(t, resp)

	if r.Error != nil {
		t.Fatalf("unexpected error: %v", r.Error)
	}

	var result struct {
		Tools []transport.ToolSchema `json:"tools"`
	}
	json.Unmarshal(r.Result, &result)

	if len(result.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(result.Tools))
	}
}

func TestToolsCallEndToEnd(t *testing.T) {
	mock := &mockTransport{
		tools: []transport.ToolSchema{{Name: "greet"}},
		roundTrip: func(_ context.Context, req *jsonrpc.Request) (transport.TransportResult, error) {
			resp, _ := jsonrpc.NewResponse(req.ID, map[string]any{"text": "hello"})
			return transport.NewJSONResult(resp), nil
		},
	}
	ts := newTestServer(t, mock)
	defer ts.Close()

	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  "tools/call",
		"params":  map[string]any{"name": "test__greet", "arguments": map[string]any{}},
		"id":      1,
	}

	resp := postMCP(t, ts.URL, req)
	r := readResponse(t, resp)

	if r.Error != nil {
		t.Fatalf("unexpected error: %v", r.Error)
	}

	var result map[string]any
	json.Unmarshal(r.Result, &result)
	if result["text"] != "hello" {
		t.Errorf("expected text=hello, got %v", result)
	}
}

func TestToolsCallSSEEndToEnd(t *testing.T) {
	ssePayload := "event: message\ndata: {\"jsonrpc\":\"2.0\",\"result\":{\"streamed\":true},\"id\":1}\n\n"

	mock := &mockTransport{
		tools: []transport.ToolSchema{{Name: "stream-tool"}},
		roundTrip: func(_ context.Context, _ *jsonrpc.Request) (transport.TransportResult, error) {
			return transport.NewStreamResult(io.NopCloser(strings.NewReader(ssePayload))), nil
		},
	}
	ts := newTestServer(t, mock)
	defer ts.Close()

	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  "tools/call",
		"params":  map[string]any{"name": "test__stream-tool"},
		"id":      1,
	}

	data, _ := json.Marshal(req)
	resp, err := http.Post(ts.URL+"/mcp", "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("expected Content-Type text/event-stream, got %q", ct)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"streamed":true`) {
		t.Errorf("expected streamed data in body, got %q", string(body))
	}
}

func TestBatchRequest(t *testing.T) {
	mock := &mockTransport{tools: []transport.ToolSchema{{Name: "test-tool"}}}
	ts := newTestServer(t, mock)
	defer ts.Close()

	batch := []map[string]any{
		{"jsonrpc": "2.0", "method": "ping", "id": 1},
		{"jsonrpc": "2.0", "method": "ping", "id": 2},
	}

	data, _ := json.Marshal(batch)
	resp, err := http.Post(ts.URL+"/mcp", "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var responses []jsonrpc.Response
	if err := json.Unmarshal(body, &responses); err != nil {
		t.Fatalf("unmarshal batch response: %v (body: %s)", err, string(body))
	}

	if len(responses) != 2 {
		t.Fatalf("expected 2 responses, got %d", len(responses))
	}
}

func TestBatchWithNotification(t *testing.T) {
	mock := &mockTransport{tools: []transport.ToolSchema{{Name: "test-tool"}}}
	ts := newTestServer(t, mock)
	defer ts.Close()

	// Batch with one request and one notification.
	batch := []map[string]any{
		{"jsonrpc": "2.0", "method": "ping", "id": 1},
		{"jsonrpc": "2.0", "method": "notifications/initialized"},
	}

	data, _ := json.Marshal(batch)
	resp, err := http.Post(ts.URL+"/mcp", "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var responses []jsonrpc.Response
	if err := json.Unmarshal(body, &responses); err != nil {
		t.Fatalf("unmarshal batch response: %v (body: %s)", err, string(body))
	}

	// Only the ping should produce a response, not the notification.
	if len(responses) != 1 {
		t.Fatalf("expected 1 response (notification should produce none), got %d", len(responses))
	}
}

func TestAdminRefresh(t *testing.T) {
	mock := &mockTransport{
		tools: []transport.ToolSchema{
			{Name: "tool-a"},
			{Name: "tool-b"},
		},
	}
	ts := newTestServer(t, mock)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/admin/refresh", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var result router.RefreshResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("unmarshal refresh result: %v (body: %s)", err, string(body))
	}

	if result.TotalTools != 2 {
		t.Errorf("expected total_tools=2, got %d", result.TotalTools)
	}
	status, ok := result.Upstreams["test"]
	if !ok {
		t.Fatal("expected upstream 'test' in result")
	}
	if status.Tools != 2 {
		t.Errorf("expected 2 tools for upstream 'test', got %d", status.Tools)
	}
	if status.Stale {
		t.Error("upstream 'test' should not be stale")
	}
}

func TestOversizedRequestBody(t *testing.T) {
	mock := &mockTransport{tools: []transport.ToolSchema{{Name: "test-tool"}}}
	ts := newTestServer(t, mock)
	defer ts.Close()

	// Create a body larger than 10 MB.
	bigBody := make([]byte, 11<<20)
	for i := range bigBody {
		bigBody[i] = 'x'
	}
	resp, err := http.Post(ts.URL+"/mcp", "application/json", bytes.NewReader(bigBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	r := readResponse(t, resp)
	if r.Error == nil {
		t.Fatal("expected error for oversized body")
	}
	if !strings.Contains(r.Error.Message, "too large") {
		t.Errorf("expected 'too large' in error, got %q", r.Error.Message)
	}
}

func TestOversizedBatch(t *testing.T) {
	mock := &mockTransport{tools: []transport.ToolSchema{{Name: "test-tool"}}}
	ts := newTestServer(t, mock)
	defer ts.Close()

	// Build a batch with 101 requests (over the limit of 100).
	batch := make([]map[string]any, 101)
	for i := range batch {
		batch[i] = map[string]any{
			"jsonrpc": "2.0",
			"method":  "ping",
			"id":      i + 1,
		}
	}

	data, _ := json.Marshal(batch)
	resp, err := http.Post(ts.URL+"/mcp", "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	r := readResponse(t, resp)
	if r.Error == nil {
		t.Fatal("expected error for oversized batch")
	}
	if !strings.Contains(r.Error.Message, "batch too large") {
		t.Errorf("expected 'batch too large' in error, got %q", r.Error.Message)
	}
}

// Verify unused import is used.
var _ = fmt.Sprintf
