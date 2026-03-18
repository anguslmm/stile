# Task 10: Integration Tests + Release Packaging

**Status:** not started
**Depends on:** All previous tasks
**Needed by:** nothing — this is the final task

---

## Goal

Verify the full system works end-to-end with realistic scenarios, and package it for deployment. After this task, Stile is ready for real use.

---

## 1. Integration Test Suite

### Location: `tests/integration/`

These tests spin up the full gateway with mock upstreams and exercise the entire pipeline: auth → routing → rate limiting → proxy → observability.

### Test infrastructure

**Mock HTTP MCP server:** an `httptest.Server` that implements a realistic MCP server:
- Responds to `initialize`, `ping`, `tools/list`, `tools/call`
- Configurable tool list (set per test)
- Configurable response mode (JSON or SSE)
- Configurable latency (for timeout/streaming tests)
- Records received requests for assertion

**Mock stdio MCP server:** reuse the test helper from Task 4.

**Gateway under test:** start the real gateway binary (or construct it in-process) with a test config pointing at the mock upstreams.

**Test client:** a simple HTTP client that sends JSON-RPC requests to the gateway and parses responses.

### Test scenarios

#### Happy path

1. **Full lifecycle:** initialize → tools/list → tools/call → verify response matches mock upstream's response
2. **Multiple upstreams:** configure HTTP + stdio upstreams → tools/list returns tools from both → tools/call routes to the correct one
3. **SSE passthrough end-to-end:** mock upstream returns SSE stream → client receives SSE events through gateway

#### Auth & access control

4. **Valid key accesses allowed tools:** caller with `["github/*"]` → can list and call github tools
5. **Valid key blocked from other tools:** same caller → cannot call deploy tools
6. **Invalid key rejected:** bad API key → JSON-RPC error on any request
7. **No key rejected:** no Authorization header → JSON-RPC error
8. **Filtered tools/list:** caller only sees tools matching their ACL

#### Rate limiting

9. **Caller rate limit enforced:** send requests faster than the limit → get rate limit errors
10. **Rate limit per-tool:** spam one tool → limited, other tools still work
11. **Upstream rate limit:** multiple callers hit same upstream → aggregate limit enforced

#### Resilience

12. **Upstream down at startup:** one upstream unreachable → gateway starts, serves tools from healthy upstreams
13. **Upstream goes down mid-operation:** upstream stops responding → error returned to client, gateway stays up
14. **Upstream recovers:** restart a stopped mock upstream → after cache refresh, tools are available again
15. **Tool cache refresh:** upstream adds a new tool → after refresh interval (or manual /admin/refresh), new tool appears

#### Policy

16. **Blocklisted tool hidden and rejected:** add tool to blocklist → not in tools/list, tools/call returns error
17. **Input validation rejects bad params:** upstream with validate_input, send invalid args → error before reaching upstream

#### Observability

18. **Metrics populated:** after several requests → /metrics shows non-zero counters with correct labels
19. **Audit log entries:** enable audit, make tool calls → JSONL entries with correct data

#### Admin

20. **Config reload:** change config, POST /admin/reload with admin key → changes take effect
21. **Manual refresh:** POST /admin/refresh with admin key → tool cache refreshed

#### Shutdown

22. **Graceful shutdown:** start a slow request, send SIGTERM → request completes, then process exits cleanly

---

## 2. README

### File: `README.md`

Write a README covering:

- **What Stile is** — one-paragraph description
- **Quick start** — install, create a config, run the binary, point an agent at it
- **Configuration reference** — all config file options with examples
- **Authentication setup** — how to configure callers and API keys
- **Admin endpoints** — /admin/refresh, /admin/reload
- **Observability** — logging config, Prometheus metrics, audit log
- **Deployment** — Docker, systemd, Kubernetes (health check endpoints)

Keep it practical. Link to the design doc for architecture details.

---

## 3. Dockerfile

### File: `Dockerfile`

Multi-stage build:

```dockerfile
# Build stage
FROM golang:1.22 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /stile ./cmd/gateway/

# Runtime stage
FROM gcr.io/distroless/static
COPY --from=build /stile /stile
ENTRYPOINT ["/stile"]
CMD ["-config", "/etc/stile/config.yaml"]
```

### File: `docker-compose.yml`

Example compose file showing the gateway with a config file mount:

```yaml
services:
  stile:
    build: .
    ports:
      - "8080:8080"
    volumes:
      - ./configs/example.yaml:/etc/stile/config.yaml:ro
    environment:
      - GITHUB_MCP_TOKEN
      - DEV_GATEWAY_KEY
```

---

## 4. Testable Deliverables

### All integration tests pass

```bash
go test ./tests/integration/ -v -count=1
```

Tests should be tagged or in a separate directory so they can be run independently from unit tests (they may be slower).

### Full build and test

```bash
go build ./...
go test ./...
go vet ./...
```

### Docker build succeeds

```bash
docker build -t stile .
```

### Manual verification

The agent completing this task should verify:

1. Build the binary: `go build -o stile ./cmd/gateway/`
2. Run with example config: `./stile -config configs/example.yaml`
3. Confirm it starts and logs its listen address
4. Confirm `/healthz` returns 200
5. Confirm `/metrics` returns Prometheus format
6. Confirm Ctrl+C triggers graceful shutdown

---

## 5. Out of Scope

- CI/CD pipeline setup
- Published Docker images
- Benchmarking / load testing
- Tool filtering features (covered in the separate tool filtering design doc)
