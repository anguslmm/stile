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
	h, ok := upstreams[0].(*HTTPUpstreamConfig)
	if !ok {
		t.Fatalf("expected *HTTPUpstreamConfig, got %T", upstreams[0])
	}
	if h.Name() != "http-upstream" {
		t.Errorf("name = %q, want %q", h.Name(), "http-upstream")
	}
	if h.URL() != "https://mcp.example.com/v1" {
		t.Errorf("url = %q, want %q", h.URL(), "https://mcp.example.com/v1")
	}
	if h.Auth() == nil {
		t.Fatal("auth is nil, want non-nil")
	}
	if h.Auth().Type() != "bearer" {
		t.Errorf("auth type = %q, want %q", h.Auth().Type(), "bearer")
	}
	if h.Auth().TokenEnv() != "MCP_TOKEN" {
		t.Errorf("token_env = %q, want %q", h.Auth().TokenEnv(), "MCP_TOKEN")
	}
	tools := h.Tools()
	if len(tools) != 2 || tools[0] != "search" || tools[1] != "summarize" {
		t.Errorf("tools = %v, want [search summarize]", tools)
	}

	// Stdio upstream.
	s, ok := upstreams[1].(*StdioUpstreamConfig)
	if !ok {
		t.Fatalf("expected *StdioUpstreamConfig, got %T", upstreams[1])
	}
	if s.Name() != "stdio-upstream" {
		t.Errorf("name = %q, want %q", s.Name(), "stdio-upstream")
	}
	cmd := s.Command()
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
	upstreams[0] = nil
	if cfg.Upstreams()[0].Name() != "http-upstream" {
		t.Error("mutating Upstreams() return value affected internal state")
	}

	tools := cfg.Upstreams()[0].Tools()
	tools[0] = "mutated"
	if cfg.Upstreams()[0].Tools()[0] != "search" {
		t.Error("mutating Tools() return value affected internal state")
	}
}

func TestRoleConfigLoads(t *testing.T) {
	yaml := `
upstreams:
  - name: github
    transport: streamable-http
    url: https://mcp.github.com
  - name: notion
    transport: streamable-http
    url: https://mcp.notion.com

roles:
  web-tools:
    allowed_tools:
      - "github/*"
      - "notion/*"
    credentials:
      github: GITHUB_DEV_TOKEN
      notion: NOTION_DEV_TOKEN
  database:
    allowed_tools:
      - "db_*"
`
	cfg, err := LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	roles := cfg.Roles()
	if len(roles) != 2 {
		t.Fatalf("expected 2 roles, got %d", len(roles))
	}

	// Find the web-tools role.
	var webTools *RoleConfig
	for i := range roles {
		if roles[i].Name() == "web-tools" {
			webTools = &roles[i]
		}
	}
	if webTools == nil {
		t.Fatal("missing web-tools role")
	}
	tools := webTools.AllowedTools()
	if len(tools) != 2 || tools[0] != "github/*" || tools[1] != "notion/*" {
		t.Errorf("web-tools allowed_tools = %v, want [github/* notion/*]", tools)
	}
	creds := webTools.Credentials()
	if creds["github"] != "GITHUB_DEV_TOKEN" {
		t.Errorf("web-tools github credential = %q, want GITHUB_DEV_TOKEN", creds["github"])
	}
	if creds["notion"] != "NOTION_DEV_TOKEN" {
		t.Errorf("web-tools notion credential = %q, want NOTION_DEV_TOKEN", creds["notion"])
	}
}

