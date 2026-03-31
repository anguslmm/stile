# Task 34.1 — Fix OAuth connection UX and security

Status: **done**

## Problem

Three related issues make the OAuth connection flow broken or confusing for end users:

### 1. Browser auth is missing

`/oauth/connect/{provider}` is behind `Authenticator.Middleware`, which requires an API key or OIDC bearer token in the `Authorization` header. A human clicking a link in their browser can't attach that. The OAuth connection flow — which is inherently browser-based (redirect to provider → authorize → redirect back) — is unusable.

### 2. Unconnected OAuth tools show up and then fail

When a user calls `tools/list`, they see every tool including ones from OAuth-protected upstreams they haven't connected yet. When they try to use one, it fails with _"user has not connected mock — visit /oauth/connect/mock to authorize"_. This is confusing — the tool shouldn't have appeared in the first place. A user's tool list should only include tools they can actually use.

### 3. Callback endpoint security

`/oauth/callback` currently relies on the `state` parameter (cryptographically random, single-use, 10-minute expiry) plus PKCE (`code_verifier` only Stile knows) to prevent CSRF and code injection. This is the standard OAuth 2.1 pattern and is sound against external attackers. However, the callback is completely unauthenticated — any request with a valid state+code pair will store tokens. Audit this and document the threat model. If there are gaps (e.g. an authenticated user can initiate a flow and then have a different user's browser hit the callback with a leaked state), address them.

## User journey (target state)

1. User connects their MCP client (Claude, Cursor, etc.) to Stile. Their client authenticates via OIDC or API key.
2. `tools/list` returns only tools the user can actually use right now — OAuth-protected tools where the user hasn't connected the provider are **excluded**.
3. For excluded tools, the `tools/list` response (or a separate mechanism) tells the user which providers they need to connect and gives them a URL to do so.
4. User opens the connection URL in their browser. Stile identifies them without requiring them to manually paste a token.
5. They authorize with the provider (GitHub, etc.), get redirected back to Stile, and see a success page.
6. Next `tools/list` call now includes the newly available tools. The user can start using them immediately.

## Design

### Browser auth for `/oauth/connect/{provider}`

The connect endpoint needs to know who the user is so it can store their token after the callback. Options:

- **Short-lived signed URL**: When the MCP client or connections UI needs to send a user to the connect page, Stile generates a URL like `/oauth/connect/github?tok=<signed-short-lived-token>`. The token is an HMAC-signed payload containing the caller name and expiry (e.g. 5 minutes). No session state needed. The MCP error message or a dedicated `connections/status` method could return these URLs.
- **Session cookie from an authenticated endpoint**: An authenticated API call sets a short-lived cookie, then the browser uses that cookie for the connect page. Adds cookie/session infrastructure.

The signed URL approach is simpler (no sessions, no cookies, stateless) and fits naturally into the MCP flow where the client can present the URL to the user.

### Tool filtering for unconnected OAuth upstreams

In `proxy.HandleToolsList` and `proxy.FilteredTools`: after ACL filtering, also exclude tools from OAuth-protected upstreams where the current caller hasn't connected the required provider. This requires the proxy to have access to the `TokenStore` (or the `OAuthResolver`) to check connection status.

Additionally, include metadata about _why_ tools are hidden and how to connect. This could be:
- A separate `x-stile-pending-connections` field in the `tools/list` response listing providers the user needs to connect, with URLs.
- Or a dedicated JSON-RPC method like `stile/connectionStatus` that returns the user's connection state and connect URLs.

### Callback security audit

Document the threat model for `/oauth/callback`:
- **State parameter**: 32 bytes of `crypto/rand`, base64url-encoded. Single-use (deleted from map after consumption). 10-minute expiry. Stored in-memory only.
- **PKCE**: S256 code challenge sent to provider; code verifier stored server-side with the pending auth. Provider validates the verifier during code exchange.
- **Attack surface**: An attacker would need both a valid state and the corresponding authorization code from the provider. The state is only transmitted browser→provider→browser→Stile and never logged. The code is single-use at the provider.
- **Cross-user risk**: The state is bound to a specific caller name at creation time. Even if an attacker obtained a state+code pair, the token would be stored under the original user's name, not the attacker's.
- **Consider**: Adding the callback to the signed-URL mechanism or requiring the original browser session to complete the flow. Evaluate whether the current protections are sufficient or if defense-in-depth warrants additional checks.

## Implementation plan

### 34.1.1 — Signed connect URLs

1. Add a signing key (generated at startup or from config) for HMAC-SHA256.
2. Add a helper that generates signed connect URLs: `/oauth/connect/{provider}?tok=base64url(caller|expiry|hmac)`.
3. Modify `handleConnect` to accept auth from either the existing `Authenticator.Middleware` OR a valid signed token in the `tok` query parameter.
4. Remove the blanket `Authenticator.Middleware` wrapping of OAuth routes in `server.go`. Instead, auth logic moves into the handler itself.

### 34.1.2 — Filter unconnected OAuth tools from tools/list

1. Give the proxy `Handler` access to `TokenStore` and the upstream→provider mapping (from `OAuthResolver` or config).
2. In `HandleToolsList` / `FilteredTools`, after ACL filtering, check each tool's upstream: if it requires OAuth and the caller hasn't connected, exclude it.
3. Include connection metadata in the response (pending providers + signed connect URLs).

### 34.1.3 — Callback security hardening (if needed after audit)

1. Audit the current callback security per the analysis above.
2. If gaps are found, add mitigations (e.g. bind the callback to the originating browser via a cookie set during the connect step).

## Files likely affected

- `internal/server/server.go` — OAuth route wiring (remove blanket auth middleware)
- `internal/auth/oauth_handler.go` — Signed URL generation and verification, connect endpoint auth
- `internal/proxy/proxy.go` — Tool filtering for unconnected OAuth upstreams
- `internal/auth/oauth_resolver.go` — Possibly expose upstream→provider mapping for filtering
- New: signing key infrastructure (could be a small helper in `auth` package)
