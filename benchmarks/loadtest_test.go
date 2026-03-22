package benchmarks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/jsonrpc"
	"github.com/anguslmm/stile/internal/policy"
	"github.com/anguslmm/stile/internal/proxy"
	"github.com/anguslmm/stile/internal/router"
	"github.com/anguslmm/stile/internal/server"
	"github.com/anguslmm/stile/internal/transport"
)

// --- Mock upstream servers ---

// newMockUpstream creates an httptest.Server that responds to MCP JSON-RPC
// requests with configurable latency.
func newMockUpstream(tools []string, latency time.Duration) *httptest.Server {
	toolSchemas := make([]map[string]string, len(tools))
	for i, name := range tools {
		toolSchemas[i] = map[string]string{"name": name, "description": "mock tool " + name}
	}
	toolsResult, _ := json.Marshal(map[string]any{"tools": toolSchemas})

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req jsonrpc.Request
		json.Unmarshal(body, &req)

		if latency > 0 {
			time.Sleep(latency)
		}

		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			resp, _ := jsonrpc.NewResponse(req.ID, map[string]any{
				"protocolVersion": "2025-11-25",
				"capabilities":    map[string]any{"tools": map[string]any{"listChanged": true}},
				"serverInfo":      map[string]any{"name": "mock", "version": "0.1.0"},
			})
			data, _ := json.Marshal(resp)
			w.Write(data)
		case "tools/list":
			resp, _ := jsonrpc.NewResponse(req.ID, json.RawMessage(toolsResult))
			data, _ := json.Marshal(resp)
			w.Write(data)
		case "tools/call":
			resp, _ := jsonrpc.NewResponse(req.ID, map[string]any{
				"content": []map[string]any{{"type": "text", "text": "ok"}},
			})
			data, _ := json.Marshal(resp)
			w.Write(data)
		default:
			resp := jsonrpc.NewErrorResponse(req.ID, jsonrpc.CodeMethodNotFound, "method not found")
			data, _ := json.Marshal(resp)
			w.Write(data)
		}
	}))
}

// newMockSSEUpstream responds to tools/call with SSE events.
func newMockSSEUpstream(tools []string, numEvents int) *httptest.Server {
	toolSchemas := make([]map[string]string, len(tools))
	for i, name := range tools {
		toolSchemas[i] = map[string]string{"name": name}
	}
	toolsResult, _ := json.Marshal(map[string]any{"tools": toolSchemas})

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req jsonrpc.Request
		json.Unmarshal(body, &req)

		switch req.Method {
		case "initialize":
			w.Header().Set("Content-Type", "application/json")
			resp, _ := jsonrpc.NewResponse(req.ID, map[string]any{
				"protocolVersion": "2025-11-25",
				"capabilities":    map[string]any{"tools": map[string]any{"listChanged": true}},
				"serverInfo":      map[string]any{"name": "mock-sse", "version": "0.1.0"},
			})
			data, _ := json.Marshal(resp)
			w.Write(data)
		case "tools/list":
			w.Header().Set("Content-Type", "application/json")
			resp, _ := jsonrpc.NewResponse(req.ID, json.RawMessage(toolsResult))
			data, _ := json.Marshal(resp)
			w.Write(data)
		case "tools/call":
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			flusher, _ := w.(http.Flusher)
			for i := range numEvents - 1 {
				fmt.Fprintf(w, "event: message\ndata: {\"progress\":%d}\n\n", i)
				if flusher != nil {
					flusher.Flush()
				}
			}
			// Final event with JSON-RPC response.
			resp, _ := jsonrpc.NewResponse(req.ID, map[string]any{
				"content": []map[string]any{{"type": "text", "text": "done"}},
			})
			data, _ := json.Marshal(resp)
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
			if flusher != nil {
				flusher.Flush()
			}
		default:
			w.Header().Set("Content-Type", "application/json")
			resp := jsonrpc.NewErrorResponse(req.ID, jsonrpc.CodeMethodNotFound, "not found")
			data, _ := json.Marshal(resp)
			w.Write(data)
		}
	}))
}

// --- Load test infrastructure ---

