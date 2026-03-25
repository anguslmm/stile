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
	rate   float64 // requests per second
	burst  int     // burst size (for token bucket)
	count  int     // requests per window (for sliding window)
	window int     // window in seconds (for sliding window)
}

// Rate returns the requests-per-second rate.
func (r RateLimit) Rate() float64 { return r.rate }

// Burst returns the burst size.
func (r RateLimit) Burst() int { return r.burst }

// Count returns the number of requests allowed per window (for sliding window).
func (r RateLimit) Count() int { return r.count }

// Window returns the window duration in seconds (for sliding window).
func (r RateLimit) Window() int { return r.window }

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
	var windowSecs int
	switch parts[1] {
	case "sec", "second":
		perSecond = count
		windowSecs = 1
	case "min", "minute":
		perSecond = count / 60
		windowSecs = 60
	case "hour":
		perSecond = count / 3600
		windowSecs = 3600
	default:
		return RateLimit{}, fmt.Errorf("config: invalid rate limit %q: unit must be sec, min, or hour", s)
	}

	burst := int(math.Ceil(perSecond))
	if burst < 1 {
		burst = 1
	}

	windowCount := int(math.Ceil(count))

	return RateLimit{rate: perSecond, burst: burst, count: windowCount, window: windowSecs}, nil
}

// UpstreamConfig is a sealed interface representing an upstream's configuration.
// The concrete types are *HTTPUpstreamConfig and *StdioUpstreamConfig.
type UpstreamConfig interface {
	upstreamConfig() // sealed — only types in this package can implement UpstreamConfig
	Name() string
	Tools() []string
	RateLimit() *RateLimit
	Timeout() time.Duration
	CircuitBreaker() *CircuitBreakerConfig
	Retry() *RetryConfig
}

// CircuitBreakerConfig provides read-only access to circuit breaker settings.
type CircuitBreakerConfig struct {
	failureThreshold int
	cooldown         time.Duration
}

// FailureThreshold returns the number of consecutive failures to trip the circuit.
func (c *CircuitBreakerConfig) FailureThreshold() int { return c.failureThreshold }

// Cooldown returns the time to wait in open state before probing.
func (c *CircuitBreakerConfig) Cooldown() time.Duration { return c.cooldown }

// RetryConfig provides read-only access to retry settings.
type RetryConfig struct {
	maxAttempts     int
	backoff         time.Duration
	maxBackoff      time.Duration
	retryableErrors []string
}

// MaxAttempts returns the total number of attempts including the original.
func (r *RetryConfig) MaxAttempts() int { return r.maxAttempts }

// Backoff returns the initial backoff duration.
func (r *RetryConfig) Backoff() time.Duration { return r.backoff }

// MaxBackoff returns the maximum backoff duration.
func (r *RetryConfig) MaxBackoff() time.Duration { return r.maxBackoff }

// RetryableErrors returns a copy of the retryable error types.
func (r *RetryConfig) RetryableErrors() []string {
	out := make([]string, len(r.retryableErrors))
	copy(out, r.retryableErrors)
	return out
}

// ServerTLSConfig provides read-only access to inbound TLS settings.
type ServerTLSConfig struct {
	certFile     string
	keyFile      string
	minVersion   string
	clientCAFile string
}

// CertFile returns the path to the server certificate.
func (t *ServerTLSConfig) CertFile() string { return t.certFile }

// KeyFile returns the path to the server private key.
func (t *ServerTLSConfig) KeyFile() string { return t.keyFile }

// MinVersion returns the minimum TLS version (e.g. "1.2"). Default: "1.2".
func (t *ServerTLSConfig) MinVersion() string { return t.minVersion }

// ClientCAFile returns the path to the CA file for client certificate verification.
// If set, inbound mTLS is required.
func (t *ServerTLSConfig) ClientCAFile() string { return t.clientCAFile }

// UpstreamTLSConfig provides read-only access to per-upstream outbound TLS settings.
type UpstreamTLSConfig struct {
	caFile             string
	certFile           string
	keyFile            string
	insecureSkipVerify bool
}

// CAFile returns the path to a custom CA certificate for verifying the upstream.
func (u *UpstreamTLSConfig) CAFile() string { return u.caFile }

