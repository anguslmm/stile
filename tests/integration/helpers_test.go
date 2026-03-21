package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/anguslmm/stile/internal/admin"
	"github.com/anguslmm/stile/internal/audit"
	"github.com/anguslmm/stile/internal/auth"
	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/health"
	"github.com/anguslmm/stile/internal/jsonrpc"
	"github.com/anguslmm/stile/internal/metrics"
	"github.com/anguslmm/stile/internal/policy"
	"github.com/anguslmm/stile/internal/proxy"
	"github.com/anguslmm/stile/internal/router"
	"github.com/anguslmm/stile/internal/server"
	"github.com/anguslmm/stile/internal/transport"
	"github.com/prometheus/client_golang/prometheus"
)

// mockMCPServer is a configurable mock MCP server for integration tests.
type mockMCPServer struct {
	mu       sync.Mutex
	tools    []transport.ToolSchema
	handler  func(req *jsonrpc.Request) *jsonrpc.Response // custom per-tool handler
	requests []*jsonrpc.Request                           // recorded requests
	server   *httptest.Server
	sseMode  bool
	healthy  atomic.Bool
}

func newMockMCPServer(t *testing.T, tools []transport.ToolSchema) *mockMCPServer {
	t.Helper()
	m := &mockMCPServer{tools: tools}
	m.healthy.Store(true)
	m.server = httptest.NewServer(http.HandlerFunc(m.handleRequest))
	t.Cleanup(func() { m.server.Close() })
	return m
}

func (m *mockMCPServer) URL() string {
	return m.server.URL
}

func (m *mockMCPServer) SetTools(tools []transport.ToolSchema) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tools = tools
}

func (m *mockMCPServer) SetHandler(h func(req *jsonrpc.Request) *jsonrpc.Response) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handler = h
}

func (m *mockMCPServer) SetSSEMode(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sseMode = enabled
}

func (m *mockMCPServer) Requests() []*jsonrpc.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*jsonrpc.Request, len(m.requests))
	copy(out, m.requests)
	return out
}

func (m *mockMCPServer) handleRequest(w http.ResponseWriter, r *http.Request) {
	if !m.healthy.Load() {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	reqs, _, err := jsonrpc.ParseMessage(body)
	if err != nil {
		http.Error(w, "parse error", http.StatusBadRequest)
		return
	}

	if len(reqs) == 0 {
		return
	}

	req := reqs[0]
	m.mu.Lock()
	m.requests = append(m.requests, req)
	tools := make([]transport.ToolSchema, len(m.tools))
	copy(tools, m.tools)
	handler := m.handler
	sseMode := m.sseMode
	m.mu.Unlock()

	var resp *jsonrpc.Response

	switch req.Method {
	case "initialize":
		resp, _ = jsonrpc.NewResponse(req.ID, map[string]any{
			"protocolVersion": "2025-11-25",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "mock-mcp", "version": "0.1.0"},
		})

	case "tools/list":
		resp, _ = jsonrpc.NewResponse(req.ID, map[string]any{"tools": tools})

	case "tools/call":
		if handler != nil {
			resp = handler(req)
		} else {
			resp, _ = jsonrpc.NewResponse(req.ID, map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "mock response"},
				},
			})
		}

	case "ping":
		resp, _ = jsonrpc.NewResponse(req.ID, map[string]any{})

	default:
		resp = jsonrpc.NewErrorResponse(req.ID, jsonrpc.CodeMethodNotFound, "method not found")
	}

	data, _ := json.Marshal(resp)

	if sseMode && req.Method == "tools/call" {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", string(data))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

// mockTransport wraps a mock MCP server as a transport.Transport.
type mockTransport struct {
	tools     []transport.ToolSchema
	roundTrip func(ctx context.Context, req *jsonrpc.Request) (transport.TransportResult, error)
	healthy   atomic.Bool
}

var _ transport.Transport = (*mockTransport)(nil)

func newMockTransport(tools []transport.ToolSchema) *mockTransport {
	mt := &mockTransport{tools: tools}
	mt.healthy.Store(true)
	mt.roundTrip = func(_ context.Context, req *jsonrpc.Request) (transport.TransportResult, error) {
		switch req.Method {
		case "tools/list":
			resp, _ := jsonrpc.NewResponse(req.ID, map[string]any{"tools": mt.tools})
			return transport.NewJSONResult(resp), nil
		case "tools/call":
			resp, _ := jsonrpc.NewResponse(req.ID, map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "mock response"},
				},
			})
			return transport.NewJSONResult(resp), nil
		default:
			resp := jsonrpc.NewErrorResponse(req.ID, jsonrpc.CodeMethodNotFound, "not found")
			return transport.NewJSONResult(resp), nil
		}
	}
	return mt
}

