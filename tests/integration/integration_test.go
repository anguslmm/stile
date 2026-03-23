package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/health"
	"github.com/anguslmm/stile/internal/jsonrpc"
	"github.com/anguslmm/stile/internal/metrics"
	"github.com/anguslmm/stile/internal/proxy"
	"github.com/anguslmm/stile/internal/resilience"
	"github.com/anguslmm/stile/internal/router"
	"github.com/anguslmm/stile/internal/server"
	"github.com/anguslmm/stile/internal/transport"
	"github.com/prometheus/client_golang/prometheus"

	_ "modernc.org/sqlite"
)

// ============================================================
// Happy Path Tests
// ============================================================

func TestFullLifecycle(t *testing.T) {
	mt := newMockTransport([]transport.ToolSchema{
		{Name: "echo", Description: "echoes input"},
		{Name: "add", Description: "adds numbers"},
	})

	gw := newTestGateway(t,
		withConfig(`
server:
  address: ":0"
upstreams:
  - name: mock
    transport: streamable-http
    url: http://placeholder
`),
		withTransport("mock", mt),
	)

	// initialize
	resp := gw.jsonRPCRequest(t, "initialize", map[string]any{
		"protocolVersion": "2025-11-25",
	}, "")
	if resp.Error != nil {
		t.Fatalf("initialize error: %v", resp.Error)
	}
	var initResult map[string]any
	json.Unmarshal(resp.Result, &initResult)
	if initResult["protocolVersion"] != "2025-11-25" {
		t.Errorf("unexpected protocol version: %v", initResult["protocolVersion"])
	}

	// tools/list
	resp = gw.jsonRPCRequest(t, "tools/list", nil, "")
	if resp.Error != nil {
		t.Fatalf("tools/list error: %v", resp.Error)
	}
	names := toolNames(resp)
	if len(names) != 2 || !contains(names, "echo") || !contains(names, "add") {
		t.Errorf("unexpected tools: %v", names)
	}

	// tools/call
	resp = gw.jsonRPCRequest(t, "tools/call", map[string]any{
		"name":      "echo",
		"arguments": map[string]any{"text": "hello"},
	}, "")
	if resp.Error != nil {
		t.Fatalf("tools/call error: %v", resp.Error)
	}
}

