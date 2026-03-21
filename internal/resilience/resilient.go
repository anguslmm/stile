package resilience

import (
	"context"
	"log/slog"

	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/jsonrpc"
	"github.com/anguslmm/stile/internal/metrics"
	"github.com/anguslmm/stile/internal/transport"
)

var _ transport.Transport = (*ResilientTransport)(nil)

// ResilientTransport wraps a Transport with circuit breaker and retry logic.
type ResilientTransport struct {
	inner   transport.Transport
	name    string
	cb      *CircuitBreaker
	retry   *RetryPolicy // nil if no retry
	metrics *metrics.Metrics
}

// Wrap creates a ResilientTransport if the upstream config has resilience settings.
// If neither circuit breaker nor retry is configured, returns the original transport.
// m may be nil.
func Wrap(t transport.Transport, cfg config.UpstreamConfig, m *metrics.Metrics) transport.Transport {
	cbCfg := cfg.CircuitBreaker()
	retryCfg := cfg.Retry()

	if cbCfg == nil && retryCfg == nil {
		return t
	}

	rt := &ResilientTransport{
		inner:   t,
		name:    cfg.Name(),
		metrics: m,
	}

	if cbCfg != nil {
		rt.cb = NewCircuitBreaker(cbCfg.FailureThreshold(), cbCfg.Cooldown())
	}

	if retryCfg != nil {
		retryableSet := make(map[string]bool, len(retryCfg.RetryableErrors()))
		for _, e := range retryCfg.RetryableErrors() {
			retryableSet[e] = true
		}
		rt.retry = &RetryPolicy{
			MaxAttempts:     retryCfg.MaxAttempts(),
			Backoff:         retryCfg.Backoff(),
			MaxBackoff:      retryCfg.MaxBackoff(),
			RetryableErrors: retryableSet,
		}
	}

	return rt
}

// RoundTrip sends a request through the resilience stack:
// circuit breaker check → retry loop → inner transport.
func (rt *ResilientTransport) RoundTrip(ctx context.Context, req *jsonrpc.Request) (transport.TransportResult, error) {
	// Circuit breaker check.
	if rt.cb != nil {
		if err := rt.cb.Allow(); err != nil {
			rt.recordCircuitState()
			return nil, err
		}
	}

	maxAttempts := 1
	if rt.retry != nil && rt.retry.MaxAttempts > 1 {
		maxAttempts = rt.retry.MaxAttempts
	}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			// Check context before waiting.
			if ctx.Err() != nil {
				lastErr = ctx.Err()
				break
			}

			slog.Warn("retrying upstream request",
				"upstream", rt.name,
				"attempt", attempt+1,
				"max_attempts", maxAttempts,
				"error", lastErr,
			)

			if rt.metrics != nil {
				rt.metrics.RecordRetry(rt.name)
			}

			if err := rt.retry.WaitBackoff(ctx, attempt-1); err != nil {
				lastErr = err
				break
			}
		}

		result, err := rt.inner.RoundTrip(ctx, req)
		if err == nil {
			if rt.cb != nil {
				rt.cb.RecordSuccess()
				rt.recordCircuitState()
			}
			return result, nil
		}

		lastErr = err

		// Only retry if the error is retryable and we have retry policy.
		if rt.retry == nil || !rt.retry.IsRetryable(err) {
			break
		}
	}

	// Record failure for circuit breaker.
	if rt.cb != nil {
		rt.cb.RecordFailure()
		rt.recordCircuitState()
	}

	return nil, lastErr
}

// Close closes the inner transport.
func (rt *ResilientTransport) Close() error {
	return rt.inner.Close()
}

// Healthy delegates to the inner transport.
func (rt *ResilientTransport) Healthy() bool {
	return rt.inner.Healthy()
}

func (rt *ResilientTransport) recordCircuitState() {
	if rt.metrics == nil || rt.cb == nil {
		return
	}
	rt.metrics.SetCircuitState(rt.name, float64(rt.cb.State()))
}
