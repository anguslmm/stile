package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"

	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/jsonrpc"
)

// mockServerBinary is set by TestMain so the go build happens before any
// parallel test execution (avoids build-cache lock contention with go test ./...).
var mockServerBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "stile-mock-server")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)

	binary := dir + "/mock_stdio_server"
	cmd := exec.Command("go", "build", "-o", binary, "./testdata/mock_stdio_server.go")
	cmd.Dir = "."
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "build mock stdio server: %v\n", err)
		os.Exit(1)
	}
	mockServerBinary = binary
	os.Exit(m.Run())
}

// buildMockServer returns the path to the pre-built mock stdio server binary.
func buildMockServer(t *testing.T) string {
	t.Helper()
	if mockServerBinary == "" {
		t.Fatal("mock server binary not built (TestMain failed?)")
	}
	return mockServerBinary
}

func newStdioUpstream(t *testing.T, binary string) config.UpstreamConfig {
	t.Helper()
	yaml := `
upstreams:
  - name: stdio-test
    transport: stdio
    command: ["` + binary + `"]
`
	cfg, err := config.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("failed to create test config: %v", err)
	}
	return cfg.Upstreams()[0]
}

func TestStdioToolsList(t *testing.T) {
	binary := buildMockServer(t)
	ucfg := newStdioUpstream(t, binary)

	tr, err := NewStdioTransport(ucfg)
	if err != nil {
		t.Fatalf("NewStdioTransport: %v", err)
	}
	defer tr.Close()

	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "tools/list",
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

	resp := jr.Response()
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	var toolsResult struct {
		Tools []ToolSchema `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &toolsResult); err != nil {
		t.Fatalf("unmarshal tools: %v", err)
	}

	if len(toolsResult.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(toolsResult.Tools))
	}
	if toolsResult.Tools[0].Name != "test_echo" {
		t.Errorf("expected tool name 'test_echo', got %q", toolsResult.Tools[0].Name)
	}
}

func TestStdioToolsCall(t *testing.T) {
	binary := buildMockServer(t)
	ucfg := newStdioUpstream(t, binary)

	tr, err := NewStdioTransport(ucfg)
	if err != nil {
		t.Fatalf("NewStdioTransport: %v", err)
	}
	defer tr.Close()

	params, _ := json.Marshal(map[string]interface{}{
		"name":      "test_echo",
		"arguments": map[string]string{"message": "hello"},
	})

	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "tools/call",
		Params:  params,
		ID:      jsonrpc.IntID(2),
	}

	result, err := tr.RoundTrip(context.Background(), req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}

	jr, ok := result.(*JSONResult)
	if !ok {
		t.Fatalf("expected *JSONResult, got %T", result)
	}

	resp := jr.Response()
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	if resp.Result == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestStdioResultIsNonStreaming(t *testing.T) {
	binary := buildMockServer(t)
	ucfg := newStdioUpstream(t, binary)

	tr, err := NewStdioTransport(ucfg)
	if err != nil {
		t.Fatalf("NewStdioTransport: %v", err)
	}
	defer tr.Close()

	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "ping",
		ID:      jsonrpc.IntID(1),
	}

	result, err := tr.RoundTrip(context.Background(), req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}

	if _, ok := result.(*JSONResult); !ok {
		t.Fatalf("expected *JSONResult (non-streaming), got %T", result)
	}
}

func TestStdioProcessCrashRecovery(t *testing.T) {
	binary := buildMockServer(t)
	ucfg := newStdioUpstream(t, binary)

	tr, err := NewStdioTransport(ucfg)
	if err != nil {
		t.Fatalf("NewStdioTransport: %v", err)
	}
	defer tr.Close()

	// Send a request to start the process.
	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "ping",
		ID:      jsonrpc.IntID(1),
	}
	if _, err := tr.RoundTrip(context.Background(), req); err != nil {
		t.Fatalf("first RoundTrip: %v", err)
	}

	// Kill the process.
	tr.mu.Lock()
	if tr.cmd != nil && tr.cmd.Process != nil {
		tr.cmd.Process.Kill()
		tr.cmd.Wait()
	}
	tr.mu.Unlock()

	// Next request should trigger a restart and succeed.
	req.ID = jsonrpc.IntID(2)
	result, err := tr.RoundTrip(context.Background(), req)
	if err != nil {
		t.Fatalf("RoundTrip after crash: %v", err)
	}

	jr, ok := result.(*JSONResult)
	if !ok {
		t.Fatalf("expected *JSONResult, got %T", result)
	}
	if jr.Response().Error != nil {
		t.Errorf("unexpected error after restart: %v", jr.Response().Error)
	}
}

func TestStdioClose(t *testing.T) {
	binary := buildMockServer(t)
	ucfg := newStdioUpstream(t, binary)

	tr, err := NewStdioTransport(ucfg)
	if err != nil {
		t.Fatalf("NewStdioTransport: %v", err)
	}

	// Start the process.
	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "ping",
		ID:      jsonrpc.IntID(1),
	}
	if _, err := tr.RoundTrip(context.Background(), req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}

	// Verify process is running.
	if !tr.Healthy() {
		t.Fatal("expected process to be healthy before Close")
	}

	// Close should terminate the process.
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if tr.Healthy() {
		t.Error("expected process to be unhealthy after Close")
	}

	// Further requests should fail.
	_, err = tr.RoundTrip(context.Background(), req)
	if err == nil {
		t.Error("expected error after Close")
	}
}

func TestStdioConcurrentRequests(t *testing.T) {
	binary := buildMockServer(t)
	ucfg := newStdioUpstream(t, binary)

	tr, err := NewStdioTransport(ucfg)
	if err != nil {
		t.Fatalf("NewStdioTransport: %v", err)
	}
	defer tr.Close()

	const n = 10
	var wg sync.WaitGroup
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			params, _ := json.Marshal(map[string]interface{}{
				"name":      "test_echo",
				"arguments": map[string]string{"id": "request"},
			})

			req := &jsonrpc.Request{
				JSONRPC: jsonrpc.Version,
				Method:  "tools/call",
				Params:  params,
				ID:      jsonrpc.IntID(int64(id)),
			}

			result, err := tr.RoundTrip(context.Background(), req)
			if err != nil {
				errs <- err
				return
			}

			jr, ok := result.(*JSONResult)
			if !ok {
				errs <- fmt.Errorf("request %d: expected *JSONResult, got %T", id, result)
				return
			}
			if jr.Response().Error != nil {
				errs <- fmt.Errorf("request %d: unexpected error: %v", id, jr.Response().Error)
			}
		}(i)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}
}

func TestStdioEmptyCommandFails(t *testing.T) {
	yaml := `
upstreams:
  - name: bad
    transport: stdio
    command: ["/nonexistent/binary"]
`
	cfg, err := config.LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("failed to create config: %v", err)
	}

	// NewStdioTransport should succeed (process not started yet).
	tr, err := NewStdioTransport(cfg.Upstreams()[0])
	if err != nil {
		t.Fatalf("NewStdioTransport: %v", err)
	}
	defer tr.Close()

	// But RoundTrip should fail since the binary doesn't exist.
	req := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "ping",
		ID:      jsonrpc.IntID(1),
	}
	_, err = tr.RoundTrip(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for nonexistent binary")
	}
}
