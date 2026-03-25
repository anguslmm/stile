# config

Loads and validates Stile gateway configuration from YAML. Returns valid objects or errors — no separate validation step.

## Key Exported Types

- **`Config`** — Top-level immutable config. All fields unexported; accessed via getters that return value types or copies.
- **`UpstreamConfig`** (interface) — Sealed interface for upstream config. Only two implementations exist in this package: `*HTTPUpstreamConfig` and `*StdioUpstreamConfig`. Type-switch on these to access transport-specific fields.
- **`HTTPUpstreamConfig`** — HTTP (streamable-http) upstream: URL, auth, TLS, resilience settings.
- **`StdioUpstreamConfig`** — Stdio upstream: command slice, env map, resilience settings.
- **`RoleConfig`** — Named role with glob-pattern `allowed_tools`, per-upstream credential env vars, and optional rate limit overrides.
- **`RateLimit`** — Parsed rate limit (e.g. `"100/min"`). Holds both token-bucket fields (`Rate`, `Burst`) and sliding-window fields (`Count`, `Window`).
- **`ServerConfig`** — Listen address, TLS, tool cache TTL, shutdown timeout, database, auth cache TTL.
- **`DatabaseConfig`** — Driver (`sqlite`/`postgres`) + DSN. Returned by `ServerConfig.Database()` and `AuditConfig.DatabaseConfig()`.
- **`CircuitBreakerConfig`**, **`RetryConfig`** — Per-upstream resilience settings; nil if not configured.
- **`ServerTLSConfig`**, **`UpstreamTLSConfig`** — Inbound and outbound TLS settings respectively.
- **`RedisConfig`** — Shared Redis settings used by both rate limiting and health backends.
- **`OIDCConfig`** — OIDC authentication settings: issuer, audience, caller_claim, validation mode (jwt/userinfo), auto_provision, default_roles, allowed_domains. Returned by `Config.OIDC()` (nil when not configured). Slice getters return copies.
- **`RateLimitDefaults`**, **`LoggingConfig`**, **`AuditConfig`**, **`TelemetryConfig`**, **`TracesConfig`**, **`HealthConfig`** — Subsystem config value types returned by `Config` getters.

## Key Exported Functions

- **`Load(path string) (*Config, error)`** — Reads a YAML file, parses, and validates. Primary entry point.
- **`LoadBytes(data []byte) (*Config, error)`** — Same but from raw bytes. Used in tests.
- **`ParseRateLimit(s string) (RateLimit, error)`** — Parses `"N/sec"`, `"N/min"`, or `"N/hour"`.
- **`NewStdioUpstreamConfig(...)`** — Programmatic constructor for stdio upstreams (used in tests/admin).
- **`NewDatabaseConfig(driver, dsn string) DatabaseConfig`** — Programmatic constructor for database config.

## Design Decisions

- **Dual raw/exported type split:** All YAML unmarshaling goes into unexported `raw*` types. A `convert()` function then validates and builds the immutable exported types. This keeps YAML tags out of the public API and ensures you cannot hold an invalid config.
- **Role order preserved:** `LoadBytes` uses a second YAML AST parse (`extractRoleOrder`) to recover YAML map key order, since Go maps don't preserve insertion order. Roles are stored as a slice in document order.
- **`UpstreamConfig` is sealed:** The unexported `upstreamConfig()` marker method prevents external implementations. Callers must type-switch on `*HTTPUpstreamConfig` / `*StdioUpstreamConfig`.
- **`server.db_path` is deprecated** in favour of `server.database`; both cannot be set simultaneously. `db_path` is silently promoted to a `DatabaseConfig{driver: "sqlite"}`.
- **Health Redis falls back** to `rate_limits.redis` if `health.redis` is not set explicitly.
- **Compile-time interface checks** are present for both upstream config implementations.
