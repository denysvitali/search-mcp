package resilience

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/denysvitali/search-mcp/internal/search"
)

// ErrCircuitOpen is returned (wrapped) when the breaker is open and rejects a
// call without invoking the inner provider.
var ErrCircuitOpen = errors.New("circuit breaker open")

type breakerState int

const (
	stateClosed breakerState = iota
	stateOpen
	stateHalfOpen
)

// BreakerOptions configures CircuitBreaker.
type BreakerOptions struct {
	// Threshold is the number of consecutive failures that trips the breaker
	// from closed to open. Values < 1 are clamped to 1.
	Threshold int

	// Cooldown is how long the breaker stays open before allowing a half-open
	// trial call. Values <= 0 are clamped to a small default.
	Cooldown time.Duration

	// now, if set, replaces time.Now for testing.
	now func() time.Time
}

// CircuitBreaker is a per-instance circuit breaker around an inner provider.
//
// Closed: calls pass through; N consecutive failures open the breaker.
// Open: calls fast-fail with ErrCircuitOpen until Cooldown elapses.
// Half-open: a single trial call is allowed; success closes the breaker,
// failure re-opens it.
type CircuitBreaker struct {
	inner search.Provider
	opts  BreakerOptions

	mu           sync.Mutex
	state        breakerState
	failures     int
	openedAt     time.Time
	halfOpenBusy bool // a half-open trial is in flight
}

// NewCircuitBreaker wraps inner with circuit-breaking behaviour.
func NewCircuitBreaker(inner search.Provider, opts BreakerOptions) *CircuitBreaker {
	if opts.Threshold < 1 {
		opts.Threshold = 1
	}
	if opts.Cooldown <= 0 {
		opts.Cooldown = 30 * time.Second
	}
	if opts.now == nil {
		opts.now = time.Now
	}
	return &CircuitBreaker{
		inner: inner,
		opts:  opts,
		state: stateClosed,
	}
}

// Name delegates to the inner provider.
func (c *CircuitBreaker) Name() string { return c.inner.Name() }

// Search proxies to the inner provider, applying circuit-breaker logic.
func (c *CircuitBreaker) Search(ctx context.Context, req search.Request) (search.Response, error) {
	if err := c.beforeCall(); err != nil {
		return search.Response{}, err
	}

	resp, err := c.inner.Search(ctx, req)
	c.afterCall(err)
	if err != nil {
		return search.Response{}, err
	}
	return resp, nil
}

// beforeCall checks the breaker state and decides whether the call may proceed.
func (c *CircuitBreaker) beforeCall() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch c.state {
	case stateOpen:
		if c.opts.now().Sub(c.openedAt) >= c.opts.Cooldown {
			// Transition to half-open and let this single call be the trial.
			c.state = stateHalfOpen
			c.halfOpenBusy = true
			return nil
		}
		return fmt.Errorf("%s: %w", c.inner.Name(), ErrCircuitOpen)
	case stateHalfOpen:
		// Only one trial call is permitted while half-open.
		if c.halfOpenBusy {
			return fmt.Errorf("%s: %w", c.inner.Name(), ErrCircuitOpen)
		}
		c.halfOpenBusy = true
		return nil
	default: // stateClosed
		return nil
	}
}

// afterCall records the outcome and updates the breaker state.
func (c *CircuitBreaker) afterCall(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.state == stateHalfOpen {
		c.halfOpenBusy = false
	}

	if err == nil {
		// Success closes the breaker and resets the failure count.
		c.state = stateClosed
		c.failures = 0
		return
	}

	switch c.state {
	case stateHalfOpen:
		// Trial failed: re-open and restart the cooldown.
		c.state = stateOpen
		c.openedAt = c.opts.now()
	default: // stateClosed
		c.failures++
		if c.failures >= c.opts.Threshold {
			c.state = stateOpen
			c.openedAt = c.opts.now()
		}
	}
}
