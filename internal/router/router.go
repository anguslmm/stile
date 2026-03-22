// Package router maps tool names to upstream transports.
// It handles tool discovery, caching, background refresh, and conflict resolution.
package router

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/jsonrpc"
	"github.com/anguslmm/stile/internal/metrics"
	"github.com/anguslmm/stile/internal/transport"
)

// Route maps a tool to the upstream that owns it.
type Route struct {
	Tool     transport.ToolSchema
	Upstream *Upstream
}

// Upstream holds a transport and its discovered tools.
type Upstream struct {
	Name        string
	Transport   transport.Transport
	Tools       []transport.ToolSchema
	Stale       bool
	LastRefresh time.Time
}

// UpstreamStatus reports the state of an upstream after a refresh.
type UpstreamStatus struct {
	Tools int  `json:"tools"`
	Stale bool `json:"stale"`
}

// RefreshResult is the response returned by Refresh, used by the admin endpoint.
type RefreshResult struct {
	Upstreams  map[string]UpstreamStatus `json:"upstreams"`
	TotalTools int                       `json:"total_tools"`
}

// RouteTable maps tool names to upstreams and manages tool discovery.
type RouteTable struct {
	mu        sync.RWMutex
	entries   map[string]*Route // tool name → route
	upstreams []*Upstream
	metrics   *metrics.Metrics

	stopCh    chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

// New creates a RouteTable, builds the upstream list from transports and configs,
// and runs an initial Refresh. Individual upstream failures during initial refresh
// are non-fatal — the upstream is marked stale and its tools remain empty.
// m may be nil to disable metrics.
func New(transports map[string]transport.Transport, configs []config.UpstreamConfig, m *metrics.Metrics) (*RouteTable, error) {
	rt := &RouteTable{
		entries: make(map[string]*Route),
		metrics: m,
		stopCh:  make(chan struct{}),
		done:    make(chan struct{}),
	}
	close(rt.done) // no background goroutine yet

	for _, cfg := range configs {
		t, ok := transports[cfg.Name()]
		if !ok {
			slog.Warn("no transport for upstream, skipping", "upstream", cfg.Name())
			continue
		}
		rt.upstreams = append(rt.upstreams, &Upstream{
			Name:      cfg.Name(),
			Transport: t,
		})
	}

	result := rt.Refresh(context.Background())
	for name, us := range result.Upstreams {
		if us.Stale {
			slog.Warn("initial route refresh failed for upstream", "upstream", name)
		}
	}

	return rt, nil
}

// Resolve looks up which upstream handles a tool.
func (rt *RouteTable) Resolve(toolName string) (*Route, error) {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	route, ok := rt.entries[toolName]
	if !ok {
		return nil, fmt.Errorf("unknown tool %q", toolName)
	}
	return route, nil
}

// ListTools returns the merged list of all tools from all upstreams.
func (rt *RouteTable) ListTools() []transport.ToolSchema {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	var tools []transport.ToolSchema
	for _, u := range rt.upstreams {
		tools = append(tools, u.Tools...)
	}
	return tools
}

// Refresh re-discovers tools from all upstreams and rebuilds the route table.
// Individual upstream failures are non-fatal: the upstream is marked stale but
// its existing tools are kept in the route table.
func (rt *RouteTable) Refresh(ctx context.Context) *RefreshResult {
	type discovered struct {
		upstream *Upstream
		tools    []transport.ToolSchema
		err      error
	}

	results := make([]discovered, len(rt.upstreams))
	for i, u := range rt.upstreams {
		start := time.Now()
		tools, err := discoverTools(ctx, u.Transport)
		duration := time.Since(start)
		results[i] = discovered{upstream: u, tools: tools, err: err}

		status := "success"
		if err != nil {
			status = "failure"
		}
		if rt.metrics != nil {
			rt.metrics.RecordToolCacheRefresh(u.Name, status)
		}
		slog.Info("tool cache refresh",
			"upstream", u.Name,
			"status", status,
			"tools", len(tools),
			"duration_ms", duration.Milliseconds(),
		)
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()

	for _, d := range results {
		if d.err != nil {
			slog.Warn("refresh upstream failed", "upstream", d.upstream.Name, "error", d.err)
			d.upstream.Stale = true
		} else {
			d.upstream.Tools = d.tools
			d.upstream.Stale = false
			d.upstream.LastRefresh = time.Now()
		}
	}

	rt.rebuildEntriesLocked()

	status := &RefreshResult{
		Upstreams:  make(map[string]UpstreamStatus),
		TotalTools: len(rt.entries),
	}
	for _, u := range rt.upstreams {
		status.Upstreams[u.Name] = UpstreamStatus{
			Tools: len(u.Tools),
			Stale: u.Stale,
		}
	}
	return status
}

// RefreshUpstream refreshes a single upstream.
func (rt *RouteTable) RefreshUpstream(ctx context.Context, name string) error {
	var target *Upstream
	for _, u := range rt.upstreams {
		if u.Name == name {
			target = u
			break
		}
	}
	if target == nil {
		return fmt.Errorf("unknown upstream %q", name)
	}

	tools, err := discoverTools(ctx, target.Transport)

	rt.mu.Lock()
	defer rt.mu.Unlock()

	if err != nil {
		target.Stale = true
		return err
	}

	target.Tools = tools
	target.Stale = false
	target.LastRefresh = time.Now()

	rt.rebuildEntriesLocked()
	return nil
}

// rebuildEntriesLocked rebuilds the route table entries from all upstreams.
// First upstream in order wins for duplicate tool names. Must be called with mu held.
func (rt *RouteTable) rebuildEntriesLocked() {
	rt.entries = make(map[string]*Route)
	for _, u := range rt.upstreams {
		for _, tool := range u.Tools {
			if _, exists := rt.entries[tool.Name]; exists {
				slog.Warn("duplicate tool, keeping first", "tool", tool.Name, "upstream", u.Name)
				continue
			}
			rt.entries[tool.Name] = &Route{
				Tool:     tool,
				Upstream: u,
			}
		}
	}
}

// StartBackgroundRefresh starts a goroutine that refreshes all upstreams
// on the given interval. Stop it by calling Close.
func (rt *RouteTable) StartBackgroundRefresh(interval time.Duration) {
	rt.done = make(chan struct{})
	go func() {
		defer close(rt.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				rt.Refresh(context.Background())
			case <-rt.stopCh:
				return
			}
		}
	}()
}

// AddUpstream adds a new upstream to the route table and refreshes it.
func (rt *RouteTable) AddUpstream(name string, t transport.Transport) {
	rt.mu.Lock()
	rt.upstreams = append(rt.upstreams, &Upstream{
		Name:      name,
		Transport: t,
	})
	rt.mu.Unlock()

	rt.RefreshUpstream(context.Background(), name)
}

// RemoveUpstream removes an upstream by name, closes its transport,
// and rebuilds the route table.
func (rt *RouteTable) RemoveUpstream(name string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	for i, u := range rt.upstreams {
		if u.Name == name {
			u.Transport.Close()
			rt.upstreams = append(rt.upstreams[:i], rt.upstreams[i+1:]...)
			break
		}
	}
	rt.rebuildEntriesLocked()
}

// Upstreams returns the current list of upstream names.
func (rt *RouteTable) Upstreams() []string {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	names := make([]string, len(rt.upstreams))
	for i, u := range rt.upstreams {
		names[i] = u.Name
	}
	return names
}

// UpstreamDetails returns the current upstreams for health checking.
func (rt *RouteTable) UpstreamDetails() []*Upstream {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	out := make([]*Upstream, len(rt.upstreams))
	copy(out, rt.upstreams)
	return out
}

// Close stops the background refresh goroutine and closes all upstream transports.
func (rt *RouteTable) Close() {
	rt.closeOnce.Do(func() {
		close(rt.stopCh)
		<-rt.done
		for _, u := range rt.upstreams {
			u.Transport.Close()
		}
	})
}

func discoverTools(ctx context.Context, t transport.Transport) ([]transport.ToolSchema, error) {
	// MCP spec requires initialize before any other method.
	// Some servers (e.g. mcp-server-fetch) enforce this strictly.
	initReq := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "initialize",
		ID:      jsonrpc.IntID(0),
		Params:  json.RawMessage(`{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"stile","version":"0.1.0"}}`),
	}
	initResp, err := transport.Send(ctx, t, initReq)
	if err != nil {
		slog.Debug("initialize failed (non-fatal)", "error", err)
	} else if initResp.Error != nil {
		slog.Debug("initialize returned error (non-fatal)", "error", initResp.Error.Message)
	} else {
		// MCP spec: client must send notifications/initialized after
		// a successful initialize. Some servers (Python SDK) require this
		// before they accept any other requests.
		notif := &jsonrpc.Request{
			JSONRPC: jsonrpc.Version,
			Method:  "notifications/initialized",
			// No ID — this is a notification.
		}
		transport.Send(ctx, t, notif)
	}

	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "tools/list",
		ID:      jsonrpc.IntID(1),
	}

	resp, err := transport.Send(ctx, t, req)
	if err != nil {
		return nil, fmt.Errorf("tools/list request: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("tools/list error: %s", resp.Error.Message)
	}

	var result struct {
		Tools []transport.ToolSchema `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("parse tools/list result: %w", err)
	}

	return result.Tools, nil
}
