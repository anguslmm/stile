# Task 32 — Inbound OIDC authentication

Status: **done**

## Goal

Add OIDC as an inbound auth strategy so callers can authenticate to Stile with OIDC tokens (e.g., Google SSO via Coder external auth). API key auth remains active alongside OIDC — both produce the same `Caller` struct and the rest of the pipeline is unaware of which method was used.

## Background

Today, inbound auth is API-key-only (SHA-256 hashed, looked up in the DB). For deployment inside Coder, users authenticate via Google SSO. We want Stile to accept OIDC tokens so it can be registered as an external auth provider in Coder (or similar platforms). Users authenticate once in the platform UI; all their workspaces and agents are automatically authenticated to Stile.

Stile is not tied to Coder — any platform that can perform an OIDC flow and pass the resulting token to Stile will work.

## Coder integration

The authorization server is the OIDC provider (e.g., Google), not Stile. You register a Google OAuth client specifically for Stile, then configure it as an external auth provider in Coder:

```bash
CODER_EXTERNAL_AUTH_0_ID=stile
CODER_EXTERNAL_AUTH_0_TYPE=oidc
CODER_EXTERNAL_AUTH_0_CLIENT_ID=<google-oauth-client-id-for-stile>
CODER_EXTERNAL_AUTH_0_CLIENT_SECRET=<secret>
CODER_EXTERNAL_AUTH_0_AUTH_URL=https://accounts.google.com/o/oauth2/v2/auth
CODER_EXTERNAL_AUTH_0_TOKEN_URL=https://oauth2.googleapis.com/token
CODER_EXTERNAL_AUTH_0_SCOPES=openid,email
```

Users see a "Connect to Stile" prompt in the Coder UI (same as the GitHub external auth prompt). They click it, consent via Google, done. In workspaces, the token is retrieved via:

```bash
TOKEN=$(coder external-auth access-token stile)
# Use as Bearer token to Stile
```

This is the same pattern as GitHub external auth that lets Coder clone repos — Coder handles the OAuth flow, stores the tokens, and makes them available to workspaces.

### End-to-end flow for agent tasks

1. User opens Coder and does the "Connect to Stile" OAuth flow (typically prompted on workspace creation; persists across workspaces until the token is revoked or expires).
2. User launches an agent task.
3. The agent task environment (provisioned by the Coder template) retrieves the token and configures the MCP client. For example, the template startup script sets:
   ```bash
   export STILE_TOKEN=$(coder external-auth access-token stile)
   ```
   And the MCP config references it:
   ```json
   {
     "mcpServers": {
       "stile": {
         "url": "https://stile.example.com/mcp",
         "headers": {
           "Authorization": "Bearer ${STILE_TOKEN}"
         }
       }
     }
   }
   ```
4. Claude Code (or any MCP client) uses Stile as its only MCP — all tool calls go through Stile, authenticated with the user's token.

**Token refresh for long-running tasks**: The token Coder stores does expire. Coder handles refresh automatically if it has a refresh token, and `coder external-auth access-token` always returns a fresh token. For long-running agent tasks, the MCP client config should call the command per-connection rather than caching the value in an env var at startup. This is a Coder template concern, not a Stile concern — Stile just validates whatever token it receives.

## Token validation modes

Different OIDC providers return different types of access tokens:

- **JWT access tokens** (Keycloak, Auth0, Okta): Self-contained, can be validated locally by checking the signature against the provider's JWKS. No network call needed per request.
- **Opaque access tokens** (Google): Opaque strings that must be validated by calling the provider's userinfo endpoint (e.g., `https://www.googleapis.com/oauth2/v3/userinfo`). This is a network call per auth (cacheable with a short TTL).

Since Google (our primary IdP) issues opaque access tokens, Stile must support both modes. Note: Coder's `external-auth access-token` returns the OAuth access token, not the OIDC ID token — so even though Google returns a JWT ID token during the OAuth flow, what Stile receives from the workspace is the opaque access token.

## Config design

New top-level `auth` section:

```yaml
auth:
  oidc:
    issuer: https://accounts.google.com
    audience: "stile-client-id"
    caller_claim: email                 # JWT claim / userinfo field that maps to the Stile caller name
    validation: userinfo                # "jwt" (default) or "userinfo"
    auto_provision: true                # create caller on first OIDC login
    default_roles: ["developer"]        # roles assigned to auto-provisioned callers
    allowed_domains: ["yourcompany.com"] # optional: reject tokens from other domains
```

- `issuer` — OIDC provider's issuer URL. Stile fetches `{issuer}/.well-known/openid-configuration` for JWKS and metadata.
- `audience` — expected `aud` claim in the JWT (the OAuth client ID registered with the provider). Used in JWT mode; ignored in userinfo mode.
- `caller_claim` — which JWT claim (or userinfo field) becomes the caller name. Typically `email` or `sub`.
- `validation` — how Stile validates incoming tokens:
  - `jwt` (default): Validate the JWT locally — verify signature against JWKS, check `iss`, `aud`, `exp`. Use this for providers that issue JWT access tokens (Keycloak, Auth0, Okta).
  - `userinfo`: Validate by calling the provider's userinfo endpoint with the token. Use this for providers that issue opaque access tokens (Google). Results are cached per-token with a short TTL to avoid hammering the provider.
