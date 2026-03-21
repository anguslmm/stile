// Package resilience provides circuit breakers, retries, and resilient
// transport wrappers for upstream communication.
package resilience

import (
	"errors"
	"sync"
	"time"
)

// State represents the circuit breaker state.
type State int

const (
	StateClosed   State = 0
	StateOpen     State = 1
	StateHalfOpen State = 2
)

// ErrCircuitOpen is returned when the circuit breaker is open.
var ErrCircuitOpen = errors.New("upstream circuit open")

// CircuitBreaker implements the circuit breaker pattern with three states:
// closed (normal), open (fail fast), and half-open (probing).
type CircuitBreaker struct {
	mu               sync.Mutex
	state            State
	consecutiveFails int
	threshold        int
	cooldown         time.Duration
	lastTripped      time.Time
	halfOpenActive   bool

	// nowFunc is overridable for testing.
	nowFunc func() time.Time
}

// NewCircuitBreaker creates a circuit breaker with the given threshold and cooldown.
func NewCircuitBreaker(threshold int, cooldown time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		state:     StateClosed,
		threshold: threshold,
		cooldown:  cooldown,
		nowFunc:   time.Now,
	}
}

// Allow checks if a request is allowed through the circuit breaker.
// Returns nil if allowed, ErrCircuitOpen if the circuit is open.
func (cb *CircuitBreaker) Allow() error {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case StateClosed:
		return nil
	case StateOpen:
		if cb.nowFunc().Sub(cb.lastTripped) >= cb.cooldown {
			cb.state = StateHalfOpen
			cb.halfOpenActive = true
			return nil
		}
		return ErrCircuitOpen
	case StateHalfOpen:
		if cb.halfOpenActive {
			return ErrCircuitOpen
		}
		cb.halfOpenActive = true
		return nil
	}
	return nil
}

// RecordSuccess records a successful request. Closes the circuit if half-open.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.consecutiveFails = 0
	cb.state = StateClosed
	cb.halfOpenActive = false
}

// RecordFailure records a failed request. May trip the circuit to open.
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == StateHalfOpen {
		cb.state = StateOpen
		cb.lastTripped = cb.nowFunc()
		cb.halfOpenActive = false
		return
	}

	cb.consecutiveFails++
	if cb.consecutiveFails >= cb.threshold {
		cb.state = StateOpen
		cb.lastTripped = cb.nowFunc()
	}
}

// State returns the current circuit breaker state.
func (cb *CircuitBreaker) State() State {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}
