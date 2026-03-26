# Outbound OAuth Setup

Stile can inject per-user OAuth tokens into upstream requests. When an upstream requires OAuth, each user connects their account through the browser once, and Stile stores and refreshes their tokens automatically.

This guide covers configuration, the user flow, and admin tools.

## How it works

1. An upstream is configured with `auth.type: oauth` pointing to a named provider.
2. When a user's request hits that upstream, Stile looks up their stored token for that provider.
3. If found, the token is injected as `Authorization: Bearer <token>` on the upstream request.
4. If the token is expired, Stile refreshes it automatically using the stored refresh token.
5. If no token exists, the request fails with a clear error telling the user to connect.

## Configuration

### 1. Define an OAuth provider

Add an entry under `oauth_providers` in your Stile config:

```yaml
oauth_providers:
  github:
    auth_url: https://github.com/login/oauth/authorize
    token_url: https://github.com/login/oauth/access_token
    client_id_env: GITHUB_OAUTH_CLIENT_ID
    client_secret_env: GITHUB_OAUTH_CLIENT_SECRET
    scopes: ["repo", "read:user"]
```

- `auth_url` / `token_url` — the provider's standard OAuth 2.0 endpoints.
- `client_id_env` / `client_secret_env` — names of environment variables holding the OAuth app credentials. Stile reads these at startup; the actual secrets never appear in config files.
- `scopes` — requested OAuth scopes.

You can define multiple providers (e.g. `github`, `google`, `slack`).

### 2. Register an OAuth app with the provider

Create an OAuth application in the provider's developer settings. Set the **redirect URI** to:

```
{STILE_BASE_URL}/oauth/callback
```

For example: `https://stile.example.com/oauth/callback`

Set the `STILE_BASE_URL` environment variable to match the externally-reachable URL of your Stile instance (no trailing slash). This is the URL users' browsers will be redirected back to after authorizing.

### 3. Point an upstream at the provider

```yaml
upstreams:
  - name: github-tools
    transport: streamable-http
    url: http://github-mcp:9090/mcp
    auth:
      type: oauth
      provider: github   # must match a key in oauth_providers
```

Any upstream with `auth.type: oauth` will use per-user token injection instead of a static bearer token.

### 4. Set environment variables

```bash
export GITHUB_OAUTH_CLIENT_ID="your-client-id"
export GITHUB_OAUTH_CLIENT_SECRET="your-client-secret"
export STILE_BASE_URL="https://stile.example.com"
```

## User flow

### Browser-based (standard)

1. User visits the admin dashboard at `/admin/ui/connections`.
2. They enter their username (email) and click **Look up**.
3. For each unconfigured provider, a **Connect** button links to `/oauth/connect/{provider}`.
4. Clicking it redirects to the provider's authorization page.
5. After the user approves, the provider redirects back to `/oauth/callback`.
6. Stile exchanges the authorization code for tokens (using PKCE) and stores them.
7. The user sees a "Connected!" confirmation page.

From this point, all the user's MCP requests to that upstream will include their personal token.

### Programmatic (admin escape hatch)

For scripted testing, CI, or recovery, admins can insert tokens directly:

```bash
curl -X PUT https://stile.example.com/admin/connections/github \
  -H "Authorization: Bearer $ADMIN_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"caller":"alice@example.com","access_token":"gho_xxxx","refresh_token":"ghr_xxxx"}'
```

This bypasses the browser flow entirely.

## Admin API

All endpoints require admin authentication (`Authorization: Bearer $ADMIN_API_KEY`).

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/admin/connections?caller=alice@example.com` | List all providers and connection status for a user |
| `PUT` | `/admin/connections/{provider}` | Insert/overwrite a token (body: `caller`, `access_token`, optional `refresh_token`) |
| `DELETE` | `/admin/connections/{provider}?caller=alice@example.com` | Disconnect: delete stored token |

## Token lifecycle

- **Storage** — tokens are stored in the same database as callers (SQLite or Postgres), in the `user_oauth_tokens` table keyed by `(user_name, provider)`.
- **Refresh** — when a token is within 30 seconds of expiry, Stile automatically refreshes it using the provider's token endpoint before injecting it. If refresh fails, the stale token is tried anyway (some providers accept recently-expired tokens).
- **Revocation** — deleting a connection removes the token from the store. Stile does not call the provider's revocation endpoint; if you need that, revoke via the provider's UI.

## Local testing

The `docker-compose.scaling.yml` stack includes a fully self-contained mock OAuth setup:

- **mock-oauth** (port 9100) — a minimal OAuth provider that auto-approves and issues deterministic tokens.
- **mock-oauth-upstream** (port 9101) — an MCP server with a `whoami` tool that echoes the `Authorization` header it received.

```bash
export ADMIN_API_KEY="pick-a-secret"
export GITHUB_PERSONAL_ACCESS_TOKEN="$(gh auth token)"
docker compose -f docker-compose.scaling.yml up --build
```

Then run the automated test:

```bash
scripts/test-oauth-flow.sh
```

Or test manually — see the step-by-step guide in the [task 34 doc](tasks/task-34.md).

## Security notes

- PKCE (S256) is always used for the authorization code exchange.
- OAuth state parameters are single-use, stored in memory, and expire after 10 minutes.
- Client secrets are read from environment variables, never stored in config files.
- Access tokens in the database are stored in plaintext (they must be usable). Protect the database accordingly — use Postgres with TLS in production, or encrypt at rest.
