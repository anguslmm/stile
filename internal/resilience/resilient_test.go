package resilience

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anguslmm/stile/internal/jsonrpc"
	"github.com/anguslmm/stile/internal/transport"
)

// --- mock transport ---

type mockTransport struct {
	roundTrip func(ctx context.Context, req *jsonrpc.Request) (transport.TransportResult, error)
	healthy   bool
}

func (m *mockTransport) RoundTrip(ctx context.Context, req *jsonrpc.Request) (transport.TransportResult, error) {
	return m.roundTrip(ctx, req)
}

func (m *mockTransport) Close() error    { return nil }
func (m *mockTransport) Healthy() bool   { return m.healthy }

var _ transport.Transport = (*mockTransport)(nil)

func okResult() transport.TransportResult {
	resp, _ := jsonrpc.NewResponse(jsonrpc.IntID(1), "ok")
	return transport.NewJSONResult(resp)
}

func testReq() *jsonrpc.Request {
	return &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "test/method",
		ID:      jsonrpc.IntID(1),
	}
}

// --- circuit breaker tests ---

func TestCircuitBreakerTripsAfterThreshold(t *testing.T) {
	cb := NewCircuitBreaker(3, 30*time.Second)

	for i := 0; i < 3; i++ {
		if err := cb.Allow(); err != nil {
			t.Fatalf("expected allow on attempt %d", i)
		}
		cb.RecordFailure()
	}

	if err := cb.Allow(); err == nil {
		t.Fatal("expected circuit to be open")
	}
	if cb.State() != StateOpen {
		t.Fatalf("expected StateOpen, got %d", cb.State())
	}
}

func TestCircuitBreakerHalfOpenOnCooldown(t *testing.T) {
	cb := NewCircuitBreaker(1, 10*time.Millisecond)

	// Trip the circuit.
	cb.Allow()
	cb.RecordFailure()
	if cb.State() != StateOpen {
		t.Fatal("expected open")
	}

	// Wait for cooldown.
	time.Sleep(15 * time.Millisecond)

	// Should transition to half-open and allow one request.
	if err := cb.Allow(); err != nil {
		t.Fatalf("expected allow after cooldown: %v", err)
	}
	if cb.State() != StateHalfOpen {
		t.Fatalf("expected StateHalfOpen, got %d", cb.State())
	}

	// Second request in half-open should be blocked.
	if err := cb.Allow(); err == nil {
		t.Fatal("expected second request blocked in half-open")
	}
}

func TestCircuitBreakerHalfOpenRecovery(t *testing.T) {
	cb := NewCircuitBreaker(1, 10*time.Millisecond)

	cb.Allow()
	cb.RecordFailure()
	time.Sleep(15 * time.Millisecond)
	cb.Allow() // transitions to half-open

	// Probe succeeds — should close the circuit.
	cb.RecordSuccess()
	if cb.State() != StateClosed {
		t.Fatalf("expected closed after half-open success, got %d", cb.State())
	}

	// Should allow requests again.
	if err := cb.Allow(); err != nil {
		t.Fatalf("expected allow after recovery: %v", err)
	}
}

func TestCircuitBreakerHalfOpenFailure(t *testing.T) {
	cb := NewCircuitBreaker(1, 10*time.Millisecond)

	cb.Allow()
	cb.RecordFailure()
	time.Sleep(15 * time.Millisecond)
	cb.Allow() // transitions to half-open

	// Probe fails — should reopen.
	cb.RecordFailure()
	if cb.State() != StateOpen {
		t.Fatalf("expected open after half-open failure, got %d", cb.State())
	}
}

func TestCircuitBreakerSuccessResetsFailCount(t *testing.T) {
	cb := NewCircuitBreaker(3, time.Second)

	// 2 failures then success — should not trip.
	cb.Allow()
	cb.RecordFailure()
	cb.Allow()
	cb.RecordFailure()
	cb.Allow()
	cb.RecordSuccess()

	if cb.State() != StateClosed {
		t.Fatalf("expected closed, got %d", cb.State())
	}

	// Need 3 more consecutive failures to trip.
	for i := 0; i < 3; i++ {
		cb.Allow()
		cb.RecordFailure()
	}
	if cb.State() != StateOpen {
		t.Fatal("expected open after 3 new consecutive failures")
	}
}

// --- retry tests ---

func TestRetrySucceedsOnTransientFailure(t *testing.T) {
	var calls atomic.Int32
	inner := &mockTransport{
		healthy: true,
		roundTrip: func(_ context.Context, _ *jsonrpc.Request) (transport.TransportResult, error) {
			n := calls.Add(1)
			if n <= 2 {
				return nil, &transport.ConnectError{Err: fmt.Errorf("connection refused")}
			}
			return okResult(), nil
		},
	}

	rt := &ResilientTransport{
		inner: inner,
		name:  "test",
		retry: &RetryPolicy{
			MaxAttempts:     3,
			Backoff:         time.Millisecond,
			MaxBackoff:      10 * time.Millisecond,
			RetryableErrors: map[string]bool{"connection_error": true},
		},
	}

	result, err := rt.RoundTrip(context.Background(), testReq())
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if calls.Load() != 3 {
		t.Fatalf("expected 3 calls, got %d", calls.Load())
	}
}