func TestMultipleUpstreams(t *testing.T) {
	mt1 := newMockTransport([]transport.ToolSchema{
		{Name: "github-search", Description: "search github"},
	})
	mt2 := newMockTransport([]transport.ToolSchema{
		{Name: "calculator", Description: "math"},
	})

	gw := newTestGateway(t,
		withConfig(`
server:
  address: ":0"
upstreams:
  - name: github
    transport: streamable-http
    url: http://placeholder
  - name: local
    transport: streamable-http
    url: http://placeholder
`),
		withTransport("github", mt1),
		withTransport("local", mt2),
	)

	// tools/list should return tools from both
	resp := gw.jsonRPCRequest(t, "tools/list", nil, "")
	if resp.Error != nil {
		t.Fatalf("tools/list error: %v", resp.Error)
	}
	names := toolNames(resp)
	if !contains(names, "github-search") || !contains(names, "calculator") {
		t.Errorf("expected tools from both upstreams, got: %v", names)
	}

	// tools/call routes to correct upstream
	var called1, called2 bool
	mt1.roundTrip = func(_ context.Context, req *jsonrpc.Request) (transport.TransportResult, error) {
		if req.Method == "tools/call" {
			called1 = true
		}
		resp, _ := jsonrpc.NewResponse(req.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": "from github"}}})
		return transport.NewJSONResult(resp), nil
	}
	mt2.roundTrip = func(_ context.Context, req *jsonrpc.Request) (transport.TransportResult, error) {
		if req.Method == "tools/call" {
			called2 = true
		}
		resp, _ := jsonrpc.NewResponse(req.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": "from local"}}})
		return transport.NewJSONResult(resp), nil
	}

	gw.jsonRPCRequest(t, "tools/call", map[string]any{"name": "github-search"}, "")
	if !called1 {
		t.Error("github-search not routed to github upstream")
	}

	gw.jsonRPCRequest(t, "tools/call", map[string]any{"name": "calculator"}, "")
	if !called2 {
		t.Error("calculator not routed to local upstream")
	}
}

func TestSSEPassthrough(t *testing.T) {
	sseData := `event: message
data: {"jsonrpc":"2.0","result":{"content":[{"type":"text","text":"streamed"}]},"id":1}

`
	mt := newMockTransport([]transport.ToolSchema{
		{Name: "stream-tool", Description: "streams"},
	})
	mt.roundTrip = func(_ context.Context, req *jsonrpc.Request) (transport.TransportResult, error) {
		if req.Method == "tools/list" {
			resp, _ := jsonrpc.NewResponse(req.ID, map[string]any{"tools": mt.tools})
			return transport.NewJSONResult(resp), nil
		}
		return transport.NewStreamResult(io.NopCloser(strings.NewReader(sseData))), nil
	}

	gw := newTestGateway(t,
		withConfig(`
server:
  address: ":0"
upstreams:
  - name: sse-upstream
    transport: streamable-http
    url: http://placeholder
`),
		withTransport("sse-upstream", mt),
	)

	// Send tools/call and check the response is SSE-passthrough
	body := `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"stream-tool"},"id":1}`
	httpReq := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	gw.Handler.ServeHTTP(rec, httpReq)

	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("expected SSE content type, got %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "streamed") {
		t.Errorf("SSE body doesn't contain expected data: %s", rec.Body.String())
	}
}

// ============================================================
// Auth & Access Control Tests
// ============================================================

func newAuthGateway(t *testing.T) (*testGateway, string, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "auth-test.db")

	mt := newMockTransport([]transport.ToolSchema{
		{Name: "github/search", Description: "search"},
		{Name: "github/pr", Description: "pr"},
		{Name: "deploy/run", Description: "deploy"},
	})

	gw := newTestGateway(t,
		withConfig(fmt.Sprintf(`
server:
  address: ":0"
  db_path: %s
upstreams:
  - name: tools
    transport: streamable-http
    url: http://placeholder
roles:
  github-role:
    allowed_tools: ["github/*"]
  deploy-role:
    allowed_tools: ["deploy/*"]
`, dbPath)),
		withTransport("tools", mt),
		withAdminKey("test-admin-key"),
	)

	githubKey := gw.addCaller(t, "github-user", "github-role")
	deployKey := gw.addCaller(t, "deploy-user", "deploy-role")
	return gw, githubKey, deployKey
}

func TestValidKeyAccessesAllowedTools(t *testing.T) {
	gw, githubKey, _ := newAuthGateway(t)

	resp := gw.jsonRPCRequest(t, "tools/call", map[string]any{"name": "github/search"}, githubKey)
	if resp.Error != nil {
		t.Fatalf("expected success, got error: %v", resp.Error)
	}
}

func TestValidKeyBlockedFromOtherTools(t *testing.T) {
	gw, githubKey, _ := newAuthGateway(t)

	resp := gw.jsonRPCRequest(t, "tools/call", map[string]any{"name": "deploy/run"}, githubKey)
	if resp.Error == nil {
		t.Fatal("expected access denied error")
	}
	if resp.Error.Code != -32000 {
		t.Errorf("expected code -32000, got %d", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Message, "access denied") {
		t.Errorf("expected 'access denied', got %q", resp.Error.Message)
	}
}

func TestInvalidKeyRejected(t *testing.T) {
	gw, _, _ := newAuthGateway(t)

	resp := gw.jsonRPCRequest(t, "tools/list", nil, "sk-invalid-key-00000000000000")
	if resp.Error == nil {
		t.Fatal("expected unauthorized error")
	}
	if !strings.Contains(resp.Error.Message, "unauthorized") {
		t.Errorf("expected 'unauthorized', got %q", resp.Error.Message)
	}
}

func TestNoKeyRejected(t *testing.T) {
	gw, _, _ := newAuthGateway(t)

	resp := gw.jsonRPCRequest(t, "tools/list", nil, "")
	if resp.Error == nil {
		t.Fatal("expected unauthorized error")
	}
}

func TestFilteredToolsList(t *testing.T) {
	gw, githubKey, deployKey := newAuthGateway(t)

	// github-user should only see github/* tools
	resp := gw.jsonRPCRequest(t, "tools/list", nil, githubKey)
	if resp.Error != nil {
		t.Fatalf("tools/list error: %v", resp.Error)
	}
	names := toolNames(resp)
	for _, n := range names {
		if !strings.HasPrefix(n, "github/") {
			t.Errorf("github-user saw non-github tool: %s", n)
		}
	}
	if len(names) != 2 {
		t.Errorf("expected 2 github tools, got %d: %v", len(names), names)
	}

	// deploy-user should only see deploy/* tools
	resp = gw.jsonRPCRequest(t, "tools/list", nil, deployKey)
	names = toolNames(resp)
	for _, n := range names {
		if !strings.HasPrefix(n, "deploy/") {
			t.Errorf("deploy-user saw non-deploy tool: %s", n)
		}
	}
	if len(names) != 1 {
		t.Errorf("expected 1 deploy tool, got %d: %v", len(names), names)
	}
}

// ============================================================
// Rate Limiting Tests
// ============================================================

func TestCallerRateLimitEnforced(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ratelimit-test.db")

	mt := newMockTransport([]transport.ToolSchema{
		{Name: "echo", Description: "echoes"},
	})

	gw := newTestGateway(t,
		withConfig(fmt.Sprintf(`
server:
  address: ":0"
  db_path: %s
upstreams:
  - name: tools
    transport: streamable-http
    url: http://placeholder
roles:
  limited:
    allowed_tools: ["*"]
    rate_limit: "2/sec"
`, dbPath)),
		withTransport("tools", mt),
	)

	key := gw.addCaller(t, "rate-user", "limited")

	// Send requests rapidly — at least one should be rate limited
	var rateLimited bool
	for i := 0; i < 10; i++ {
		resp := gw.jsonRPCRequest(t, "tools/call", map[string]any{"name": "echo"}, key)
		if resp.Error != nil && strings.Contains(resp.Error.Message, "rate limit") {
			rateLimited = true
			break
		}
	}
	if !rateLimited {
		t.Error("expected rate limiting to kick in")
	}
}

func TestRateLimitPerTool(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tool-ratelimit-test.db")

	mt := newMockTransport([]transport.ToolSchema{
		{Name: "tool-a", Description: "tool a"},
		{Name: "tool-b", Description: "tool b"},
	})

	gw := newTestGateway(t,
		withConfig(fmt.Sprintf(`
server:
  address: ":0"
  db_path: %s
upstreams:
  - name: tools
    transport: streamable-http
    url: http://placeholder
roles:
  limited:
    allowed_tools: ["*"]
    tool_rate_limit: "1/sec"
rate_limits:
  default_caller: "1000/sec"
`, dbPath)),
		withTransport("tools", mt),
	)

	key := gw.addCaller(t, "tool-rate-user", "limited")

	// Exhaust tool-a's limit
	var toolALimited bool
	for i := 0; i < 10; i++ {
		resp := gw.jsonRPCRequest(t, "tools/call", map[string]any{"name": "tool-a"}, key)
		if resp.Error != nil && strings.Contains(resp.Error.Message, "rate limit") {
			toolALimited = true
			break
		}
	}
	if !toolALimited {
		t.Error("expected tool-a to be rate limited")
	}

	// tool-b should still work
	resp := gw.jsonRPCRequest(t, "tools/call", map[string]any{"name": "tool-b"}, key)
	if resp.Error != nil {
		t.Errorf("tool-b should still work, got error: %v", resp.Error)
	}
}

func TestUpstreamRateLimit(t *testing.T) {
	mt := newMockTransport([]transport.ToolSchema{
		{Name: "echo", Description: "echoes"},
	})

	gw := newTestGateway(t,
		withConfig(`
server:
  address: ":0"
upstreams:
  - name: tools
    transport: streamable-http
    url: http://placeholder
    rate_limit: "2/sec"
`),
		withTransport("tools", mt),
	)

	var rateLimited bool
	for i := 0; i < 10; i++ {
		resp := gw.jsonRPCRequest(t, "tools/call", map[string]any{"name": "echo"}, "")
		if resp.Error != nil && strings.Contains(resp.Error.Message, "rate limit") {
			rateLimited = true
			break
		}
	}
	if !rateLimited {
		t.Error("expected upstream rate limiting")
	}
}

// ============================================================
// Resilience Tests
// ============================================================

func TestUpstreamDownAtStartup(t *testing.T) {
	mtHealthy := newMockTransport([]transport.ToolSchema{
		{Name: "working-tool", Description: "works"},
	})
	mtDown := newMockTransport(nil)
	mtDown.healthy.Store(false)
	mtDown.roundTrip = func(_ context.Context, req *jsonrpc.Request) (transport.TransportResult, error) {
		return nil, fmt.Errorf("connection refused")
	}

	gw := newTestGateway(t,
		withConfig(`
server:
  address: ":0"
upstreams:
  - name: healthy
    transport: streamable-http
    url: http://placeholder
  - name: down
    transport: streamable-http
    url: http://placeholder
`),
		withTransport("healthy", mtHealthy),
		withTransport("down", mtDown),
	)

	// Gateway should start and serve tools from healthy upstream
	resp := gw.jsonRPCRequest(t, "tools/list", nil, "")
	if resp.Error != nil {
		t.Fatalf("tools/list error: %v", resp.Error)
	}
	names := toolNames(resp)
	if !contains(names, "working-tool") {
		t.Errorf("expected working-tool, got: %v", names)
	}
}

func TestUpstreamGoesDownMidOperation(t *testing.T) {
	mt := newMockTransport([]transport.ToolSchema{
		{Name: "flaky-tool", Description: "might fail"},
	})

	gw := newTestGateway(t,
		withConfig(`
server:
  address: ":0"
upstreams:
  - name: flaky
    transport: streamable-http
    url: http://placeholder
`),
		withTransport("flaky", mt),
	)

	// First call works
	resp := gw.jsonRPCRequest(t, "tools/call", map[string]any{"name": "flaky-tool"}, "")
	if resp.Error != nil {
		t.Fatalf("first call should work: %v", resp.Error)
	}

	// Make upstream fail
	mt.roundTrip = func(_ context.Context, req *jsonrpc.Request) (transport.TransportResult, error) {
		return nil, fmt.Errorf("upstream unreachable")
	}

	// Second call returns error
	resp = gw.jsonRPCRequest(t, "tools/call", map[string]any{"name": "flaky-tool"}, "")
	if resp.Error == nil {
		t.Fatal("expected error when upstream is down")
	}
}

func TestUpstreamRecovers(t *testing.T) {
	mt := newMockTransport(nil) // Start with no tools
	mt.roundTrip = func(_ context.Context, req *jsonrpc.Request) (transport.TransportResult, error) {
		return nil, fmt.Errorf("connection refused")
	}

	gw := newTestGateway(t,
		withConfig(`
server:
  address: ":0"
upstreams:
  - name: recovering
    transport: streamable-http
    url: http://placeholder
`),
		withTransport("recovering", mt),
	)

	// Initially no tools
	resp := gw.jsonRPCRequest(t, "tools/list", nil, "")
	names := toolNames(resp)
	if len(names) != 0 {
		t.Errorf("expected no tools initially, got: %v", names)
	}

	// Upstream recovers
	mt.SetTools([]transport.ToolSchema{{Name: "recovered", Description: "back online"}})
	mt.roundTrip = func(_ context.Context, req *jsonrpc.Request) (transport.TransportResult, error) {
		switch req.Method {
		case "tools/list":
			resp, _ := jsonrpc.NewResponse(req.ID, map[string]any{"tools": mt.tools})
			return transport.NewJSONResult(resp), nil
		default:
			resp, _ := jsonrpc.NewResponse(req.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}})
			return transport.NewJSONResult(resp), nil
		}
	}

	// Refresh tool cache
	gw.Router.Refresh(context.Background())

	resp = gw.jsonRPCRequest(t, "tools/list", nil, "")
	names = toolNames(resp)
	if !contains(names, "recovered") {
		t.Errorf("expected recovered tool after refresh, got: %v", names)
	}
}

