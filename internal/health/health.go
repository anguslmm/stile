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

	"github.com/anguslmm/stile/internal/jsonrpc"
	"github.com/anguslmm/stile/internal/metrics"
	"github.com/anguslmm/stile/internal/transport"
)

// UpstreamInfo holds the transport and metadata for a monitored upstream.
type UpstreamInfo struct {
	Name      string
	Transport transport.Transport
	Tools     func() int // returns current tool count
	Stale     func() bool
	Local     bool // true for stdio upstreams — always checked in-process, never via store
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

// CheckerOption configures a Checker.
type CheckerOption func(*Checker)

// WithStore sets a StatusStore for reading or writing health state.
func WithStore(store StatusStore) CheckerOption {
	return func(c *Checker) { c.store = store }
}

// WithReadFromStore makes the Checker read health from the store instead of
// polling upstreams directly. This is used when an external health agent
// writes results to the store.
func WithReadFromStore(b bool) CheckerOption {
	return func(c *Checker) { c.readFromStore = b }
}

// WithMissingStatus sets the default health assumption when a store lookup
// fails or returns ErrNotFound. If true, missing upstreams are treated as
// healthy (fail-open); if false, as unhealthy (fail-closed).
func WithMissingStatus(healthy bool) CheckerOption {
	return func(c *Checker) { c.missingStatus = healthy }
}

// WithCheckInterval overrides the health check interval (default 30s).
func WithCheckInterval(d time.Duration) CheckerOption {
	return func(c *Checker) { c.checkInterval = d }
}

// WithStoreTTL sets the TTL used when writing health status to the store.
// Only relevant in active/write mode (health agent). Default: 2x check interval.
func WithStoreTTL(d time.Duration) CheckerOption {
	return func(c *Checker) { c.storeTTL = d }
}

// WithActiveProbe enables active health probing via RoundTrip instead of
// passively calling Healthy(). This is required for the health agent, where
// the transport's Healthy() only reflects outcomes of real requests — which
// the agent never sends. The probe sends a JSON-RPC "ping" through the
// transport to trigger recordSuccess/recordFailure.
func WithActiveProbe(b bool) CheckerOption {
	return func(c *Checker) { c.activeProbe = b }
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

	// Store-backed health checking.
	store         StatusStore
	readFromStore bool // true = read from store (passive/gateway mode)
	missingStatus bool // default health when store returns ErrNotFound
	storeTTL      time.Duration
	activeProbe   bool // true = probe via RoundTrip instead of calling Healthy()

	stopCh chan struct{}
	done   chan struct{}
}

// NewChecker creates a Checker from the given upstreams. m may be nil.
func NewChecker(upstreams []UpstreamInfo, m *metrics.Metrics, opts ...CheckerOption) *Checker {
	h := make(map[string]bool, len(upstreams))
	fails := make(map[string]int, len(upstreams))
	for _, u := range upstreams {
		h[u.Name] = u.Transport.Healthy()
		fails[u.Name] = 0
	}
	c := &Checker{
		upstreams:        upstreams,
		health:           h,
		metrics:          m,
		discoveryDone:    true,
		checkInterval:    30 * time.Second,
		failThreshold:    3,
		consecutiveFails: fails,
		missingStatus:    true, // fail-open by default
		stopCh:           make(chan struct{}),
		done:             make(chan struct{}),
	}
	for _, opt := range opts {
		opt(c)
	}
	if c.storeTTL == 0 {
		c.storeTTL = 2 * c.checkInterval
	}
	return c
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

	var storeCtx context.Context
	var cancel context.CancelFunc
	if c.store != nil {
		storeCtx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
	}

	for _, u := range c.upstreams {
		// Local (stdio) upstreams are always checked in-process. An external
		// health agent cannot check a subprocess owned by this gateway instance.
		// Remote (HTTP) upstreams use the store when configured.
		useStore := !u.Local && c.store != nil && c.readFromStore

		if useStore {
			status, err := c.store.Get(storeCtx, u.Name)
			if err != nil {
				if c.health[u.Name] != c.missingStatus {
					slog.Warn("health store lookup failed, using default",
						"upstream", u.Name,
						"default_healthy", c.missingStatus,
						"error", err,
					)
				}
				c.health[u.Name] = c.missingStatus
			} else {
				c.health[u.Name] = status.Healthy
			}
		} else {
			var healthy bool
			if c.activeProbe {
				healthy = c.probe(u.Transport)
			} else {
				healthy = u.Transport.Healthy()
			}

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

			// Write to store for remote upstreams in active mode (health agent).
			if !u.Local && c.store != nil {
				if err := c.store.Set(storeCtx, u.Name, Status{
					Healthy:   c.health[u.Name],
					CheckedAt: time.Now(),
				}, c.storeTTL); err != nil {
					slog.Error("failed to write health status to store",
						"upstream", u.Name, "error", err)
				}
			}
		}

		if c.metrics != nil {
			val := 0.0
			if c.health[u.Name] {
				val = 1.0
			}
			c.metrics.SetUpstreamHealth(u.Name, val)
		}
	}
}

// probe sends a JSON-RPC "ping" request through the transport to actively
// test upstream connectivity. The transport's recordSuccess/recordFailure
// updates its Healthy() state as a side effect, but we return the direct
// outcome of the probe.
func (c *Checker) probe(t transport.Transport) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req := &jsonrpc.Request{
		JSONRPC: "2.0",
		Method:  "ping",
		ID:      jsonrpc.IntID(0),
	}
	result, err := t.RoundTrip(ctx, req)
	if err != nil {
		return false
	}
	// Resolve the result to ensure streaming responses are consumed.
	result.Resolve()
	return true
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