type loadTestResult struct {
	TotalRequests   int
	SuccessCount    int
	ErrorCount      int
	Duration        time.Duration
	RPS             float64
	Latencies       []time.Duration
	P50             time.Duration
	P90             time.Duration
	P95             time.Duration
	P99             time.Duration
	PeakGoroutines  int
	PeakAllocBytes  uint64
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}

// runLoad sends concurrent requests to the given handler for the specified duration.
func runLoad(handler http.Handler, concurrency int, duration time.Duration, reqBody []byte) loadTestResult {
	var (
		totalRequests atomic.Int64
		successCount  atomic.Int64
		errorCount    atomic.Int64
		mu            sync.Mutex
		latencies     []time.Duration
		peakGR        atomic.Int64
	)

	stop := make(chan struct{})
	var wg sync.WaitGroup

	start := time.Now()
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var localLatencies []time.Duration
			for {
				select {
				case <-stop:
					mu.Lock()
					latencies = append(latencies, localLatencies...)
					mu.Unlock()
					return
				default:
				}

				reqStart := time.Now()
				req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(reqBody))
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, req)
				elapsed := time.Since(reqStart)

				totalRequests.Add(1)
				if w.Code == http.StatusOK {
					successCount.Add(1)
				} else {
					errorCount.Add(1)
				}
				localLatencies = append(localLatencies, elapsed)

				gr := int64(runtime.NumGoroutine())
				for {
					cur := peakGR.Load()
					if gr <= cur || peakGR.CompareAndSwap(cur, gr) {
						break
					}
				}
			}
		}()
	}

	time.Sleep(duration)
	close(stop)
	wg.Wait()
	elapsed := time.Since(start)

	// Memory stats.
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })

	return loadTestResult{
		TotalRequests:  int(totalRequests.Load()),
		SuccessCount:   int(successCount.Load()),
		ErrorCount:     int(errorCount.Load()),
		Duration:       elapsed,
		RPS:            float64(totalRequests.Load()) / elapsed.Seconds(),
		Latencies:      latencies,
		P50:            percentile(latencies, 0.50),
		P90:            percentile(latencies, 0.90),
		P95:            percentile(latencies, 0.95),
		P99:            percentile(latencies, 0.99),
		PeakGoroutines: int(peakGR.Load()),
		PeakAllocBytes: memStats.TotalAlloc,
	}
}

func logResult(t *testing.T, name string, r loadTestResult) {
	t.Logf("=== %s ===", name)
	t.Logf("  Duration:       %v", r.Duration.Round(time.Millisecond))
	t.Logf("  Total requests: %d", r.TotalRequests)
	t.Logf("  Success:        %d", r.SuccessCount)
	t.Logf("  Errors:         %d", r.ErrorCount)
	t.Logf("  RPS:            %.0f", r.RPS)
	t.Logf("  Latency p50:    %v", r.P50)
	t.Logf("  Latency p90:    %v", r.P90)
	t.Logf("  Latency p95:    %v", r.P95)
	t.Logf("  Latency p99:    %v", r.P99)
	t.Logf("  Peak goroutines:%d", r.PeakGoroutines)
}

// --- Helper to build a Stile server handler ---

func buildStileHandler(t *testing.T, upstreamURLs map[string]string, rateLimitYAML string) http.Handler {
	t.Helper()

	var yamlParts []string
	yamlParts = append(yamlParts, "upstreams:")
	for name, url := range upstreamURLs {
		yamlParts = append(yamlParts, fmt.Sprintf("  - name: %s\n    transport: streamable-http\n    url: %s", name, url))
	}
	if rateLimitYAML != "" {
		yamlParts = append(yamlParts, rateLimitYAML)
	}

	cfg, err := config.LoadBytes([]byte(strings.Join(yamlParts, "\n")))
	if err != nil {
		t.Fatal(err)
	}

	transports := make(map[string]transport.Transport)
	for _, ucfg := range cfg.Upstreams() {
		tr, err := transport.NewFromConfig(ucfg)
		if err != nil {
			t.Fatalf("create transport %q: %v", ucfg.Name(), err)
		}
		transports[ucfg.Name()] = tr
	}

	rt, err := router.New(transports, cfg.Upstreams(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)

	var rl policy.RateLimiter
	if rateLimitYAML != "" {
		rl = policy.NewLocalRateLimiter(cfg)
	}

	handler := proxy.NewHandler(rt, rl, nil, nil)
	srv := server.New(cfg, handler, rt, nil, nil)
	return srv.Handler()
}

func toolCallBody(toolName string) []byte {
	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  "tools/call",
		"params":  map[string]any{"name": toolName, "arguments": map[string]any{}},
		"id":      1,
	}
	data, _ := json.Marshal(req)
	return data
}

