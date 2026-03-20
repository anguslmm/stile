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
