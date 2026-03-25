# Task 33 — Outbound OAuth core (config, token store, injection, flow endpoints)

Status: **done**

Depends on: **Task 32** (inbound OIDC — Stile must know who the user is before it can manage per-user upstream tokens)

## Goal

Stile acts as an OAuth client to obtain per-user tokens for upstream HTTP MCP servers. This task builds the core plumbing: config schema, token storage, per-request token injection, and the OAuth authorization code flow endpoints. After this task, the OAuth machinery works end-to-end — a user can connect an account and Stile injects their token into upstream requests. The admin UI and local testing infrastructure come in task 34.

Stdio MCP auth is explicitly out of scope — all target upstreams use HTTP transport.

## Background

Today, outbound auth is static bearer tokens loaded from env vars at startup, shared per-role. Many upstream MCPs require per-user credentials (e.g., a user's GitHub token, not a shared team token). Stile needs to broker OAuth flows: each user connects their accounts once, and Stile injects the right token into upstream requests based on who's calling.

This is the same pattern used by Coder (external auth), Zapier, Retool, and most MCP gateways (Envoy AI Gateway, mcp-auth-proxy, Cloudflare MCP Workers). The MCP spec explicitly prohibits forwarding the client's inbound token to upstreams (confused deputy prevention), so Stile must obtain separate upstream tokens via its own OAuth flows.

## Config design

### OAuth providers

New top-level `oauth_providers` section defining OAuth clients for upstream providers:

```yaml
oauth_providers:
  github:
    auth_url: https://github.com/login/oauth/authorize
    token_url: https://github.com/login/oauth/access_token
    client_id_env: GITHUB_OAUTH_CLIENT_ID
    client_secret_env: GITHUB_OAUTH_CLIENT_SECRET
    scopes: ["repo", "read:org"]

  notion:
    auth_url: https://api.notion.com/v1/oauth/authorize
    token_url: https://api.notion.com/v1/oauth/token
    client_id_env: NOTION_OAUTH_CLIENT_ID
    client_secret_env: NOTION_OAUTH_CLIENT_SECRET
    scopes: ["read_content", "update_content"]
```

- `_env` suffix for secrets — YAML holds the env var name, not the secret itself (consistent with existing `token_env` pattern).
- Each provider represents an OAuth client that Stile uses to perform the authorization code flow on behalf of users.

### Upstream auth reference

Upstreams reference an OAuth provider in their `auth` block:

```yaml
upstreams:
  - name: github
    transport: streamable-http
    url: https://github-mcp.example.com
    auth:
      type: oauth
      provider: github          # references oauth_providers.github

  - name: internal-api
    transport: streamable-http
    url: https://internal-mcp.example.com
    auth:
      type: bearer
      token_env: INTERNAL_API_TOKEN   # static token, as today
```

- `type: oauth` — per-user tokens from the token store. Stile looks up the calling user's token for this provider at request time.
- `type: bearer` — static token from env var, same as today.
- If no `auth` block, the upstream doesn't require auth.

### Config validation (conflict prevention)

When an upstream has `auth.type: oauth`:
- Any role with a `credentials` entry for that upstream is a **config parse error**. OAuth credentials are per-user, not per-role.
- Error example: `config: roles["developer"]: has credentials for upstream "github", but "github" uses oauth auth (credentials are per-user, not per-role)`

When an upstream has `auth.type: bearer`:
- Roles may define `credentials` for it (existing behavior).
- The upstream must NOT reference an `oauth_providers` entry.

This is validated in `convert()` alongside the existing cross-reference checks.

### Full config example

```yaml
server:
  address: ":8080"
  database:
    driver: postgres
    dsn: "postgres://stile:stile@postgres:5432/stile?sslmode=disable"

auth:
  oidc:
    issuer: https://accounts.google.com
    audience: "stile-client-id"
    caller_claim: email
    auto_provision: true
    default_roles: ["developer"]
    allowed_domains: ["yourcompany.com"]

oauth_providers:
  github:
    auth_url: https://github.com/login/oauth/authorize
    token_url: https://github.com/login/oauth/access_token
    client_id_env: GITHUB_OAUTH_CLIENT_ID
    client_secret_env: GITHUB_OAUTH_CLIENT_SECRET
    scopes: ["repo", "read:org"]

upstreams:
  - name: github
    transport: streamable-http
    url: https://github-mcp.example.com
    auth:
      type: oauth
      provider: github

  - name: filesystem
    transport: stdio
    command: ["npx", "-y", "@modelcontextprotocol/server-filesystem"]

roles:
  developer:
    allowed_tools: ["github/*", "filesystem/*"]
    rate_limit: "500/min"

  reader:
    allowed_tools: ["get_*", "list_*", "search_*"]
    rate_limit: "200/min"
```

## Implementation plan

### 33.1 — OAuth provider config and token store

1. **Config**: Add `oauth_providers` top-level section and `auth.type: oauth` + `auth.provider` to upstream config. Validate cross-references: provider exists, no role credential conflicts with OAuth upstreams.
2. **Token store**: New DB table `user_oauth_tokens(user, provider, access_token, refresh_token, token_type, expiry, scopes, created_at, updated_at)`. Tokens encrypted at rest. CRUD operations via a `TokenStore` interface.
3. **Token refresh**: On token lookup, check expiry. If expired (or near-expiry), use refresh token to get a new access token from the provider's token endpoint. Update the stored tokens. If refresh fails, surface the error clearly.
4. **Tests**: Token store CRUD, refresh flow with mock provider.

**Files**:
- Create: `internal/auth/tokenstore.go`, `internal/auth/tokenstore_test.go`
- Modify: `internal/config/config.go` (add `oauth_providers`, extend upstream auth, validation)
- DB migration for `user_oauth_tokens` table

### 33.2 — Per-request token injection

1. Modify `HTTPTransport.RoundTrip` to accept a per-request token (from context) instead of only the static token set at construction time. When an upstream has `auth.type: oauth`, look up the user's token from the store and inject it as the `Authorization: Bearer` header.
2. If a user hasn't connected a required provider, return a clear JSON-RPC error: `"user has not connected github — visit https://stile.example.com/connections"`.
3. **Tests**: Injection into upstream requests, error on missing connection, static bearer auth unchanged.

**Files**:
- Modify: `internal/transport/http.go` (per-request token injection)
- Modify: `internal/proxy/proxy.go` (pass user identity through to transport for token lookup)

### 33.3 — OAuth authorization code flow endpoints

Add the HTTP endpoints that handle the user-facing OAuth flow.

1. **`GET /oauth/connect/{provider}`** — Starts the OAuth authorization code flow. Generates a random `state` parameter (tied to the user's session), stores it, and redirects the user's browser to the provider's `auth_url` with `client_id`, `redirect_uri`, `scope`, and `state`.
2. **`GET /oauth/callback`** — Handles the provider's redirect. Validates `state` (CSRF protection), exchanges the authorization `code` for tokens via the provider's `token_url`, stores the tokens in the token store, and shows a success page (or redirects back to the connections UI).
3. **Session handling**: The user must be authenticated (via OIDC or API key) to initiate a connection. The `state` parameter encodes which user is connecting.
4. **PKCE**: Implement Proof Key for Code Exchange (required by OAuth 2.1, recommended for all flows).
5. **Tests**: Full flow with mock OAuth provider, CSRF validation, PKCE verification, error handling for denied consent / invalid codes.

**Files**:
- Create: `internal/auth/oauth_handler.go`, `internal/auth/oauth_handler_test.go`
- Modify: `cmd/gateway/main.go` (register OAuth endpoints)

## What this does NOT include

- **Connections UI, admin API, and local testing infrastructure** — see task 34.
- **Stdio MCP auth** — injecting per-user credentials into stdio processes (env vars, CLI args, etc.). Deferred until we have stdio MCPs that need it.
- **Client credentials grant** — service-to-service OAuth where Stile authenticates as itself, not on behalf of a user. Could be a future subtask.
- **MCP spec OAuth flow participation** — the MCP spec defines an OAuth mechanism for HTTP transport where the server challenges the client. We're bypassing this by injecting tokens directly. Supporting the spec's challenge/response flow could be a future enhancement.
- **Token exchange (RFC 8693)** or on-behalf-of flows.