// CertFile returns the path to the client certificate for mTLS with the upstream.
func (u *UpstreamTLSConfig) CertFile() string { return u.certFile }

// KeyFile returns the path to the client key for mTLS with the upstream.
func (u *UpstreamTLSConfig) KeyFile() string { return u.keyFile }

// InsecureSkipVerify returns whether to skip TLS certificate verification (dev only).
func (u *UpstreamTLSConfig) InsecureSkipVerify() bool { return u.insecureSkipVerify }

// HTTPUpstreamConfig is an UpstreamConfig for streamable-http upstreams.
type HTTPUpstreamConfig struct {
	name           string
	url            string
	auth           *AuthConfig
	tls            *UpstreamTLSConfig
	tools          []string
	rateLimit      *RateLimit
	timeout        time.Duration
	circuitBreaker *CircuitBreakerConfig
	retry          *RetryConfig
}

func (*HTTPUpstreamConfig) upstreamConfig() {}

func (h *HTTPUpstreamConfig) Name() string                { return h.name }
func (h *HTTPUpstreamConfig) URL() string                  { return h.url }
func (h *HTTPUpstreamConfig) Auth() *AuthConfig            { return h.auth }
func (h *HTTPUpstreamConfig) TLS() *UpstreamTLSConfig      { return h.tls }
func (h *HTTPUpstreamConfig) RateLimit() *RateLimit        { return h.rateLimit }

// Tools returns a copy of the tools slice.
func (h *HTTPUpstreamConfig) Tools() []string {
	if h.tools == nil {
		return nil
	}
	out := make([]string, len(h.tools))
	copy(out, h.tools)
	return out
}

// Timeout returns the per-upstream request timeout (default 60s).
func (h *HTTPUpstreamConfig) Timeout() time.Duration { return h.timeout }

// CircuitBreaker returns the circuit breaker config, or nil if not set.
func (h *HTTPUpstreamConfig) CircuitBreaker() *CircuitBreakerConfig { return h.circuitBreaker }

// Retry returns the retry config, or nil if not set.
func (h *HTTPUpstreamConfig) Retry() *RetryConfig { return h.retry }

