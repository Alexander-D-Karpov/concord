package circuitbreaker

import (
	"errors"
	"sync"
	"time"
)

// ErrCircuitOpen is returned by Call when the breaker is open and the timeout
// has not yet elapsed, so the wrapped function is not invoked.
var ErrCircuitOpen = errors.New("circuit breaker is open")

// State is the breaker's current mode: closed (calls pass), open (calls are
// rejected), or half-open (a single trial call is allowed to probe recovery).
type State int

const (
	// StateClosed passes calls through and counts failures.
	StateClosed State = iota
	// StateOpen rejects calls until the timeout elapses.
	StateOpen
	// StateHalfOpen allows a trial call; success closes the breaker, failure reopens it.
	StateHalfOpen
)

// CircuitBreaker is a three-state breaker that stops calling a failing dependency
// after maxFailures consecutive errors and retries it after timeout. It is safe
// for concurrent use, but note Call serializes all calls through the breaker.
type CircuitBreaker struct {
	maxFailures int
	timeout     time.Duration
	state       State
	failures    int
	lastFailure time.Time
	mu          sync.RWMutex
}

// New returns a closed breaker that opens after maxFailures consecutive failures
// and transitions to half-open once timeout has elapsed since the last failure.
func New(maxFailures int, timeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		maxFailures: maxFailures,
		timeout:     timeout,
		state:       StateClosed,
	}
}

// Call runs fn unless the breaker is open, returning ErrCircuitOpen without
// invoking fn in that case. A returned error increments the failure count (and
// opens the breaker at the threshold); success resets the count and closes the
// breaker. The breaker's lock is held for the entire duration of fn, so calls
// through a single breaker execute one at a time.
func (cb *CircuitBreaker) Call(fn func() error) error {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == StateOpen {
		if time.Since(cb.lastFailure) > cb.timeout {
			cb.state = StateHalfOpen
		} else {
			return ErrCircuitOpen
		}
	}

	err := fn()
	if err != nil {
		cb.failures++
		cb.lastFailure = time.Now()

		if cb.failures >= cb.maxFailures {
			cb.state = StateOpen
		}

		return err
	}

	if cb.state == StateHalfOpen {
		cb.state = StateClosed
	}

	cb.failures = 0
	return nil
}

// GetState returns the breaker's current state. Note the state may be reported as
// open even after the timeout has elapsed, because the lazy transition to
// half-open happens inside Call, not here.
func (cb *CircuitBreaker) GetState() State {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// Reset forces the breaker back to closed and clears the failure count, discarding
// any open/half-open state.
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.state = StateClosed
	cb.failures = 0
}
