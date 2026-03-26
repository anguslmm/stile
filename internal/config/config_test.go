package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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
	// Backwards compat: db_path should populate Database() as sqlite.
	if cfg.Server().Database().Driver() != "sqlite" {
		t.Errorf("database.driver = %q, want sqlite", cfg.Server().Database().Driver())
	}
	if cfg.Server().Database().DSN() != "/data/stile.db" {
		t.Errorf("database.dsn = %q, want /data/stile.db", cfg.Server().Database().DSN())
	}
}

func TestDatabaseConfigExplicit(t *testing.T) {
	yaml := `
server:
  database:
    driver: postgres
    dsn: "postgres://localhost/stile"

upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
`
	cfg, err := LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server().Database().Driver() != "postgres" {
		t.Errorf("database.driver = %q, want postgres", cfg.Server().Database().Driver())
	}
	if cfg.Server().Database().DSN() != "postgres://localhost/stile" {
		t.Errorf("database.dsn = %q, want postgres://localhost/stile", cfg.Server().Database().DSN())
	}
}

func TestDatabaseConfigDefaultDriver(t *testing.T) {
	yaml := `
server:
  database:
    dsn: stile.db

upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
`
	cfg, err := LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server().Database().Driver() != "sqlite" {
		t.Errorf("database.driver = %q, want sqlite (default)", cfg.Server().Database().Driver())
	}
}

