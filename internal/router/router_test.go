package router

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/jsonrpc"
	"github.com/anguslmm/stile/internal/transport"
)

// mockTransport implements transport.Transport with canned responses.
type mockTransport struct {
	mu        sync.Mutex
	tools     []transport.ToolSchema
	listCalls int
	roundTrip func(ctx context.Context, req *jsonrpc.Request) (transport.TransportResult, error)
}

func (m *mockTransport) RoundTrip(ctx context.Context, req *jsonrpc.Request) (transport.TransportResult, error) {
	if req.Method == "tools/list" {
		m.mu.Lock()
		m.listCalls++
		tools := m.tools
		m.mu.Unlock()
		result := struct {
			Tools []transport.ToolSchema `json:"tools"`
		}{Tools: tools}
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

func (m *mockTransport) ListCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listCalls
}

func (m *mockTransport) SetTools(tools []transport.ToolSchema) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tools = tools
}

// failingTransport always fails on RoundTrip.
type failingTransport struct {
	healthy bool
}

func (f *failingTransport) RoundTrip(_ context.Context, _ *jsonrpc.Request) (transport.TransportResult, error) {
	return nil, fmt.Errorf("connection refused")
}
func (f *failingTransport) Close() error  { return nil }
func (f *failingTransport) Healthy() bool { return f.healthy }

// controllableTransport can switch between succeeding and failing.
type controllableTransport struct {
	mu    sync.Mutex
	tools []transport.ToolSchema
	fail  bool
}

func (c *controllableTransport) RoundTrip(_ context.Context, req *jsonrpc.Request) (transport.TransportResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.fail {
		return nil, fmt.Errorf("connection refused")
	}

	if req.Method == "tools/list" {
		result := struct {
			Tools []transport.ToolSchema `json:"tools"`
		}{Tools: c.tools}
		resp, _ := jsonrpc.NewResponse(req.ID, result)
		return transport.NewJSONResult(resp), nil
	}
	resp, _ := jsonrpc.NewResponse(req.ID, map[string]any{"ok": true})
	return transport.NewJSONResult(resp), nil
}

func (c *controllableTransport) Close() error  { return nil }
func (c *controllableTransport) Healthy() bool { return true }

func (c *controllableTransport) SetFail(fail bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fail = fail
}

func newConfigs(names ...string) []config.UpstreamConfig {
	yaml := "upstreams:\n"
	for _, n := range names {
		yaml += fmt.Sprintf("  - name: %s\n    transport: streamable-http\n    url: http://fake/%s\n", n, n)
	}
	cfg, err := config.LoadBytes([]byte(yaml))
	if err != nil {
		panic(err)
	}
	return cfg.Upstreams()
}

// Test 1: Initial discovery with two upstreams
func TestInitialDiscovery(t *testing.T) {
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

	rt, err := New(
		map[string]transport.Transport{"a": mockA, "b": mockB},
		newConfigs("a", "b"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	// Resolve each tool.
	for _, name := range []string{"alpha", "beta", "gamma"} {
		route, err := rt.Resolve(name)
		if err != nil {
			t.Errorf("Resolve(%q): %v", name, err)
			continue
		}
		if route.Tool.Name != name {
			t.Errorf("Resolve(%q): got tool name %q", name, route.Tool.Name)
		}
	}

	// alpha belongs to upstream a.
	route, _ := rt.Resolve("alpha")
	if route.Upstream.Name != "a" {
		t.Errorf("expected alpha -> upstream a, got %q", route.Upstream.Name)
	}

	// beta belongs to upstream b.
	route, _ = rt.Resolve("beta")
	if route.Upstream.Name != "b" {
		t.Errorf("expected beta -> upstream b, got %q", route.Upstream.Name)
	}
}

// Test 2: ListTools merges correctly
func TestListToolsMerges(t *testing.T) {
	mockA := &mockTransport{
		tools: []transport.ToolSchema{{Name: "alpha"}},
	}
	mockB := &mockTransport{
		tools: []transport.ToolSchema{{Name: "beta"}, {Name: "gamma"}},
	}

	rt, err := New(
		map[string]transport.Transport{"a": mockA, "b": mockB},
		newConfigs("a", "b"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	tools := rt.ListTools()
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools))
	}

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"alpha", "beta", "gamma"} {
		if !names[want] {
			t.Errorf("missing tool %q", want)
		}
	}
}

