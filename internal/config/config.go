// Package config loads and validates Stile gateway configuration from YAML.
package config

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gobwas/glob"
	"gopkg.in/yaml.v3"
)

// RateLimit represents a parsed rate limit specification (e.g. "100/min").
type RateLimit struct {
	rate  float64 // requests per second
	burst int     // burst size
}

// Rate returns the requests-per-second rate.
func (r RateLimit) Rate() float64 { return r.rate }

// Burst returns the burst size.
func (r RateLimit) Burst() int { return r.burst }

// ParseRateLimit parses a rate limit string like "100/min", "10/sec", "5000/hour".
// Burst is set to the per-second rate rounded up (at least 1).
func ParseRateLimit(s string) (RateLimit, error) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return RateLimit{}, fmt.Errorf("config: invalid rate limit %q: expected format N/unit", s)
	}

	count, err := strconv.ParseFloat(parts[0], 64)
	if err != nil || count <= 0 {
		return RateLimit{}, fmt.Errorf("config: invalid rate limit %q: count must be a positive number", s)
	}

	var perSecond float64
	switch parts[1] {
	case "sec", "second":
		perSecond = count
	case "min", "minute":
		perSecond = count / 60
	case "hour":
		perSecond = count / 3600
	default:
		return RateLimit{}, fmt.Errorf("config: invalid rate limit %q: unit must be sec, min, or hour", s)
	}

	burst := int(math.Ceil(perSecond))
	if burst < 1 {
		burst = 1
	}

	return RateLimit{rate: perSecond, burst: burst}, nil
}

// UpstreamConfig is a sealed interface representing an upstream's configuration.
// The concrete types are *HTTPUpstreamConfig and *StdioUpstreamConfig.
type UpstreamConfig interface {
	upstreamConfig() // sealed — only types in this package can implement UpstreamConfig
	Name() string
	Tools() []string
	RateLimit() *RateLimit
}

// HTTPUpstreamConfig is an UpstreamConfig for streamable-http upstreams.
type HTTPUpstreamConfig struct {
	name      string
	url       string
	auth      *AuthConfig
	tools     []string
	rateLimit *RateLimit
}

func (*HTTPUpstreamConfig) upstreamConfig() {}

func (h *HTTPUpstreamConfig) Name() string         { return h.name }
func (h *HTTPUpstreamConfig) URL() string           { return h.url }
func (h *HTTPUpstreamConfig) Auth() *AuthConfig     { return h.auth }
func (h *HTTPUpstreamConfig) RateLimit() *RateLimit { return h.rateLimit }

// Tools returns a copy of the tools slice.
func (h *HTTPUpstreamConfig) Tools() []string {
	if h.tools == nil {
		return nil
	}
	out := make([]string, len(h.tools))
	copy(out, h.tools)
	return out
}

// StdioUpstreamConfig is an UpstreamConfig for stdio upstreams.
type StdioUpstreamConfig struct {
	name      string
	command   []string
	env       map[string]string
	tools     []string
	rateLimit *RateLimit
}

func (*StdioUpstreamConfig) upstreamConfig() {}

func (s *StdioUpstreamConfig) Name() string         { return s.name }
func (s *StdioUpstreamConfig) RateLimit() *RateLimit { return s.rateLimit }

// Command returns a copy of the command slice.
func (s *StdioUpstreamConfig) Command() []string {
	if s.command == nil {
		return nil
	}
	out := make([]string, len(s.command))
	copy(out, s.command)
	return out
}

// Env returns a copy of the environment variable map.
func (s *StdioUpstreamConfig) Env() map[string]string {
	if s.env == nil {
		return nil
	}
	out := make(map[string]string, len(s.env))
	for k, v := range s.env {
		out[k] = v
	}
	return out
}

// Tools returns a copy of the tools slice.
func (s *StdioUpstreamConfig) Tools() []string {
	if s.tools == nil {
		return nil
	}
	out := make([]string, len(s.tools))
	copy(out, s.tools)
	return out
}

// Compile-time interface satisfaction checks.
var (
	_ UpstreamConfig = (*HTTPUpstreamConfig)(nil)
	_ UpstreamConfig = (*StdioUpstreamConfig)(nil)
)

// Config is immutable after construction via Load.
type Config struct {
	server            serverConfig
	upstreams         []UpstreamConfig
	roles             []RoleConfig
	rateLimitDefaults rateLimitDefaults
	logging           loggingConfig
	audit             auditConfig
	telemetry         telemetryConfig
}