func TestDatabaseConfigRejectsUnknownDriver(t *testing.T) {
	yaml := `
server:
  database:
    driver: mysql
    dsn: "root@tcp(localhost)/stile"

upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
`
	_, err := LoadBytes([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for unknown database driver")
	}
}

func TestDatabaseConfigRejectsBothDBPathAndDatabase(t *testing.T) {
	yaml := `
server:
  db_path: stile.db
  database:
    driver: sqlite
    dsn: stile.db

upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
`
	_, err := LoadBytes([]byte(yaml))
	if err == nil {
		t.Fatal("expected error when both db_path and database are set")
	}
}

func TestDatabaseConfigRequiresDSN(t *testing.T) {
	yaml := `
server:
  database:
    driver: postgres

upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
`
	_, err := LoadBytes([]byte(yaml))
	if err == nil {
		t.Fatal("expected error when database.dsn is empty")
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

func TestTelemetryConfigDefaults(t *testing.T) {
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
	if cfg.Telemetry().Traces().Enabled() {
		t.Error("tracing should be disabled by default")
	}
}

func TestTelemetryConfigExplicit(t *testing.T) {
	yaml := `
telemetry:
  traces:
    enabled: true
    endpoint: "tempo:4318"
    sample_rate: 0.5

upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
`
	cfg, err := LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Telemetry().Traces().Enabled() {
		t.Error("tracing should be enabled")
	}
	if cfg.Telemetry().Traces().Endpoint() != "tempo:4318" {
		t.Errorf("endpoint = %q, want tempo:4318", cfg.Telemetry().Traces().Endpoint())
	}
	if cfg.Telemetry().Traces().SampleRate() != 0.5 {
		t.Errorf("sample_rate = %f, want 0.5", cfg.Telemetry().Traces().SampleRate())
	}
}

func TestTelemetryInvalidSampleRate(t *testing.T) {
	yaml := `
telemetry:
  traces:
    sample_rate: 1.5

upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
`
	_, err := LoadBytes([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for sample_rate > 1.0")
	}
}

func TestTelemetryDefaultEndpoint(t *testing.T) {
	yaml := `
telemetry:
  traces:
    enabled: true
    sample_rate: 1.0

upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
`
	cfg, err := LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Telemetry().Traces().Endpoint() != "localhost:4318" {
		t.Errorf("default endpoint = %q, want localhost:4318", cfg.Telemetry().Traces().Endpoint())
	}
}

func TestTimeoutDefault(t *testing.T) {
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
	u := cfg.Upstreams()[0]
	if u.Timeout().Seconds() != 60 {
		t.Errorf("default timeout = %v, want 60s", u.Timeout())
	}
}

func TestTimeoutExplicit(t *testing.T) {
	yaml := `
upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
    timeout: 5s
`
	cfg, err := LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	u := cfg.Upstreams()[0]
	if u.Timeout().Seconds() != 5 {
		t.Errorf("timeout = %v, want 5s", u.Timeout())
	}
}

func TestTimeoutInvalid(t *testing.T) {
	yaml := `
upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
    timeout: banana
`
	_, err := LoadBytes([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for invalid timeout")
	}
}

func TestCircuitBreakerConfig(t *testing.T) {
	yaml := `
upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
    circuit_breaker:
      failure_threshold: 10
      cooldown: 1m
`
	cfg, err := LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cb := cfg.Upstreams()[0].CircuitBreaker()
	if cb == nil {
		t.Fatal("expected circuit_breaker config")
	}
	if cb.FailureThreshold() != 10 {
		t.Errorf("failure_threshold = %d, want 10", cb.FailureThreshold())
	}
	if cb.Cooldown().Seconds() != 60 {
		t.Errorf("cooldown = %v, want 1m", cb.Cooldown())
	}
}

func TestCircuitBreakerDefaults(t *testing.T) {
	yaml := `
upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
    circuit_breaker: {}
`
	cfg, err := LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cb := cfg.Upstreams()[0].CircuitBreaker()
	if cb == nil {
		t.Fatal("expected circuit_breaker config")
	}
	if cb.FailureThreshold() != 5 {
		t.Errorf("default failure_threshold = %d, want 5", cb.FailureThreshold())
	}
	if cb.Cooldown().Seconds() != 30 {
		t.Errorf("default cooldown = %v, want 30s", cb.Cooldown())
	}
}

func TestCircuitBreakerNotConfigured(t *testing.T) {
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
	if cfg.Upstreams()[0].CircuitBreaker() != nil {
		t.Error("expected nil circuit_breaker when not configured")
	}
}

func TestRetryConfig(t *testing.T) {
	yaml := `
upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
    retry:
      max_attempts: 3
      backoff: 200ms
      max_backoff: 5s
      retryable_errors:
        - connection_error
        - "502"
        - "503"
`
	cfg, err := LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	r := cfg.Upstreams()[0].Retry()
	if r == nil {
		t.Fatal("expected retry config")
	}
	if r.MaxAttempts() != 3 {
		t.Errorf("max_attempts = %d, want 3", r.MaxAttempts())
	}
	if r.Backoff() != 200*time.Millisecond {
		t.Errorf("backoff = %v, want 200ms", r.Backoff())
	}
	if r.MaxBackoff() != 5*time.Second {
		t.Errorf("max_backoff = %v, want 5s", r.MaxBackoff())
	}
	errs := r.RetryableErrors()
	if len(errs) != 3 || errs[0] != "connection_error" || errs[1] != "502" || errs[2] != "503" {
		t.Errorf("retryable_errors = %v, want [connection_error 502 503]", errs)
	}
}

func TestRetryNotConfigured(t *testing.T) {
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
	if cfg.Upstreams()[0].Retry() != nil {
		t.Error("expected nil retry when not configured")
	}
}

func TestRetryDefaults(t *testing.T) {
	yaml := `
upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
    retry: {}
`
	cfg, err := LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	r := cfg.Upstreams()[0].Retry()
	if r == nil {
		t.Fatal("expected retry config")
	}
	if r.MaxAttempts() != 1 {
		t.Errorf("default max_attempts = %d, want 1", r.MaxAttempts())
	}
	if r.Backoff() != 100*time.Millisecond {
		t.Errorf("default backoff = %v, want 100ms", r.Backoff())
	}
	if r.MaxBackoff() != 2*time.Second {
		t.Errorf("default max_backoff = %v, want 2s", r.MaxBackoff())
	}
	errs := r.RetryableErrors()
	if len(errs) != 1 || errs[0] != "connection_error" {
		t.Errorf("default retryable_errors = %v, want [connection_error]", errs)
	}
}

func TestStdioUpstreamWithResilience(t *testing.T) {
	yaml := `
upstreams:
  - name: stdio-svc
    transport: stdio
    command: ["python", "server.py"]
    timeout: 300s
    circuit_breaker:
      failure_threshold: 3
    retry:
      max_attempts: 2
`
	cfg, err := LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	u := cfg.Upstreams()[0]
	if u.Timeout() != 300*time.Second {
		t.Errorf("timeout = %v, want 300s", u.Timeout())
	}
	if u.CircuitBreaker() == nil {
		t.Fatal("expected circuit_breaker")
	}
	if u.Retry() == nil {
		t.Fatal("expected retry")
	}
}

func TestServerTLSConfigLoads(t *testing.T) {
	yaml := `
server:
  address: ":8443"
  tls:
    cert_file: /path/to/cert.pem
    key_file: /path/to/key.pem
    min_version: "1.3"
    client_ca_file: /path/to/ca.pem

upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
`
	cfg, err := LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tls := cfg.Server().TLS()
	if tls == nil {
		t.Fatal("expected server TLS config")
	}
	if tls.CertFile() != "/path/to/cert.pem" {
		t.Errorf("cert_file = %q, want /path/to/cert.pem", tls.CertFile())
	}
	if tls.KeyFile() != "/path/to/key.pem" {
		t.Errorf("key_file = %q, want /path/to/key.pem", tls.KeyFile())
	}
	if tls.MinVersion() != "1.3" {
		t.Errorf("min_version = %q, want 1.3", tls.MinVersion())
	}
	if tls.ClientCAFile() != "/path/to/ca.pem" {
		t.Errorf("client_ca_file = %q, want /path/to/ca.pem", tls.ClientCAFile())
	}
}

func TestServerTLSDefaultMinVersion(t *testing.T) {
	yaml := `
server:
  tls:
    cert_file: /cert.pem
    key_file: /key.pem

upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
`
	cfg, err := LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server().TLS().MinVersion() != "1.2" {
		t.Errorf("default min_version = %q, want 1.2", cfg.Server().TLS().MinVersion())
	}
}

func TestServerTLSNilWhenNotConfigured(t *testing.T) {
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
	if cfg.Server().TLS() != nil {
		t.Error("expected nil TLS when not configured")
	}
}

func TestServerTLSMissingCertFile(t *testing.T) {
	yaml := `
server:
  tls:
    key_file: /key.pem

upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
`
	_, err := LoadBytes([]byte(yaml))
	if err == nil {
		t.Fatal("expected error when cert_file is missing")
	}
}

func TestServerTLSMissingKeyFile(t *testing.T) {
	yaml := `
server:
  tls:
    cert_file: /cert.pem

upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
`
	_, err := LoadBytes([]byte(yaml))
	if err == nil {
		t.Fatal("expected error when key_file is missing")
	}
}

func TestServerTLSInvalidMinVersion(t *testing.T) {
	yaml := `
server:
  tls:
    cert_file: /cert.pem
    key_file: /key.pem
    min_version: "2.0"

upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
`
	_, err := LoadBytes([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for invalid min_version")
	}
}

func TestUpstreamTLSConfigLoads(t *testing.T) {
	yaml := `
upstreams:
  - name: secure-tools
    transport: streamable-http
    url: https://tools.internal:8443/mcp
    tls:
      ca_file: /path/to/ca.pem
      cert_file: /path/to/client-cert.pem
      key_file: /path/to/client-key.pem
      insecure_skip_verify: true
`
	cfg, err := LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	h := cfg.Upstreams()[0].(*HTTPUpstreamConfig)
	tls := h.TLS()
	if tls == nil {
		t.Fatal("expected upstream TLS config")
	}
	if tls.CAFile() != "/path/to/ca.pem" {
		t.Errorf("ca_file = %q, want /path/to/ca.pem", tls.CAFile())
	}
	if tls.CertFile() != "/path/to/client-cert.pem" {
		t.Errorf("cert_file = %q, want /path/to/client-cert.pem", tls.CertFile())
	}
	if tls.KeyFile() != "/path/to/client-key.pem" {
		t.Errorf("key_file = %q, want /path/to/client-key.pem", tls.KeyFile())
	}
	if !tls.InsecureSkipVerify() {
		t.Error("insecure_skip_verify should be true")
	}
}

func TestUpstreamTLSCertWithoutKey(t *testing.T) {
	yaml := `
upstreams:
  - name: bad
    transport: streamable-http
    url: https://example.com
    tls:
      cert_file: /cert.pem
`
	_, err := LoadBytes([]byte(yaml))
	if err == nil {
		t.Fatal("expected error when cert_file is set without key_file")
	}
}

func TestUpstreamTLSKeyWithoutCert(t *testing.T) {
	yaml := `
upstreams:
  - name: bad
    transport: streamable-http
    url: https://example.com
    tls:
      key_file: /key.pem
`
	_, err := LoadBytes([]byte(yaml))
	if err == nil {
		t.Fatal("expected error when key_file is set without cert_file")
	}
}

func TestUpstreamTLSOnStdioRejected(t *testing.T) {
	yaml := `
upstreams:
  - name: bad
    transport: stdio
    command: ["python", "server.py"]
    tls:
      insecure_skip_verify: true
`
	_, err := LoadBytes([]byte(yaml))
	if err == nil {
		t.Fatal("expected error when TLS is set on stdio transport")
	}
}

func TestUpstreamTLSNilWhenNotConfigured(t *testing.T) {
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
	h := cfg.Upstreams()[0].(*HTTPUpstreamConfig)
	if h.TLS() != nil {
		t.Error("expected nil TLS when not configured")
	}
}

func TestToolPrefixExplicit(t *testing.T) {
	yaml := `
upstreams:
  - name: github
    transport: streamable-http
    url: https://github-mcp.example.com
    tool_prefix: "gh"
`
	cfg, err := LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	u := cfg.Upstreams()[0]
	if u.ToolPrefix() == nil {
		t.Fatal("expected non-nil ToolPrefix")
	}
	if *u.ToolPrefix() != "gh" {
		t.Errorf("expected ToolPrefix=gh, got %q", *u.ToolPrefix())
	}
}

func TestToolPrefixEmptyDisablesPrefixing(t *testing.T) {
	yaml := `
upstreams:
  - name: legacy
    transport: streamable-http
    url: https://legacy.example.com
    tool_prefix: ""
`
	cfg, err := LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	u := cfg.Upstreams()[0]
	if u.ToolPrefix() == nil {
		t.Fatal("expected non-nil ToolPrefix for explicit empty string")
	}
	if *u.ToolPrefix() != "" {
		t.Errorf("expected empty ToolPrefix, got %q", *u.ToolPrefix())
	}
}

func TestToolPrefixNilDefault(t *testing.T) {
	yaml := `
upstreams:
  - name: github
    transport: streamable-http
    url: https://github-mcp.example.com
`
	cfg, err := LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	u := cfg.Upstreams()[0]
	if u.ToolPrefix() != nil {
		t.Errorf("expected nil ToolPrefix when not set, got %q", *u.ToolPrefix())
	}
}

func TestToolPrefixInvalidCharsRejected(t *testing.T) {
	yaml := `
upstreams:
  - name: github
    transport: streamable-http
    url: https://github-mcp.example.com
    tool_prefix: "my-prefix"
`
	_, err := LoadBytes([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for tool_prefix with hyphens")
	}
}

func TestOAuthProviderConfigLoads(t *testing.T) {
	yaml := `
oauth_providers:
  github:
    auth_url: https://github.com/login/oauth/authorize
    token_url: https://github.com/login/oauth/access_token
    client_id_env: GH_CLIENT_ID
    client_secret_env: GH_CLIENT_SECRET
    scopes: ["repo", "read:org"]

upstreams:
  - name: github-mcp
    transport: streamable-http
    url: https://github-mcp.example.com
    auth:
      type: oauth
      provider: github
`
	cfg, err := LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	providers := cfg.OAuthProviders()
	if len(providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(providers))
	}
	p := providers[0]
	if p.Name() != "github" {
		t.Errorf("provider name = %q, want github", p.Name())
	}
	if p.AuthURL() != "https://github.com/login/oauth/authorize" {
		t.Errorf("auth_url = %q", p.AuthURL())
	}
	if p.TokenURL() != "https://github.com/login/oauth/access_token" {
		t.Errorf("token_url = %q", p.TokenURL())
	}
	if p.ClientIDEnv() != "GH_CLIENT_ID" {
		t.Errorf("client_id_env = %q", p.ClientIDEnv())
	}
	if p.ClientSecretEnv() != "GH_CLIENT_SECRET" {
		t.Errorf("client_secret_env = %q", p.ClientSecretEnv())
	}
	scopes := p.Scopes()
	if len(scopes) != 2 || scopes[0] != "repo" || scopes[1] != "read:org" {
		t.Errorf("scopes = %v", scopes)
	}

	// Upstream auth should reference the provider.
	h := cfg.Upstreams()[0].(*HTTPUpstreamConfig)
	if h.Auth() == nil {
		t.Fatal("expected auth config")
	}
	if h.Auth().Type() != "oauth" {
		t.Errorf("auth.type = %q, want oauth", h.Auth().Type())
	}
	if h.Auth().Provider() != "github" {
		t.Errorf("auth.provider = %q, want github", h.Auth().Provider())
	}

	// OAuthProvider() lookup.
	found := cfg.OAuthProvider("github")
	if found == nil {
		t.Fatal("OAuthProvider(github) returned nil")
	}
	if found.Name() != "github" {
		t.Errorf("found provider name = %q", found.Name())
	}
	if cfg.OAuthProvider("nonexistent") != nil {
		t.Error("expected nil for nonexistent provider")
	}
}

func TestOAuthProviderMissingFields(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{
			name: "missing auth_url",
			yaml: `
oauth_providers:
  github:
    token_url: https://example.com/token
    client_id_env: X
    client_secret_env: Y
upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
`,
		},
		{
			name: "missing token_url",
			yaml: `
oauth_providers:
  github:
    auth_url: https://example.com/auth
    client_id_env: X
    client_secret_env: Y
upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
`,
		},
		{
			name: "missing client_id_env",
			yaml: `
oauth_providers:
  github:
    auth_url: https://example.com/auth
    token_url: https://example.com/token
    client_secret_env: Y
upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
`,
		},
		{
			name: "missing client_secret_env",
			yaml: `
oauth_providers:
  github:
    auth_url: https://example.com/auth
    token_url: https://example.com/token
    client_id_env: X
upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadBytes([]byte(tt.yaml))
			if err == nil {
				t.Fatal("expected error for missing field")
			}
		})
	}
}

func TestOAuthUpstreamMissingProvider(t *testing.T) {
	yaml := `
oauth_providers:
  github:
    auth_url: https://example.com/auth
    token_url: https://example.com/token
    client_id_env: X
    client_secret_env: Y

upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
    auth:
      type: oauth
`
	_, err := LoadBytes([]byte(yaml))
	if err == nil {
		t.Fatal("expected error when auth.type=oauth but provider is missing")
	}
}

func TestOAuthUpstreamUnknownProvider(t *testing.T) {
	yaml := `
oauth_providers:
  github:
    auth_url: https://example.com/auth
    token_url: https://example.com/token
    client_id_env: X
    client_secret_env: Y

upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
    auth:
      type: oauth
      provider: nonexistent
`
	_, err := LoadBytes([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for unknown provider reference")
	}
}

func TestOAuthUpstreamRejectsRoleCredentials(t *testing.T) {
	yaml := `
oauth_providers:
  github:
    auth_url: https://example.com/auth
    token_url: https://example.com/token
    client_id_env: X
    client_secret_env: Y

upstreams:
  - name: github-mcp
    transport: streamable-http
    url: https://example.com
    auth:
      type: oauth
      provider: github

roles:
  developer:
    allowed_tools: ["*"]
    credentials:
      github-mcp: GH_TOKEN
`
	_, err := LoadBytes([]byte(yaml))
	if err == nil {
		t.Fatal("expected error when role has credentials for OAuth upstream")
	}
}

func TestOAuthUpstreamInvalidAuthType(t *testing.T) {
	yaml := `
upstreams:
  - name: svc
    transport: streamable-http
    url: https://example.com
    auth:
      type: basic
`
	_, err := LoadBytes([]byte(yaml))
	if err == nil {
		t.Fatal("expected error for invalid auth type")
	}
}

func TestBearerUpstreamStillWorks(t *testing.T) {
	yaml := `
oauth_providers:
  github:
    auth_url: https://example.com/auth
    token_url: https://example.com/token
    client_id_env: X
    client_secret_env: Y

upstreams:
  - name: internal
    transport: streamable-http
    url: https://example.com
    auth:
      type: bearer
      token_env: INTERNAL_TOKEN

roles:
  developer:
    allowed_tools: ["*"]
    credentials:
      internal: INTERNAL_TOKEN
`
	_, err := LoadBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