func TestRoleMissingAllowedTools(t *testing.T) {
	yaml := `
upstreams:
  - name: github
    transport: streamable-http
    url: https://mcp.github.com

roles:
  empty-role:
    credentials:
      github: GITHUB_TOKEN
`
	_, err := LoadBytes([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for role missing allowed_tools")
	}
}

func TestRoleInvalidGlobPattern(t *testing.T) {
	yaml := `
upstreams:
  - name: github
    transport: streamable-http
    url: https://mcp.github.com

roles:
  bad-glob:
    allowed_tools:
      - "[invalid"
    credentials:
      github: GITHUB_TOKEN
`
	_, err := LoadBytes([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for invalid glob pattern in role")
	}
}

func TestRoleUnknownUpstream(t *testing.T) {
	yaml := `
upstreams:
  - name: github
    transport: streamable-http
    url: https://mcp.github.com

roles:
  bad-ref:
    allowed_tools:
      - "*"
    credentials:
      nonexistent: SOME_TOKEN
`
	_, err := LoadBytes([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for role referencing unknown upstream")
	}
}

func TestRoleEmptyEnvVar(t *testing.T) {
	yaml := `
upstreams:
  - name: github
    transport: streamable-http
    url: https://mcp.github.com

roles:
  bad-env:
    allowed_tools:
      - "*"
    credentials:
      github: ""
`
	_, err := LoadBytes([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for empty env var in role")
	}
}

func TestParseRateLimit(t *testing.T) {
	tests := []struct {
		input    string
		wantRate float64
		wantErr  bool
	}{
		{"100/min", 100.0 / 60, false},
		{"10/sec", 10.0, false},
		{"5000/hour", 5000.0 / 3600, false},
		{"1/second", 1.0, false},
		{"60/minute", 1.0, false},
		{"invalid", 0, true},
		{"abc/min", 0, true},
		{"100/days", 0, true},
		{"-5/sec", 0, true},
		{"0/sec", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			rl, err := ParseRateLimit(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseRateLimit(%q) should fail", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseRateLimit(%q) unexpected error: %v", tt.input, err)
			}
			// Allow small floating point tolerance.
			diff := rl.Rate() - tt.wantRate
			if diff < -0.01 || diff > 0.01 {
				t.Errorf("ParseRateLimit(%q).Rate() = %f, want ~%f", tt.input, rl.Rate(), tt.wantRate)
			}
			if rl.Burst() < 1 {
				t.Errorf("ParseRateLimit(%q).Burst() = %d, want >= 1", tt.input, rl.Burst())
			}
		})
	}
}

func TestRateLimitConfigLoads(t *testing.T) {
	yaml := `
upstreams:
  - name: github
    transport: streamable-http
    url: https://mcp.github.com
    rate_limit: 200/min

roles:
  dev:
    allowed_tools: ["*"]
    rate_limit: 100/min
    tool_rate_limit: 20/min

rate_limits:
  default_caller: 60/min
  default_tool: 30/min
  default_upstream: 300/min
`
	cfg, err := LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check upstream rate limit.
	u := cfg.Upstreams()[0]
	if u.RateLimit() == nil {
		t.Fatal("upstream rate limit should be set")
	}
	// 200/min = 3.33/sec
	if r := u.RateLimit().Rate(); r < 3.3 || r > 3.4 {
		t.Errorf("upstream rate = %f, want ~3.33", r)
	}

	// Check role rate limits.
	roles := cfg.Roles()
	if len(roles) != 1 {
		t.Fatalf("expected 1 role, got %d", len(roles))
	}
	if roles[0].RateLimit() == nil {
		t.Fatal("role rate limit should be set")
	}
	if roles[0].ToolRateLimit() == nil {
		t.Fatal("role tool rate limit should be set")
	}

	// Check defaults.
	defaults := cfg.RateLimitDefaults()
	if defaults.DefaultCaller() == nil {
		t.Fatal("default caller rate limit should be set")
	}
	if defaults.DefaultTool() == nil {
		t.Fatal("default tool rate limit should be set")
	}
	if defaults.DefaultUpstream() == nil {
		t.Fatal("default upstream rate limit should be set")
	}
}

func TestInvalidRateLimitInConfig(t *testing.T) {
	yaml := `
upstreams:
  - name: svc
    transport: streamable-http
    url: http://example.com
    rate_limit: invalid
`
	_, err := LoadBytes([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for invalid rate limit")
	}
}

func TestDBPathLoads(t *testing.T) {
	yaml := `
server:
  db_path: /data/stile.db

upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
`
	cfg, err := LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server().DBPath() != "/data/stile.db" {
		t.Errorf("db_path = %q, want /data/stile.db", cfg.Server().DBPath())
	}
}

func TestLoggingConfigDefaults(t *testing.T) {
	yaml := `
upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
`
	cfg, err := LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Logging().Level() != "info" {
		t.Errorf("logging level = %q, want info", cfg.Logging().Level())
	}
	if cfg.Logging().Format() != "json" {
		t.Errorf("logging format = %q, want json", cfg.Logging().Format())
	}
}

func TestLoggingConfigExplicit(t *testing.T) {
	yaml := `
logging:
  level: debug
  format: text

upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
`
	cfg, err := LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Logging().Level() != "debug" {
		t.Errorf("logging level = %q, want debug", cfg.Logging().Level())
	}
	if cfg.Logging().Format() != "text" {
		t.Errorf("logging format = %q, want text", cfg.Logging().Format())
	}
}

func TestLoggingInvalidLevel(t *testing.T) {
	yaml := `
logging:
  level: trace

upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
`
	_, err := LoadBytes([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for invalid logging level")
	}
}

func TestLoggingInvalidFormat(t *testing.T) {
	yaml := `
logging:
  format: xml

upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
`
	_, err := LoadBytes([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for invalid logging format")
	}
}

func TestAuditConfigLoads(t *testing.T) {
	yaml := `
audit:
  enabled: true
  database: /var/lib/stile/audit.db

upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
`
	cfg, err := LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Audit().Enabled() {
		t.Error("audit.enabled should be true")
	}
	if cfg.Audit().Database() != "/var/lib/stile/audit.db" {
		t.Errorf("audit.database = %q, want /var/lib/stile/audit.db", cfg.Audit().Database())
	}
}

func TestAuditDisabledByDefault(t *testing.T) {
	yaml := `
upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
`
	cfg, err := LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Audit().Enabled() {
		t.Error("audit should be disabled by default")
	}
}

func TestAuditEnabledWithoutDatabase(t *testing.T) {
	yaml := `
audit:
  enabled: true

upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
`
	_, err := LoadBytes([]byte(yaml))
	if err == nil {
		t.Fatal("expected error when audit is enabled without database")
	}
}

func TestShutdownTimeoutDefault(t *testing.T) {
	yaml := `
upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
`
	cfg, err := LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server().ShutdownTimeout().Seconds() != 30 {
		t.Errorf("default shutdown_timeout = %v, want 30s", cfg.Server().ShutdownTimeout())
	}
}

func TestShutdownTimeoutExplicit(t *testing.T) {
	yaml := `
server:
  shutdown_timeout: 10s

upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
`
	cfg, err := LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server().ShutdownTimeout().Seconds() != 10 {
		t.Errorf("shutdown_timeout = %v, want 10s", cfg.Server().ShutdownTimeout())
	}
}

func TestShutdownTimeoutInvalid(t *testing.T) {
	yaml := `
server:
  shutdown_timeout: banana

upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
`
	_, err := LoadBytes([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for invalid shutdown_timeout")
	}
}