- `auto_provision` — if true, Stile creates a caller record on first OIDC login. If false, the caller must be pre-registered via the admin API.
- `default_roles` — roles assigned to auto-provisioned callers.
- `allowed_domains` — if set, only tokens with an `email` claim/field matching these domains are accepted. Safety net for providers like Google where anyone can get a token.

API key auth remains active alongside OIDC. The authenticator checks if the token looks like a JWT (three dot-separated base64 segments) and JWT validation is configured — if so, validates via OIDC. In userinfo mode, all non-API-key tokens are validated via the userinfo endpoint. Falls back to API key hash lookup if OIDC validation fails or isn't configured.

## Horizontal scaling

JWT validation is stateless — each Stile instance validates tokens independently against the provider's JWKS. No shared state needed.

- **JWKS caching**: Each instance fetches and caches the provider's public keys independently. They all fetch from the same endpoint, so they converge. Key rotation is handled by re-fetching on unknown `kid`.
- **Userinfo caching**: Each instance maintains its own userinfo cache (short TTL, e.g., 5 minutes). No cross-instance coordination needed — cache misses just result in a call to the provider.
- **Auto-provisioning**: This is the one stateful operation. Two instances could see a new user simultaneously and both try to create a caller record. Use upsert semantics in Postgres (`INSERT ... ON CONFLICT DO NOTHING`) so the second write is a no-op. The existing cache invalidation mechanism (Postgres `LISTEN`/`NOTIFY` from task 29) propagates the new caller to other instances' auth caches.

No new shared state or infrastructure required beyond the existing Postgres database.

## Implementation plan

### 32.1 — Config and OIDC discovery

1. Add `auth.oidc` raw/exported config types with all fields above, including `validation` mode. Parse and validate in `convert()`.
2. Fetch `{issuer}/.well-known/openid-configuration` at startup. Cache the JWKS URI and userinfo endpoint.
3. For JWT mode: implement background JWKS refresh — keys rotate, so re-fetch on unknown `kid` plus periodic refresh with TTL.
4. For userinfo mode: implement a userinfo client with response caching (short TTL, keyed by token hash).
5. Validate that the provider's metadata supports the configured validation mode.

**Files**:
- Create: `internal/auth/oidc.go`
- Modify: `internal/config/config.go` (add `auth.oidc` config types + validation)

### 32.2 — Token validation and auth strategy

1. **JWT mode**: Verify signature against JWKS, check `iss`, `aud`, `exp` claims. Extract caller identity from `caller_claim`.
2. **Userinfo mode**: Call the provider's userinfo endpoint with the Bearer token. Extract caller identity from the response field matching `caller_claim`. Cache the response.
3. Refactor `Authenticator.Authenticate()` to try OIDC (based on configured validation mode), fall back to API key hash lookup. Both strategies produce the same `Caller` struct.
4. Domain filtering: if `allowed_domains` is set, verify the `email` claim/field matches before proceeding.

**Dependencies**: `github.com/coreos/go-oidc/v3` for OIDC discovery + JWT validation, or `github.com/golang-jwt/jwt/v5` for lighter-weight JWT-only validation. Userinfo validation uses stdlib `net/http`.

**Files**:
- Modify: `internal/auth/oidc.go`
- Modify: `internal/auth/auth.go` (refactor `Authenticate` to support multiple strategies)

### 32.3 — Auto-provisioning and wiring

1. If `auto_provision` is true and the caller doesn't exist in the DB, create a caller record with `default_roles`. Use upsert (`INSERT ... ON CONFLICT DO NOTHING`) for safe concurrent auto-provisioning across multiple instances.
2. Wire OIDC config into the authenticator at startup.
3. Tests: fake OIDC provider (httptest server issuing JWTs with test keys), JWT validation, userinfo validation with mock endpoint, expiry handling, auto-provisioning (including concurrent race), domain filtering, fallback to API key auth, unknown `kid` triggers JWKS refresh.

**Files**:
- Create: `internal/auth/oidc_test.go`
- Modify: `cmd/gateway/main.go` (wire OIDC config into authenticator)
- Modify: `internal/auth/CLAUDE.md` (document OIDC strategy)

## What this does NOT include

- **Outbound OAuth** — authenticating to upstream MCP servers on behalf of users. That's task 33.
- **Stile as an OAuth authorization server** — Stile only validates tokens; it doesn't issue them. The OIDC flow (login page, consent, token issuance) happens at the IdP.
- **Multiple OIDC providers** — single provider for now. Multiple provider support can be added later if needed.