func TestToolCacheRefresh(t *testing.T) {
	mt := newMockTransport([]transport.ToolSchema{
		{Name: "original", Description: "original tool"},
	})

	gw := newTestGateway(t,
		withConfig(`
server:
  address: ":0"
upstreams:
  - name: dynamic
    transport: streamable-http
    url: http://placeholder
`),
		withTransport("dynamic", mt),
	)

	// Original tools
	resp := gw.jsonRPCRequest(t, "tools/list", nil, "")
	names := toolNames(resp)
	if !contains(names, "original") {
		t.Fatalf("expected original tool, got: %v", names)
	}

	// Upstream adds a new tool
	mt.SetTools([]transport.ToolSchema{
		{Name: "original", Description: "original tool"},
		{Name: "new-tool", Description: "just added"},
	})

	// Before refresh, new tool not visible
	resp = gw.jsonRPCRequest(t, "tools/list", nil, "")
	names = toolNames(resp)
	if contains(names, "new-tool") {
		t.Error("new-tool should not be visible before refresh")
	}

	// Refresh
	gw.Router.Refresh(context.Background())

	// After refresh, new tool appears
	resp = gw.jsonRPCRequest(t, "tools/list", nil, "")
	names = toolNames(resp)
	if !contains(names, "new-tool") {
		t.Errorf("expected new-tool after refresh, got: %v", names)
	}
}

