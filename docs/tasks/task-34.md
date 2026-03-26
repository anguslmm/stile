# Task 34 — Connections UI and local OAuth testing infrastructure

Status: **done**

Depends on: **Task 33** (outbound OAuth core — token store, injection, and flow endpoints must exist)

## Goal

Make outbound OAuth visible and verifiable. Add the admin dashboard UI for managing connections, the admin API endpoints (including a token insertion escape hatch), and a fully self-contained local testing stack with mock OAuth provider, token-echo upstream, and an end-to-end test script.

After this task, you can `docker compose up`, open a browser, connect a mock account, make an MCP request, and see the per-user token arrive at the upstream — all without any external accounts.

## Implementation plan

### 34.1 — Connections admin API

Add API endpoints to the admin handler for managing OAuth connections.

1. **`GET /admin/connections`** — List all connections for a user (or all users, for admins). Returns provider name, connection status, token expiry, and scopes for each.
2. **`DELETE /admin/connections/{provider}`** — Disconnect: revoke and delete the stored token for the calling user and provider.
3. **`PUT /admin/connections/{provider}`** — Admin-only: insert/overwrite a token for a specific caller. Accepts `{"caller": "...", "access_token": "...", "refresh_token": "..."}`. This is the scripted-testing and admin-recovery escape hatch — if a user's OAuth flow breaks, an admin can manually set a token.
4. **Tests**: CRUD operations, admin-only enforcement on PUT, listing with mixed connected/unconnected providers.

**Files**:
- Modify: `internal/admin/handler.go` (add connections endpoints)
- Create: `internal/admin/connections_test.go`

### 34.2 — Connections dashboard page

Add a user-facing page in the admin dashboard where users can see and manage their connected accounts.

1. **Connections page**: List all configured OAuth providers. For each, show whether the current user has a valid connection (with expiry info). "Connect" button starts the OAuth flow (`/oauth/connect/{provider}`). "Disconnect" button calls the delete endpoint.
2. **Integration with admin dashboard**: Add a "Connections" nav item to the existing embedded web UI (task 30).

**Files**:
- Modify: `internal/admin/ui/` (add connections page template, update nav)

### 34.3 — Mock OAuth provider

A small Go server (`scripts/mock-oauth-provider.go`) that implements the authorization code flow for local testing:

- **`GET /authorize`** — Renders a minimal consent page that auto-submits after 1 second (or instantly with `?auto=true`). Validates `state`, `redirect_uri`, and PKCE `code_challenge`. Issues a random authorization code.
- **`POST /token`** — Exchanges authorization code for tokens. Returns a deterministic access token that encodes the user (e.g., `mock-token-alice@example.com`), a refresh token, and a 1-hour expiry.
- **`POST /token` (refresh)** — Accepts `grant_type=refresh_token`, returns a new access token.

This mirrors the same auto-approve pattern from Dex's password grant but for the redirect-based authorization code flow.

**Files**:
- Create: `scripts/mock-oauth-provider.go`

### 34.4 — Token-echo upstream

A mock MCP upstream (`scripts/mock-oauth-upstream.go`, or extend `scripts/fake-upstream.go`) that:

- Responds to `tools/list` with a single tool: `whoami`.
- On `tools/call` for `whoami`, returns the `Authorization` header it received. This lets you verify that token injection is working end-to-end — the token you see in the response should be the per-user token Stile retrieved from the store, not the inbound OIDC token.

**Files**:
- Create: `scripts/mock-oauth-upstream.go`

### 34.5 — Docker compose and config wiring

Add the mock services to `docker-compose.scaling.yml` and update the scaling config.

Add to `docker-compose.scaling.yml`:

```yaml
  # Mock OAuth provider for testing outbound OAuth flow
  mock-oauth:
    build: .
    command: ["go", "run", "scripts/mock-oauth-provider.go", "-port", "9100"]
    ports:
      - "9100:9100"

  # Mock upstream that echoes the token it receives
  mock-oauth-upstream:
    build: .
    command: ["go", "run", "scripts/mock-oauth-upstream.go", "-port", "9101"]
```

Update `configs/scaling.yaml` to add:

```yaml
oauth_providers:
  mock:
    auth_url: http://mock-oauth:9100/authorize
    token_url: http://mock-oauth:9100/token
    client_id_env: MOCK_OAUTH_CLIENT_ID       # "mock-client"
    client_secret_env: MOCK_OAUTH_CLIENT_SECRET # "mock-secret"
    scopes: ["read", "write"]

upstreams:
  - name: mock-oauth-tools
    transport: streamable-http
    url: http://mock-oauth-upstream:9101/mcp
    auth:
      type: oauth
      provider: mock
```

Dex stays unchanged — it handles inbound OIDC only.

**Files**:
- Modify: `docker-compose.scaling.yml`
- Modify: `configs/scaling.yaml`

### 34.6 — End-to-end test script

`scripts/test-oauth-flow.sh` — exercises both the scripted and browser flows:

```bash
#!/usr/bin/env bash
# Test the outbound OAuth flow end-to-end.
# Requires: docker-compose.scaling.yml running.

# 1. Get OIDC token for alice (existing pattern)
OIDC_TOKEN=$(scripts/get-oidc-token.sh alice@example.com | grep "ID Token" -A1 | tail -1 | xargs)

# 2. Insert a mock upstream token via admin API
curl -sf -X PUT http://localhost:8080/admin/connections/mock \
  -H "X-Admin-Key: $ADMIN_API_KEY" \
  -d '{"caller": "alice@example.com", "access_token": "test-token-for-alice"}'

# 3. Call the mock upstream's whoami tool — verify the injected token
RESULT=$(curl -sf http://localhost:8080/mcp \
  -H "Authorization: Bearer $OIDC_TOKEN" \
  -d '{"jsonrpc":"2.0","method":"tools/call","params":{"name":"whoami"},"id":1}')

# 4. Assert the upstream saw the per-user token, not the OIDC token
echo "$RESULT" | jq -e '.result.content[0].text == "Bearer test-token-for-alice"'
echo "PASS: upstream received per-user token"

# 5. Test missing connection — bob hasn't connected
BOB_TOKEN=$(scripts/get-oidc-token.sh bob@example.com | grep "ID Token" -A1 | tail -1 | xargs)
RESULT=$(curl -sf http://localhost:8080/mcp \
  -H "Authorization: Bearer $BOB_TOKEN" \
  -d '{"jsonrpc":"2.0","method":"tools/call","params":{"name":"whoami"},"id":1}')
echo "$RESULT" | jq -e '.error.message | test("not connected")'
echo "PASS: missing connection returns clear error"

# 6. Test browser flow (print URL for manual testing)
echo ""
echo "Manual browser test:"
echo "  1. Open: http://localhost:8080/admin/connections"
echo "  2. Click 'Connect' for the mock provider"
echo "  3. Mock provider auto-approves and redirects back"
echo "  4. Verify connection shows as active"
echo "  5. Re-run whoami — should now work for bob too"
```

**Files**:
- Create: `scripts/test-oauth-flow.sh`

## What this does NOT include

- **OAuth core** — config, token store, token injection, flow endpoints are all in task 33.
- **Real provider testing** — this task uses mock providers only. Testing against real GitHub/Google OAuth is a manual exercise using the same infrastructure.