type loggingConfig struct {
	level  string
	format string
}

type auditConfig struct {
	enabled  bool
	database string
	driver   string
}

type telemetryConfig struct {
	traces tracesConfig
}

type tracesConfig struct {
	enabled    bool
	endpoint   string
	sampleRate float64
}

type rateLimitDefaults struct {
	defaultCaller   *RateLimit
	defaultTool     *RateLimit
	defaultUpstream *RateLimit
}

// RateLimitDefaults provides read-only access to global rate limit defaults.
type RateLimitDefaults struct {
	defaultCaller   *RateLimit
	defaultTool     *RateLimit
	defaultUpstream *RateLimit
}

// DefaultCaller returns the default per-caller rate limit, or nil if unset.
func (r RateLimitDefaults) DefaultCaller() *RateLimit { return r.defaultCaller }

// DefaultTool returns the default per-caller-per-tool rate limit, or nil if unset.
func (r RateLimitDefaults) DefaultTool() *RateLimit { return r.defaultTool }

// DefaultUpstream returns the default per-upstream rate limit, or nil if unset.
func (r RateLimitDefaults) DefaultUpstream() *RateLimit { return r.defaultUpstream }

// Server returns the server configuration.
func (c *Config) Server() ServerConfig {
	return ServerConfig{
		address:         c.server.address,
		toolCacheTTL:    c.server.toolCacheTTL,
		dbPath:          c.server.dbPath,
		database:        c.server.database,
		shutdownTimeout: c.server.shutdownTimeout,
	}
}

// Upstreams returns a copy of the upstream configurations.
func (c *Config) Upstreams() []UpstreamConfig {
	out := make([]UpstreamConfig, len(c.upstreams))
	copy(out, c.upstreams)
	return out
}

// Roles returns a copy of the role configurations.
func (c *Config) Roles() []RoleConfig {
	out := make([]RoleConfig, len(c.roles))
	copy(out, c.roles)
	return out
}

// Logging returns the logging configuration.
func (c *Config) Logging() LoggingConfig {
	return LoggingConfig{level: c.logging.level, format: c.logging.format}
}

// Audit returns the audit configuration.
func (c *Config) Audit() AuditConfig {
	return AuditConfig{enabled: c.audit.enabled, database: c.audit.database, driver: c.audit.driver}
}

// Telemetry returns the telemetry configuration.
func (c *Config) Telemetry() TelemetryConfig {
	return TelemetryConfig{
		traces: TracesConfig{
			enabled:    c.telemetry.traces.enabled,
			endpoint:   c.telemetry.traces.endpoint,
			sampleRate: c.telemetry.traces.sampleRate,
		},
	}
}

// RateLimitDefaults returns the global rate limit defaults.
func (c *Config) RateLimitDefaults() RateLimitDefaults {
	return RateLimitDefaults{
		defaultCaller:   c.rateLimitDefaults.defaultCaller,
		defaultTool:     c.rateLimitDefaults.defaultTool,
		defaultUpstream: c.rateLimitDefaults.defaultUpstream,
	}
}

// ServerConfig provides read-only access to server settings.
type ServerConfig struct {
	address         string
	toolCacheTTL    time.Duration
	dbPath          string
	database        DatabaseConfig
	shutdownTimeout time.Duration
}

// Address returns the listen address (e.g. ":8080").
func (s ServerConfig) Address() string { return s.address }

// ToolCacheTTL returns how often tool schemas are refreshed from upstreams.
// Default: 5 minutes.
func (s ServerConfig) ToolCacheTTL() time.Duration { return s.toolCacheTTL }

// DBPath returns the path to the SQLite database for caller storage.
// Deprecated: use Database() instead.
func (s ServerConfig) DBPath() string { return s.dbPath }

// Database returns the database configuration.
func (s ServerConfig) Database() DatabaseConfig { return s.database }

// ShutdownTimeout returns the graceful shutdown timeout. Default: 30s.
func (s ServerConfig) ShutdownTimeout() time.Duration { return s.shutdownTimeout }

// DatabaseConfig provides read-only access to database settings.
type DatabaseConfig struct {
	driver string
	dsn    string
}

// Driver returns the database driver ("sqlite" or "postgres"). Empty means no database configured.
func (d DatabaseConfig) Driver() string { return d.driver }

// DSN returns the data source name (file path for sqlite, connection string for postgres).
func (d DatabaseConfig) DSN() string { return d.dsn }