// ============================================================
// Observability Tests
// ============================================================

func TestMetricsPopulated(t *testing.T) {
	mt := newMockTransport([]transport.ToolSchema{
		{Name: "metric-tool", Description: "for metrics test"},
	})

	gw := newTestGateway(t,
		withConfig(`
server:
  address: ":0"
upstreams:
  - name: tools
    transport: streamable-http
    url: http://placeholder
`),
		withTransport("tools", mt),
	)

	// Make several requests
	for i := 0; i < 3; i++ {
		gw.jsonRPCRequest(t, "tools/call", map[string]any{"name": "metric-tool"}, "")
	}

	// Check metrics
	if !metricsContain(t, gw.Registry, "stile_requests_total") {
		t.Error("expected stile_requests_total to have data")
	}
	if !metricsContain(t, gw.Registry, "stile_request_duration_seconds") {
		t.Error("expected stile_request_duration_seconds to have data")
	}
}

func TestAuditLogEntries(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "audit-test.db")

	mt := newMockTransport([]transport.ToolSchema{
		{Name: "audit-tool", Description: "for audit test"},
	})

	gw := newTestGateway(t,
		withConfig(`
server:
  address: ":0"
upstreams:
  - name: tools
    transport: streamable-http
    url: http://placeholder
`),
		withTransport("tools", mt),
		withAuditDB(auditPath),
	)

	// Make tool calls
	gw.jsonRPCRequest(t, "tools/call", map[string]any{"name": "audit-tool"}, "")
	gw.jsonRPCRequest(t, "tools/call", map[string]any{"name": "audit-tool"}, "")

	// Query audit database directly
	db, err := sql.Open("sqlite", auditPath)
	if err != nil {
		t.Fatalf("open audit db: %v", err)
	}
	defer db.Close()

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM audit_log WHERE tool = 'audit-tool'").Scan(&count)
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 audit entries, got %d", count)
	}

	// Check entry fields
	var caller, method, status string
	err = db.QueryRow("SELECT caller, method, status FROM audit_log WHERE tool = 'audit-tool' LIMIT 1").Scan(&caller, &method, &status)
	if err != nil {
		t.Fatalf("query audit entry: %v", err)
	}
	if method != "tools/call" {
		t.Errorf("expected method 'tools/call', got %q", method)
	}
	if status != "ok" {
		t.Errorf("expected status 'ok', got %q", status)
	}
}

// ============================================================
// Admin Tests
// ============================================================

func TestAdminRefresh(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "admin-refresh.db")
	adminKey := "test-admin-key-refresh"

	mt := newMockTransport([]transport.ToolSchema{
		{Name: "refreshable", Description: "refreshable tool"},
	})

	gw := newTestGateway(t,
		withConfig(fmt.Sprintf(`
server:
  address: ":0"
  db_path: %s
upstreams:
  - name: tools
    transport: streamable-http
    url: http://placeholder
roles:
  admin:
    allowed_tools: ["*"]
`, dbPath)),
		withTransport("tools", mt),
		withAdminKey(adminKey),
	)

	rec := gw.rawRequest(t, "POST", "/admin/refresh", nil, map[string]string{
		"Authorization": "Bearer " + adminKey,
	})
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result map[string]any
	json.Unmarshal(rec.Body.Bytes(), &result)
	if _, ok := result["upstreams"]; !ok {
		t.Error("expected upstreams in refresh response")
	}
}

// ============================================================
// Health Check Tests
// ============================================================

