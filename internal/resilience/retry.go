package resilience

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/anguslmm/stile/internal/transport"
)

// RetryPolicy determines whether and how to retry failed requests.
type RetryPolicy struct {
	MaxAttempts     int
	Backoff         time.Duration
	MaxBackoff      time.Duration
	RetryableErrors map[string]bool // "connection_error", "502", etc.
}

// IsRetryable checks if the given error should be retried.
func (rp *RetryPolicy) IsRetryable(err error) bool {
	if rp == nil || len(rp.RetryableErrors) == 0 {
		return false
	}

	var connectErr *transport.ConnectError
	if errors.As(err, &connectErr) {
		return rp.RetryableErrors["connection_error"]
	}

	var statusErr *transport.StatusError
	if errors.As(err, &statusErr) {
		return rp.RetryableErrors[strconv.Itoa(statusErr.Code)]
	}

	return false
}

// ComputeBackoff returns the jittered exponential backoff for the given attempt.
// Formula: backoff * 2^attempt * (0.5 + rand(0.5))
func (rp *RetryPolicy) ComputeBackoff(attempt int) time.Duration {
	base := float64(rp.Backoff) * math.Pow(2, float64(attempt))
	jitter := 0.5 + rand.Float64()*0.5
	d := time.Duration(base * jitter)
	if d > rp.MaxBackoff {
		d = rp.MaxBackoff
	}
	return d
}

// WaitBackoff sleeps for the computed backoff duration, respecting context cancellation.
// Returns the context error if cancelled, nil otherwise.
func (rp *RetryPolicy) WaitBackoff(ctx context.Context, attempt int) error {
	d := rp.ComputeBackoff(attempt)
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
