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

	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/jsonrpc"
)

// HTTPTransport implements Transport for Streamable HTTP MCP servers.
type HTTPTransport struct {
	url    string
	token  string
	client *http.Client
}

// NewHTTPTransport creates an HTTPTransport from the given upstream config.
func NewHTTPTransport(cfg config.UpstreamConfig) (*HTTPTransport, error) {
	t := &HTTPTransport{
		url:    cfg.URL(),
		client: &http.Client{},
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
		return nil, fmt.Errorf("transport/http: send request: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("transport/http: upstream returned status %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")

	if strings.HasPrefix(ct, "text/event-stream") {
		return NewStreamResult(resp.Body), nil
	}

	// Default: treat as JSON response.
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("transport/http: read response body: %w", err)
	}

	var rpcResp jsonrpc.Response
	if err := json.Unmarshal(data, &rpcResp); err != nil {
		return nil, fmt.Errorf("transport/http: unmarshal response: %w", err)
	}

	return NewJSONResult(&rpcResp), nil
}

// Close is a no-op for HTTP transport.
func (t *HTTPTransport) Close() error { return nil }

// Healthy always returns true for now. Real health checks come in Task 9.
func (t *HTTPTransport) Healthy() bool { return true }
