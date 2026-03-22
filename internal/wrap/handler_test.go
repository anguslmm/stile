package wrap

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/jsonrpc"
	"github.com/anguslmm/stile/internal/transport"
)

// echoServer is a minimal stdio MCP server that echoes requests back.
// It responds to initialize, ping, tools/list, and tools/call.
const echoServer = `
import sys, json

for line in sys.stdin:
    req = json.loads(line.strip())
    rid = req.get("id")
    method = req.get("method", "")

    if method == "initialize":
        resp = {"jsonrpc":"2.0","id":rid,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{"listChanged":False}},"serverInfo":{"name":"echo","version":"0.1.0"}}}
    elif method == "ping":
        resp = {"jsonrpc":"2.0","id":rid,"result":{}}
    elif method == "tools/list":
        resp = {"jsonrpc":"2.0","id":rid,"result":{"tools":[{"name":"echo","description":"echoes input","inputSchema":{"type":"object","properties":{"msg":{"type":"string"}}}}]}}
    elif method == "tools/call":
        params = req.get("params", {})
        resp = {"jsonrpc":"2.0","id":rid,"result":{"content":[{"type":"text","text":json.dumps(params)}]}}
    else:
        resp = {"jsonrpc":"2.0","id":rid,"error":{"code":-32601,"message":"method not found"}}

    sys.stdout.write(json.dumps(resp) + "\n")
    sys.stdout.flush()
`

func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	cfg := config.NewStdioUpstreamConfig("echo-test", []string{"python3", "-c", echoServer}, nil)
	tr, err := transport.NewStdioTransport(cfg)
	if err != nil {
		t.Fatalf("create transport: %v", err)
	}
	t.Cleanup(func() { tr.Close() })
	return NewHandler(tr)
}

func doPost(t *testing.T, handler http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestWrapPing(t *testing.T) {
	h := newTestHandler(t)
	mux := h.ServeMux()

	rec := doPost(t, mux, "/mcp", `{"jsonrpc":"2.0","method":"ping","id":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp jsonrpc.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
}

func TestWrapToolsList(t *testing.T) {
	h := newTestHandler(t)
	mux := h.ServeMux()

	rec := doPost(t, mux, "/mcp", `{"jsonrpc":"2.0","method":"tools/list","id":2}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp jsonrpc.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	var result struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(result.Tools) != 1 || result.Tools[0].Name != "echo" {
		t.Fatalf("expected 1 tool named 'echo', got %+v", result.Tools)
	}
}

func TestWrapToolsCall(t *testing.T) {
	h := newTestHandler(t)
	mux := h.ServeMux()

	body := `{"jsonrpc":"2.0","method":"tools/call","id":3,"params":{"name":"echo","arguments":{"msg":"hello"}}}`
	rec := doPost(t, mux, "/mcp", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp jsonrpc.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
}

func TestWrapInitialize(t *testing.T) {
	h := newTestHandler(t)
	mux := h.ServeMux()

	body := `{"jsonrpc":"2.0","method":"initialize","id":4,"params":{"protocolVersion":"2025-11-25"}}`
	rec := doPost(t, mux, "/mcp", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp jsonrpc.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
}

func TestWrapBatch(t *testing.T) {
	h := newTestHandler(t)
	mux := h.ServeMux()

	body := `[{"jsonrpc":"2.0","method":"ping","id":10},{"jsonrpc":"2.0","method":"ping","id":11}]`
	rec := doPost(t, mux, "/mcp", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var responses []jsonrpc.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &responses); err != nil {
		t.Fatalf("unmarshal batch: %v", err)
	}
	if len(responses) != 2 {
		t.Fatalf("expected 2 responses, got %d", len(responses))
	}
}

func TestWrapHealthz(t *testing.T) {
	h := newTestHandler(t)
	mux := h.ServeMux()

	// Force the child to start by sending a ping first.
	doPost(t, mux, "/mcp", `{"jsonrpc":"2.0","method":"ping","id":1}`)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestWrapNotification(t *testing.T) {
	h := newTestHandler(t)
	mux := h.ServeMux()

	// Notifications have no id field — should return 202 Accepted.
	body := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	rec := doPost(t, mux, "/mcp", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestWrapBodyTooLarge(t *testing.T) {
	h := newTestHandler(t)
	mux := h.ServeMux()

	// Create a body larger than maxRequestBody.
	large := strings.Repeat("x", maxRequestBody+10)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(large))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// Should still return 200 with a JSON-RPC error.
	body, _ := io.ReadAll(rec.Body)
	var resp jsonrpc.Response
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error == nil {
		t.Fatal("expected error response for oversized body")
	}
}
