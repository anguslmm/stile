package admin

import (
	"fmt"
	"time"

	"github.com/anguslmm/stile/internal/config"
)

// ConfigView is a JSON-safe, sanitized representation of the running config.
// Sensitive values (passwords, DSNs, TLS key paths) are omitted.
type ConfigView struct {
	Server    serverView    `json:"server"`
	Upstreams []upstreamView `json:"upstreams"`
	Roles     []roleView    `json:"roles"`
	RateLimits rateLimitView `json:"rate_limits"`
	Logging   loggingView   `json:"logging"`
	Audit     auditView     `json:"audit"`
	Telemetry telemetryView `json:"telemetry"`
	Health    healthView    `json:"health"`
}

type serverView struct {
	Address         string `json:"address"`
	ToolCacheTTL    string `json:"tool_cache_ttl,omitempty"`
	ShutdownTimeout string `json:"shutdown_timeout"`
	Database        string `json:"database_driver,omitempty"`
	TLS             bool   `json:"tls"`
	MTLS            bool   `json:"mtls"`
	AuthCacheTTL    string `json:"auth_cache_ttl,omitempty"`
}

type upstreamView struct {
	Name           string `json:"name"`
	Transport      string `json:"transport"`
	URL            string `json:"url,omitempty"`
	Command        string `json:"command,omitempty"`
	AuthType       string `json:"auth_type,omitempty"`
	AuthTokenEnv   string `json:"auth_token_env,omitempty"`
	TLS            bool   `json:"tls,omitempty"`
	RateLimit      string `json:"rate_limit,omitempty"`
	Timeout        string `json:"timeout,omitempty"`
	CircuitBreaker bool   `json:"circuit_breaker,omitempty"`
	Retry          bool   `json:"retry,omitempty"`
}

type roleView struct {
	Name          string            `json:"name"`
	AllowedTools  []string          `json:"allowed_tools"`
	Credentials   map[string]string `json:"credentials,omitempty"`
	RateLimit     string            `json:"rate_limit,omitempty"`
	ToolRateLimit string            `json:"tool_rate_limit,omitempty"`
}

type rateLimitView struct {
	Backend         string `json:"backend"`
	DefaultCaller   string `json:"default_caller,omitempty"`
	DefaultTool     string `json:"default_tool,omitempty"`
	DefaultUpstream string `json:"default_upstream,omitempty"`
}

type loggingView struct {
	Level  string `json:"level"`
	Format string `json:"format"`
}

type auditView struct {
	Enabled bool   `json:"enabled"`
	Driver  string `json:"driver,omitempty"`
}

type telemetryView struct {
	TracingEnabled bool    `json:"tracing_enabled"`
	Endpoint       string  `json:"endpoint,omitempty"`
	SampleRate     float64 `json:"sample_rate,omitempty"`
}

type healthView struct {
	Store         string `json:"store"`
	CheckInterval string `json:"check_interval"`
	MissingStatus string `json:"missing_status,omitempty"`
}

// NewConfigView produces a sanitized, JSON-safe view of the running config.
func NewConfigView(cfg *config.Config) ConfigView {
	sv := serverView{
		Address:         cfg.Server().Address(),
		ShutdownTimeout: cfg.Server().ShutdownTimeout().String(),
		Database:        cfg.Server().Database().Driver(),
	}
	if ttl := cfg.Server().ToolCacheTTL(); ttl > 0 {
		sv.ToolCacheTTL = ttl.String()
	}
	if ttl := cfg.Server().AuthCacheTTL(); ttl > 0 {
		sv.AuthCacheTTL = ttl.String()
	}
	if tls := cfg.Server().TLS(); tls != nil {
		sv.TLS = true
		sv.MTLS = tls.ClientCAFile() != ""
	}

	upstreams := make([]upstreamView, len(cfg.Upstreams()))
	for i, u := range cfg.Upstreams() {
		uv := upstreamView{
			Name:    u.Name(),
			Timeout: formatDuration(u.Timeout()),
		}
		if rl := u.RateLimit(); rl != nil {
			uv.RateLimit = formatRateLimit(rl)
		}
		uv.CircuitBreaker = u.CircuitBreaker() != nil
		uv.Retry = u.Retry() != nil

		switch c := u.(type) {
		case *config.HTTPUpstreamConfig:
			uv.Transport = "streamable-http"
			uv.URL = c.URL()
			if auth := c.Auth(); auth != nil {
				uv.AuthType = auth.Type()
				uv.AuthTokenEnv = auth.TokenEnv()
			}
			uv.TLS = c.TLS() != nil
		case *config.StdioUpstreamConfig:
			uv.Transport = "stdio"
			cmd := c.Command()
			if len(cmd) > 0 {
				uv.Command = cmd[0]
			}
		}
		upstreams[i] = uv
	}

	roles := make([]roleView, len(cfg.Roles()))
	for i, r := range cfg.Roles() {
		rv := roleView{
			Name:         r.Name(),
			AllowedTools: r.AllowedTools(),
			Credentials:  r.Credentials(),
		}
		if rl := r.RateLimit(); rl != nil {
			rv.RateLimit = formatRateLimit(rl)
		}
		if rl := r.ToolRateLimit(); rl != nil {
			rv.ToolRateLimit = formatRateLimit(rl)
		}
		roles[i] = rv
	}

	rlv := rateLimitView{
		Backend: cfg.RateLimitBackend(),
	}
	defaults := cfg.RateLimitDefaults()
	if rl := defaults.DefaultCaller(); rl != nil {
		rlv.DefaultCaller = formatRateLimit(rl)
	}
	if rl := defaults.DefaultTool(); rl != nil {
		rlv.DefaultTool = formatRateLimit(rl)
	}
	if rl := defaults.DefaultUpstream(); rl != nil {
		rlv.DefaultUpstream = formatRateLimit(rl)
	}

	lv := loggingView{
		Level:  cfg.Logging().Level(),
		Format: cfg.Logging().Format(),
	}

	av := auditView{
		Enabled: cfg.Audit().Enabled(),
	}
	if cfg.Audit().Enabled() {
		av.Driver = cfg.Audit().DatabaseConfig().Driver()
	}

	tv := telemetryView{
		TracingEnabled: cfg.Telemetry().Traces().Enabled(),
	}
	if cfg.Telemetry().Traces().Enabled() {
		tv.Endpoint = cfg.Telemetry().Traces().Endpoint()
		tv.SampleRate = cfg.Telemetry().Traces().SampleRate()
	}

	hv := healthView{
		Store:         cfg.Health().Store(),
		CheckInterval: cfg.Health().CheckInterval().String(),
		MissingStatus: cfg.Health().MissingStatus(),
	}

	return ConfigView{
		Server:    sv,
		Upstreams: upstreams,
		Roles:     roles,
		RateLimits: rlv,
		Logging:   lv,
		Audit:     av,
		Telemetry: tv,
		Health:    hv,
	}
}

func formatDuration(d time.Duration) string {
	if d == 0 {
		return ""
	}
	return d.String()
}

func formatRateLimit(rl *config.RateLimit) string {
	if rl == nil {
		return ""
	}
	// Reconstruct a human-readable form.
	switch rl.Window() {
	case 1:
		return fmt.Sprintf("%d/sec", rl.Count())
	case 60:
		return fmt.Sprintf("%d/min", rl.Count())
	case 3600:
		return fmt.Sprintf("%d/hour", rl.Count())
	default:
		return fmt.Sprintf("%.2f/sec", rl.Rate())
	}
}