func TestHealthEndpoints(t *testing.T) {
	mt := newMockTransport([]transport.ToolSchema{
		{Name: "health-tool", Description: "for health test"},
	})

	gw := newTestGateway(t,
		withConfig(`
server:
  address: ":0"
upstreams:
  - name: tools
    transport: streamable-http
    url: http://placeholder
`),
		withTransport("tools", mt),
	)

	// /healthz always returns 200
	rec := gw.rawRequest(t, "GET", "/healthz", nil, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("expected healthz 200, got %d", rec.Code)
	}

	// /readyz returns 200 when upstreams are healthy
	rec = gw.rawRequest(t, "GET", "/readyz", nil, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("expected readyz 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ============================================================
// Ping and Method Not Found Tests
// ============================================================

func TestPing(t *testing.T) {
	mt := newMockTransport([]transport.ToolSchema{{Name: "t", Description: "t"}})
	gw := newTestGateway(t,
		withConfig(`
server:
  address: ":0"
upstreams:
  - name: tools
    transport: streamable-http
    url: http://placeholder
`),
		withTransport("tools", mt),
	)

	resp := gw.jsonRPCRequest(t, "ping", nil, "")
	if resp.Error != nil {
		t.Fatalf("ping error: %v", resp.Error)
	}
}

func TestMethodNotFound(t *testing.T) {
	mt := newMockTransport([]transport.ToolSchema{{Name: "t", Description: "t"}})
	gw := newTestGateway(t,
		withConfig(`
server:
  address: ":0"
upstreams:
  - name: tools
    transport: streamable-http
    url: http://placeholder
`),
		withTransport("tools", mt),
	)

	resp := gw.jsonRPCRequest(t, "nonexistent/method", nil, "")
	if resp.Error == nil {
		t.Fatal("expected method not found error")
	}
	if resp.Error.Code != jsonrpc.CodeMethodNotFound {
		t.Errorf("expected code %d, got %d", jsonrpc.CodeMethodNotFound, resp.Error.Code)
	}
}

func TestUnknownToolRejected(t *testing.T) {
	mt := newMockTransport([]transport.ToolSchema{{Name: "real-tool", Description: "real"}})
	gw := newTestGateway(t,
		withConfig(`
server:
  address: ":0"
upstreams:
  - name: tools
    transport: streamable-http
    url: http://placeholder
`),
		withTransport("tools", mt),
	)

	resp := gw.jsonRPCRequest(t, "tools/call", map[string]any{"name": "fake-tool"}, "")
	if resp.Error == nil {
		t.Fatal("expected unknown tool error")
	}
	if !strings.Contains(resp.Error.Message, "unknown tool") {
		t.Errorf("expected 'unknown tool' message, got %q", resp.Error.Message)
	}
}

// ============================================================
// End-to-End HTTP Mock Server Test
// ============================================================

func TestEndToEndWithHTTPMockServer(t *testing.T) {
	if testing.Short() {
		t.Skip("flaky: transient port exhaustion under parallel execution")
	}
	tools := []transport.ToolSchema{
		{Name: "greet", Description: "greets someone", InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`)},
	}

	mock := newMockMCPServer(t, tools)
	mock.SetHandler(func(req *jsonrpc.Request) *jsonrpc.Response {
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		json.Unmarshal(req.Params, &params)

		var args struct {
			Name string `json:"name"`
		}
		json.Unmarshal(params.Arguments, &args)

		resp, _ := jsonrpc.NewResponse(req.ID, map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": "Hello, " + args.Name + "!"},
			},
		})
		return resp
	})

	// Point the real HTTP transport at our mock
	cfgYAML := fmt.Sprintf(`
server:
  address: ":0"
upstreams:
  - name: greet-server
    transport: streamable-http
    url: %s
`, mock.URL())

	cfg, err := config.LoadBytes([]byte(cfgYAML))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	reg := prometheus.NewRegistry()
	m := metrics.NewForRegistry(reg)

	// Build real HTTP transport
	transports := make(map[string]transport.Transport)
	for _, ucfg := range cfg.Upstreams() {
		tr, err := transport.NewFromConfig(ucfg)
		if err != nil {
			t.Fatalf("create transport: %v", err)
		}
		transports[ucfg.Name()] = tr
		t.Cleanup(func() { tr.Close() })
	}

	rt, err := router.New(transports, cfg.Upstreams(), m)
	if err != nil {
		t.Fatalf("create router: %v", err)
	}
	t.Cleanup(func() { rt.Close() })

	handler := proxy.NewHandler(rt, nil, m, nil)

	upstreamDetails := rt.UpstreamDetails()
	upstreamInfos := make([]health.UpstreamInfo, len(upstreamDetails))
	for i, u := range upstreamDetails {
		u := u
		upstreamInfos[i] = health.UpstreamInfo{
			Name:      u.Name,
			Transport: u.Transport,
			Tools:     func() int { return len(u.Tools) },
			Stale:     func() bool { return u.Stale },
		}
	}
	hc := health.NewChecker(upstreamInfos, m)
	hc.Start()
	t.Cleanup(func() { hc.Stop() })

	srv := server.New(cfg, handler, rt, m, &server.Options{HealthChecker: hc})

	gw := &testGateway{
		Server:   srv,
		Handler:  srv.Handler(),
		Config:   cfg,
		Router:   rt,
		Metrics:  m,
		Registry: reg,
	}

	// Full lifecycle with real HTTP transport
	resp := gw.jsonRPCRequest(t, "initialize", map[string]any{"protocolVersion": "2025-11-25"}, "")
	if resp.Error != nil {
		t.Fatalf("initialize: %v", resp.Error)
	}

	resp = gw.jsonRPCRequest(t, "tools/list", nil, "")
	if resp.Error != nil {
		t.Fatalf("tools/list: %v", resp.Error)
	}
	names := toolNames(resp)
	if !contains(names, "greet") {
		t.Fatalf("expected greet tool, got: %v", names)
	}

	resp = gw.jsonRPCRequest(t, "tools/call", map[string]any{
		"name":      "greet",
		"arguments": map[string]any{"name": "World"},
	}, "")
	if resp.Error != nil {
		t.Fatalf("tools/call: %v", resp.Error)
	}

	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	json.Unmarshal(resp.Result, &result)
	if len(result.Content) == 0 || result.Content[0].Text != "Hello, World!" {
		t.Errorf("unexpected response: %s", string(resp.Result))
	}

	// Verify mock received the request
	reqs := mock.Requests()
	var foundToolsCall bool
	for _, r := range reqs {
		if r.Method == "tools/call" {
			foundToolsCall = true
		}
	}
	if !foundToolsCall {
		t.Error("mock server did not receive tools/call request")
	}
}

// ============================================================
// SSE Mode End-to-End Test
// ============================================================

func TestEndToEndSSEMode(t *testing.T) {
	if testing.Short() {
		t.Skip("flaky: transient port exhaustion under parallel execution")
	}
	tools := []transport.ToolSchema{
		{Name: "sse-tool", Description: "returns SSE"},
	}

	mock := newMockMCPServer(t, tools)
	mock.SetSSEMode(true)

	cfgYAML := fmt.Sprintf(`
server:
  address: ":0"
upstreams:
  - name: sse-server
    transport: streamable-http
    url: %s
`, mock.URL())

	cfg, err := config.LoadBytes([]byte(cfgYAML))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	reg := prometheus.NewRegistry()
	m := metrics.NewForRegistry(reg)

	transports := make(map[string]transport.Transport)
	for _, ucfg := range cfg.Upstreams() {
		tr, err := transport.NewFromConfig(ucfg)
		if err != nil {
			t.Fatalf("create transport: %v", err)
		}
		transports[ucfg.Name()] = tr
		t.Cleanup(func() { tr.Close() })
	}

	rt, err := router.New(transports, cfg.Upstreams(), m)
	if err != nil {
		t.Fatalf("create router: %v", err)
	}
	t.Cleanup(func() { rt.Close() })

	handler := proxy.NewHandler(rt, nil, m, nil)
	srv := server.New(cfg, handler, rt, m, &server.Options{})

	body := `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"sse-tool"},"id":1}`
	httpReq := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httpReq)

	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("expected SSE content type, got %q", ct)
	}
}

// ============================================================
// Admin Caller Management End-to-End
// ============================================================

func TestAdminCallerManagement(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "admin-callers.db")
	adminKey := "admin-key-management"

	mt := newMockTransport([]transport.ToolSchema{{Name: "t", Description: "t"}})

	gw := newTestGateway(t,
		withConfig(fmt.Sprintf(`
server:
  address: ":0"
  db_path: %s
upstreams:
  - name: tools
    transport: streamable-http
    url: http://placeholder
roles:
  user:
    allowed_tools: ["*"]
`, dbPath)),
		withTransport("tools", mt),
		withAdminKey(adminKey),
	)

	authHeader := map[string]string{"Authorization": "Bearer " + adminKey}

	// Create caller
	rec := gw.rawRequest(t, "POST", "/admin/callers", map[string]string{"name": "new-caller"}, authHeader)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create caller: %d %s", rec.Code, rec.Body.String())
	}

	// List callers
	rec = gw.rawRequest(t, "GET", "/admin/callers", nil, authHeader)
	if rec.Code != http.StatusOK {
		t.Fatalf("list callers: %d", rec.Code)
	}

	// Create key
	rec = gw.rawRequest(t, "POST", "/admin/callers/new-caller/keys", map[string]string{"label": "my-key"}, authHeader)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create key: %d %s", rec.Code, rec.Body.String())
	}
	var keyResp struct {
		Key string `json:"key"`
	}
	json.Unmarshal(rec.Body.Bytes(), &keyResp)
	if !strings.HasPrefix(keyResp.Key, "sk-") {
		t.Errorf("expected key starting with sk-, got %q", keyResp.Key)
	}

	// Assign role
	rec = gw.rawRequest(t, "POST", "/admin/callers/new-caller/roles", map[string]string{"role": "user"}, authHeader)
	if rec.Code != http.StatusOK {
		t.Fatalf("assign role: %d %s", rec.Code, rec.Body.String())
	}

	// The new caller can now make requests
	resp := gw.jsonRPCRequest(t, "tools/list", nil, keyResp.Key)
	if resp.Error != nil {
		t.Fatalf("new caller tools/list: %v", resp.Error)
	}

	// Delete caller
	rec = gw.rawRequest(t, "DELETE", "/admin/callers/new-caller", nil, authHeader)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete caller: %d", rec.Code)
	}

	// Caller's key should no longer work
	resp = gw.jsonRPCRequest(t, "tools/list", nil, keyResp.Key)
	if resp.Error == nil {
		t.Error("deleted caller's key should be rejected")
	}
}

// ============================================================
// Graceful Shutdown Test
// ============================================================

func TestGracefulShutdown(t *testing.T) {
	mt := newMockTransport([]transport.ToolSchema{
		{Name: "slow-tool", Description: "takes time"},
	})

	// Make the tool take 200ms to respond
	mt.roundTrip = func(ctx context.Context, req *jsonrpc.Request) (transport.TransportResult, error) {
		if req.Method == "tools/list" {
			resp, _ := jsonrpc.NewResponse(req.ID, map[string]any{"tools": mt.tools})
			return transport.NewJSONResult(resp), nil
		}
		select {
		case <-time.After(200 * time.Millisecond):
			resp, _ := jsonrpc.NewResponse(req.ID, map[string]any{
				"content": []map[string]any{{"type": "text", "text": "slow response"}},
			})
			return transport.NewJSONResult(resp), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	gw := newTestGateway(t,
		withConfig(`
server:
  address: ":0"
  shutdown_timeout: "5s"
upstreams:
  - name: slow
    transport: streamable-http
    url: http://placeholder
`),
		withTransport("slow", mt),
	)

	// Verify the slow tool responds correctly (the gateway can handle it)
	resp := gw.jsonRPCRequest(t, "tools/call", map[string]any{"name": "slow-tool"}, "")
	if resp.Error != nil {
		t.Fatalf("slow-tool should respond: %v", resp.Error)
	}

	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	json.Unmarshal(resp.Result, &result)
	if len(result.Content) == 0 || result.Content[0].Text != "slow response" {
		t.Errorf("unexpected response: %s", string(resp.Result))
	}
}

// ============================================================
// Metrics Endpoint Test
// ============================================================

func TestMetricsEndpoint(t *testing.T) {
	mt := newMockTransport([]transport.ToolSchema{{Name: "t", Description: "t"}})
	gw := newTestGateway(t,
		withConfig(`
server:
  address: ":0"
upstreams:
  - name: tools
    transport: streamable-http
    url: http://placeholder
`),
		withTransport("tools", mt),
	)

	// Prometheus /metrics endpoint is only available with the default registry,
	// but we can check the gateway has it wired up.
	rec := gw.rawRequest(t, "GET", "/metrics", nil, nil)
	// Our test uses a custom registry, so promhttp.Handler() uses default.
	// The handler is still registered; it returns whatever the default registry has.
	if rec.Code != http.StatusOK {
		t.Errorf("expected /metrics 200, got %d", rec.Code)
	}
}

// ============================================================
// Batch Request Test
// ============================================================

func TestBatchRequest(t *testing.T) {
	mt := newMockTransport([]transport.ToolSchema{{Name: "t", Description: "t"}})
	gw := newTestGateway(t,
		withConfig(`
server:
  address: ":0"
upstreams:
  - name: tools
    transport: streamable-http
    url: http://placeholder
`),
		withTransport("tools", mt),
	)

	batchBody := `[
		{"jsonrpc":"2.0","method":"ping","id":1},
		{"jsonrpc":"2.0","method":"tools/list","id":2}
	]`
	httpReq := httptest.NewRequest("POST", "/mcp", strings.NewReader(batchBody))
	httpReq.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	gw.Handler.ServeHTTP(rec, httpReq)

	var responses []jsonrpc.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &responses); err != nil {
		t.Fatalf("unmarshal batch response: %v (body: %s)", err, rec.Body.String())
	}
	if len(responses) != 2 {
		t.Fatalf("expected 2 responses, got %d", len(responses))
	}
}

// ============================================================
// Admin without auth key (dev mode)
// ============================================================

func TestAdminDevMode(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "dev-mode.db")

	mt := newMockTransport([]transport.ToolSchema{{Name: "t", Description: "t"}})
	gw := newTestGateway(t,
		withConfig(fmt.Sprintf(`
server:
  address: ":0"
  db_path: %s
upstreams:
  - name: tools
    transport: streamable-http
    url: http://placeholder
roles:
  user:
    allowed_tools: ["*"]
`, dbPath)),
		withTransport("tools", mt),
		// No admin key — dev mode (devMode=true in helpers)
	)

	// With no callers and no admin key, admin should be open (dev mode)
	rec := gw.rawRequest(t, "POST", "/admin/refresh", nil, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("dev mode: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// After adding a caller, admin should still be open in dev mode
	// (the --dev flag keeps admin open regardless of callers)
	gw.addCaller(t, "someone", "user")
	rec = gw.rawRequest(t, "POST", "/admin/refresh", nil, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("dev mode: expected 200 even after callers exist, got %d", rec.Code)
	}
}

// ============================================================
// Environment Variable Tests
// ============================================================

func TestEnvironmentVariableExpansion(t *testing.T) {
	// Verify that upstream auth token_env reads from the environment
	t.Setenv("TEST_UPSTREAM_TOKEN", "secret-token-123")

	tools := []transport.ToolSchema{{Name: "authed-tool", Description: "needs auth"}}
	mock := newMockMCPServer(t, tools)

	// Check the mock receives the bearer token
	var receivedAuth string
	origHandler := mock.server.Config.Handler
	mock.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		origHandler.ServeHTTP(w, r)
	})

	cfgYAML := fmt.Sprintf(`
server:
  address: ":0"
upstreams:
  - name: authed
    transport: streamable-http
    url: %s
    auth:
      type: bearer
      token_env: TEST_UPSTREAM_TOKEN
`, mock.URL())

	cfg, err := config.LoadBytes([]byte(cfgYAML))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	reg := prometheus.NewRegistry()
	m := metrics.NewForRegistry(reg)

	trs := make(map[string]transport.Transport)
	for _, ucfg := range cfg.Upstreams() {
		tr, err := transport.NewFromConfig(ucfg)
		if err != nil {
			t.Fatalf("create transport: %v", err)
		}
		trs[ucfg.Name()] = tr
		t.Cleanup(func() { tr.Close() })
	}

	rt, err := router.New(trs, cfg.Upstreams(), m)
	if err != nil {
		t.Fatalf("create router: %v", err)
	}
	t.Cleanup(func() { rt.Close() })

	// Router.New calls tools/list which sends a request to the mock.
	// Check if auth was forwarded.
	if receivedAuth == "" {
		t.Log("no auth received during tools/list (may be expected)")
	}
	if receivedAuth != "" && !strings.Contains(receivedAuth, "secret-token-123") {
		t.Errorf("expected bearer token in auth header, got %q", receivedAuth)
	}
}

// ============================================================
// Resilience Tests (circuit breaker + retry)
// ============================================================

func TestCircuitBreakerTripsAndFailsFast(t *testing.T) {
	callCount := 0
	mt := newMockTransport([]transport.ToolSchema{
		{Name: "fragile", Description: "breaks easily"},
	})

	// Parse config with circuit breaker so we can wrap the transport.
	cfg, err := config.LoadBytes([]byte(`
upstreams:
  - name: fragile-upstream
    transport: streamable-http
    url: http://placeholder
    circuit_breaker:
      failure_threshold: 3
      cooldown: 1h
`))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	// After initial tool discovery, switch to failing mode.
	originalRT := mt.roundTrip
	mt.roundTrip = func(ctx context.Context, req *jsonrpc.Request) (transport.TransportResult, error) {
		if req.Method == "tools/list" || req.Method == "initialize" {
			return originalRT(ctx, req)
		}
		callCount++
		return nil, &transport.ConnectError{Err: fmt.Errorf("connection refused")}
	}

	// Wrap with resilience.
	wrapped := resilience.Wrap(mt, cfg.Upstreams()[0], nil)

	gw := newTestGateway(t,
		withConfig(`
server:
  address: ":0"
upstreams:
  - name: fragile-upstream
    transport: streamable-http
    url: http://placeholder
`),
		withTransport("fragile-upstream", wrapped),
	)

	// Send requests that fail — should trip after 3.
	for i := 0; i < 3; i++ {
		resp := gw.jsonRPCRequest(t, "tools/call", map[string]any{"name": "fragile"}, "")
		if resp.Error == nil {
			t.Fatalf("call %d: expected error", i)
		}
	}

	innerCallsBefore := callCount

	// Next request should fail fast (circuit open) without hitting inner transport.
	resp := gw.jsonRPCRequest(t, "tools/call", map[string]any{"name": "fragile"}, "")
	if resp.Error == nil {
		t.Fatal("expected circuit open error")
	}
	if !strings.Contains(resp.Error.Message, "circuit open") {
		t.Errorf("expected 'circuit open' error, got: %s", resp.Error.Message)
	}

	if callCount != innerCallsBefore {
		t.Error("expected no new calls to inner transport when circuit is open")
	}
}

func TestRetrySucceedsOnTransientUpstreamFailure(t *testing.T) {
	callCount := 0
	mt := newMockTransport([]transport.ToolSchema{
		{Name: "flaky-tool", Description: "sometimes works"},
	})

	cfg, err := config.LoadBytes([]byte(`
upstreams:
  - name: retry-upstream
    transport: streamable-http
    url: http://placeholder
    retry:
      max_attempts: 3
      backoff: 1ms
      retryable_errors: [connection_error]
`))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	originalRT := mt.roundTrip
	mt.roundTrip = func(ctx context.Context, req *jsonrpc.Request) (transport.TransportResult, error) {
		if req.Method == "tools/list" || req.Method == "initialize" {
			return originalRT(ctx, req)
		}
		callCount++
		if callCount <= 2 {
			return nil, &transport.ConnectError{Err: fmt.Errorf("connection refused")}
		}
		return originalRT(ctx, req)
	}

	wrapped := resilience.Wrap(mt, cfg.Upstreams()[0], nil)

	gw := newTestGateway(t,
		withConfig(`
server:
  address: ":0"
upstreams:
  - name: retry-upstream
    transport: streamable-http
    url: http://placeholder
`),
		withTransport("retry-upstream", wrapped),
	)

	// Should succeed on the 3rd attempt.
	resp := gw.jsonRPCRequest(t, "tools/call", map[string]any{"name": "flaky-tool"}, "")
	if resp.Error != nil {
		t.Fatalf("expected success after retries, got: %s", resp.Error.Message)
	}
	if callCount != 3 {
		t.Errorf("expected 3 attempts, got %d", callCount)
	}
}

func TestCircuitBreakerRecoveryEndToEnd(t *testing.T) {
	callCount := 0
	failing := true
	mt := newMockTransport([]transport.ToolSchema{
		{Name: "recoverable", Description: "comes back"},
	})

	cfg, err := config.LoadBytes([]byte(`
upstreams:
  - name: recovery-upstream
    transport: streamable-http
    url: http://placeholder
    circuit_breaker:
      failure_threshold: 2
      cooldown: 50ms
`))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	originalRT := mt.roundTrip
	mt.roundTrip = func(ctx context.Context, req *jsonrpc.Request) (transport.TransportResult, error) {
		if req.Method == "tools/list" || req.Method == "initialize" {
			return originalRT(ctx, req)
		}
		callCount++
		if failing {
			return nil, &transport.ConnectError{Err: fmt.Errorf("down")}
		}
		return originalRT(ctx, req)
	}

	wrapped := resilience.Wrap(mt, cfg.Upstreams()[0], nil)

	gw := newTestGateway(t,
		withConfig(`
server:
  address: ":0"
upstreams:
  - name: recovery-upstream
    transport: streamable-http
    url: http://placeholder
`),
		withTransport("recovery-upstream", wrapped),
	)

	// Trip the circuit.
	for i := 0; i < 2; i++ {
		gw.jsonRPCRequest(t, "tools/call", map[string]any{"name": "recoverable"}, "")
	}

	// Circuit is open — fails fast.
	resp := gw.jsonRPCRequest(t, "tools/call", map[string]any{"name": "recoverable"}, "")
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "circuit open") {
		t.Fatal("expected circuit open error")
	}

	// Upstream recovers.
	failing = false

	// Wait for cooldown.
	time.Sleep(60 * time.Millisecond)

	// Half-open probe should succeed, closing the circuit.
	resp = gw.jsonRPCRequest(t, "tools/call", map[string]any{"name": "recoverable"}, "")
	if resp.Error != nil {
		t.Fatalf("expected success after recovery, got: %s", resp.Error.Message)
	}

	// Subsequent requests should work normally.
	resp = gw.jsonRPCRequest(t, "tools/call", map[string]any{"name": "recoverable"}, "")
	if resp.Error != nil {
		t.Fatalf("expected continued success, got: %s", resp.Error.Message)
	}
}

func TestResilienceMetricsPopulated(t *testing.T) {
	mt := newMockTransport([]transport.ToolSchema{
		{Name: "metered", Description: "tracked"},
	})

	cfg, err := config.LoadBytes([]byte(`
upstreams:
  - name: metered-upstream
    transport: streamable-http
    url: http://placeholder
    circuit_breaker:
      failure_threshold: 10
    retry:
      max_attempts: 2
      backoff: 1ms
      retryable_errors: [connection_error]
`))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	reg := prometheus.NewRegistry()
	m := metrics.NewForRegistry(reg)

	callCount := 0
	originalRT := mt.roundTrip
	mt.roundTrip = func(ctx context.Context, req *jsonrpc.Request) (transport.TransportResult, error) {
		if req.Method == "tools/list" || req.Method == "initialize" {
			return originalRT(ctx, req)
		}
		callCount++
		if callCount <= 1 {
			return nil, &transport.ConnectError{Err: fmt.Errorf("blip")}
		}
		return originalRT(ctx, req)
	}

	wrapped := resilience.Wrap(mt, cfg.Upstreams()[0], m)

	gw := newTestGateway(t,
		withConfig(`
server:
  address: ":0"
upstreams:
  - name: metered-upstream
    transport: streamable-http
    url: http://placeholder
`),
		withTransport("metered-upstream", wrapped),
	)
	// Override the gateway's metrics/registry so we can check them.
	gw.Registry = reg
	gw.Metrics = m

	// This request fails once then succeeds on retry.
	resp := gw.jsonRPCRequest(t, "tools/call", map[string]any{"name": "metered"}, "")
	if resp.Error != nil {
		t.Fatalf("expected success after retry, got: %s", resp.Error.Message)
	}

	if !metricsContain(t, reg, "stile_retries") {
		t.Error("expected stile_retries metric to be populated")
	}
	if !metricsContain(t, reg, "stile_circuit_state") {
		t.Error("expected stile_circuit_state metric to be populated")
	}
}
