package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestRequestCounterIncrements(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewForRegistry(reg)

	m.RecordRequest("alice", "search", "upstream-a", "ok")
	m.RecordRequest("alice", "search", "upstream-a", "ok")
	m.RecordRequest("bob", "query", "upstream-b", "error")

	body := scrape(t, m)
	if !strings.Contains(body, `stile_requests_total{caller="alice",status="ok",tool="search",upstream="upstream-a"} 2`) {
		t.Errorf("missing alice/search counter=2 in:\n%s", body)
	}
	if !strings.Contains(body, `stile_requests_total{caller="bob",status="error",tool="query",upstream="upstream-b"} 1`) {
		t.Errorf("missing bob/query counter=1 in:\n%s", body)
	}
}

func TestDurationHistogramRecorded(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewForRegistry(reg)

	m.RecordDuration("alice", "search", "upstream-a", 0.05)
	m.RecordDuration("alice", "search", "upstream-a", 0.15)

	body := scrape(t, m)
	if !strings.Contains(body, "stile_request_duration_seconds") {
		t.Errorf("missing duration histogram in:\n%s", body)
	}
}

func TestRateLimitRejectionCounted(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewForRegistry(reg)

	m.RecordRateLimitRejection("alice", "search")

	body := scrape(t, m)
	if !strings.Contains(body, `stile_rate_limit_rejections_total{caller="alice",tool="search"} 1`) {
		t.Errorf("missing rate limit rejection in:\n%s", body)
	}
}

func TestToolCacheRefreshCounted(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewForRegistry(reg)

	m.RecordToolCacheRefresh("upstream-a", "success")
	m.RecordToolCacheRefresh("upstream-b", "failure")

	body := scrape(t, m)
	if !strings.Contains(body, `stile_tool_cache_refresh_total{status="success",upstream="upstream-a"} 1`) {
		t.Errorf("missing cache refresh success in:\n%s", body)
	}
}

func TestUpstreamHealthGauge(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewForRegistry(reg)

	m.SetUpstreamHealth("upstream-a", 1)
	m.SetUpstreamHealth("upstream-b", 0)

	body := scrape(t, m)
	if !strings.Contains(body, `stile_upstream_health{upstream="upstream-a"} 1`) {
		t.Errorf("missing upstream-a health=1 in:\n%s", body)
	}
	if !strings.Contains(body, `stile_upstream_health{upstream="upstream-b"} 0`) {
		t.Errorf("missing upstream-b health=0 in:\n%s", body)
	}
}

func TestMetricsEndpointServes(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewForRegistry(reg)

	m.RecordRequest("alice", "search", "upstream-a", "ok")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	m.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "stile_requests_total") {
		t.Error("metrics endpoint does not contain stile_requests_total")
	}
	if !strings.Contains(body, `caller="alice"`) {
		t.Error("metrics endpoint does not contain caller label")
	}
}

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	m.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("scrape: expected 200, got %d", w.Code)
	}
	return w.Body.String()
}