// --- Load test scenarios ---

func TestLoadJSONPassthrough(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping load test in short mode")
	}
	upstream := newMockUpstream([]string{"echo"}, 0)
	defer upstream.Close()

	handler := buildStileHandler(t, map[string]string{"svc": upstream.URL}, "")
	body := toolCallBody("echo")

	result := runLoad(handler, 50, 3*time.Second, body)
	logResult(t, "JSON Passthrough (50 concurrent)", result)

	if result.ErrorCount > 0 {
		t.Errorf("expected zero errors, got %d", result.ErrorCount)
	}
}

func TestLoadSSEPassthrough(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping load test in short mode")
	}
	upstream := newMockSSEUpstream([]string{"stream"}, 5)
	defer upstream.Close()

	handler := buildStileHandler(t, map[string]string{"svc": upstream.URL}, "")
	body := toolCallBody("stream")

	result := runLoad(handler, 20, 3*time.Second, body)
	logResult(t, "SSE Passthrough (20 concurrent, 5 events)", result)

	if result.ErrorCount > 0 {
		t.Errorf("expected zero errors, got %d", result.ErrorCount)
	}
}

func TestLoadHighConcurrency(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping load test in short mode")
	}
	upstream := newMockUpstream([]string{"echo"}, 0)
	defer upstream.Close()

	handler := buildStileHandler(t, map[string]string{"svc": upstream.URL}, "")
	body := toolCallBody("echo")

	result := runLoad(handler, 500, 3*time.Second, body)
	logResult(t, "High Concurrency (500 concurrent)", result)

	if result.ErrorCount > 0 {
		t.Errorf("expected zero errors, got %d", result.ErrorCount)
	}
}

func TestLoadManyUpstreams(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping load test in short mode")
	}
	upstreams := make(map[string]string)
	var servers []*httptest.Server
	var allTools []string
	for i := range 20 {
		name := fmt.Sprintf("upstream-%d", i)
		toolName := fmt.Sprintf("tool_%d", i)
		srv := newMockUpstream([]string{toolName}, 0)
		servers = append(servers, srv)
		upstreams[name] = srv.URL
		allTools = append(allTools, toolName)
	}
	defer func() {
		for _, s := range servers {
			s.Close()
		}
	}()

	handler := buildStileHandler(t, upstreams, "")

	// Distribute requests across tools.
	var bodies [][]byte
	for _, tool := range allTools {
		bodies = append(bodies, toolCallBody(tool))
	}

	var totalReqs atomic.Int64
	var totalErrors atomic.Int64
	var mu sync.Mutex
	var allLatencies []time.Duration

	stop := make(chan struct{})
	var wg sync.WaitGroup
	start := time.Now()

	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var localLats []time.Duration
			i := 0
			for {
				select {
				case <-stop:
					mu.Lock()
					allLatencies = append(allLatencies, localLats...)
					mu.Unlock()
					return
				default:
				}
				body := bodies[i%len(bodies)]
				i++
				reqStart := time.Now()
				req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, req)
				localLats = append(localLats, time.Since(reqStart))
				totalReqs.Add(1)
				if w.Code != http.StatusOK {
					totalErrors.Add(1)
				}
			}
		}()
	}
	time.Sleep(3 * time.Second)
	close(stop)
	wg.Wait()
	elapsed := time.Since(start)

	sort.Slice(allLatencies, func(i, j int) bool { return allLatencies[i] < allLatencies[j] })

	t.Logf("=== Many Upstreams (20 upstreams, 50 concurrent) ===")
	t.Logf("  Duration:       %v", elapsed.Round(time.Millisecond))
	t.Logf("  Total requests: %d", totalReqs.Load())
	t.Logf("  Errors:         %d", totalErrors.Load())
	t.Logf("  RPS:            %.0f", float64(totalReqs.Load())/elapsed.Seconds())
	t.Logf("  Latency p50:    %v", percentile(allLatencies, 0.50))
	t.Logf("  Latency p99:    %v", percentile(allLatencies, 0.99))

	if totalErrors.Load() > 0 {
		t.Errorf("expected zero errors, got %d", totalErrors.Load())
	}
}

