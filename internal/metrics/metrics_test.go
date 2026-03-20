package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRequestCounterIncrements(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewForRegistry(reg)

	m.RequestsTotal.WithLabelValues("alice", "search", "upstream-a", "ok").Inc()
	m.RequestsTotal.WithLabelValues("alice", "search", "upstream-a", "ok").Inc()
	m.RequestsTotal.WithLabelValues("bob", "query", "upstream-b", "error").Inc()

	val := testutil.ToFloat64(m.RequestsTotal.WithLabelValues("alice", "search", "upstream-a", "ok"))
	if val != 2 {
		t.Errorf("expected alice/search counter = 2, got %f", val)
	}

	val = testutil.ToFloat64(m.RequestsTotal.WithLabelValues("bob", "query", "upstream-b", "error"))
	if val != 1 {
		t.Errorf("expected bob/query counter = 1, got %f", val)
	}
}

func TestDurationHistogramRecorded(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewForRegistry(reg)

	m.RequestDuration.WithLabelValues("alice", "search", "upstream-a").Observe(0.05)
	m.RequestDuration.WithLabelValues("alice", "search", "upstream-a").Observe(0.15)

	count := testutil.CollectAndCount(m.RequestDuration)
	if count == 0 {
		t.Error("expected histogram to have observations")
	}
}

func TestRateLimitRejectionCounted(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewForRegistry(reg)

	m.RateLimitRejections.WithLabelValues("alice", "search").Inc()

	val := testutil.ToFloat64(m.RateLimitRejections.WithLabelValues("alice", "search"))
	if val != 1 {
		t.Errorf("expected rate limit rejection counter = 1, got %f", val)
	}
}

func TestToolCacheRefreshCounted(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewForRegistry(reg)

	m.ToolCacheRefresh.WithLabelValues("upstream-a", "success").Inc()
	m.ToolCacheRefresh.WithLabelValues("upstream-b", "failure").Inc()

	val := testutil.ToFloat64(m.ToolCacheRefresh.WithLabelValues("upstream-a", "success"))
	if val != 1 {
		t.Errorf("expected cache refresh success = 1, got %f", val)
	}
}

func TestUpstreamHealthGauge(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewForRegistry(reg)

	m.UpstreamHealth.WithLabelValues("upstream-a").Set(1)
	m.UpstreamHealth.WithLabelValues("upstream-b").Set(0)

	val := testutil.ToFloat64(m.UpstreamHealth.WithLabelValues("upstream-a"))
	if val != 1 {
		t.Errorf("expected upstream-a health = 1, got %f", val)
	}
	val = testutil.ToFloat64(m.UpstreamHealth.WithLabelValues("upstream-b"))
	if val != 0 {
		t.Errorf("expected upstream-b health = 0, got %f", val)
	}
}

func TestMetricsEndpointServes(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewForRegistry(reg)

	m.RequestsTotal.WithLabelValues("alice", "search", "upstream-a", "ok").Inc()

	handler := promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	handler.ServeHTTP(w, r)

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
