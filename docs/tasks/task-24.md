# Task 24: Load Testing and Performance Benchmarks

**Status:** todo
**Depends on:** 21

---

## Goal

Establish baseline performance characteristics for Stile so that operators can capacity-plan and engineers can detect regressions. No production readiness review will pass without knowing the proxy's overhead, throughput limits, and resource consumption under load.

---

## 1. Create a benchmark suite

Add `benchmarks/` at the project root with a load testing harness. Use Go's `testing.B` benchmarks for microbenchmarks and a load generator (e.g. `vegeta`, `hey`, or a custom Go harness) for system-level tests.

### Microbenchmarks (Go `testing.B`)

Add to the relevant `_test.go` files:

- **JSON-RPC codec:** parse, marshal, batch parse — measures serialization overhead
- **Auth lookup:** `Authenticate()` with N callers in the store — measures per-request auth cost
- **Route resolution:** `Resolve()` with N tools across M upstreams
- **Rate limiter:** `Allow()` with various bucket states — measures per-request policy overhead
- **Full proxy round-trip:** end-to-end through the handler with a mock upstream (httptest server returning a canned response) — measures total proxy overhead excluding network

### System-level load tests

Create `benchmarks/loadtest.go` (or use an external tool) that:

1. Starts a Stile instance with a realistic config (auth enabled, rate limits, multiple upstreams)
2. Starts mock upstream servers that respond with configurable latency
3. Sends sustained load at increasing RPS
4. Measures and reports:
   - **Throughput:** max RPS at target latency (p99 < 50ms proxy overhead)
   - **Latency distribution:** p50, p90, p95, p99 at various RPS levels
   - **Resource consumption:** peak RSS, goroutine count, open file descriptors
   - **Saturation point:** RPS at which latency degrades or errors appear

### Scenarios to benchmark

| Scenario | Description |
|---|---|
| JSON passthrough | Simple tool call, upstream returns JSON |
| SSE passthrough | Tool call, upstream returns SSE stream (N events) |
| High concurrency | 1000 concurrent callers, single upstream |
| Many upstreams | 50 upstreams, requests distributed across them |
| Rate limit heavy | Every request hits the rate limiter near its limit |
| Auth heavy | 1000 distinct callers in the auth store |

---

## 2. Publish baseline numbers

Run the benchmarks on a reference machine (document the specs) and record results in `docs/performance.md`:

- Proxy overhead per request (time added beyond upstream latency)
- Max throughput (RPS) for JSON and SSE responses
- Memory consumption per concurrent connection
- Goroutine count under load

These numbers serve as a baseline for regression detection and as guidance for operators doing capacity planning.

---

## 3. CI integration

Add benchmark runs to CI (not on every PR — too slow). Options:
- Nightly benchmark job that records results and alerts on regressions (> 10% p99 increase)
- Or: run Go microbenchmarks on PR with `benchstat` comparison against main branch

---

## Verification

- All benchmarks run without error
- `docs/performance.md` exists with baseline numbers and test methodology
- CI job runs benchmarks (at least nightly) and results are accessible
