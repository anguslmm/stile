package health

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/anguslmm/stile/internal/jsonrpc"
	"github.com/anguslmm/stile/internal/transport"
)

// mockTransport is a controllable transport for testing.
type mockTransport struct {
	mu      sync.Mutex
	healthy bool
}

func (m *mockTransport) RoundTrip(_ context.Context, req *jsonrpc.Request) (transport.TransportResult, error) {
	resp, _ := jsonrpc.NewResponse(req.ID, map[string]any{"ok": true})
	return transport.NewJSONResult(resp), nil
}
func (m *mockTransport) Close() error { return nil }
func (m *mockTransport) Healthy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.healthy
}
func (m *mockTransport) SetHealthy(h bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.healthy = h
}

func TestLivenessAlwaysPasses(t *testing.T) {
	c := NewChecker(nil, nil)
	rec := httptest.NewRecorder()
	c.HandleLiveness(rec, httptest.NewRequest("GET", "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body map[string]string
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body["status"] != "ok" {
		t.Errorf("expected status=ok, got %q", body["status"])
	}
}

func TestReadinessWithHealthyUpstreams(t *testing.T) {
	mt := &mockTransport{healthy: true}
	infos := []UpstreamInfo{
		{
			Name:      "test",
			Transport: mt,
			Tools:     func() int { return 5 },
			Stale:     func() bool { return false },
		},
	}
	c := NewChecker(infos, nil)
	c.CheckNow(context.Background())

	rec := httptest.NewRecorder()
	c.HandleReadiness(rec, httptest.NewRequest("GET", "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp ReadinessResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Status != "ready" {
		t.Errorf("expected status=ready, got %q", resp.Status)
	}
	if uh, ok := resp.Upstreams["test"]; !ok {
		t.Error("missing upstream 'test' in response")
	} else {
		if !uh.Healthy {
			t.Error("expected upstream 'test' to be healthy")
		}
		if uh.Tools != 5 {
			t.Errorf("expected tools=5, got %d", uh.Tools)
		}
	}
}

func TestReadinessWithNoHealthyUpstreams(t *testing.T) {
	mt := &mockTransport{healthy: false}
	infos := []UpstreamInfo{
		{
			Name:      "down",
			Transport: mt,
			Tools:     func() int { return 0 },
			Stale:     func() bool { return true },
		},
	}
	c := NewChecker(infos, nil)

	// Mark unhealthy through consecutive failures exceeding threshold.
	for i := 0; i < 3; i++ {
		c.CheckNow(context.Background())
	}

	rec := httptest.NewRecorder()
	c.HandleReadiness(rec, httptest.NewRequest("GET", "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}

	var resp ReadinessResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Status != "not_ready" {
		t.Errorf("expected status=not_ready, got %q", resp.Status)
	}
}

func TestReadinessDetail(t *testing.T) {
	mt1 := &mockTransport{healthy: true}
	mt2 := &mockTransport{healthy: false}
	infos := []UpstreamInfo{
		{Name: "up1", Transport: mt1, Tools: func() int { return 3 }, Stale: func() bool { return false }},
		{Name: "down1", Transport: mt2, Tools: func() int { return 0 }, Stale: func() bool { return true }},
	}
	c := NewChecker(infos, nil)

	// Run enough checks to mark down1 as unhealthy.
	for i := 0; i < 3; i++ {
		c.CheckNow(context.Background())
	}

	rec := httptest.NewRecorder()
	c.HandleReadiness(rec, httptest.NewRequest("GET", "/readyz", nil))

	var resp ReadinessResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)

	if len(resp.Upstreams) != 2 {
		t.Fatalf("expected 2 upstreams, got %d", len(resp.Upstreams))
	}

	// Still ready because up1 is healthy.
	if resp.Status != "ready" {
		t.Errorf("expected ready (one healthy upstream), got %q", resp.Status)
	}

	if !resp.Upstreams["up1"].Healthy {
		t.Error("expected up1 to be healthy")
	}
	if resp.Upstreams["down1"].Healthy {
		t.Error("expected down1 to be unhealthy")
	}
}

func TestUpstreamHealthTracking(t *testing.T) {
	mt := &mockTransport{healthy: true}
	infos := []UpstreamInfo{
		{Name: "flaky", Transport: mt, Tools: func() int { return 1 }, Stale: func() bool { return false }},
	}
	c := NewChecker(infos, nil)
	c.CheckNow(context.Background())

	if !c.IsReady() {
		t.Fatal("expected ready when upstream is healthy")
	}

	// Simulate failures: must fail failThreshold (3) consecutive times.
	mt.SetHealthy(false)
	c.CheckNow(context.Background())
	c.CheckNow(context.Background())

	// After 2 failures, still healthy (threshold is 3).
	if !c.IsReady() {
		t.Fatal("expected still ready after only 2 failures")
	}

	c.CheckNow(context.Background()) // 3rd failure

	if c.IsReady() {
		t.Fatal("expected not ready after 3 consecutive failures")
	}

	// Recovery: upstream becomes healthy again.
	mt.SetHealthy(true)
	c.CheckNow(context.Background())

	if !c.IsReady() {
		t.Fatal("expected ready after recovery")
	}
}

func TestCheckerWritesToStore(t *testing.T) {
	store := NewLocalStore()
	mt := &mockTransport{healthy: true}
	infos := []UpstreamInfo{
		{Name: "srv", Transport: mt, Tools: func() int { return 1 }, Stale: func() bool { return false }},
	}
	c := NewChecker(infos, nil, WithStore(store))
	c.CheckNow(context.Background())

	// Store should have the health status.
	st, err := store.Get(context.Background(), "srv")
	if err != nil {
		t.Fatalf("Get from store failed: %v", err)
	}
	if !st.Healthy {
		t.Error("expected healthy=true in store")
	}

	// Mark unhealthy and check again.
	mt.SetHealthy(false)
	for i := 0; i < 3; i++ {
		c.CheckNow(context.Background())
	}

	st, err = store.Get(context.Background(), "srv")
	if err != nil {
		t.Fatalf("Get from store failed: %v", err)
	}
	if st.Healthy {
		t.Error("expected healthy=false in store after failures")
	}
}

func TestCheckerReadsFromStore(t *testing.T) {
	store := NewLocalStore()
	mt := &mockTransport{healthy: false} // transport says unhealthy
	infos := []UpstreamInfo{
		{Name: "srv", Transport: mt, Tools: func() int { return 2 }, Stale: func() bool { return false }},
	}
	c := NewChecker(infos, nil,
		WithStore(store),
		WithReadFromStore(true),
	)

	// Store says healthy — checker should read from store, not transport.
	store.Set(context.Background(), "srv", Status{Healthy: true}, 0)
	c.CheckNow(context.Background())

	if !c.IsReady() {
		t.Fatal("expected ready when store says healthy (transport is unhealthy)")
	}

	// Store says unhealthy.
	store.Set(context.Background(), "srv", Status{Healthy: false}, 0)
	c.CheckNow(context.Background())

	if c.IsReady() {
		t.Fatal("expected not ready when store says unhealthy")
	}
}

func TestCheckerMissingStatusDefault(t *testing.T) {
	store := NewLocalStore() // empty store
	mt := &mockTransport{healthy: false}
	infos := []UpstreamInfo{
		{Name: "srv", Transport: mt, Tools: func() int { return 0 }, Stale: func() bool { return false }},
	}

	// Default: missing = healthy (fail-open).
	c := NewChecker(infos, nil,
		WithStore(store),
		WithReadFromStore(true),
		WithMissingStatus(true),
	)
	c.CheckNow(context.Background())

	if !c.IsReady() {
		t.Fatal("expected ready when missing_status=healthy and store has no key")
	}

	// missing = unhealthy (fail-closed).
	c2 := NewChecker(infos, nil,
		WithStore(store),
		WithReadFromStore(true),
		WithMissingStatus(false),
	)
	c2.CheckNow(context.Background())

	if c2.IsReady() {
		t.Fatal("expected not ready when missing_status=unhealthy and store has no key")
	}
}

func TestLocalUpstreamAlwaysPolledDirectly(t *testing.T) {
	store := NewLocalStore()
	localTransport := &mockTransport{healthy: true}
	remoteTransport := &mockTransport{healthy: false}

	infos := []UpstreamInfo{
		{Name: "stdio-srv", Transport: localTransport, Tools: func() int { return 1 }, Stale: func() bool { return false }, Local: true},
		{Name: "http-srv", Transport: remoteTransport, Tools: func() int { return 1 }, Stale: func() bool { return false }, Local: false},
	}

	// Store says http-srv is healthy; transport says it's not.
	store.Set(context.Background(), "http-srv", Status{Healthy: true}, 0)

	c := NewChecker(infos, nil,
		WithStore(store),
		WithReadFromStore(true),
	)
	c.CheckNow(context.Background())

	statuses := c.UpstreamStatuses()

	// Local upstream: should use transport (healthy=true), NOT store.
	if !statuses["stdio-srv"].Healthy {
		t.Error("expected stdio-srv healthy via direct transport poll")
	}

	// Remote upstream: should use store (healthy=true), not transport (healthy=false).
	if !statuses["http-srv"].Healthy {
		t.Error("expected http-srv healthy via store read")
	}

	// Verify local upstream does NOT write to store.
	_, err := store.Get(context.Background(), "stdio-srv")
	if err != ErrNotFound {
		t.Errorf("expected local upstream NOT to be written to store, got err=%v", err)
	}
}

func TestLocalUpstreamNotWrittenToStore(t *testing.T) {
	store := NewLocalStore()
	mt := &mockTransport{healthy: true}

	infos := []UpstreamInfo{
		{Name: "stdio-srv", Transport: mt, Tools: func() int { return 0 }, Stale: func() bool { return false }, Local: true},
		{Name: "http-srv", Transport: mt, Tools: func() int { return 0 }, Stale: func() bool { return false }, Local: false},
	}

	// Active mode (health agent would use this): store set, readFromStore=false.
	c := NewChecker(infos, nil, WithStore(store))
	c.CheckNow(context.Background())

	// Remote upstream should be written to store.
	st, err := store.Get(context.Background(), "http-srv")
	if err != nil {
		t.Fatalf("expected http-srv in store, got %v", err)
	}
	if !st.Healthy {
		t.Error("expected http-srv healthy in store")
	}

	// Local upstream should NOT be written to store.
	_, err = store.Get(context.Background(), "stdio-srv")
	if err != ErrNotFound {
		t.Errorf("expected stdio-srv NOT in store, got err=%v", err)
	}
}

func TestCheckerWithCheckInterval(t *testing.T) {
	mt := &mockTransport{healthy: true}
	infos := []UpstreamInfo{
		{Name: "srv", Transport: mt, Tools: func() int { return 0 }, Stale: func() bool { return false }},
	}
	c := NewChecker(infos, nil, WithCheckInterval(5000000000)) // 5s in nanoseconds
	if c.checkInterval != 5000000000 {
		t.Errorf("expected checkInterval=5s, got %v", c.checkInterval)
	}
	if c.storeTTL != 10000000000 {
		t.Errorf("expected storeTTL=10s (2x interval), got %v", c.storeTTL)
	}
}
