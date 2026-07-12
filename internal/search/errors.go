package search

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// Sentinel errors let callers (the service's fan-out, resilience decorators,
// MCP/CLI handlers) classify failures via errors.Is. Providers wrap them with
// fmt.Errorf("...: %w", ErrX) so the underlying sentinel stays inspectable.
//
// They live in the search package rather than the provider package because
// provider imports search; keeping them here lets both sides reference the
// same values without an import cycle.
var (
	// ErrRateLimited signals a transient rate-limit (e.g. HTTP 429). Callers
	// should back off and may retry later or fall back to another provider.
	ErrRateLimited = errors.New("rate limited")

	// ErrBlocked signals an anti-bot/captcha challenge. It is effectively
	// permanent for the current source IP/fingerprint; retrying immediately is
	// pointless, so callers should fall back to another provider.
	ErrBlocked = errors.New("blocked by anti-bot challenge")
)

// RateLimitedError is an ErrRateLimited that carries the server-advised wait
// from a Retry-After header. errors.Is(err, ErrRateLimited) matches it via
// Unwrap, and resilience.RetryProvider uses RetryAfter to size its backoff.
type RateLimitedError struct {
	RetryAfter time.Duration
}

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("rate limited (retry after %s)", e.RetryAfter)
}

func (e *RateLimitedError) Unwrap() error { return ErrRateLimited }

// NewRateLimitedError builds the rate-limit error for an HTTP 429 response,
// attaching the parsed Retry-After hint when the header is usable.
func NewRateLimitedError(header http.Header) error {
	if after, ok := ParseRetryAfter(header.Get("Retry-After"), time.Now()); ok {
		return &RateLimitedError{RetryAfter: after}
	}
	return ErrRateLimited
}

// ParseRetryAfter parses a Retry-After header value, which is either a delay
// in seconds or an HTTP date. Zero, negative, and unparseable values report
// ok=false.
func ParseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	}
	if t, err := http.ParseTime(value); err == nil {
		if d := t.Sub(now); d > 0 {
			return d, true
		}
	}
	return 0, false
}