func TestRetryRespectsMaxAttempts(t *testing.T) {
	var calls atomic.Int32
	inner := &mockTransport{
		healthy: true,
		roundTrip: func(_ context.Context, _ *jsonrpc.Request) (transport.TransportResult, error) {
			calls.Add(1)
			return nil, &transport.ConnectError{Err: fmt.Errorf("connection refused")}
		},
	}

	rt := &ResilientTransport{
		inner: inner,
		name:  "test",
		retry: &RetryPolicy{
			MaxAttempts:     3,
			Backoff:         time.Millisecond,
			MaxBackoff:      10 * time.Millisecond,
			RetryableErrors: map[string]bool{"connection_error": true},
		},
	}

	_, err := rt.RoundTrip(context.Background(), testReq())
	if err == nil {
		t.Fatal("expected error")
	}
	if calls.Load() != 3 {
		t.Fatalf("expected 3 attempts, got %d", calls.Load())
	}
}

func TestRetryNotAttemptedOnNonRetryableError(t *testing.T) {
	var calls atomic.Int32
	inner := &mockTransport{
		healthy: true,
		roundTrip: func(_ context.Context, _ *jsonrpc.Request) (transport.TransportResult, error) {
			calls.Add(1)
			return nil, &transport.StatusError{Code: 400}
		},
	}

	rt := &ResilientTransport{
		inner: inner,
		name:  "test",
		retry: &RetryPolicy{
			MaxAttempts:     3,
			Backoff:         time.Millisecond,
			MaxBackoff:      10 * time.Millisecond,
			RetryableErrors: map[string]bool{"connection_error": true, "502": true},
		},
	}

	_, err := rt.RoundTrip(context.Background(), testReq())
	if err == nil {
		t.Fatal("expected error")
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 attempt (no retry), got %d", calls.Load())
	}
}

func TestRetryOnSpecificStatusCodes(t *testing.T) {
	var calls atomic.Int32
	inner := &mockTransport{
		healthy: true,
		roundTrip: func(_ context.Context, _ *jsonrpc.Request) (transport.TransportResult, error) {
			n := calls.Add(1)
			if n <= 2 {
				return nil, &transport.StatusError{Code: 503}
			}
			return okResult(), nil
		},
	}

	rt := &ResilientTransport{
		inner: inner,
		name:  "test",
		retry: &RetryPolicy{
			MaxAttempts:     3,
			Backoff:         time.Millisecond,
			MaxBackoff:      10 * time.Millisecond,
			RetryableErrors: map[string]bool{"503": true},
		},
	}

	result, err := rt.RoundTrip(context.Background(), testReq())
	if err != nil {
		t.Fatalf("expected success: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if calls.Load() != 3 {
		t.Fatalf("expected 3 attempts, got %d", calls.Load())
	}
}

func TestRetryStopsOnContextCancel(t *testing.T) {
	var calls atomic.Int32
	inner := &mockTransport{
		healthy: true,
		roundTrip: func(_ context.Context, _ *jsonrpc.Request) (transport.TransportResult, error) {
			calls.Add(1)
			return nil, &transport.ConnectError{Err: fmt.Errorf("connection refused")}
		},
	}

	ctx, cancel := context.WithCancel(context.Background())

	rt := &ResilientTransport{
		inner: inner,
		name:  "test",
		retry: &RetryPolicy{
			MaxAttempts:     5,
			Backoff:         50 * time.Millisecond,
			MaxBackoff:      100 * time.Millisecond,
			RetryableErrors: map[string]bool{"connection_error": true},
		},
	}

	// Cancel context after first attempt completes.
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	_, err := rt.RoundTrip(ctx, testReq())
	if err == nil {
		t.Fatal("expected error")
	}
	// Should have stopped early due to context cancel.
	if calls.Load() >= 5 {
		t.Fatalf("expected fewer than 5 attempts, got %d", calls.Load())
	}
}

func TestSSEStreamDoesNotTriggerRetry(t *testing.T) {
	// When RoundTrip returns a StreamResult (success), there's no error to retry.
	// This test verifies that a successful stream response is returned immediately.
	var calls atomic.Int32
	inner := &mockTransport{
		healthy: true,
		roundTrip: func(_ context.Context, _ *jsonrpc.Request) (transport.TransportResult, error) {
			calls.Add(1)
			return transport.NewStreamResult(io.NopCloser(strings.NewReader("event: message\ndata: {}\n\n"))), nil
		},
	}

	rt := &ResilientTransport{
		inner: inner,
		name:  "test",
		retry: &RetryPolicy{
			MaxAttempts:     3,
			Backoff:         time.Millisecond,
			MaxBackoff:      10 * time.Millisecond,
			RetryableErrors: map[string]bool{"connection_error": true},
		},
	}

	result, err := rt.RoundTrip(context.Background(), testReq())
	if err != nil {
		t.Fatalf("expected success: %v", err)
	}
	sr, ok := result.(*transport.StreamResult)
	if !ok {
		t.Fatalf("expected *StreamResult, got %T", result)
	}
	sr.Stream().Close()
	if calls.Load() != 1 {
		t.Fatalf("expected 1 call (no retry on success), got %d", calls.Load())
	}
}

func TestJitteredBackoffTiming(t *testing.T) {
	rp := &RetryPolicy{
		Backoff:    100 * time.Millisecond,
		MaxBackoff: 2 * time.Second,
	}

	for attempt := 0; attempt < 5; attempt++ {
		d := rp.ComputeBackoff(attempt)
		// Expected range: backoff * 2^attempt * [0.5, 1.0]
		base := float64(100*time.Millisecond) * float64(int(1)<<attempt)
		minExpected := time.Duration(base * 0.5)
		maxExpected := time.Duration(base * 1.0)
		if maxExpected > 2*time.Second {
			maxExpected = 2 * time.Second
		}
		if d < minExpected || d > maxExpected {
			t.Errorf("attempt %d: backoff %v outside expected range [%v, %v]",
				attempt, d, minExpected, maxExpected)
		}
	}
}

func TestBackoffRespectsCap(t *testing.T) {
	rp := &RetryPolicy{
		Backoff:    100 * time.Millisecond,
		MaxBackoff: 500 * time.Millisecond,
	}

	for attempt := 0; attempt < 20; attempt++ {
		d := rp.ComputeBackoff(attempt)
		if d > 500*time.Millisecond {
			t.Errorf("attempt %d: backoff %v exceeds max %v", attempt, d, 500*time.Millisecond)
		}
	}
}

// --- resilient transport integration tests ---

func TestResilientTransportCircuitBreakerFailFast(t *testing.T) {
	var calls atomic.Int32
	inner := &mockTransport{
		healthy: true,
		roundTrip: func(_ context.Context, _ *jsonrpc.Request) (transport.TransportResult, error) {
			calls.Add(1)
			return nil, &transport.ConnectError{Err: fmt.Errorf("down")}
		},
	}

	cb := NewCircuitBreaker(2, time.Hour)
	rt := &ResilientTransport{
		inner: inner,
		name:  "test",
		cb:    cb,
	}

	// First two requests fail and trip the circuit.
	rt.RoundTrip(context.Background(), testReq())
	rt.RoundTrip(context.Background(), testReq())

	if cb.State() != StateOpen {
		t.Fatalf("expected open, got %d", cb.State())
	}

	// Third request should fail fast without calling inner.
	_, err := rt.RoundTrip(context.Background(), testReq())
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen, got: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected 2 calls (fail fast on third), got %d", calls.Load())
	}
}

