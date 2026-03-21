package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/health"
	"github.com/anguslmm/stile/internal/jsonrpc"
	"github.com/anguslmm/stile/internal/proxy"
	"github.com/anguslmm/stile/internal/router"
	"github.com/anguslmm/stile/internal/transport"
)

func newTestServerWithOpts(t *testing.T, mock *mockTransport, opts *Options) *httptest.Server {
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
	srv := New(cfg, h, rt, nil, opts)
	return httptest.NewServer(srv.Handler())
}

func TestHealthzEndpoint(t *testing.T) {
	mt := &healthMockTransport{healthy: true}
	infos := []health.UpstreamInfo{
		{
			Name:      "test",
			Transport: mt,
			Tools:     func() int { return 1 },
			Stale:     func() bool { return false },
		},
	}
	checker := health.NewChecker(infos, nil)

	mock := &mockTransport{tools: []transport.ToolSchema{{Name: "test-tool"}}}
	ts := newTestServerWithOpts(t, mock, &Options{HealthChecker: checker})
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	if body["status"] != "ok" {
		t.Errorf("expected status=ok, got %q", body["status"])
	}
}

func TestReadyzEndpointReady(t *testing.T) {
	mt := &healthMockTransport{healthy: true}
	infos := []health.UpstreamInfo{
		{
			Name:      "test",
			Transport: mt,
			Tools:     func() int { return 3 },
			Stale:     func() bool { return false },
		},
	}
	checker := health.NewChecker(infos, nil)
	checker.CheckNow(context.Background())

	mock := &mockTransport{tools: []transport.ToolSchema{{Name: "test-tool"}}}
	ts := newTestServerWithOpts(t, mock, &Options{HealthChecker: checker})
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body health.ReadinessResponse
	json.NewDecoder(resp.Body).Decode(&body)
	if body.Status != "ready" {
		t.Errorf("expected status=ready, got %q", body.Status)
	}
}

func TestReadyzEndpointNotReady(t *testing.T) {
	mt := &healthMockTransport{healthy: false}
	infos := []health.UpstreamInfo{
		{
			Name:      "test",
			Transport: mt,
			Tools:     func() int { return 0 },
			Stale:     func() bool { return true },
		},
	}
	checker := health.NewChecker(infos, nil)
	// Run checks to exceed failure threshold.
	for range 3 {
		checker.CheckNow(context.Background())
	}

	mock := &mockTransport{tools: []transport.ToolSchema{{Name: "test-tool"}}}
	ts := newTestServerWithOpts(t, mock, &Options{HealthChecker: checker})
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
}

// healthMockTransport for health checker integration.
type healthMockTransport struct {
	healthy bool
}

func (m *healthMockTransport) RoundTrip(_ context.Context, req *jsonrpc.Request) (transport.TransportResult, error) {
	resp, _ := jsonrpc.NewResponse(req.ID, map[string]any{"ok": true})
	return transport.NewJSONResult(resp), nil
}
func (m *healthMockTransport) Close() error  { return nil }
func (m *healthMockTransport) Healthy() bool { return m.healthy }
