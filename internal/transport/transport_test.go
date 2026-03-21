package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/jsonrpc"
)

// newTestUpstream creates an HTTPUpstreamConfig via LoadBytes for testing.
func newTestUpstream(t *testing.T, url string, auth bool) *config.HTTPUpstreamConfig {
	t.Helper()
	yaml := fmt.Sprintf(`
upstreams:
  - name: test
    transport: streamable-http
    url: %s
`, url)
	if auth {
		yaml += `    auth:
      type: bearer
      token_env: TEST_MCP_TOKEN
`
	}
	cfg, err := config.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("failed to create test config: %v", err)
	}
	return cfg.Upstreams()[0].(*config.HTTPUpstreamConfig)
}

func TestJSONResponseRoundTrip(t *testing.T) {
	resp := jsonrpc.NewErrorResponse(jsonrpc.IntID(1), jsonrpc.CodeMethodNotFound, "not found")
	respBytes, _ := json.Marshal(resp)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(respBytes)
	}))
	defer srv.Close()

	upstream := newTestUpstream(t, srv.URL, false)
	tr, err := NewHTTPTransport(upstream)
	if err != nil {
		t.Fatalf("NewHTTPTransport: %v", err)
	}

	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "test/method",
		ID:      jsonrpc.IntID(1),
	}

	result, err := tr.RoundTrip(context.Background(), req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}

	jr, ok := result.(*JSONResult)
	if !ok {
		t.Fatalf("expected *JSONResult, got %T", result)
	}
	if jr.ContentType() != "application/json" {
		t.Errorf("content type = %q, want %q", jr.ContentType(), "application/json")
	}
	if jr.Response().Error == nil || jr.Response().Error.Code != jsonrpc.CodeMethodNotFound {
		t.Errorf("unexpected response: %+v", jr.Response())
	}
}

func TestSSEResponseRoundTrip(t *testing.T) {
	sseBody := "event: message\ndata: {\"jsonrpc\":\"2.0\",\"result\":\"hello\",\"id\":1}\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sseBody))
	}))
	defer srv.Close()

	upstream := newTestUpstream(t, srv.URL, false)
	tr, _ := NewHTTPTransport(upstream)

	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "test/method",
		ID:      jsonrpc.IntID(1),
	}

	result, err := tr.RoundTrip(context.Background(), req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}

	sr, ok := result.(*StreamResult)
	if !ok {
		t.Fatalf("expected *StreamResult, got %T", result)
	}
	defer sr.Stream().Close()
	if sr.ContentType() != "text/event-stream" {
		t.Errorf("content type = %q, want %q", sr.ContentType(), "text/event-stream")
	}
}

func TestSendHelperJSON(t *testing.T) {
	resp, _ := jsonrpc.NewResponse(jsonrpc.IntID(1), map[string]string{"key": "value"})
	respBytes, _ := json.Marshal(resp)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(respBytes)
	}))
	defer srv.Close()

	upstream := newTestUpstream(t, srv.URL, false)
	tr, _ := NewHTTPTransport(upstream)

	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "test/method",
		ID:      jsonrpc.IntID(1),
	}

	got, err := Send(context.Background(), tr, req)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.Result == nil {
		t.Fatal("expected result to be set")
	}
}

func TestSendHelperSSE(t *testing.T) {
	// Notification then final response.
	sseBody := `event: message
data: {"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":50}}

event: message
data: {"jsonrpc":"2.0","result":{"tools":[]},"id":1}

`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sseBody))
	}))
	defer srv.Close()

	upstream := newTestUpstream(t, srv.URL, false)
	tr, _ := NewHTTPTransport(upstream)

	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "tools/list",
		ID:      jsonrpc.IntID(1),
	}

	got, err := Send(context.Background(), tr, req)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.Result == nil {
		t.Fatal("expected result to be set")
	}

	var result struct {
		Tools []any `json:"tools"`
	}
	if err := json.Unmarshal(got.Result, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
}

func TestAuthHeaderInjection(t *testing.T) {
	t.Setenv("TEST_MCP_TOKEN", "secret-token-123")

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		resp, _ := jsonrpc.NewResponse(jsonrpc.IntID(1), "ok")
		data, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}))
	defer srv.Close()

	upstream := newTestUpstream(t, srv.URL, true)
	tr, _ := NewHTTPTransport(upstream)

	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "test/method",
		ID:      jsonrpc.IntID(1),
	}

	_, err := tr.RoundTrip(context.Background(), req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}

	if gotAuth != "Bearer secret-token-123" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer secret-token-123")
	}
}

func TestUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	upstream := newTestUpstream(t, srv.URL, false)
	tr, _ := NewHTTPTransport(upstream)

	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "test/method",
		ID:      jsonrpc.IntID(1),
	}

	_, err := tr.RoundTrip(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestSSEReader(t *testing.T) {
	input := `event: message
data: {"jsonrpc":"2.0","result":"first","id":1}

event: message
data: {"jsonrpc":"2.0","result":"second","id":2}

`
	reader := NewSSEReader(strings.NewReader(input))

	ev1, err := reader.Next()
	if err != nil {
		t.Fatalf("event 1: %v", err)
	}
	if ev1.Event != "message" {
		t.Errorf("event 1 type = %q, want %q", ev1.Event, "message")
	}
	if !strings.Contains(ev1.Data, "first") {
		t.Errorf("event 1 data = %q, want to contain 'first'", ev1.Data)
	}

	ev2, err := reader.Next()
	if err != nil {
		t.Fatalf("event 2: %v", err)
	}
	if ev2.Event != "message" {
		t.Errorf("event 2 type = %q, want %q", ev2.Event, "message")
	}
	if !strings.Contains(ev2.Data, "second") {
		t.Errorf("event 2 data = %q, want to contain 'second'", ev2.Data)
	}

	_, err = reader.Next()
	if err != io.EOF {
		t.Errorf("expected io.EOF, got %v", err)
	}
}

func TestSSEReaderMultiLineData(t *testing.T) {
	input := "event: message\ndata: line1\ndata: line2\ndata: line3\n\n"
	reader := NewSSEReader(strings.NewReader(input))

	ev, err := reader.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Event != "message" {
		t.Errorf("event = %q, want %q", ev.Event, "message")
	}
	want := "line1\nline2\nline3"
	if ev.Data != want {
		t.Errorf("data = %q, want %q", ev.Data, want)
	}
}

func TestHTTPClientTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sleep longer than the header timeout but short enough for the test.
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	upstream := newTestUpstream(t, srv.URL, false)
	tr, _ := NewHTTPTransport(upstream)
	// Override with a short timeout for the test.
	tr.client.Transport.(*http.Transport).ResponseHeaderTimeout = 50 * time.Millisecond

	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "test/hang",
		ID:      jsonrpc.IntID(1),
	}

	_, err := tr.RoundTrip(context.Background(), req)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timeout") && !strings.Contains(err.Error(), "Timeout") {
		t.Fatalf("expected timeout-related error, got: %v", err)
	}
}

func TestPerUpstreamTimeoutOverride(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		resp, _ := jsonrpc.NewResponse(jsonrpc.IntID(1), "ok")
		data, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}))
	defer srv.Close()

	// Config with 50ms timeout — should fail because server takes 200ms.
	yaml := fmt.Sprintf(`
upstreams:
  - name: test
    transport: streamable-http
    url: %s
    timeout: 50ms
`, srv.URL)
	cfg, err := config.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("failed to create test config: %v", err)
	}
	upstream := cfg.Upstreams()[0].(*config.HTTPUpstreamConfig)
	tr, err := NewHTTPTransport(upstream)
	if err != nil {
		t.Fatalf("NewHTTPTransport: %v", err)
	}

	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "test/method",
		ID:      jsonrpc.IntID(1),
	}

	_, err = tr.RoundTrip(context.Background(), req)
	if err == nil {
		t.Fatal("expected timeout error with 50ms timeout")
	}
}

func TestSSEReaderOversizedLine(t *testing.T) {
	// Create a line larger than 1 MB — should produce an error, not a panic.
	bigLine := "data: " + strings.Repeat("x", 1<<20+100) + "\n\n"
	reader := NewSSEReader(strings.NewReader(bigLine))

	_, err := reader.Next()
	if err == nil {
		t.Fatal("expected error for oversized SSE line, got nil")
	}
}

func Test400DoesNotMarkUnhealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	upstream := newTestUpstream(t, srv.URL, false)
	tr, _ := NewHTTPTransport(upstream)

	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "test/method",
		ID:      jsonrpc.IntID(1),
	}

	// Send many 400s — should never mark unhealthy.
	for i := 0; i < 10; i++ {
		_, err := tr.RoundTrip(context.Background(), req)
		if err == nil {
			t.Fatal("expected error for 400 response")
		}
	}

	if !tr.Healthy() {
		t.Fatal("expected upstream to remain healthy after 400 responses")
	}
}

func TestHTTPTransportHealthTracking(t *testing.T) {
	requestCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		// Fail the first 3 requests, then succeed.
		if requestCount <= 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		resp, _ := jsonrpc.NewResponse(jsonrpc.IntID(1), "ok")
		data, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}))
	defer srv.Close()

	upstream := newTestUpstream(t, srv.URL, false)
	tr, _ := NewHTTPTransport(upstream)

	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "test/method",
		ID:      jsonrpc.IntID(1),
	}

	if !tr.Healthy() {
		t.Fatal("expected healthy initially")
	}

	// First two failures: still healthy (threshold is 3).
	tr.RoundTrip(context.Background(), req)
	tr.RoundTrip(context.Background(), req)
	if !tr.Healthy() {
		t.Fatal("expected healthy after 2 failures")
	}

	// Third failure: now unhealthy.
	tr.RoundTrip(context.Background(), req)
	if tr.Healthy() {
		t.Fatal("expected unhealthy after 3 consecutive failures")
	}

	// Success resets health.
	tr.RoundTrip(context.Background(), req)
	if !tr.Healthy() {
		t.Fatal("expected healthy after successful request")
	}
}
