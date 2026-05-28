// Package resilience provides composable search.Provider decorators that add
// retry, circuit breaking, and in-memory caching around an inner provider.
//
// Each decorator implements search.Provider by wrapping an inner provider and
// delegating Name(). They can be composed individually or via Wrap, which
// stacks them (innermost to outermost) as cache(breaker(retry(inner))) so that
// cache hits skip the breaker and retry logic, while the retry layer wraps the
// real network call.
package resilience

import (
	"time"

	"github.com/denysvitali/search-mcp/internal/search"
)

// Config bundles the tunables for the resilience decorators. The zero value is
// usable: DefaultConfig fills in sensible defaults, and constructors clamp
// individual fields, so callers only need to override what they care about.
type Config struct {
	// Retry
	RetryMaxAttempts int           // total attempts including the first (>=1)
	RetryBaseDelay   time.Duration // base for exponential backoff

	// Circuit breaker
	BreakerThreshold int           // consecutive failures before opening (>=1)
	BreakerCooldown  time.Duration // open -> half-open wait

	// Cache
	CacheTTL time.Duration // 0 or negative disables caching
}

// DefaultConfig returns the backward-compatible defaults: retry and breaker
// enabled, caching disabled.
func DefaultConfig() Config {
	return Config{
		RetryMaxAttempts: 3,
		RetryBaseDelay:   200 * time.Millisecond,
		BreakerThreshold: 5,
		BreakerCooldown:  30 * time.Second,
		CacheTTL:         0,
	}
}

// Wrap composes the decorators around inner in the order
// cache(breaker(retry(inner))). The retry layer is innermost so it retries the
// real call; the cache is outermost so hits skip the breaker and retry.
//
// A non-positive CacheTTL leaves the cache as a pass-through. Retry and breaker
// are always applied (with their defaults clamped in).
func Wrap(inner search.Provider, cfg Config) search.Provider {
	retried := NewRetryProvider(inner, RetryOptions{
		MaxAttempts: cfg.RetryMaxAttempts,
		BaseDelay:   cfg.RetryBaseDelay,
	})
	broken := NewCircuitBreaker(retried, BreakerOptions{
		Threshold: cfg.BreakerThreshold,
		Cooldown:  cfg.BreakerCooldown,
	})
	cached := NewCachingProvider(broken, CacheOptions{
		TTL: cfg.CacheTTL,
	})
	return cached
}
