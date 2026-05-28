package search

import "errors"

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
