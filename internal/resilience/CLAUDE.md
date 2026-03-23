# resilience

Wraps a `transport.Transport` with circuit breaker and retry logic.

## Key Types

- **`ResilientTransport`** — implements `transport.Transport`; composes a circuit breaker and retry policy around an inner transport. Created via `Wrap()`; returns the original transport unchanged if neither feature is configured.
- **`CircuitBreaker`** — three-state (Closed / Open / HalfOpen) circuit breaker. Trips to Open after `threshold` consecutive failures; transitions to HalfOpen after `cooldown`; closes on success. Only one probe request is allowed through in HalfOpen at a time (`halfOpenActive` flag).
- **`RetryPolicy`** — holds `MaxAttempts`, exponential `Backoff`/`MaxBackoff`, and a set of retryable error keys (`"connection_error"`, `"502"`, etc.).
- **`State`** (`int`) — enum for circuit breaker state: `StateClosed=0`, `StateOpen=1`, `StateHalfOpen=2`.

## Key Functions / Methods

- **`Wrap(t, cfg, m)`** — entry point; reads `CircuitBreaker()` and `Retry()` from `config.UpstreamConfig`, builds and returns a `*ResilientTransport` (or the raw transport if nothing is configured).
- **`NewCircuitBreaker(threshold, cooldown)`** — constructs a `CircuitBreaker`.
- **`(*CircuitBreaker).Allow()`** — returns `ErrCircuitOpen` or nil; handles Open→HalfOpen transition on cooldown expiry.
- **`(*CircuitBreaker).RecordSuccess/RecordFailure()`** — update state; both are called by `ResilientTransport.RoundTrip` after every attempt.
- **`(*RetryPolicy).IsRetryable(err)`** — matches `transport.ConnectError` → `"connection_error"` and `transport.StatusError` → HTTP status code string.
- **`(*RetryPolicy).ComputeBackoff(attempt)`** — jittered exponential: `backoff * 2^attempt * [0.5, 1.0)`, capped at `MaxBackoff`.
- **`(*RetryPolicy).WaitBackoff(ctx, attempt)`** — sleeps for the computed duration, cancellable via context.

## Design Notes

- Circuit breaker check happens before the retry loop; a single open-circuit error short-circuits all retries.
- Failures are recorded on the circuit breaker only after all retry attempts are exhausted.
- `CircuitBreaker.nowFunc` is injectable for deterministic testing.
- Retryable error matching is by error type only (`transport.ConnectError`, `transport.StatusError`); arbitrary errors are never retried.
- Metrics (`RecordRetry`, `SetCircuitState`) are optional — `m` may be nil.
