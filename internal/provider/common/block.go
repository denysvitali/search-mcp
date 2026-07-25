package common

import (
	"bytes"
	"fmt"

	"github.com/denysvitali/search-mcp/internal/search"
)

// challengeMarkers are substrings that identify an anti-bot interstitial served
// with a 2xx status. Public search backends routinely answer a suspicious client
// with a syntactically valid page that carries a captcha instead of results, so
// the status code alone cannot be trusted. Matching is case-insensitive against
// the lowercased body.
var challengeMarkers = [][]byte{
	// DuckDuckGo's anomaly interstitial.
	[]byte("anomaly.js"),
	[]byte("/anomaly/"),
	// Mojeek answers HTTP 200 with <title>Captcha</title> and this prompt.
	[]byte("<title>captcha</title>"),
	[]byte("verification required"),
	// Generic phrasing used by Google/Bing/Yahoo style rate-limit pages.
	[]byte("unusual traffic"),
	[]byte("detected unusual"),
	// Cloudflare and friends.
	[]byte("cf-browser-verification"),
	[]byte("just a moment..."),
	[]byte("checking your browser before accessing"),
	[]byte("enable javascript and cookies to continue"),
}

// IsChallengePage reports whether body looks like an anti-bot challenge rather
// than a real result page. Callers should treat a positive match as
// search.ErrBlocked so the search falls back to another provider and the circuit
// breaker sees a real failure.
func IsChallengePage(body []byte) bool {
	lower := bytes.ToLower(body)
	for _, marker := range challengeMarkers {
		if bytes.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// ErrChallenge builds the ErrBlocked-wrapped error a provider returns when
// IsChallengePage matches.
func ErrChallenge(provider string) error {
	return fmt.Errorf("%s served an anti-bot challenge page instead of results; source ip is rate-limited or fingerprinted: %w", provider, search.ErrBlocked)
}

// ErrMissingResultsContainer builds the ErrBlocked-wrapped error a provider
// returns when a 2xx response parses cleanly but the element that holds the
// result list is absent.
//
// That state is never a legitimate empty result page — an engine with no matches
// still renders its results container. It means either an unrecognised challenge
// page or upstream markup drift, and both must surface as an error: reporting
// "no results" would silently hide a broken provider, let the circuit breaker
// believe it is healthy, and cost a wasted round trip on every future search.
func ErrMissingResultsContainer(provider, selector string) error {
	return fmt.Errorf("%s response contained no %s container; the page markup changed or an unrecognised challenge was served: %w", provider, selector, search.ErrBlocked)
}
