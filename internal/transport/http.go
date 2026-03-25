package transport

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/anguslmm/stile/internal/auth"
	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/jsonrpc"
)

var _ Transport = (*HTTPTransport)(nil)

// ConnectError indicates a connection-level failure (TCP, DNS, TLS).
type ConnectError struct {
	Err error
}

func (e *ConnectError) Error() string { return e.Err.Error() }
func (e *ConnectError) Unwrap() error { return e.Err }

// StatusError indicates the upstream returned an HTTP error status.
type StatusError struct {
	Code int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("upstream returned status %d", e.Code)
}

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

// NewHTTPTransport creates an HTTPTransport from the given HTTP upstream config.
func NewHTTPTransport(cfg *config.HTTPUpstreamConfig) (*HTTPTransport, error) {
	timeout := cfg.Timeout()
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	httpTransport := &http.Transport{
		ResponseHeaderTimeout: timeout,
	}

	if tlsCfg := cfg.TLS(); tlsCfg != nil {
		tc, err := buildUpstreamTLSConfig(tlsCfg)
		if err != nil {
			return nil, fmt.Errorf("transport/http: build TLS config for %q: %w", cfg.Name(), err)
		}
		httpTransport.TLSClientConfig = tc
	}

	t := &HTTPTransport{
		url: cfg.URL(),
		client: &http.Client{
			Transport: httpTransport,
		},
		failThreshold: 3,
		healthy:       true,
	}

	if auth := cfg.Auth(); auth != nil && auth.TokenEnv() != "" {
		t.token = os.Getenv(auth.TokenEnv())
	}

	return t, nil
}

func buildUpstreamTLSConfig(cfg *config.UpstreamTLSConfig) (*tls.Config, error) {
	tc := &tls.Config{
		InsecureSkipVerify: cfg.InsecureSkipVerify(),
	}

	if cfg.CAFile() != "" {
		caCert, err := os.ReadFile(cfg.CAFile())
		if err != nil {
			return nil, fmt.Errorf("read CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse CA certificate from %s", cfg.CAFile())
		}
		tc.RootCAs = pool
	}

	if cfg.CertFile() != "" && cfg.KeyFile() != "" {
		cert, err := tls.LoadX509KeyPair(cfg.CertFile(), cfg.KeyFile())
		if err != nil {
			return nil, fmt.Errorf("load client certificate: %w", err)
		}
		tc.Certificates = []tls.Certificate{cert}
	}

	return tc, nil
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

	// Inject W3C Trace Context (traceparent/tracestate) into outbound headers.
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(httpReq.Header))

	// Per-request token (from OAuth flow) takes priority over static token.
	if perReqToken := auth.UpstreamTokenFromContext(ctx); perReqToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+perReqToken)
	} else if t.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+t.token)
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		t.recordFailure()
		return nil, &ConnectError{Err: fmt.Errorf("transport/http: send request: %w", err)}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		if resp.StatusCode >= 500 {
			t.recordFailure()
		} else {
			t.recordSuccess()
		}
		return nil, &StatusError{Code: resp.StatusCode}
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
