// Package config loads and validates Stile gateway configuration from YAML.
package config

import (
	"fmt"
	"os"
	"time"

	"github.com/gobwas/glob"
	"gopkg.in/yaml.v3"
)

// Config is immutable after construction via Load.
type Config struct {
	server    serverConfig
	upstreams []UpstreamConfig
	roles     []RoleConfig
}

// Server returns the server configuration.
func (c *Config) Server() ServerConfig {
	return ServerConfig{address: c.server.address, toolCacheTTL: c.server.toolCacheTTL, dbPath: c.server.dbPath}
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

// ServerConfig provides read-only access to server settings.
type ServerConfig struct {
	address      string
	toolCacheTTL time.Duration
	dbPath       string
}

// Address returns the listen address (e.g. ":8080").
func (s ServerConfig) Address() string { return s.address }

// ToolCacheTTL returns how often tool schemas are refreshed from upstreams.
// Default: 5 minutes.
func (s ServerConfig) ToolCacheTTL() time.Duration { return s.toolCacheTTL }

// DBPath returns the path to the SQLite database for caller storage.
func (s ServerConfig) DBPath() string { return s.dbPath }

type serverConfig struct {
	address      string
	toolCacheTTL time.Duration
	dbPath       string
}

// RoleConfig provides read-only access to a role's settings.
type RoleConfig struct {
	name         string
	allowedTools []string          // glob patterns
	credentials  map[string]string // upstream name → env var name
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

// UpstreamConfig provides read-only access to an upstream's settings.
type UpstreamConfig struct {
	name      string
	url       string
	command   []string
	transport string
	auth      *AuthConfig
	tools     []string
}

func (u *UpstreamConfig) Name() string    { return u.name }
func (u *UpstreamConfig) URL() string     { return u.url }
func (u *UpstreamConfig) Transport() string { return u.transport }
func (u *UpstreamConfig) Auth() *AuthConfig { return u.auth }

// Command returns a copy of the command slice.
func (u *UpstreamConfig) Command() []string {
	if u.command == nil {
		return nil
	}
	out := make([]string, len(u.command))
	copy(out, u.command)
	return out
}

// Tools returns a copy of the tools slice.
func (u *UpstreamConfig) Tools() []string {
	if u.tools == nil {
		return nil
	}
	out := make([]string, len(u.tools))
	copy(out, u.tools)
	return out
}

// AuthConfig provides read-only access to upstream auth settings.
type AuthConfig struct {
	authType string
	tokenEnv string
}

func (a *AuthConfig) Type() string     { return a.authType }
func (a *AuthConfig) TokenEnv() string { return a.tokenEnv }

// --- raw types for YAML unmarshaling ---

type rawRoleConfig struct {
	AllowedTools []string          `yaml:"allowed_tools"`
	Credentials  map[string]string `yaml:"credentials"`
}

type rawConfig struct {
	Server    rawServerConfig          `yaml:"server"`
	Upstreams []rawUpstreamConfig      `yaml:"upstreams"`
	Roles     map[string]rawRoleConfig `yaml:"roles"`

	// rolesOrdered preserves YAML key order for roles.
	// Populated by Load/LoadBytes before convert is called.
	rolesOrdered []string
}

type rawServerConfig struct {
	Address      string `yaml:"address"`
	ToolCacheTTL string `yaml:"tool_cache_ttl"`
	DBPath       string `yaml:"db_path"`
}

type rawUpstreamConfig struct {
	Name      string        `yaml:"name"`
	URL       string        `yaml:"url"`
	Command   []string      `yaml:"command"`
	Transport string        `yaml:"transport"`
	Auth      *rawAuthConfig `yaml:"auth"`
	Tools     []string      `yaml:"tools"`
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

	cfg.upstreams = make([]UpstreamConfig, len(raw.Upstreams))
	for i, ru := range raw.Upstreams {
		u := UpstreamConfig{
			name:      ru.Name,
			url:       ru.URL,
			transport: ru.Transport,
		}
		if ru.Command != nil {
			u.command = make([]string, len(ru.Command))
			copy(u.command, ru.Command)
		}
		if ru.Auth != nil {
			u.auth = &AuthConfig{
				authType: ru.Auth.Type,
				tokenEnv: ru.Auth.TokenEnv,
			}
		}
		if ru.Tools != nil {
			u.tools = make([]string, len(ru.Tools))
			copy(u.tools, ru.Tools)
		}
		cfg.upstreams[i] = u
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
		cfg.roles = append(cfg.roles, rc)
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