// StdioUpstreamConfig is an UpstreamConfig for stdio upstreams.
type StdioUpstreamConfig struct {
	name           string
	command        []string
	env            map[string]string
	tools          []string
	rateLimit      *RateLimit
	timeout        time.Duration
	circuitBreaker *CircuitBreakerConfig
	retry          *RetryConfig
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

// Timeout returns the per-upstream request timeout (default 60s).
func (s *StdioUpstreamConfig) Timeout() time.Duration { return s.timeout }

// CircuitBreaker returns the circuit breaker config, or nil if not set.
func (s *StdioUpstreamConfig) CircuitBreaker() *CircuitBreakerConfig { return s.circuitBreaker }

// Retry returns the retry config, or nil if not set.
func (s *StdioUpstreamConfig) Retry() *RetryConfig { return s.retry }

// NewStdioUpstreamConfig creates a StdioUpstreamConfig programmatically.
func NewStdioUpstreamConfig(name string, command []string, env map[string]string) *StdioUpstreamConfig {
	s := &StdioUpstreamConfig{
		name: name,
	}
	if command != nil {
		s.command = make([]string, len(command))
		copy(s.command, command)
	}
	if env != nil {
		s.env = make(map[string]string, len(env))
		for k, v := range env {
			s.env[k] = v
		}
	}
	return s
}

// Compile-time interface satisfaction checks.
var (
	_ UpstreamConfig = (*HTTPUpstreamConfig)(nil)
	_ UpstreamConfig = (*StdioUpstreamConfig)(nil)
)

// OIDCConfig provides read-only access to OIDC authentication settings.
type OIDCConfig struct {
	issuer         string
	audience       string
	callerClaim    string
	validation     string // "jwt" or "userinfo"
	autoProvision  bool
	defaultRoles   []string
	allowedDomains []string
}

// Issuer returns the OIDC provider's issuer URL.
func (o *OIDCConfig) Issuer() string { return o.issuer }

// Audience returns the expected audience claim (OAuth client ID).
func (o *OIDCConfig) Audience() string { return o.audience }

// CallerClaim returns the JWT claim / userinfo field that maps to the caller name.
func (o *OIDCConfig) CallerClaim() string { return o.callerClaim }

// Validation returns the token validation mode ("jwt" or "userinfo").
func (o *OIDCConfig) Validation() string { return o.validation }

// AutoProvision returns whether to create callers on first OIDC login.
func (o *OIDCConfig) AutoProvision() bool { return o.autoProvision }

// DefaultRoles returns a copy of the roles assigned to auto-provisioned callers.
func (o *OIDCConfig) DefaultRoles() []string {
	out := make([]string, len(o.defaultRoles))
	copy(out, o.defaultRoles)
	return out
}

// AllowedDomains returns a copy of the allowed email domains.
func (o *OIDCConfig) AllowedDomains() []string {
	out := make([]string, len(o.allowedDomains))
	copy(out, o.allowedDomains)
	return out
}

// OAuthProviderConfig provides read-only access to an OAuth provider's settings.
type OAuthProviderConfig struct {
	name            string
	authURL         string
	tokenURL        string
	clientIDEnv     string
	clientSecretEnv string
	scopes          []string
}

// Name returns the provider name (the YAML map key).
func (o *OAuthProviderConfig) Name() string { return o.name }

// AuthURL returns the OAuth authorization endpoint.
func (o *OAuthProviderConfig) AuthURL() string { return o.authURL }

// TokenURL returns the OAuth token endpoint.
func (o *OAuthProviderConfig) TokenURL() string { return o.tokenURL }

// ClientIDEnv returns the env var name for the OAuth client ID.
func (o *OAuthProviderConfig) ClientIDEnv() string { return o.clientIDEnv }

// ClientSecretEnv returns the env var name for the OAuth client secret.
func (o *OAuthProviderConfig) ClientSecretEnv() string { return o.clientSecretEnv }

// Scopes returns a copy of the OAuth scopes.
func (o *OAuthProviderConfig) Scopes() []string {
	out := make([]string, len(o.scopes))
	copy(out, o.scopes)
	return out
}

// Config is immutable after construction via Load.
type Config struct {
	server            serverConfig
	upstreams         []UpstreamConfig
	roles             []RoleConfig
	rateLimitDefaults rateLimitDefaults
	logging           loggingConfig
	audit             auditConfig
	telemetry         telemetryConfig
	health            healthConfig
	oidc              *OIDCConfig
	oauthProviders    []OAuthProviderConfig
}

type healthConfig struct {
	store         string
	checkInterval time.Duration
	missingStatus string
	redis         *RedisConfig
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
	backend         string
	redis           *RedisConfig
	defaultCaller   *RateLimit
	defaultTool     *RateLimit
	defaultUpstream *RateLimit
}

// RedisConfig provides read-only access to Redis rate limiter settings.
type RedisConfig struct {
	address   string
	password  string
	db        int
	keyPrefix string
}

// Address returns the Redis server address.
func (r *RedisConfig) Address() string { return r.address }

// Password returns the Redis password.
func (r *RedisConfig) Password() string { return r.password }

// DB returns the Redis database number.
func (r *RedisConfig) DB() int { return r.db }

// KeyPrefix returns the key prefix for namespacing.
func (r *RedisConfig) KeyPrefix() string { return r.keyPrefix }

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
		tls:             c.server.tls,
		authCacheTTL:    c.server.authCacheTTL,
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

// Health returns the health check configuration.
func (c *Config) Health() HealthConfig {
	return HealthConfig{
		store:         c.health.store,
		checkInterval: c.health.checkInterval,
		missingStatus: c.health.missingStatus,
		redis:         c.health.redis,
	}
}

// OIDC returns the OIDC authentication config, or nil if not configured.
func (c *Config) OIDC() *OIDCConfig { return c.oidc }

// OAuthProviders returns a copy of the OAuth provider configurations.
func (c *Config) OAuthProviders() []OAuthProviderConfig {
	out := make([]OAuthProviderConfig, len(c.oauthProviders))
	copy(out, c.oauthProviders)
	return out
}

// OAuthProvider returns the named OAuth provider config, or nil if not found.
func (c *Config) OAuthProvider(name string) *OAuthProviderConfig {
	for i := range c.oauthProviders {
		if c.oauthProviders[i].name == name {
			p := c.oauthProviders[i]
			return &p
		}
	}
	return nil
}

// RateLimitDefaults returns the global rate limit defaults.
func (c *Config) RateLimitDefaults() RateLimitDefaults {
	return RateLimitDefaults{
		defaultCaller:   c.rateLimitDefaults.defaultCaller,
		defaultTool:     c.rateLimitDefaults.defaultTool,
		defaultUpstream: c.rateLimitDefaults.defaultUpstream,
	}
}

// RateLimitBackend returns the rate limiter backend ("local" or "redis").
func (c *Config) RateLimitBackend() string {
	if c.rateLimitDefaults.backend == "" {
		return "local"
	}
	return c.rateLimitDefaults.backend
}

// RedisConfig returns the Redis rate limiter configuration, or nil if not set.
func (c *Config) RedisConfig() *RedisConfig {
	return c.rateLimitDefaults.redis
}

// ServerConfig provides read-only access to server settings.
type ServerConfig struct {
	address         string
	toolCacheTTL    time.Duration
	dbPath          string
	database        DatabaseConfig
	shutdownTimeout time.Duration
	tls             *ServerTLSConfig
	authCacheTTL    time.Duration
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

// TLS returns the server TLS configuration, or nil if TLS is not configured.
func (s ServerConfig) TLS() *ServerTLSConfig { return s.tls }

// AuthCacheTTL returns the TTL for the in-memory auth cache.
// Zero means caching is disabled.
func (s ServerConfig) AuthCacheTTL() time.Duration { return s.authCacheTTL }

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

// HealthConfig provides read-only access to health check settings.
type HealthConfig struct {
	store         string
	checkInterval time.Duration
	missingStatus string
	redis         *RedisConfig
}

// Store returns the health store backend ("local" or "redis"). Default: "local".
func (h HealthConfig) Store() string { return h.store }

// CheckInterval returns the health check interval. Default: 30s.
func (h HealthConfig) CheckInterval() time.Duration { return h.checkInterval }

// MissingStatus returns the default health assumption when a store key is
// missing or expired ("healthy" or "unhealthy"). Default: "healthy".
func (h HealthConfig) MissingStatus() string { return h.missingStatus }

// Redis returns the Redis configuration for health checking, or nil if unset.
func (h HealthConfig) Redis() *RedisConfig { return h.redis }

type serverConfig struct {
	address         string
	toolCacheTTL    time.Duration
	dbPath          string
	database        DatabaseConfig
	shutdownTimeout time.Duration
	tls             *ServerTLSConfig
	authCacheTTL    time.Duration
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
	provider string
}

// Type returns the auth type ("bearer" or "oauth").
func (a *AuthConfig) Type() string { return a.authType }

// TokenEnv returns the env var name for static bearer tokens.
func (a *AuthConfig) TokenEnv() string { return a.tokenEnv }

// Provider returns the OAuth provider name (references oauth_providers key).
func (a *AuthConfig) Provider() string { return a.provider }

// --- raw types for YAML unmarshaling ---

type rawRoleConfig struct {
	AllowedTools  []string          `yaml:"allowed_tools"`
	Credentials   map[string]string `yaml:"credentials"`
	RateLimit     string            `yaml:"rate_limit"`
	ToolRateLimit string            `yaml:"tool_rate_limit"`
}

type rawRateLimitDefaults struct {
	Backend         string          `yaml:"backend"`
	Redis           *rawRedisConfig `yaml:"redis"`
	DefaultCaller   string          `yaml:"default_caller"`
	DefaultTool     string          `yaml:"default_tool"`
	DefaultUpstream string          `yaml:"default_upstream"`
}

type rawRedisConfig struct {
	Address   string `yaml:"address"`
	Password  string `yaml:"password"`
	DB        int    `yaml:"db"`
	KeyPrefix string `yaml:"key_prefix"`
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

type rawHealthConfig struct {
	Store         string          `yaml:"store"`
	CheckInterval string          `yaml:"check_interval"`
	MissingStatus string          `yaml:"missing_status"`
	Redis         *rawRedisConfig `yaml:"redis"`
}

type rawInboundAuthConfig struct {
	OIDC *rawOIDCConfig `yaml:"oidc"`
}

type rawOIDCConfig struct {
	Issuer         string   `yaml:"issuer"`
	Audience       string   `yaml:"audience"`
	CallerClaim    string   `yaml:"caller_claim"`
	Validation     string   `yaml:"validation"`
	AutoProvision  bool     `yaml:"auto_provision"`
	DefaultRoles   []string `yaml:"default_roles"`
	AllowedDomains []string `yaml:"allowed_domains"`
}

type rawOAuthProviderConfig struct {
	AuthURL         string   `yaml:"auth_url"`
	TokenURL        string   `yaml:"token_url"`
	ClientIDEnv     string   `yaml:"client_id_env"`
	ClientSecretEnv string   `yaml:"client_secret_env"`
	Scopes          []string `yaml:"scopes"`
}

type rawConfig struct {
	Server         rawServerConfig                    `yaml:"server"`
	Upstreams      []rawUpstreamConfig                `yaml:"upstreams"`
	Roles          map[string]rawRoleConfig           `yaml:"roles"`
	RateLimits     *rawRateLimitDefaults              `yaml:"rate_limits"`
	Logging        *rawLoggingConfig                  `yaml:"logging"`
	Audit          *rawAuditConfig                    `yaml:"audit"`
	Telemetry      *rawTelemetryConfig                `yaml:"telemetry"`
	Health         *rawHealthConfig                   `yaml:"health"`
	Auth           *rawInboundAuthConfig              `yaml:"auth"`
	OAuthProviders map[string]rawOAuthProviderConfig  `yaml:"oauth_providers"`

	// rolesOrdered preserves YAML key order for roles.
	// Populated by Load/LoadBytes before convert is called.
	rolesOrdered []string
}

type rawDatabaseConfig struct {
	Driver string `yaml:"driver"`
	DSN    string `yaml:"dsn"`
}

type rawServerConfig struct {
	Address         string              `yaml:"address"`
	ToolCacheTTL    string              `yaml:"tool_cache_ttl"`
	DBPath          string              `yaml:"db_path"`
	Database        *rawDatabaseConfig  `yaml:"database"`
	ShutdownTimeout string              `yaml:"shutdown_timeout"`
	TLS             *rawServerTLSConfig `yaml:"tls"`
	AuthCacheTTL    string              `yaml:"auth_cache_ttl"`
}

type rawUpstreamConfig struct {
	Name           string                   `yaml:"name"`
	URL            string                   `yaml:"url"`
	Command        []string                 `yaml:"command"`
	Env            map[string]string        `yaml:"env"`
	Transport      string                   `yaml:"transport"`
	Auth           *rawAuthConfig           `yaml:"auth"`
	TLS            *rawUpstreamTLSConfig    `yaml:"tls"`
	Tools          []string                 `yaml:"tools"`
	RateLimit      string                   `yaml:"rate_limit"`
	Timeout        string                   `yaml:"timeout"`
	CircuitBreaker *rawCircuitBreakerConfig `yaml:"circuit_breaker"`
	Retry          *rawRetryConfig          `yaml:"retry"`
}

type rawCircuitBreakerConfig struct {
	FailureThreshold int    `yaml:"failure_threshold"`
	Cooldown         string `yaml:"cooldown"`
}

type rawRetryConfig struct {
	MaxAttempts     int      `yaml:"max_attempts"`
	Backoff         string   `yaml:"backoff"`
	MaxBackoff      string   `yaml:"max_backoff"`
	RetryableErrors []string `yaml:"retryable_errors"`
}

type rawAuthConfig struct {
	Type     string `yaml:"type"`
	TokenEnv string `yaml:"token_env"`
	Provider string `yaml:"provider"`
}

type rawServerTLSConfig struct {
	CertFile     string `yaml:"cert_file"`
	KeyFile      string `yaml:"key_file"`
	MinVersion   string `yaml:"min_version"`
	ClientCAFile string `yaml:"client_ca_file"`
}

type rawUpstreamTLSConfig struct {
	CAFile             string `yaml:"ca_file"`
	CertFile           string `yaml:"cert_file"`
	KeyFile            string `yaml:"key_file"`
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify"`
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

	if raw.Server.AuthCacheTTL != "" {
		act, err := time.ParseDuration(raw.Server.AuthCacheTTL)
		if err != nil {
			return nil, fmt.Errorf("config: invalid auth_cache_ttl %q: %w", raw.Server.AuthCacheTTL, err)
		}
		cfg.server.authCacheTTL = act
	}

	if raw.Server.TLS != nil {
		minVer := raw.Server.TLS.MinVersion
		if minVer == "" {
			minVer = "1.2"
		}
		cfg.server.tls = &ServerTLSConfig{
			certFile:     raw.Server.TLS.CertFile,
			keyFile:      raw.Server.TLS.KeyFile,
			minVersion:   minVer,
			clientCAFile: raw.Server.TLS.ClientCAFile,
		}
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

		// Parse resilience settings common to all upstream types.
		timeout := 60 * time.Second
		if ru.Timeout != "" {
			t, err := time.ParseDuration(ru.Timeout)
			if err != nil {
				return nil, fmt.Errorf("config: upstream %q: invalid timeout %q: %w", ru.Name, ru.Timeout, err)
			}
			if t <= 0 {
				return nil, fmt.Errorf("config: upstream %q: timeout must be positive", ru.Name)
			}
			timeout = t
		}

		var cbCfg *CircuitBreakerConfig
		if ru.CircuitBreaker != nil {
			threshold := ru.CircuitBreaker.FailureThreshold
			if threshold <= 0 {
				threshold = 5
			}
			cooldown := 30 * time.Second
			if ru.CircuitBreaker.Cooldown != "" {
				cd, err := time.ParseDuration(ru.CircuitBreaker.Cooldown)
				if err != nil {
					return nil, fmt.Errorf("config: upstream %q: invalid circuit_breaker.cooldown %q: %w", ru.Name, ru.CircuitBreaker.Cooldown, err)
				}
				if cd <= 0 {
					return nil, fmt.Errorf("config: upstream %q: circuit_breaker.cooldown must be positive", ru.Name)
				}
				cooldown = cd
			}
			cbCfg = &CircuitBreakerConfig{failureThreshold: threshold, cooldown: cooldown}
		}

		var retryCfg *RetryConfig
		if ru.Retry != nil {
			maxAttempts := ru.Retry.MaxAttempts
			if maxAttempts <= 0 {
				maxAttempts = 1
			}
			backoff := 100 * time.Millisecond
			if ru.Retry.Backoff != "" {
				b, err := time.ParseDuration(ru.Retry.Backoff)
				if err != nil {
					return nil, fmt.Errorf("config: upstream %q: invalid retry.backoff %q: %w", ru.Name, ru.Retry.Backoff, err)
				}
				backoff = b
			}
			maxBackoff := 2 * time.Second
			if ru.Retry.MaxBackoff != "" {
				mb, err := time.ParseDuration(ru.Retry.MaxBackoff)
				if err != nil {
					return nil, fmt.Errorf("config: upstream %q: invalid retry.max_backoff %q: %w", ru.Name, ru.Retry.MaxBackoff, err)
				}
				maxBackoff = mb
			}
			retryableErrors := []string{"connection_error"}
			if len(ru.Retry.RetryableErrors) > 0 {
				retryableErrors = make([]string, len(ru.Retry.RetryableErrors))
				copy(retryableErrors, ru.Retry.RetryableErrors)
			}
			retryCfg = &RetryConfig{
				maxAttempts:     maxAttempts,
				backoff:         backoff,
				maxBackoff:      maxBackoff,
				retryableErrors: retryableErrors,
			}
		}

		switch ru.Transport {
		case "streamable-http":
			h := &HTTPUpstreamConfig{
				name:           ru.Name,
				url:            ru.URL,
				tools:          tools,
				rateLimit:      rl,
				timeout:        timeout,
				circuitBreaker: cbCfg,
				retry:          retryCfg,
			}
			if ru.Auth != nil {
				h.auth = &AuthConfig{
					authType: ru.Auth.Type,
					tokenEnv: ru.Auth.TokenEnv,
					provider: ru.Auth.Provider,
				}
			}
			if ru.TLS != nil {
				h.tls = &UpstreamTLSConfig{
					caFile:             ru.TLS.CAFile,
					certFile:           ru.TLS.CertFile,
					keyFile:            ru.TLS.KeyFile,
					insecureSkipVerify: ru.TLS.InsecureSkipVerify,
				}
			}
			cfg.upstreams[i] = h
		case "stdio":
			s := &StdioUpstreamConfig{
				name:           ru.Name,
				tools:          tools,
				rateLimit:      rl,
				timeout:        timeout,
				circuitBreaker: cbCfg,
				retry:          retryCfg,
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

	// Parse rate limit backend and Redis config.
	if raw.RateLimits != nil {
		cfg.rateLimitDefaults.backend = raw.RateLimits.Backend
		if raw.RateLimits.Redis != nil {
			keyPrefix := raw.RateLimits.Redis.KeyPrefix
			if keyPrefix == "" {
				keyPrefix = "stile:"
			}
			cfg.rateLimitDefaults.redis = &RedisConfig{
				address:   raw.RateLimits.Redis.Address,
				password:  raw.RateLimits.Redis.Password,
				db:        raw.RateLimits.Redis.DB,
				keyPrefix: keyPrefix,
			}
		}
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

	// Parse health config with defaults.
	cfg.health.store = "local"
	cfg.health.checkInterval = 30 * time.Second
	cfg.health.missingStatus = "healthy"
	if raw.Health != nil {
		if raw.Health.Store != "" {
			cfg.health.store = raw.Health.Store
		}
		if raw.Health.CheckInterval != "" {
			d, err := time.ParseDuration(raw.Health.CheckInterval)
			if err != nil {
				return nil, fmt.Errorf("config: invalid health.check_interval %q: %w", raw.Health.CheckInterval, err)
			}
			if d <= 0 {
				return nil, fmt.Errorf("config: health.check_interval must be positive")
			}
			cfg.health.checkInterval = d
		}
		if raw.Health.MissingStatus != "" {
			cfg.health.missingStatus = raw.Health.MissingStatus
		}
		if raw.Health.Redis != nil {
			keyPrefix := raw.Health.Redis.KeyPrefix
			if keyPrefix == "" {
				keyPrefix = "stile:"
			}
			cfg.health.redis = &RedisConfig{
				address:   raw.Health.Redis.Address,
				password:  raw.Health.Redis.Password,
				db:        raw.Health.Redis.DB,
				keyPrefix: keyPrefix,
			}
		}
	}
	// Resolve Redis config for health: fall back to rate_limits.redis.
	if cfg.health.store == "redis" && cfg.health.redis == nil {
		cfg.health.redis = cfg.rateLimitDefaults.redis
	}
	if cfg.health.store == "redis" && cfg.health.redis == nil {
		return nil, fmt.Errorf("config: health.store is \"redis\" but no redis config found (set health.redis or rate_limits.redis)")
	}

	// Parse OAuth providers.
	for name, rp := range raw.OAuthProviders {
		scopes := make([]string, len(rp.Scopes))
		copy(scopes, rp.Scopes)
		cfg.oauthProviders = append(cfg.oauthProviders, OAuthProviderConfig{
			name:            name,
			authURL:         rp.AuthURL,
			tokenURL:        rp.TokenURL,
			clientIDEnv:     rp.ClientIDEnv,
			clientSecretEnv: rp.ClientSecretEnv,
			scopes:          scopes,
		})
	}

	// Parse OIDC config.
	if raw.Auth != nil && raw.Auth.OIDC != nil {
		ro := raw.Auth.OIDC
		validation := ro.Validation
		if validation == "" {
			validation = "jwt"
		}
		callerClaim := ro.CallerClaim
		if callerClaim == "" {
			callerClaim = "email"
		}
		defaultRoles := make([]string, len(ro.DefaultRoles))
		copy(defaultRoles, ro.DefaultRoles)
		allowedDomains := make([]string, len(ro.AllowedDomains))
		copy(allowedDomains, ro.AllowedDomains)
		cfg.oidc = &OIDCConfig{
			issuer:         ro.Issuer,
			audience:       ro.Audience,
			callerClaim:    callerClaim,
			validation:     validation,
			autoProvision:  ro.AutoProvision,
			defaultRoles:   defaultRoles,
			allowedDomains: allowedDomains,
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

	if raw.Server.TLS != nil {
		if raw.Server.TLS.CertFile == "" {
			return fmt.Errorf("config: server.tls.cert_file is required when TLS is configured")
		}
		if raw.Server.TLS.KeyFile == "" {
			return fmt.Errorf("config: server.tls.key_file is required when TLS is configured")
		}
		switch raw.Server.TLS.MinVersion {
		case "", "1.0", "1.1", "1.2", "1.3":
		default:
			return fmt.Errorf("config: server.tls.min_version must be 1.0, 1.1, 1.2, or 1.3, got %q", raw.Server.TLS.MinVersion)
		}
	}

	for _, u := range raw.Upstreams {
		if u.TLS != nil {
			if u.TLS.CertFile != "" && u.TLS.KeyFile == "" {
				return fmt.Errorf("config: upstream %q: tls.key_file is required when tls.cert_file is set", u.Name)
			}
			if u.TLS.KeyFile != "" && u.TLS.CertFile == "" {
				return fmt.Errorf("config: upstream %q: tls.cert_file is required when tls.key_file is set", u.Name)
			}
			if u.Transport == "stdio" {
				return fmt.Errorf("config: upstream %q: tls is not supported for stdio transport", u.Name)
			}
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

	if raw.RateLimits != nil {
		switch raw.RateLimits.Backend {
		case "", "local", "redis":
		default:
			return fmt.Errorf("config: rate_limits.backend must be \"local\" or \"redis\", got %q", raw.RateLimits.Backend)
		}
		if raw.RateLimits.Backend == "redis" {
			if raw.RateLimits.Redis == nil || raw.RateLimits.Redis.Address == "" {
				return fmt.Errorf("config: rate_limits.redis.address is required when backend is \"redis\"")
			}
		}
	}

	if raw.Health != nil {
		switch raw.Health.Store {
		case "", "local", "redis":
		default:
			return fmt.Errorf("config: health.store must be \"local\" or \"redis\", got %q", raw.Health.Store)
		}
		switch raw.Health.MissingStatus {
		case "", "healthy", "unhealthy":
		default:
			return fmt.Errorf("config: health.missing_status must be \"healthy\" or \"unhealthy\", got %q", raw.Health.MissingStatus)
		}
	}

	if raw.Auth != nil && raw.Auth.OIDC != nil {
		o := raw.Auth.OIDC
		if o.Issuer == "" {
			return fmt.Errorf("config: auth.oidc.issuer is required")
		}
		switch o.Validation {
		case "", "jwt", "userinfo":
		default:
			return fmt.Errorf("config: auth.oidc.validation must be \"jwt\" or \"userinfo\", got %q", o.Validation)
		}
		if (o.Validation == "" || o.Validation == "jwt") && o.Audience == "" {
			return fmt.Errorf("config: auth.oidc.audience is required for JWT validation")
		}
	}

	// Validate OAuth providers.
	for name, p := range raw.OAuthProviders {
		if p.AuthURL == "" {
			return fmt.Errorf("config: oauth_providers[%q]: auth_url is required", name)
		}
		if p.TokenURL == "" {
			return fmt.Errorf("config: oauth_providers[%q]: token_url is required", name)
		}
		if p.ClientIDEnv == "" {
			return fmt.Errorf("config: oauth_providers[%q]: client_id_env is required", name)
		}
		if p.ClientSecretEnv == "" {
			return fmt.Errorf("config: oauth_providers[%q]: client_secret_env is required", name)
		}
	}

	// Build a map of upstream auth types for cross-reference validation.
	oauthUpstreams := make(map[string]bool)
	for _, u := range raw.Upstreams {
		if u.Auth == nil {
			continue
		}
		switch u.Auth.Type {
		case "bearer", "":
			// OK — static token auth.
		case "oauth":
			if u.Auth.Provider == "" {
				return fmt.Errorf("config: upstream %q: auth.provider is required when auth.type is \"oauth\"", u.Name)
			}
			if _, ok := raw.OAuthProviders[u.Auth.Provider]; !ok {
				return fmt.Errorf("config: upstream %q: auth.provider %q not found in oauth_providers", u.Name, u.Auth.Provider)
			}
			oauthUpstreams[u.Name] = true
		default:
			return fmt.Errorf("config: upstream %q: auth.type must be \"bearer\" or \"oauth\", got %q", u.Name, u.Auth.Type)
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
			if oauthUpstreams[upstreamName] {
				return fmt.Errorf("config: roles[%q]: has credentials for upstream %q, but %q uses oauth auth (credentials are per-user, not per-role)", roleName, upstreamName, upstreamName)
			}
		}
	}

	return nil
}
