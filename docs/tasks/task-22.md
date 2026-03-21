# Task 22: Upstream Resilience (Circuit Breakers, Per-Upstream Timeouts, Retries)

**Status:** todo
**Depends on:** 13, 15

---

## Goal

Add circuit breakers, configurable per-upstream timeouts, and optional retry logic so that Stile degrades gracefully under upstream failures instead of sending traffic into a black hole or failing on transient errors.

---

## 1. Per-upstream timeouts

Replace the hardcoded 60s `ResponseHeaderTimeout` with a configurable value per upstream:

```yaml
upstreams:
  - name: fast-lookup
    url: https://lookup.internal/mcp
    timeout: 5s
  - name: code-execution
    url: https://exec.internal/mcp
    timeout: 300s
```

**Implementation:**

- Add `Timeout()` getter to `UpstreamConfig` returning `time.Duration` (default 60s)
- In `NewHTTPTransport`, set `ResponseHeaderTimeout` from the config value
- For `StdioTransport`, apply the timeout as a context deadline on the read (stdio has no HTTP transport to configure)

---

## 2. Circuit breaker

The existing health tracking (`recordFailure`/`recordSuccess`, `Healthy()`) detects failure but doesn't prevent traffic from reaching a broken upstream. Add a circuit breaker that short-circuits requests when an upstream is unhealthy.

**States:**
- **Closed** (normal): requests flow through. Failures are counted.
- **Open** (tripped): requests fail immediately with a clear error ("upstream circuit open"). No traffic sent.
- **Half-open** (probing): after a cooldown period, allow one request through. If it succeeds, close the circuit. If it fails, reopen.

```yaml
upstreams:
  - name: tools
    url: https://tools.internal/mcp
    circuit_breaker:
      failure_threshold: 5       # consecutive failures to trip (default: 5)
      cooldown: 30s              # time in open state before probing (default: 30s)
```

**Implementation:**

- Add `CircuitBreaker` to `internal/transport/` (or a new `internal/resilience/` package)
- Wrap the existing `Transport` interface — the circuit breaker sits in front of `RoundTrip`
- Reuse the existing `recordFailure`/`recordSuccess` and `Healthy()` tracking as the basis, extending it with the open/half-open states and cooldown timer
- Add a Prometheus gauge for circuit state per upstream (`stile_circuit_state{upstream="..."}` — 0=closed, 1=open, 2=half-open)
- If tracing is enabled (task 16), record circuit breaker trips as span events

---

## 3. Retries with backoff

Add optional retry support for transient failures. This must be opt-in per upstream because `tools/call` may not be idempotent.

```yaml
upstreams:
  - name: stateless-tools
    url: https://tools.internal/mcp
    retry:
      max_attempts: 3            # total attempts including the original (default: 1, no retry)
      backoff: 100ms             # initial backoff, doubles each retry
      max_backoff: 2s            # backoff cap
      retryable_errors:          # which failures to retry (default: connection errors only)
        - connection_error
        - 502
        - 503
        - 504
```

**Implementation:**

- Add retry logic in the transport layer, wrapping `RoundTrip`
- Use jittered exponential backoff: `backoff * 2^attempt * (0.5 + rand(0.5))`
- Respect the request context — if the caller's context is cancelled, don't retry
- Never retry if the request body was already sent and a response was partially received (SSE stream started) — only retry on connection-level and header-level failures
- Log each retry attempt at WARN level with attempt number and error
- Add a Prometheus counter for retries per upstream

---

## 4. Compose the resilience stack

The ordering matters. For a given request:

```
RoundTrip call
  → Circuit breaker check (fail fast if open)
    → Retry loop
      → Timeout (per-upstream ResponseHeaderTimeout)
        → Actual HTTP request
      → On failure: retry with backoff
    → On persistent failure: record failure, possibly trip circuit
  → On success: record success, reset circuit
```

Wire this up so that each layer is optional and controlled by config. If no circuit breaker or retry config is provided, behavior is unchanged from today.

---

## Verification

- Existing transport tests pass unchanged (no resilience config = current behavior)
- Add test: per-upstream timeout overrides the default
- Add test: circuit breaker trips after N failures, requests fail fast
- Add test: circuit breaker enters half-open after cooldown, recovers on success
- Add test: circuit breaker reopens on half-open failure
- Add test: retries succeed on transient failure
- Add test: retries respect max_attempts
- Add test: retries not attempted on non-retryable errors
- Add test: retries stop when context is cancelled
- Add test: SSE stream failure does not trigger retry (response already started)
- Add test: jittered backoff timing is within expected bounds
