package resilience

import (
	"time"

	"github.com/denysvitali/search-mcp/internal/search"
)

// innerProvider is implemented by every decorator so callers can walk the
// decorator chain down to a specific layer.
type innerProvider interface {
	Inner() search.Provider
}

// Inner returns the provider the cache wraps.
func (c *CachingProvider) Inner() search.Provider { return c.inner }

// Inner returns the provider the breaker wraps.
func (c *CircuitBreaker) Inner() search.Provider { return c.inner }

// Inner returns the provider the retrier wraps.
func (r *RetryProvider) Inner() search.Provider { return r.inner }

// BreakerStatus is a point-in-time snapshot of a circuit breaker.
type BreakerStatus struct {
	// State is "closed", "open", or "half-open".
	State string
	// ConsecutiveFailures is the current closed-state failure streak.
	ConsecutiveFailures int
	// CooldownRemaining is how long an open breaker stays open before the
	// next half-open trial; zero otherwise.
	CooldownRemaining time.Duration
	// LastError is the most recent failure, or nil after a success. It explains
	// why a provider is degraded — a silent captcha reads very differently from
	// a transport timeout.
	LastError error
}

// Status snapshots the breaker's current state.
func (c *CircuitBreaker) Status() BreakerStatus {
	c.mu.Lock()
	defer c.mu.Unlock()

	status := BreakerStatus{ConsecutiveFailures: c.failures, LastError: c.lastErr}
	switch c.state {
	case stateOpen:
		status.State = "open"
		if remaining := c.opts.Cooldown - c.opts.now().Sub(c.openedAt); remaining > 0 {
			status.CooldownRemaining = remaining
		}
	case stateHalfOpen:
		status.State = "half-open"
	default:
		status.State = "closed"
	}
	return status
}

// FindBreaker walks the decorator chain of p and returns the first circuit
// breaker, or nil when the chain has none.
func FindBreaker(p search.Provider) *CircuitBreaker {
	for p != nil {
		if breaker, ok := p.(*CircuitBreaker); ok {
			return breaker
		}
		wrapper, ok := p.(innerProvider)
		if !ok {
			return nil
		}
		p = wrapper.Inner()
	}
	return nil
}