// NewDatabaseConfig creates a DatabaseConfig with the given driver and DSN.
func NewDatabaseConfig(driver, dsn string) DatabaseConfig {
	return DatabaseConfig{driver: driver, dsn: dsn}
}

// LoggingConfig provides read-only access to logging settings.
type LoggingConfig struct {
	level  string
	format string
}

// Level returns the log level (debug, info, warn, error). Default: "info".
func (l LoggingConfig) Level() string { return l.level }

// Format returns the log format (json or text). Default: "json".
func (l LoggingConfig) Format() string { return l.format }

// AuditConfig provides read-only access to audit settings.
type AuditConfig struct {
	enabled  bool
	database string
	driver   string
}

// Enabled returns whether audit logging is enabled.
func (a AuditConfig) Enabled() bool { return a.enabled }

// Database returns the audit database DSN string.
func (a AuditConfig) Database() string { return a.database }

// DatabaseConfig returns a DatabaseConfig derived from the audit settings.
func (a AuditConfig) DatabaseConfig() DatabaseConfig {
	driver := a.driver
	if driver == "" {
		driver = "sqlite"
	}
	return DatabaseConfig{driver: driver, dsn: a.database}
}

// TelemetryConfig provides read-only access to telemetry settings.
type TelemetryConfig struct {
	traces TracesConfig
}

// Traces returns the tracing configuration.
func (t TelemetryConfig) Traces() TracesConfig { return t.traces }

// TracesConfig provides read-only access to trace settings.
type TracesConfig struct {
	enabled    bool
	endpoint   string
	sampleRate float64
}

// Enabled returns whether tracing is enabled.
func (t TracesConfig) Enabled() bool { return t.enabled }

// Endpoint returns the OTLP HTTP endpoint.
func (t TracesConfig) Endpoint() string { return t.endpoint }

// SampleRate returns the trace sample rate (0.0 to 1.0).
func (t TracesConfig) SampleRate() float64 { return t.sampleRate }

type serverConfig struct {
	address         string
	toolCacheTTL    time.Duration
	dbPath          string
	database        DatabaseConfig
	shutdownTimeout time.Duration
}

// RoleConfig provides read-only access to a role's settings.
type RoleConfig struct {
	name           string
	allowedTools   []string          // glob patterns
	credentials    map[string]string // upstream name → env var name
	rateLimit      *RateLimit        // per-caller rate limit override
	toolRateLimit  *RateLimit        // per-caller-per-tool rate limit override
}

// Name returns the role name.
func (r *RoleConfig) Name() string { return r.name }

// AllowedTools returns a copy of the allowed tool patterns.
func (r *RoleConfig) AllowedTools() []string {
	out := make([]string, len(r.allowedTools))
	copy(out, r.allowedTools)
	return out
}

// Credentials returns a copy of the credentials map.
func (r *RoleConfig) Credentials() map[string]string {
	out := make(map[string]string, len(r.credentials))
	for k, v := range r.credentials {
		out[k] = v
	}
	return out
}

// RateLimit returns the per-caller rate limit override for this role, or nil if unset.
func (r *RoleConfig) RateLimit() *RateLimit { return r.rateLimit }

// ToolRateLimit returns the per-caller-per-tool rate limit override for this role, or nil if unset.
func (r *RoleConfig) ToolRateLimit() *RateLimit { return r.toolRateLimit }


// AuthConfig provides read-only access to upstream auth settings.
type AuthConfig struct {
	authType string
	tokenEnv string
}

func (a *AuthConfig) Type() string     { return a.authType }
func (a *AuthConfig) TokenEnv() string { return a.tokenEnv }

// --- raw types for YAML unmarshaling ---

type rawRoleConfig struct {
	AllowedTools  []string          `yaml:"allowed_tools"`
	Credentials   map[string]string `yaml:"credentials"`
	RateLimit     string            `yaml:"rate_limit"`
	ToolRateLimit string            `yaml:"tool_rate_limit"`
}

type rawRateLimitDefaults struct {
	DefaultCaller   string `yaml:"default_caller"`
	DefaultTool     string `yaml:"default_tool"`
	DefaultUpstream string `yaml:"default_upstream"`
}

type rawLoggingConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

type rawAuditConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Database string `yaml:"database"`
	Driver   string `yaml:"driver"`
}

type rawTelemetryConfig struct {
	Traces *rawTracesConfig `yaml:"traces"`
}