// Test 3: Resolve unknown tool returns error
func TestResolveUnknownTool(t *testing.T) {
	mock := &mockTransport{
		tools: []transport.ToolSchema{{Name: "alpha"}},
	}

	rt, err := New(
		map[string]transport.Transport{"a": mock},
		newConfigs("a"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	_, err = rt.Resolve("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
}

// Test 4: Refresh updates tools
func TestRefreshUpdatesTools(t *testing.T) {
	mock := &mockTransport{
		tools: []transport.ToolSchema{{Name: "alpha"}},
	}

	rt, err := New(
		map[string]transport.Transport{"a": mock},
		newConfigs("a"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	// Initially only alpha.
	if _, err := rt.Resolve("alpha"); err != nil {
		t.Fatalf("expected alpha to be resolvable: %v", err)
	}
	if _, err := rt.Resolve("beta"); err == nil {
		t.Fatal("expected beta to not be resolvable before refresh")
	}

	// Add a new tool to the upstream.
	mock.SetTools([]transport.ToolSchema{{Name: "alpha"}, {Name: "beta"}})

	rt.Refresh(context.Background())

	// Now beta should be resolvable.
	if _, err := rt.Resolve("beta"); err != nil {
		t.Fatalf("expected beta to be resolvable after refresh: %v", err)
	}
}

// Test 5: Stale upstream keeps existing tools
func TestStaleUpstreamKeepsTools(t *testing.T) {
	ct := &controllableTransport{
		tools: []transport.ToolSchema{{Name: "alpha"}},
	}

	rt, err := New(
		map[string]transport.Transport{"a": ct},
		newConfigs("a"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	// alpha is resolvable.
	if _, err := rt.Resolve("alpha"); err != nil {
		t.Fatalf("expected alpha resolvable: %v", err)
	}

	// Make upstream fail.
	ct.SetFail(true)
	rt.Refresh(context.Background())

	// alpha should still be resolvable.
	route, err := rt.Resolve("alpha")
	if err != nil {
		t.Fatalf("expected alpha still resolvable after failure: %v", err)
	}

	// Upstream should be marked stale.
	if !route.Upstream.Stale {
		t.Error("expected upstream to be marked stale")
	}
}

// Test 6: Upstream recovery clears stale flag
func TestUpstreamRecovery(t *testing.T) {
	ct := &controllableTransport{
		tools: []transport.ToolSchema{{Name: "alpha"}},
	}

	rt, err := New(
		map[string]transport.Transport{"a": ct},
		newConfigs("a"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	// Fail then recover.
	ct.SetFail(true)
	rt.Refresh(context.Background())

	route, _ := rt.Resolve("alpha")
	if !route.Upstream.Stale {
		t.Fatal("expected stale after failure")
	}

	ct.SetFail(false)
	rt.Refresh(context.Background())

	route, err = rt.Resolve("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if route.Upstream.Stale {
		t.Error("expected stale flag to be cleared after recovery")
	}
}

// Test 7: Duplicate tool name — first in config order wins
func TestDuplicateToolName(t *testing.T) {
	mockA := &mockTransport{
		tools: []transport.ToolSchema{{Name: "shared", Description: "from A"}},
	}
	mockB := &mockTransport{
		tools: []transport.ToolSchema{{Name: "shared", Description: "from B"}},
	}

	rt, err := New(
		map[string]transport.Transport{"a": mockA, "b": mockB},
		newConfigs("a", "b"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	route, err := rt.Resolve("shared")
	if err != nil {
		t.Fatal(err)
	}

	// First upstream in config order (a) should win.
	if route.Upstream.Name != "a" {
		t.Errorf("expected upstream a to own 'shared', got %q", route.Upstream.Name)
	}
	if route.Tool.Description != "from A" {
		t.Errorf("expected description 'from A', got %q", route.Tool.Description)
	}
}

// Test 8: Background refresh fires and Close stops it
func TestBackgroundRefresh(t *testing.T) {
	mock := &mockTransport{
		tools: []transport.ToolSchema{{Name: "alpha"}},
	}

	rt, err := New(
		map[string]transport.Transport{"a": mock},
		newConfigs("a"),
	)
	if err != nil {
		t.Fatal(err)
	}

	// Initial refresh already called tools/list once.
	initialCalls := mock.ListCalls()

	rt.StartBackgroundRefresh(50 * time.Millisecond)

	// Wait for a few ticks.
	time.Sleep(200 * time.Millisecond)

	callsAfterWait := mock.ListCalls()
	if callsAfterWait <= initialCalls {
		t.Errorf("expected background refresh to call tools/list, got %d calls (initial: %d)", callsAfterWait, initialCalls)
	}

	// Close should stop cleanly without deadlock.
	done := make(chan struct{})
	go func() {
		rt.Close()
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("Close() did not return in time — possible deadlock")
	}
}

// Test: RefreshUpstream refreshes a single upstream
func TestRefreshUpstream(t *testing.T) {
	mockA := &mockTransport{
		tools: []transport.ToolSchema{{Name: "alpha"}},
	}
	mockB := &mockTransport{
		tools: []transport.ToolSchema{{Name: "beta"}},
	}

	rt, err := New(
		map[string]transport.Transport{"a": mockA, "b": mockB},
		newConfigs("a", "b"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	// Add a tool to upstream b.
	mockB.SetTools([]transport.ToolSchema{{Name: "beta"}, {Name: "gamma"}})

	// Refresh only upstream b.
	if err := rt.RefreshUpstream(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}

	// gamma should now be resolvable.
	route, err := rt.Resolve("gamma")
	if err != nil {
		t.Fatalf("expected gamma resolvable: %v", err)
	}
	if route.Upstream.Name != "b" {
		t.Errorf("expected gamma -> upstream b, got %q", route.Upstream.Name)
	}
}

// Test: RefreshUpstream with unknown name
func TestRefreshUpstreamUnknown(t *testing.T) {
	mock := &mockTransport{
		tools: []transport.ToolSchema{{Name: "alpha"}},
	}

	rt, err := New(
		map[string]transport.Transport{"a": mock},
		newConfigs("a"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	err = rt.RefreshUpstream(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown upstream")
	}
}

// Test: Refresh returns correct status
func TestRefreshResult(t *testing.T) {
	mockA := &mockTransport{
		tools: []transport.ToolSchema{{Name: "alpha"}},
	}
	mockB := &mockTransport{
		tools: []transport.ToolSchema{{Name: "beta"}, {Name: "gamma"}},
	}

	rt, err := New(
		map[string]transport.Transport{"a": mockA, "b": mockB},
		newConfigs("a", "b"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	result := rt.Refresh(context.Background())

	if result.TotalTools != 3 {
		t.Errorf("expected total_tools=3, got %d", result.TotalTools)
	}

	statusA, ok := result.Upstreams["a"]
	if !ok {
		t.Fatal("missing upstream 'a' in result")
	}
	if statusA.Tools != 1 || statusA.Stale {
		t.Errorf("upstream a: tools=%d stale=%v", statusA.Tools, statusA.Stale)
	}

	statusB, ok := result.Upstreams["b"]
	if !ok {
		t.Fatal("missing upstream 'b' in result")
	}
	if statusB.Tools != 2 || statusB.Stale {
		t.Errorf("upstream b: tools=%d stale=%v", statusB.Tools, statusB.Stale)
	}
}

// Test: RefreshResult JSON marshaling matches admin endpoint format
func TestRefreshResultJSON(t *testing.T) {
	mockA := &mockTransport{
		tools: []transport.ToolSchema{{Name: "alpha"}},
	}

	rt, err := New(
		map[string]transport.Transport{"a": mockA},
		newConfigs("a"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	result := rt.Refresh(context.Background())
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}

	var parsed map[string]json.RawMessage
	json.Unmarshal(data, &parsed)

	if _, ok := parsed["upstreams"]; !ok {
		t.Error("missing 'upstreams' key in JSON")
	}
	if _, ok := parsed["total_tools"]; !ok {
		t.Error("missing 'total_tools' key in JSON")
	}
}
