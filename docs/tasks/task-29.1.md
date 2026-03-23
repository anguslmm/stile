# Task 29.1 — Fix flaky test suite under parallel execution

Status: **todo**

## Problem

`go test ./...` fails inconsistently. Tests pass when run per-package (`go test ./internal/policy/`) but fail when all packages run in parallel (the default behavior of `go test ./...`). The failures are **not consistent** — different packages fail on different runs — but they happen on nearly every full-suite run.

The root cause appears to be **macOS ephemeral port exhaustion**. All errors are `dial tcp 127.0.0.1:<port>: connect: can't assign requested address`, which indicates the system has run out of ephemeral ports due to TCP connections stuck in TIME_WAIT state. Many test packages simultaneously create `httptest.NewServer` instances, miniredis servers, stdio subprocess listeners, and HTTP transports, overwhelming the available port range.

## Affected tests

These are the tests observed failing. The list is **not exhaustive** — different subsets fail on each run.

### `internal/policy` — Redis rate limiter tests (flaky)

All `TestRedis*` tests: `TestRedisUnderLimitPasses`, `TestRedisOverLimitRejects`, `TestRedisPerCallerIsolation`, `TestRedisPerToolIsolation`, `TestRedisPerUpstreamLimit`, `TestRedisNoRateLimitsConfigured`, `TestRedisGlobalLimitSharedAcrossInstances`, `TestRedisRoleBasedCallerRates`

Each test starts its own `miniredis.RunT(t)` instance, binding a new port. Under parallel pressure, these ports contribute to exhaustion and the Redis connections fail with `can't assign requested address`.

### `internal/proxy` — `TestMixedHTTPAndStdioUpstreams` (flaky)

Creates an httptest server for the HTTP upstream and a stdio subprocess. The HTTP transport fails to connect to the httptest server during port exhaustion. Error: `Post "http://127.0.0.1:<port>": dial tcp 127.0.0.1:<port>: connect: can't assign requested address`.

### `internal/server` — Multiple tests (flaky)

`TestHealthzEndpoint`, `TestReadyzEndpointReady`, `TestReadyzEndpointNotReady`, `TestInitializeHandshake`, `TestInitializeUnsupportedVersion`, `TestPing`, `TestUnknownMethod`, `TestNotificationNoResponseBody`, `TestToolsListEndToEnd`, `TestToolsCallEndToEnd`, `TestToolsCallSSEEndToEnd`

These create httptest servers for MCP protocol testing.

### `internal/transport` — Tests (flaky)

Transport tests create httptest servers and stdio subprocesses.

### `tests/integration` — Tests (flaky)

Integration tests create full server stacks with httptest servers.

### `internal/admin` — Tests (flaky)

Admin tests create httptest servers for API testing.

## What was tried

### Attempt 1: Share a single miniredis across Redis tests

Changed `internal/policy/redis_test.go` to use a package-level `*miniredis.Miniredis` instead of per-test instances, calling `FlushAll()` between tests.

**Result:** Partially worked — reduced port usage from that package. But introduced a new bug: `miniredis.RunT(t)` registers cleanup with the first test's `t`, so the shared server was closed after the first test ended and subsequent tests failed. Switching to `miniredis.Run()` (no auto-cleanup) fixed that, but state leakage between tests caused `TestRedisOverLimitRejects` to fail because sliding window counters from `TestRedisUnderLimitPasses` persisted despite `FlushAll()` (the rate limiter's internal connection pool may have cached state).

**Assessment:** The approach of sharing a miniredis is sound but the implementation was incomplete. The `FlushAll()` may not clear rate limiter state held in the Go Redis client's connection pool. Would need to also recreate the rate limiter between tests, or ensure the Redis keys use unique prefixes per test.

### Attempt 2: Reducing package parallelism with `-p 4`

Not fully tested (interrupted). `go test ./... -p 1` (fully sequential) passes 100% of the time, confirming the issue is inter-package parallelism.

**Assessment:** This would work as a workaround but doesn't fix the underlying fragility. Tests shouldn't depend on being the only thing using ports.

## What we know for certain

1. Every test passes when its package is run in isolation
2. `go test ./... -p 1` passes 100% reliably
3. `go test ./...` (default parallelism) fails on nearly every run
4. The errors are always `can't assign requested address` (ephemeral port exhaustion)
5. The failing subset varies between runs

## Possible approaches (not yet tried)

1. **Share test servers within each package** — use `TestMain` or package-level server instances with `FlushAll`/reset between tests. Needs careful implementation to avoid state leakage.
2. **Reduce httptest server creation** — reuse a single server per package where possible.
3. **Increase macOS ephemeral port range** — `sysctl net.inet.ip.portrange.first` / `net.inet.ip.portrange.hifirst`. Not a code fix but would reduce failure rate.
4. **Set SO_REUSEADDR or reduce TIME_WAIT** — modify test server setup to allow port reuse.
5. **Use Unix domain sockets** for inter-process communication in tests where possible.
6. **Add `-p` flag to Makefile/CI** — pragmatic workaround if test-level fixes prove too invasive.