func TestLoadRateLimitHeavy(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping load test in short mode")
	}
	upstream := newMockUpstream([]string{"echo"}, 0)
	defer upstream.Close()

	handler := buildStileHandler(t, map[string]string{"svc": upstream.URL}, `
rate_limits:
  default_caller: 10000/sec
  default_tool: 10000/sec
  default_upstream: 100000/sec`)
	body := toolCallBody("echo")

	result := runLoad(handler, 50, 3*time.Second, body)
	logResult(t, "Rate Limit Heavy (50 concurrent, 10k/sec limit)", result)
	// Some rate limit denials are expected — just log.
	t.Logf("  Rate limited:   %d (%.1f%%)", result.ErrorCount, float64(result.ErrorCount)/float64(result.TotalRequests)*100)
}

func TestLoadWithUpstreamLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping load test in short mode")
	}
	upstream := newMockUpstream([]string{"slow"}, 5*time.Millisecond)
	defer upstream.Close()

	handler := buildStileHandler(t, map[string]string{"svc": upstream.URL}, "")
	body := toolCallBody("slow")

	result := runLoad(handler, 50, 3*time.Second, body)
	logResult(t, "Upstream Latency (5ms, 50 concurrent)", result)

	// Proxy overhead = p99 - upstream latency (5ms).
	overhead := result.P99 - 5*time.Millisecond
	t.Logf("  Proxy overhead (p99): %v", overhead)

	if result.ErrorCount > 0 {
		t.Errorf("expected zero errors, got %d", result.ErrorCount)
	}
}

// --- Go benchmarks for system-level throughput ---

func BenchmarkSystemToolsCall(b *testing.B) {
	upstream := newMockUpstream([]string{"echo"}, 0)
	defer upstream.Close()

	yamlCfg := fmt.Sprintf(`
upstreams:
  - name: svc
    transport: streamable-http
    url: %s
`, upstream.URL)
	cfg, _ := config.LoadBytes([]byte(yamlCfg))
	transports := make(map[string]transport.Transport)
	for _, ucfg := range cfg.Upstreams() {
		tr, _ := transport.NewFromConfig(ucfg)
		transports[ucfg.Name()] = tr
	}
	rt, _ := router.New(transports, cfg.Upstreams(), nil)
	defer rt.Close()

	handler := proxy.NewHandler(rt, nil, nil, nil)
	srv := server.New(cfg, handler, rt, nil, nil)
	h := srv.Handler()

	body := toolCallBody("echo")

	b.ResetTimer()
	for b.Loop() {
		req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
	}
}

func BenchmarkSystemToolsList(b *testing.B) {
	upstream := newMockUpstream([]string{"tool1", "tool2", "tool3", "tool4", "tool5"}, 0)
	defer upstream.Close()

	yamlCfg := fmt.Sprintf(`
upstreams:
  - name: svc
    transport: streamable-http
    url: %s
`, upstream.URL)
	cfg, _ := config.LoadBytes([]byte(yamlCfg))
	transports := make(map[string]transport.Transport)
	for _, ucfg := range cfg.Upstreams() {
		tr, _ := transport.NewFromConfig(ucfg)
		transports[ucfg.Name()] = tr
	}
	rt, _ := router.New(transports, cfg.Upstreams(), nil)
	defer rt.Close()

	handler := proxy.NewHandler(rt, nil, nil, nil)
	srv := server.New(cfg, handler, rt, nil, nil)
	h := srv.Handler()

	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "tools/list", "id": 1})

	b.ResetTimer()
	for b.Loop() {
		req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
	}
}