func (m *mockTransport) RoundTrip(ctx context.Context, req *jsonrpc.Request) (transport.TransportResult, error) {
	return m.roundTrip(ctx, req)
}

func (m *mockTransport) Close() error   { return nil }
func (m *mockTransport) Healthy() bool  { return m.healthy.Load() }

func (m *mockTransport) SetTools(tools []transport.ToolSchema) {
	m.tools = tools
}

// testGateway holds everything needed for an integration test.
type testGateway struct {
	Server       *server.Server
	Handler      http.Handler
	Config       *config.Config
	Router       *router.RouteTable
	Store        *auth.SQLiteStore
	AuditStore   audit.Store
	Metrics      *metrics.Metrics
	Registry     *prometheus.Registry
	HealthCheck  *health.Checker
	AdminKey     string
	ReloadFunc   server.ReloadFunc
}

type gatewayOpt func(*gatewayBuilder)

type gatewayBuilder struct {
	configYAML string
	transports map[string]transport.Transport
	adminKey   string
	auditDB    string
}

func withConfig(yaml string) gatewayOpt {
	return func(b *gatewayBuilder) { b.configYAML = yaml }
}

func withTransport(name string, t transport.Transport) gatewayOpt {
	return func(b *gatewayBuilder) { b.transports[name] = t }
}

func withAdminKey(key string) gatewayOpt {
	return func(b *gatewayBuilder) { b.adminKey = key }
}

func withAuditDB(path string) gatewayOpt {
	return func(b *gatewayBuilder) { b.auditDB = path }
}

// newTestGateway constructs a full gateway in-process for testing.
func newTestGateway(t *testing.T, opts ...gatewayOpt) *testGateway {
	t.Helper()

	b := &gatewayBuilder{
		transports: make(map[string]transport.Transport),
	}
	for _, o := range opts {
		o(b)
	}

	if b.configYAML == "" {
		t.Fatal("config YAML is required")
	}

	cfg, err := config.LoadBytes([]byte(b.configYAML))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	reg := prometheus.NewRegistry()
	m := metrics.NewForRegistry(reg)

	rt, err := router.New(b.transports, cfg.Upstreams(), m)
	if err != nil {
		t.Fatalf("create router: %v", err)
	}
	t.Cleanup(func() { rt.Close() })

	var auditStore audit.Store
	if b.auditDB != "" {
		auditStore, err = audit.NewSQLiteStore(b.auditDB)
		if err != nil {
			t.Fatalf("create audit store: %v", err)
		}
		t.Cleanup(func() { auditStore.Close() })
	}

	rateLimiter := policy.NewLocalRateLimiter(cfg)
	handler := proxy.NewHandler(rt, rateLimiter, m, auditStore)

	serverOpts := &server.Options{}

	var callerStore *auth.SQLiteStore
	dbPath := cfg.Server().DBPath()
	if dbPath != "" {
		callerStore, err = auth.NewSQLiteStore(dbPath)
		if err != nil {
			t.Fatalf("create caller store: %v", err)
		}
		t.Cleanup(func() { callerStore.Close() })

		authenticator := auth.NewAuthenticator(callerStore, cfg.Roles())
		serverOpts.Authenticator = authenticator

		if b.adminKey != "" {
			adminHash := sha256.Sum256([]byte(b.adminKey))
			serverOpts.AdminAuth = auth.AdminAuthMiddleware(adminHash, callerStore, false)
		} else {
			var zeroHash [32]byte
			serverOpts.AdminAuth = auth.AdminAuthMiddleware(zeroHash, callerStore, true)
		}
	}

	// Build health checker.
	upstreamDetails := rt.UpstreamDetails()
	upstreamInfos := make([]health.UpstreamInfo, len(upstreamDetails))
	for i, u := range upstreamDetails {
		u := u // capture
		upstreamInfos[i] = health.UpstreamInfo{
			Name:      u.Name,
			Transport: u.Transport,
			Tools:     func() int { return len(u.Tools) },
			Stale:     func() bool { return u.Stale },
		}
	}
	healthChecker := health.NewChecker(upstreamInfos, m)
	healthChecker.Start()
	t.Cleanup(func() { healthChecker.Stop() })
	serverOpts.HealthChecker = healthChecker

	// Build reload func (simplified for tests — just refreshes tools).
	reloadFunc := func(ctx context.Context) (*server.ReloadResult, error) {
		rt.Refresh(ctx)
		return &server.ReloadResult{Status: "ok"}, nil
	}
	serverOpts.ReloadFunc = reloadFunc

	if callerStore != nil {
		serverOpts.AdminHandler = admin.NewHandler(callerStore, rt, reloadFunc)
	}

	srv := server.New(cfg, handler, rt, m, serverOpts)

	return &testGateway{
		Server:      srv,
		Handler:     srv.Handler(),
		Config:      cfg,
		Router:      rt,
		Store:       callerStore,
		AuditStore:  auditStore,
		Metrics:     m,
		Registry:    reg,
		HealthCheck: healthChecker,
		AdminKey:    b.adminKey,
		ReloadFunc:  reloadFunc,
	}
}

