// Package health provides upstream health monitoring and Kubernetes-style
// liveness/readiness endpoints.
package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/anguslmm/stile/internal/metrics"
	"github.com/anguslmm/stile/internal/transport"
)

// UpstreamInfo holds the transport and metadata for a monitored upstream.
type UpstreamInfo struct {
	Name      string
	Transport transport.Transport
	Tools     func() int // returns current tool count
	Stale     func() bool
}

// UpstreamHealth is the health status of a single upstream.
type UpstreamHealth struct {
	Healthy bool `json:"healthy"`
	Tools   int  `json:"tools"`
	Stale   bool `json:"stale"`
}

// ReadinessResponse is the JSON body for /readyz.
type ReadinessResponse struct {
	Status    string                    `json:"status"`
	Upstreams map[string]UpstreamHealth `json:"upstreams"`
}

// Checker periodically checks upstream health and serves health endpoints.
type Checker struct {
	mu               sync.RWMutex
	upstreams        []UpstreamInfo
	health           map[string]bool // upstream name → healthy
	metrics          *metrics.Metrics
	discoveryDone    bool
	checkInterval    time.Duration
	failThreshold    int
	consecutiveFails map[string]int

	stopCh chan struct{}
	done   chan struct{}
}

// NewChecker creates a Checker from the given upstreams. m may be nil.
func NewChecker(upstreams []UpstreamInfo, m *metrics.Metrics) *Checker {
	h := make(map[string]bool, len(upstreams))
	fails := make(map[string]int, len(upstreams))
	for _, u := range upstreams {
		h[u.Name] = u.Transport.Healthy()
		fails[u.Name] = 0
	}
	return &Checker{
		upstreams:        upstreams,
		health:           h,
		metrics:          m,
		discoveryDone:    true,
		checkInterval:    30 * time.Second,
		failThreshold:    3,
		consecutiveFails: fails,
		stopCh:           make(chan struct{}),
		done:             make(chan struct{}),
	}
}

// Start begins periodic health checking in a background goroutine.
func (c *Checker) Start() {
	go func() {
		defer close(c.done)
		ticker := time.NewTicker(c.checkInterval)
		defer ticker.Stop()

		// Initial check.
		c.check()

		for {
			select {
			case <-ticker.C:
				c.check()
			case <-c.stopCh:
				return
			}
		}
	}()
}

// Stop stops the background health checker.
func (c *Checker) Stop() {
	close(c.stopCh)
	<-c.done
}

func (c *Checker) check() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, u := range c.upstreams {
		healthy := u.Transport.Healthy()

		if healthy {
			c.consecutiveFails[u.Name] = 0
			c.health[u.Name] = true
		} else {
			c.consecutiveFails[u.Name]++
			if c.consecutiveFails[u.Name] >= c.failThreshold {
				if c.health[u.Name] {
					slog.Warn("upstream marked unhealthy",
						"upstream", u.Name,
						"consecutive_failures", c.consecutiveFails[u.Name],
					)
				}
				c.health[u.Name] = false
			}
		}

		if c.metrics != nil {
			val := 0.0
			if c.health[u.Name] {
				val = 1.0
			}
			c.metrics.UpstreamHealth.WithLabelValues(u.Name).Set(val)
		}
	}
}

// UpdateUpstreams replaces the monitored upstream list. New upstreams
// start as healthy; removed upstreams are pruned from health state.
func (c *Checker) UpdateUpstreams(upstreams []UpstreamInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()

	newNames := make(map[string]bool, len(upstreams))
	for _, u := range upstreams {
		newNames[u.Name] = true
		if _, exists := c.health[u.Name]; !exists {
			c.health[u.Name] = u.Transport.Healthy()
			c.consecutiveFails[u.Name] = 0
		}
	}

	// Remove state for upstreams that no longer exist.
	for name := range c.health {
		if !newNames[name] {
			delete(c.health, name)
			delete(c.consecutiveFails, name)
		}
	}

	c.upstreams = upstreams
}

// MarkDiscoveryDone marks that initial tool discovery has completed.
func (c *Checker) MarkDiscoveryDone() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.discoveryDone = true
}

// IsReady reports whether the gateway is ready to serve traffic.
func (c *Checker) IsReady() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if !c.discoveryDone {
		return false
	}
	for _, healthy := range c.health {
		if healthy {
			return true
		}
	}
	return false
}

// UpstreamStatuses returns the current health of all upstreams.
func (c *Checker) UpstreamStatuses() map[string]UpstreamHealth {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make(map[string]UpstreamHealth, len(c.upstreams))
	for _, u := range c.upstreams {
		result[u.Name] = UpstreamHealth{
			Healthy: c.health[u.Name],
			Tools:   u.Tools(),
			Stale:   u.Stale(),
		}
	}
	return result
}

// HandleLiveness handles GET /healthz — liveness probe.
func (c *Checker) HandleLiveness(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// HandleReadiness handles GET /readyz — readiness probe.
func (c *Checker) HandleReadiness(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	resp := ReadinessResponse{
		Upstreams: c.UpstreamStatuses(),
	}

	if c.IsReady() {
		resp.Status = "ready"
		w.WriteHeader(http.StatusOK)
	} else {
		resp.Status = "not_ready"
		w.WriteHeader(http.StatusServiceUnavailable)
	}

	json.NewEncoder(w).Encode(resp)
}

// CheckNow runs a health check immediately (useful for testing).
func (c *Checker) CheckNow(_ context.Context) {
	c.check()
}