type rawTracesConfig struct {
	Enabled    bool    `yaml:"enabled"`
	Endpoint   string  `yaml:"endpoint"`
	SampleRate float64 `yaml:"sample_rate"`
}

type rawConfig struct {
	Server     rawServerConfig          `yaml:"server"`
	Upstreams  []rawUpstreamConfig      `yaml:"upstreams"`
	Roles      map[string]rawRoleConfig `yaml:"roles"`
	RateLimits *rawRateLimitDefaults    `yaml:"rate_limits"`
	Logging    *rawLoggingConfig        `yaml:"logging"`
	Audit      *rawAuditConfig          `yaml:"audit"`
	Telemetry  *rawTelemetryConfig      `yaml:"telemetry"`

	// rolesOrdered preserves YAML key order for roles.
	// Populated by Load/LoadBytes before convert is called.
	rolesOrdered []string
}

type rawDatabaseConfig struct {
	Driver string `yaml:"driver"`
	DSN    string `yaml:"dsn"`
}

type rawServerConfig struct {
	Address         string             `yaml:"address"`
	ToolCacheTTL    string             `yaml:"tool_cache_ttl"`
	DBPath          string             `yaml:"db_path"`
	Database        *rawDatabaseConfig `yaml:"database"`
	ShutdownTimeout string             `yaml:"shutdown_timeout"`
}

type rawUpstreamConfig struct {
	Name      string            `yaml:"name"`
	URL       string            `yaml:"url"`
	Command   []string          `yaml:"command"`
	Env       map[string]string `yaml:"env"`
	Transport string            `yaml:"transport"`
	Auth      *rawAuthConfig    `yaml:"auth"`
	Tools     []string          `yaml:"tools"`
	RateLimit string            `yaml:"rate_limit"`
}

type rawAuthConfig struct {
	Type     string `yaml:"type"`
	TokenEnv string `yaml:"token_env"`
}

// Load reads, parses, and validates a config file. It returns a valid *Config
// or an error. It is impossible to obtain an invalid *Config through this
// package's public API.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read file: %w", err)
	}
	return LoadBytes(data)
}

// LoadBytes parses and validates config from raw YAML bytes.
func LoadBytes(data []byte) (*Config, error) {
	var raw rawConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("config: parse YAML: %w", err)
	}

	// Extract ordered role names from the YAML AST (maps don't preserve order).
	raw.rolesOrdered = extractRoleOrder(data)

	return convert(raw)
}

// extractRoleOrder parses the YAML to get role names in document order.
func extractRoleOrder(data []byte) []string {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil || doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "roles" {
			rolesNode := root.Content[i+1]
			if rolesNode.Kind != yaml.MappingNode {
				return nil
			}
			var names []string
			for j := 0; j+1 < len(rolesNode.Content); j += 2 {
				names = append(names, rolesNode.Content[j].Value)
			}
			return names
		}
	}
	return nil
}

