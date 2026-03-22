# Stile Performance Baseline

This document records benchmark results and methodology so operators can capacity-plan and engineers can detect regressions.

## Test Methodology

- **Microbenchmarks** use Go's `testing.B` framework with `b.Loop()` for accurate iteration timing.
- **System-level load tests** start a full Stile server (with real HTTP transports to mock upstream httptest servers) and send sustained concurrent traffic, measuring latency distribution and throughput.
- All benchmarks run with `testing.Short()` guards — load tests are skipped in short mode.

### Reference Machine

> Replace with your CI runner or reference machine specs.

- OS: macOS (Darwin)
- CPU: Apple Silicon (Virtualized), 12 cores
- Go: 1.25.0

---

## Microbenchmarks

### JSON-RPC Codec (`internal/jsonrpc`)

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| ParseSingleRequest | 2,879 | 1,640 | 33 |
| ParseBatch (10 requests) | 32,709 | 17,440 | 331 |
| MarshalResponse | 1,488 | 352 | 3 |
| MarshalErrorResponse | 793 | 256 | 3 |
| RoundTrip (marshal + parse) | 3,015 | 1,737 | 34 |

### Auth Lookup (`internal/auth`)

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| Authenticate (10 callers) | 35,241 | 7,022 | 64 |
| Authenticate (100 callers) | 36,292 | 7,022 | 65 |
| Authenticate (1000 callers) | 34,452 | 7,022 | 66 |

Auth cost is constant regardless of caller count — dominated by SHA-256 hashing and SQLite lookup.

### Route Resolution (`internal/router`)

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| Resolve (5 upstreams, 50 tools) | 20 | 0 | 0 |
| Resolve (10 upstreams, 500 tools) | 21 | 0 | 0 |
| Resolve (50 upstreams, 1000 tools) | 20 | 0 | 0 |
| ListTools (200 tools) | 6,465 | 38,528 | 5 |

Route resolution is a single map lookup — zero-allocation, constant-time regardless of table size.

### Rate Limiter (`internal/policy`)

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| Allow (no limits configured) | 30 | 0 | 0 |
| Allow (caller + tool limits) | 448 | 94 | 3 |
| Allow (all 3 tiers) | 484 | 109 | 3 |
| Allow (with role overrides) | 357 | 85 | 2 |

### Proxy Round-Trip (`internal/proxy`)

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| Full round-trip (mock transport) | 7,221 | 2,709 | 42 |
| Full round-trip + rate limiting | 7,938 | 2,447 | 48 |
| HandleToolsList (100 tools) | 12,803 | 20,517 | 7 |

### End-to-End System (`benchmarks/`)

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| SystemToolsCall (HTTP transport) | 92,797 | 23,071 | 224 |
| SystemToolsList (HTTP transport) | 15,355 | 10,274 | 60 |

---

## Load Test Results

All tests run for 3 seconds with the indicated concurrency level.

### JSON Passthrough (50 concurrent)

| Metric | Value |
|---|---|
| RPS | ~10,800 |
| p50 latency | 1.5 ms |
| p90 latency | 3.8 ms |
| p95 latency | 5.4 ms |
| p99 latency | 23.6 ms |
| Errors | 0 |

### SSE Passthrough (20 concurrent, 5 events per response)

| Metric | Value |
|---|---|
| RPS | ~990 |
| p50 latency | 17.5 ms |
| p99 latency | 80.9 ms |
| Errors | 0 |

### High Concurrency (500 concurrent)

| Metric | Value |
|---|---|
| RPS | ~995 |
| p50 latency | 252 ms |
| p99 latency | 2.2 s |
| Peak goroutines | ~1,008 |
| Errors | 0 |

### Many Upstreams (20 upstreams, 50 concurrent)

| Metric | Value |
|---|---|
| RPS | ~9,810 |
| p50 latency | 17 us |
| p99 latency | 82 ms |
| Errors | 0 |

### With Upstream Latency (5ms upstream delay, 50 concurrent)

| Metric | Value |
|---|---|
| RPS | ~992 |
| p50 latency | 44.3 ms |
| p99 latency | 181.6 ms |
| Errors | 0 |

---

## Running Benchmarks

```bash
# Microbenchmarks (fast, no network)
go test -run=^$ -bench=. ./internal/jsonrpc/ ./internal/auth/ ./internal/router/ ./internal/policy/ ./internal/proxy/ -benchmem

# System-level Go benchmarks (uses httptest servers)
go test -run=^$ -bench=. ./benchmarks/ -benchmem

# Full load tests (3s each, ~20s total)
go test -v -run='TestLoad' ./benchmarks/ -count=1

# Compare against a baseline with benchstat
go test -run=^$ -bench=. ./... -benchmem -count=6 > new.txt
benchstat old.txt new.txt
```

## CI Integration

The `.github/workflows/benchmark.yml` workflow runs microbenchmarks nightly and on-demand, comparing against the main branch with `benchstat`. A regression threshold of >10% p99 increase triggers a warning.
