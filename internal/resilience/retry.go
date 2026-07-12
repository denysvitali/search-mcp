package resilience

import (
	"context"
	"errors"
	"math/rand"
	"time"

	"github.com/denysvitali/search-mcp/internal/search"
)

// maxRetryAfterWait caps how long a server-advised Retry-After can make a
// single backoff sleep.
const maxRetryAfterWait = 30 * time.Second

// RetryOptions configures RetryProvider.
type RetryOptions struct {
	// MaxAttempts is the total number of attempts (including the first). Values
	// < 1 are clamped to 1 (no retries).
	MaxAttempts int

	// BaseDelay is the base for the exponential backoff. Values <= 0 are clamped
	// to a small default.
	BaseDelay time.Duration

	// sleep, if set, replaces time.Sleep-style waiting. It receives the wait
	// duration and the context; returning ctx.Err() lets the caller abort. Used
	// by tests to avoid real delays.
	sleep func(ctx context.Context, d time.Duration) error

	// jitter, if set, maps a base delay to an actual delay. Defaults to a
	// full-jitter strategy over [d/2, d]. Used by tests for determinism.
	jitter func(d time.Duration) time.Duration
}

// RetryProvider retries the inner provider's Search on transient failures using
// exponential backoff with jitter. It does not retry on ErrBlocked or on
// context cancellation/deadline.
type RetryProvider struct {
	inner search.Provider
	opts  RetryOptions
	rng   *rand.Rand
}

// NewRetryProvider wraps inner with retry behaviour.
func NewRetryProvider(inner search.Provider, opts RetryOptions) *RetryProvider {
	if opts.MaxAttempts < 1 {
		opts.MaxAttempts = 1
	}
	if opts.BaseDelay <= 0 {
		opts.BaseDelay = 200 * time.Millisecond
	}
	return &RetryProvider{
		inner: inner,
		opts:  opts,
		// Seeded per-instance; jitter does not need to be cryptographically
		// strong and tests inject their own deterministic hooks.
		rng: rand.New(rand.NewSource(time.Now().UnixNano())), //nolint:gosec // backoff jitter is not security-sensitive
	}
}

// Name delegates to the inner provider.
func (r *RetryProvider) Name() string { return r.inner.Name() }

// Search calls the inner provider, retrying on transient errors.
func (r *RetryProvider) Search(ctx context.Context, req search.Request) (search.Response, error) {
	var lastErr error
	for attempt := 0; attempt < r.opts.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return search.Response{}, lastErr
			}
			return search.Response{}, err
		}

		resp, err := r.inner.Search(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err

		if !isRetryable(err) {
			return search.Response{}, err
		}

		// No point sleeping after the final attempt.
		if attempt == r.opts.MaxAttempts-1 {
			break
		}

		delay := r.backoff(attempt)
		// A server-advised Retry-After beats the blind exponential guess,
		// capped so a hostile header cannot stall the caller.
		var rateLimited *search.RateLimitedError
		if errors.As(err, &rateLimited) && rateLimited.RetryAfter > 0 {
			delay = min(rateLimited.RetryAfter, maxRetryAfterWait)
		}
		if err := r.sleepCtx(ctx, delay); err != nil {
			// Context expired during backoff: return the last provider error so
			// callers can classify it, falling back to the ctx error otherwise.
			if lastErr != nil {
				return search.Response{}, lastErr
			}
			return search.Response{}, err
		}
	}
	return search.Response{}, lastErr
}

// isRetryable reports whether err should trigger a retry. ErrBlocked and context
// cancellation/deadline are explicitly non-retryable; ErrRateLimited and any
// other (generic network/5xx-style) error are retryable.
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, search.ErrBlocked) {
		return false
	}
	// ErrRateLimited and generic errors are retryable.
	return true
}

// backoff computes the (jittered) delay for the given zero-based attempt index.
func (r *RetryProvider) backoff(attempt int) time.Duration {
	// Exponential: base * 2^attempt, guarding against overflow.
	d := r.opts.BaseDelay
	for i := 0; i < attempt; i++ {
		next := d * 2
		if next < d { // overflow
			break
		}
		d = next
	}
	if r.opts.jitter != nil {
		return r.opts.jitter(d)
	}
	return r.fullJitter(d)
}

// fullJitter returns a random duration in [d/2, d].
func (r *RetryProvider) fullJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	half := d / 2
	return half + time.Duration(r.rng.Int63n(int64(d-half)+1))
}

// sleepCtx waits for d or until ctx is done, whichever comes first.
func (r *RetryProvider) sleepCtx(ctx context.Context, d time.Duration) error {
	if r.opts.sleep != nil {
		return r.opts.sleep(ctx, d)
	}
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