func convert(raw rawConfig) (*Config, error) {
	if err := validate(raw); err != nil {
		return nil, err
	}

	cfg := &Config{
		server: serverConfig{
			address: raw.Server.Address,
			dbPath:  raw.Server.DBPath,
		},
	}

	// Resolve database config: explicit database section takes precedence,
	// otherwise fall back to db_path for backwards compatibility.
	if raw.Server.Database != nil {
		cfg.server.database = DatabaseConfig{
			driver: raw.Server.Database.Driver,
			dsn:    raw.Server.Database.DSN,
		}
		if cfg.server.database.driver == "" {
			cfg.server.database.driver = "sqlite"
		}
	} else if raw.Server.DBPath != "" {
		cfg.server.database = DatabaseConfig{
			driver: "sqlite",
			dsn:    raw.Server.DBPath,
		}
	}
	if cfg.server.address == "" {
		cfg.server.address = ":8080"
	}

	if raw.Server.ToolCacheTTL != "" {
		ttl, err := time.ParseDuration(raw.Server.ToolCacheTTL)
		if err != nil {
			return nil, fmt.Errorf("config: invalid tool_cache_ttl %q: %w", raw.Server.ToolCacheTTL, err)
		}
		cfg.server.toolCacheTTL = ttl
	} else {
		cfg.server.toolCacheTTL = 5 * time.Minute
	}

	if raw.Server.ShutdownTimeout != "" {
		st, err := time.ParseDuration(raw.Server.ShutdownTimeout)
		if err != nil {
			return nil, fmt.Errorf("config: invalid shutdown_timeout %q: %w", raw.Server.ShutdownTimeout, err)
		}
		cfg.server.shutdownTimeout = st
	} else {
		cfg.server.shutdownTimeout = 30 * time.Second
	}

	cfg.upstreams = make([]UpstreamConfig, len(raw.Upstreams))
	for i, ru := range raw.Upstreams {
		var rl *RateLimit
		if ru.RateLimit != "" {
			parsed, err := ParseRateLimit(ru.RateLimit)
			if err != nil {
				return nil, fmt.Errorf("config: upstream %q: %w", ru.Name, err)
			}
			rl = &parsed
		}

		var tools []string
		if ru.Tools != nil {
			tools = make([]string, len(ru.Tools))
			copy(tools, ru.Tools)
		}

		switch ru.Transport {
		case "streamable-http":
			h := &HTTPUpstreamConfig{
				name:      ru.Name,
				url:       ru.URL,
				tools:     tools,
				rateLimit: rl,
			}
			if ru.Auth != nil {
				h.auth = &AuthConfig{
					authType: ru.Auth.Type,
					tokenEnv: ru.Auth.TokenEnv,
				}
			}
			cfg.upstreams[i] = h
		case "stdio":
			s := &StdioUpstreamConfig{
				name:      ru.Name,
				tools:     tools,
				rateLimit: rl,
			}
			if ru.Command != nil {
				s.command = make([]string, len(ru.Command))
				copy(s.command, ru.Command)
			}
			if ru.Env != nil {
				s.env = make(map[string]string, len(ru.Env))
				for k, v := range ru.Env {
					s.env[k] = v
				}
			}
			cfg.upstreams[i] = s
		}
	}

	// Use ordered role names to preserve YAML document order.
	for _, name := range raw.rolesOrdered {
		rawRole, ok := raw.Roles[name]
		if !ok {
			continue
		}
		rc := RoleConfig{
			name:         name,
			allowedTools: make([]string, len(rawRole.AllowedTools)),
			credentials:  make(map[string]string, len(rawRole.Credentials)),
		}
		copy(rc.allowedTools, rawRole.AllowedTools)
		for k, v := range rawRole.Credentials {
			rc.credentials[k] = v
		}
		if rawRole.RateLimit != "" {
			rl, err := ParseRateLimit(rawRole.RateLimit)
			if err != nil {
				return nil, fmt.Errorf("config: roles[%q]: %w", name, err)
			}
			rc.rateLimit = &rl
		}
		if rawRole.ToolRateLimit != "" {
			rl, err := ParseRateLimit(rawRole.ToolRateLimit)
			if err != nil {
				return nil, fmt.Errorf("config: roles[%q]: %w", name, err)
			}
			rc.toolRateLimit = &rl
		}
		cfg.roles = append(cfg.roles, rc)
	}

	// Parse logging config with defaults.
	cfg.logging.level = "info"
	cfg.logging.format = "json"
	if raw.Logging != nil {
		if raw.Logging.Level != "" {
			cfg.logging.level = raw.Logging.Level
		}
		if raw.Logging.Format != "" {
			cfg.logging.format = raw.Logging.Format
		}
	}

	// Parse audit config.
	if raw.Audit != nil {
		cfg.audit.enabled = raw.Audit.Enabled
		cfg.audit.database = raw.Audit.Database
		cfg.audit.driver = raw.Audit.Driver
	}

	// Parse global rate limit defaults.
	if raw.RateLimits != nil {
		if raw.RateLimits.DefaultCaller != "" {
			rl, err := ParseRateLimit(raw.RateLimits.DefaultCaller)
			if err != nil {
				return nil, fmt.Errorf("config: rate_limits.default_caller: %w", err)
			}
			cfg.rateLimitDefaults.defaultCaller = &rl
		}
		if raw.RateLimits.DefaultTool != "" {
			rl, err := ParseRateLimit(raw.RateLimits.DefaultTool)
			if err != nil {
				return nil, fmt.Errorf("config: rate_limits.default_tool: %w", err)
			}
			cfg.rateLimitDefaults.defaultTool = &rl
		}
		if raw.RateLimits.DefaultUpstream != "" {
			rl, err := ParseRateLimit(raw.RateLimits.DefaultUpstream)
			if err != nil {
				return nil, fmt.Errorf("config: rate_limits.default_upstream: %w", err)
			}
			cfg.rateLimitDefaults.defaultUpstream = &rl
		}
	}

	// Parse telemetry config with defaults.
	if raw.Telemetry != nil && raw.Telemetry.Traces != nil {
		cfg.telemetry.traces.enabled = raw.Telemetry.Traces.Enabled
		cfg.telemetry.traces.endpoint = raw.Telemetry.Traces.Endpoint
		cfg.telemetry.traces.sampleRate = raw.Telemetry.Traces.SampleRate
		if cfg.telemetry.traces.enabled && cfg.telemetry.traces.endpoint == "" {
			cfg.telemetry.traces.endpoint = "localhost:4318"
		}
		if cfg.telemetry.traces.sampleRate == 0 && cfg.telemetry.traces.enabled {
			cfg.telemetry.traces.sampleRate = 1.0
		}
	}

	return cfg, nil
}