func TestResilientTransportNoConfigPassthrough(t *testing.T) {
	// Without circuit breaker or retry, ResilientTransport should pass through.
	inner := &mockTransport{
		healthy: true,
		roundTrip: func(_ context.Context, _ *jsonrpc.Request) (transport.TransportResult, error) {
			return okResult(), nil
		},
	}

	rt := &ResilientTransport{
		inner: inner,
		name:  "test",
	}

	result, err := rt.RoundTrip(context.Background(), testReq())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected result")
	}
}

func TestResilientTransportCircuitBreakerWithRetry(t *testing.T) {
	// Circuit breaker + retry: retries exhaust, then circuit trips.
	var calls atomic.Int32
	inner := &mockTransport{
		healthy: true,
		roundTrip: func(_ context.Context, _ *jsonrpc.Request) (transport.TransportResult, error) {
			calls.Add(1)
			return nil, &transport.ConnectError{Err: fmt.Errorf("down")}
		},
	}

	cb := NewCircuitBreaker(2, time.Hour)
	rt := &ResilientTransport{
		inner: inner,
		name:  "test",
		cb:    cb,
		retry: &RetryPolicy{
			MaxAttempts:     3,
			Backoff:         time.Millisecond,
			MaxBackoff:      10 * time.Millisecond,
			RetryableErrors: map[string]bool{"connection_error": true},
		},
	}

	// First request: 3 retries fail, records 1 failure on circuit.
	rt.RoundTrip(context.Background(), testReq())
	if cb.State() != StateClosed {
		t.Fatal("expected still closed after 1 request")
	}

	// Second request: 3 retries fail, records 2nd failure, trips circuit.
	rt.RoundTrip(context.Background(), testReq())
	if cb.State() != StateOpen {
		t.Fatal("expected open after 2 failed requests")
	}

	// Third request: circuit open, no calls to inner.
	beforeCalls := calls.Load()
	_, err := rt.RoundTrip(context.Background(), testReq())
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen, got: %v", err)
	}
	if calls.Load() != beforeCalls {
		t.Fatal("expected no new calls when circuit is open")
	}
}
