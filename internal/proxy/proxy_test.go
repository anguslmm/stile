package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/jsonrpc"
	"github.com/anguslmm/stile/internal/router"
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

func (m *mockTransport) Close() error  { return nil }
func (m *mockTransport) Healthy() bool { return true }

// failingTransport always fails on tools/list.
type failingTransport struct{}

func (f *failingTransport) RoundTrip(_ context.Context, _ *jsonrpc.Request) (transport.TransportResult, error) {
	return nil, fmt.Errorf("connection refused")
}
func (f *failingTransport) Close() error  { return nil }
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

func newTestRouter(t *testing.T, names []string, transports map[string]transport.Transport) *router.RouteTable {
	t.Helper()
	cfg := newTestConfig(names...)
	rt, err := router.New(transports, cfg.Upstreams(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return rt
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

	rt := newTestRouter(t, []string{"a", "b"}, map[string]transport.Transport{
		"a": mockA, "b": mockB,
	})
	defer rt.Close()

	h := NewHandler(rt, nil, nil, nil)

	resp, err := h.HandleToolsList(context.Background(), jsonrpc.IntID(1))
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

	rt := newTestRouter(t, []string{"a", "b"}, map[string]transport.Transport{
		"a": mockA, "b": mockB,
	})
	defer rt.Close()

	h := NewHandler(rt, nil, nil, nil)

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

	rt := newTestRouter(t, []string{"a"}, map[string]transport.Transport{"a": mockA})
	defer rt.Close()

	h := NewHandler(rt, nil, nil, nil)

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

	rt := newTestRouter(t, []string{"healthy", "broken"}, map[string]transport.Transport{
		"healthy": healthyMock,
		"broken":  &failingTransport{},
	})
	defer rt.Close()

	h := NewHandler(rt, nil, nil, nil)

	resp, err := h.HandleToolsList(context.Background(), jsonrpc.IntID(1))
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

	rt := newTestRouter(t, []string{"a"}, map[string]transport.Transport{"a": mockA})
	defer rt.Close()

	h := NewHandler(rt, nil, nil, nil)

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

	rt := newTestRouter(t, []string{"a"}, map[string]transport.Transport{"a": mockA})
	defer rt.Close()

	h := NewHandler(rt, nil, nil, nil)

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

func TestMixedHTTPAndStdioUpstreams(t *testing.T) {
	// Set up an HTTP upstream (httptest server).
	httpTools := struct {
		Tools []transport.ToolSchema `json:"tools"`
	}{
		Tools: []transport.ToolSchema{
			{Name: "http_tool", Description: "served over HTTP"},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req jsonrpc.Request
		json.Unmarshal(body, &req)

		w.Header().Set("Content-Type", "application/json")

		switch req.Method {
		case "tools/list":
			resp, _ := jsonrpc.NewResponse(req.ID, httpTools)
			data, _ := json.Marshal(resp)
			w.Write(data)
		case "tools/call":
			resp, _ := jsonrpc.NewResponse(req.ID, map[string]any{"from": "http"})
			data, _ := json.Marshal(resp)
			w.Write(data)
		}
	}))
	defer srv.Close()

	// Set up a stdio upstream (mock server).
	binary := buildMockStdioServer(t)

	// Config with both upstreams.
	yamlCfg := fmt.Sprintf(`
upstreams:
  - name: http-upstream
    transport: streamable-http
    url: %s
  - name: stdio-upstream
    transport: stdio
    command: ["%s"]
`, srv.URL, binary)

	cfg, err := config.LoadBytes([]byte(yamlCfg))
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}

	// Create transports and router.
	transports := make(map[string]transport.Transport)
	for _, ucfg := range cfg.Upstreams() {
		tr, err := transport.NewFromConfig(ucfg)
		if err != nil {
			t.Fatalf("create transport %q: %v", ucfg.Name(), err)
		}
		transports[ucfg.Name()] = tr
	}

	rt, err := router.New(transports, cfg.Upstreams(), nil)
	if err != nil {
		t.Fatalf("New router: %v", err)
	}
	defer rt.Close()

	h := NewHandler(rt, nil, nil, nil)

	// tools/list should return tools from both upstreams.
	resp, err := h.HandleToolsList(context.Background(), jsonrpc.IntID(1))
	if err != nil {
		t.Fatalf("HandleToolsList: %v", err)
	}

	var result struct {
		Tools []transport.ToolSchema `json:"tools"`
	}
	json.Unmarshal(resp.Result, &result)

	names := make(map[string]bool)
	for _, tool := range result.Tools {
		names[tool.Name] = true
	}

	if !names["http_tool"] {
		t.Error("missing http_tool in merged tool list")
	}
	if !names["test_echo"] {
		t.Error("missing test_echo (stdio) in merged tool list")
	}

	// tools/call to http_tool should route to HTTP upstream.
	params, _ := json.Marshal(map[string]any{"name": "http_tool"})
	callReq := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "tools/call",
		Params:  params,
		ID:      jsonrpc.IntID(2),
	}

	w := httptest.NewRecorder()
	h.HandleToolsCall(context.Background(), w, callReq)

	var callResp jsonrpc.Response
	json.Unmarshal(w.Body.Bytes(), &callResp)
	if callResp.Error != nil {
		t.Fatalf("http_tool call error: %v", callResp.Error)
	}

	var httpResult map[string]any
	json.Unmarshal(callResp.Result, &httpResult)
	if httpResult["from"] != "http" {
		t.Errorf("expected from=http, got %v", httpResult["from"])
	}

	// tools/call to test_echo should route to stdio upstream.
	params, _ = json.Marshal(map[string]any{
		"name":      "test_echo",
		"arguments": map[string]string{"message": "hi"},
	})
	callReq = &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "tools/call",
		Params:  params,
		ID:      jsonrpc.IntID(3),
	}

	w = httptest.NewRecorder()
	h.HandleToolsCall(context.Background(), w, callReq)

	json.Unmarshal(w.Body.Bytes(), &callResp)
	if callResp.Error != nil {
		t.Fatalf("test_echo call error: %v", callResp.Error)
	}
	if callResp.Result == nil {
		t.Fatal("expected non-nil result from stdio tool call")
	}
}

