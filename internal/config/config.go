// Package config loads and validates Stile gateway configuration from YAML.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is immutable after construction via Load.
type Config struct {
	server    serverConfig
	upstreams []UpstreamConfig
}

// Server returns the server configuration.
func (c *Config) Server() ServerConfig {
	return ServerConfig{address: c.server.address}
}

// Upstreams returns a copy of the upstream configurations.
func (c *Config) Upstreams() []UpstreamConfig {
	out := make([]UpstreamConfig, len(c.upstreams))
	copy(out, c.upstreams)
	return out
}

// ServerConfig provides read-only access to server settings.
type ServerConfig struct {
	address string
}

// Address returns the listen address (e.g. ":8080").
func (s ServerConfig) Address() string { return s.address }

type serverConfig struct {
	address string
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

type rawConfig struct {
	Server    rawServerConfig    `yaml:"server"`
	Upstreams []rawUpstreamConfig `yaml:"upstreams"`
}

type rawServerConfig struct {
	Address string `yaml:"address"`
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

	var raw rawConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("config: parse YAML: %w", err)
	}

	return convert(raw)
}

// LoadBytes parses and validates config from raw YAML bytes.
func LoadBytes(data []byte) (*Config, error) {
	var raw rawConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("config: parse YAML: %w", err)
	}

	return convert(raw)
}

func convert(raw rawConfig) (*Config, error) {
	if err := validate(raw); err != nil {
		return nil, err
	}

	cfg := &Config{
		server: serverConfig{
			address: raw.Server.Address,
		},
	}
	if cfg.server.address == "" {
		cfg.server.address = ":8080"
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

	return cfg, nil
}

func validate(raw rawConfig) error {
	if len(raw.Upstreams) == 0 {
		return fmt.Errorf("config: at least one upstream is required")
	}

	names := make(map[string]bool, len(raw.Upstreams))
	for i, u := range raw.Upstreams {
		if u.Name == "" {
			return fmt.Errorf("config: upstream[%d]: name is required", i)
		}
		if names[u.Name] {
			return fmt.Errorf("config: upstream %q: duplicate name", u.Name)
		}
		names[u.Name] = true

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

	return nil
}
