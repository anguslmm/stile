package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/jsonrpc"
)

var _ Transport = (*HTTPTransport)(nil)

// HTTPTransport implements Transport for Streamable HTTP MCP servers.
type HTTPTransport struct {
	url    string
	token  string
	client *http.Client

	mu                sync.Mutex
	consecutiveFails  int
	failThreshold     int
	healthy           bool
}

// NewHTTPTransport creates an HTTPTransport from the given upstream config.
func NewHTTPTransport(cfg config.UpstreamConfig) (*HTTPTransport, error) {
	t := &HTTPTransport{
		url:           cfg.URL(),
		client: &http.Client{
			Transport: &http.Transport{
				ResponseHeaderTimeout: 60 * time.Second,
			},
		},
		failThreshold: 3,
		healthy:       true,
	}

	if auth := cfg.Auth(); auth != nil && auth.TokenEnv() != "" {
		t.token = os.Getenv(auth.TokenEnv())
	}

	return t, nil
}

// RoundTrip sends a JSON-RPC request to the upstream and returns the result.
func (t *HTTPTransport) RoundTrip(ctx context.Context, req *jsonrpc.Request) (TransportResult, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("transport/http: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("transport/http: create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")

	if t.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+t.token)
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		t.recordFailure()
		return nil, fmt.Errorf("transport/http: send request: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		if resp.StatusCode >= 500 {
			t.recordFailure()
		} else {
			t.recordSuccess()
		}
		return nil, fmt.Errorf("transport/http: upstream returned status %d", resp.StatusCode)
	}

	t.recordSuccess()

	ct := resp.Header.Get("Content-Type")

	if strings.HasPrefix(ct, "text/event-stream") {
		return NewStreamResult(resp.Body), nil
	}

	// Default: treat as JSON response.
	defer resp.Body.Close()
	const maxResponseBody = 10 << 20 // 10 MB
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil {
		return nil, fmt.Errorf("transport/http: read response body: %w", err)
	}
	if len(data) > maxResponseBody {
		return nil, fmt.Errorf("transport/http: response body too large")
	}

	var rpcResp jsonrpc.Response
	if err := json.Unmarshal(data, &rpcResp); err != nil {
		return nil, fmt.Errorf("transport/http: unmarshal response: %w", err)
	}

	return NewJSONResult(&rpcResp), nil
}

// Close is a no-op for HTTP transport.
func (t *HTTPTransport) Close() error { return nil }

// Healthy reports whether the upstream is reachable based on recent request outcomes.
func (t *HTTPTransport) Healthy() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.healthy
}

func (t *HTTPTransport) recordFailure() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.consecutiveFails++
	if t.consecutiveFails >= t.failThreshold {
		t.healthy = false
	}
}

func (t *HTTPTransport) recordSuccess() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.consecutiveFails = 0
	t.healthy = true
}