// buildMockStdioServer compiles the mock stdio server for proxy tests.
func buildMockStdioServer(t *testing.T) string {
	t.Helper()
	binary := t.TempDir() + "/mock_stdio_server"

	mockSrc := `package main

import (
	"bufio"
	"encoding/json"
	"os"
)

type request struct {
	JSONRPC string          ` + "`json:\"jsonrpc\"`" + `
	Method  string          ` + "`json:\"method\"`" + `
	Params  json.RawMessage ` + "`json:\"params,omitempty\"`" + `
	ID      json.RawMessage ` + "`json:\"id\"`" + `
}

type response struct {
	JSONRPC string          ` + "`json:\"jsonrpc\"`" + `
	Result  interface{}     ` + "`json:\"result,omitempty\"`" + `
	Error   *rpcError       ` + "`json:\"error,omitempty\"`" + `
	ID      json.RawMessage ` + "`json:\"id\"`" + `
}

type rpcError struct {
	Code    int    ` + "`json:\"code\"`" + `
	Message string ` + "`json:\"message\"`" + `
}

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	encoder := json.NewEncoder(os.Stdout)

	for scanner.Scan() {
		var req request
		json.Unmarshal(scanner.Bytes(), &req)

		resp := response{JSONRPC: "2.0", ID: req.ID}
		switch req.Method {
		case "tools/list":
			resp.Result = map[string]interface{}{
				"tools": []map[string]string{
					{"name": "test_echo", "description": "echo tool"},
				},
			}
		case "tools/call":
			var params map[string]interface{}
			json.Unmarshal(req.Params, &params)
			resp.Result = map[string]interface{}{
				"content": []map[string]interface{}{
					{"type": "text", "text": "echoed"},
				},
			}
		default:
			resp.Error = &rpcError{Code: -32601, Message: "not found"}
		}
		encoder.Encode(resp)
	}
}
`
	srcDir := t.TempDir()
	srcPath := srcDir + "/main.go"
	os.WriteFile(srcPath, []byte(mockSrc), 0644)
	cmd := exec.Command("go", "build", "-o", binary, srcPath)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to build mock stdio server: %v", err)
	}
	return binary
}
