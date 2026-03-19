package config

import (
	"os"
	"path/filepath"
	"testing"
)

const validConfig = `
server:
  address: ":9090"

upstreams:
  - name: http-upstream
    transport: streamable-http
    url: https://mcp.example.com/v1
    auth:
      type: bearer
      token_env: MCP_TOKEN
    tools:
      - search
      - summarize

  - name: stdio-upstream
    transport: stdio
    command: ["python", "-m", "mcp_server"]
    tools:
      - calculator
`

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadValidConfig(t *testing.T) {
	path := writeConfig(t, validConfig)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Server().Address() != ":9090" {
		t.Errorf("server address = %q, want %q", cfg.Server().Address(), ":9090")
	}

	upstreams := cfg.Upstreams()
	if len(upstreams) != 2 {
		t.Fatalf("got %d upstreams, want 2", len(upstreams))
	}

	// HTTP upstream.
	u := upstreams[0]
	if u.Name() != "http-upstream" {
		t.Errorf("name = %q, want %q", u.Name(), "http-upstream")
	}
	if u.Transport() != "streamable-http" {
		t.Errorf("transport = %q, want %q", u.Transport(), "streamable-http")
	}
	if u.URL() != "https://mcp.example.com/v1" {
		t.Errorf("url = %q, want %q", u.URL(), "https://mcp.example.com/v1")
	}
	if u.Auth() == nil {
		t.Fatal("auth is nil, want non-nil")
	}
	if u.Auth().Type() != "bearer" {
		t.Errorf("auth type = %q, want %q", u.Auth().Type(), "bearer")
	}
	if u.Auth().TokenEnv() != "MCP_TOKEN" {
		t.Errorf("token_env = %q, want %q", u.Auth().TokenEnv(), "MCP_TOKEN")
	}
	tools := u.Tools()
	if len(tools) != 2 || tools[0] != "search" || tools[1] != "summarize" {
		t.Errorf("tools = %v, want [search summarize]", tools)
	}

	// Stdio upstream.
	u = upstreams[1]
	if u.Name() != "stdio-upstream" {
		t.Errorf("name = %q, want %q", u.Name(), "stdio-upstream")
	}
	if u.Transport() != "stdio" {
		t.Errorf("transport = %q, want %q", u.Transport(), "stdio")
	}
	cmd := u.Command()
	if len(cmd) != 3 || cmd[0] != "python" {
		t.Errorf("command = %v, want [python -m mcp_server]", cmd)
	}
}

func TestValidConfigNoError(t *testing.T) {
	path := writeConfig(t, validConfig)
	_, err := Load(path)
	if err != nil {
		t.Fatalf("valid config should load without error, got: %v", err)
	}
}

func TestMissingName(t *testing.T) {
	yaml := `
upstreams:
  - transport: streamable-http
    url: https://example.com
`
	path := writeConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestHTTPWithoutURL(t *testing.T) {
	yaml := `
upstreams:
  - name: bad
    transport: streamable-http
`
	path := writeConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for HTTP upstream without URL")
	}
}

func TestStdioWithoutCommand(t *testing.T) {
	yaml := `
upstreams:
  - name: bad
    transport: stdio
`
	path := writeConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for stdio upstream without command")
	}
}

func TestBadTransport(t *testing.T) {
	yaml := `
upstreams:
  - name: bad
    transport: websocket
    url: ws://example.com
`
	path := writeConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for bad transport")
	}
}

func TestDuplicateNames(t *testing.T) {
	yaml := `
upstreams:
  - name: dup
    transport: streamable-http
    url: https://example.com
  - name: dup
    transport: streamable-http
    url: https://example2.com
`
	path := writeConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for duplicate names")
	}
}

func TestDefaultServerAddress(t *testing.T) {
	yaml := `
upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
`
	path := writeConfig(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server().Address() != ":8080" {
		t.Errorf("default address = %q, want %q", cfg.Server().Address(), ":8080")
	}
}

func TestSliceGettersReturnCopies(t *testing.T) {
	path := writeConfig(t, validConfig)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Mutating the returned slice should not affect the config.
	upstreams := cfg.Upstreams()
	upstreams[0] = UpstreamConfig{}
	if cfg.Upstreams()[0].Name() != "http-upstream" {
		t.Error("mutating Upstreams() return value affected internal state")
	}

	u := cfg.Upstreams()[0]
	tools := u.Tools()
	tools[0] = "mutated"
	if cfg.Upstreams()[0].Tools()[0] != "search" {
		t.Error("mutating Tools() return value affected internal state")
	}
}
