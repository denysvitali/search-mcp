package provider

import "github.com/denysvitali/search-mcp/internal/search"

// The canonical sentinels live in the search package (provider imports search,
// so they cannot live here without forcing search to import provider). These
// aliases keep the provider-local spelling working; because they are the same
// error values, errors.Is matches whether callers compare against
// provider.ErrRateLimited or search.ErrRateLimited.
var (
	// ErrRateLimited signals a transient rate-limit (e.g. HTTP 429).
	ErrRateLimited = search.ErrRateLimited

	// ErrBlocked signals an anti-bot/captcha challenge.
	ErrBlocked = search.ErrBlocked
)