func validate(raw rawConfig) error {
	if len(raw.Upstreams) == 0 {
		return fmt.Errorf("config: at least one upstream is required")
	}

	upstreamNames := make(map[string]bool, len(raw.Upstreams))
	for i, u := range raw.Upstreams {
		if u.Name == "" {
			return fmt.Errorf("config: upstream[%d]: name is required", i)
		}
		if upstreamNames[u.Name] {
			return fmt.Errorf("config: upstream %q: duplicate name", u.Name)
		}
		upstreamNames[u.Name] = true

		switch u.Transport {
		case "streamable-http":
			if u.URL == "" {
				return fmt.Errorf("config: upstream %q: url is required for streamable-http transport", u.Name)
			}
		case "stdio":
			if len(u.Command) == 0 {
				return fmt.Errorf("config: upstream %q: command is required for stdio transport", u.Name)
			}
		default:
			return fmt.Errorf("config: upstream %q: transport must be \"streamable-http\" or \"stdio\", got %q", u.Name, u.Transport)
		}
	}

	if raw.Server.Database != nil {
		switch raw.Server.Database.Driver {
		case "", "sqlite", "postgres":
		default:
			return fmt.Errorf("config: server.database.driver must be \"sqlite\" or \"postgres\", got %q", raw.Server.Database.Driver)
		}
		if raw.Server.Database.DSN == "" {
			return fmt.Errorf("config: server.database.dsn is required when database is configured")
		}
		if raw.Server.DBPath != "" {
			return fmt.Errorf("config: cannot set both db_path and database")
		}
	}

	if raw.Logging != nil {
		switch raw.Logging.Level {
		case "", "debug", "info", "warn", "error":
		default:
			return fmt.Errorf("config: logging.level must be debug, info, warn, or error, got %q", raw.Logging.Level)
		}
		switch raw.Logging.Format {
		case "", "json", "text":
		default:
			return fmt.Errorf("config: logging.format must be json or text, got %q", raw.Logging.Format)
		}
	}

	if raw.Audit != nil && raw.Audit.Enabled {
		if raw.Audit.Database == "" {
			return fmt.Errorf("config: audit.database is required when audit is enabled")
		}
		switch raw.Audit.Driver {
		case "", "sqlite", "postgres":
		default:
			return fmt.Errorf("config: audit.driver must be \"sqlite\" or \"postgres\", got %q", raw.Audit.Driver)
		}
	}

	if raw.Telemetry != nil && raw.Telemetry.Traces != nil {
		sr := raw.Telemetry.Traces.SampleRate
		if sr < 0 || sr > 1 {
			return fmt.Errorf("config: telemetry.traces.sample_rate must be between 0.0 and 1.0, got %f", sr)
		}
	}

	for roleName, role := range raw.Roles {
		if roleName == "" {
			return fmt.Errorf("config: roles: empty role name")
		}
		if len(role.AllowedTools) == 0 {
			return fmt.Errorf("config: roles[%q]: allowed_tools is required", roleName)
		}
		for _, pattern := range role.AllowedTools {
			if _, err := glob.Compile(pattern); err != nil {
				return fmt.Errorf("config: roles[%q]: invalid glob pattern %q: %w", roleName, pattern, err)
			}
		}
		for upstreamName, envVar := range role.Credentials {
			if !upstreamNames[upstreamName] {
				return fmt.Errorf("config: roles[%q]: references unknown upstream %q", roleName, upstreamName)
			}
			if envVar == "" {
				return fmt.Errorf("config: roles[%q]: empty env var for upstream %q", roleName, upstreamName)
			}
		}
	}

	return nil
}