// addCaller creates a caller, assigns roles, and returns the raw API key.
func (gw *testGateway) addCaller(t *testing.T, name string, roles ...string) string {
	t.Helper()
	if gw.Store == nil {
		t.Fatal("no caller store")
	}

	if err := gw.Store.AddCaller(name); err != nil {
		t.Fatalf("add caller %q: %v", name, err)
	}
	for _, role := range roles {
		if err := gw.Store.AssignRole(name, role); err != nil {
			t.Fatalf("assign role %q to %q: %v", role, name, err)
		}
	}

	rawKey, err := auth.GenerateAPIKey()
	if err != nil {
		t.Fatalf("generate key for %q: %v", name, err)
	}
	hash := sha256.Sum256([]byte(rawKey))
	if err := gw.Store.AddKey(name, hash, "test-key"); err != nil {
		t.Fatalf("add key for %q: %v", name, err)
	}
	return rawKey
}

// jsonRPCRequest sends a JSON-RPC request to POST /mcp and returns the response.
func (gw *testGateway) jsonRPCRequest(t *testing.T, method string, params any, apiKey string) *jsonrpc.Response {
	t.Helper()

	var paramsJSON json.RawMessage
	if params != nil {
		var err error
		paramsJSON, err = json.Marshal(params)
		if err != nil {
			t.Fatalf("marshal params: %v", err)
		}
	}

	reqBody := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"id":      1,
	}
	if paramsJSON != nil {
		reqBody["params"] = json.RawMessage(paramsJSON)
	}

	body, _ := json.Marshal(reqBody)
	httpReq := httptest.NewRequest("POST", "/mcp", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	rec := httptest.NewRecorder()
	gw.Handler.ServeHTTP(rec, httpReq)

	var resp jsonrpc.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v (body: %s)", err, rec.Body.String())
	}
	return &resp
}

// rawRequest sends an HTTP request and returns the recorder.
func (gw *testGateway) rawRequest(t *testing.T, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	httpReq := httptest.NewRequest(method, path, bodyReader)
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}

	rec := httptest.NewRecorder()
	gw.Handler.ServeHTTP(rec, httpReq)
	return rec
}

// toolsList helper that returns tool names from a tools/list response.
func toolNames(resp *jsonrpc.Response) []string {
	var result struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	json.Unmarshal(resp.Result, &result)
	names := make([]string, len(result.Tools))
	for i, t := range result.Tools {
		names[i] = t.Name
	}
	return names
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func metricsContain(t *testing.T, reg *prometheus.Registry, substr string) bool {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, fam := range families {
		if strings.Contains(fam.GetName(), substr) {
			for _, m := range fam.GetMetric() {
				// Check if any metric has a non-zero value.
				if m.GetCounter() != nil && m.GetCounter().GetValue() > 0 {
					return true
				}
				if m.GetHistogram() != nil && m.GetHistogram().GetSampleCount() > 0 {
					return true
				}
				if m.GetGauge() != nil {
					return true
				}
			}
		}
	}
	return false
}
