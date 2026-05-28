package provider

import "errors"

// Sentinel errors let callers distinguish retryable from permanent failures via
// errors.Is. Providers wrap them with fmt.Errorf("...: %w", ErrX) so the
// underlying sentinel stays inspectable.
var (
	// ErrRateLimited signals a transient rate-limit (e.g. HTTP 429). Callers
	// should back off and may retry later or fall back to another provider.
	ErrRateLimited = errors.New("rate limited")

	// ErrBlocked signals an anti-bot/captcha challenge. It is effectively
	// permanent for the current source IP/fingerprint; retrying immediately is
	// pointless, so callers should fall back to another provider.
	ErrBlocked = errors.New("blocked by anti-bot challenge")
)
