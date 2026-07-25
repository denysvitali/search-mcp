package common

import (
	"errors"
	"testing"

	"github.com/denysvitali/search-mcp/internal/search"
)

func TestIsChallengePage(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"duckduckgo anomaly script", `<html><head><script src="/dist/anomaly.js"></script></head></html>`, true},
		{"duckduckgo anomaly path", `<a href="/anomaly/">verify</a>`, true},
		{"mojeek captcha title", `<html><head><title>Captcha</title></head><body>x</body></html>`, true},
		{"mojeek verification prompt", `<h1>Verification required</h1><p>Please complete the challenge to continue.</p>`, true},
		{"title casing is ignored", `<TITLE>CAPTCHA</TITLE>`, true},
		{"unusual traffic notice", `Our systems have detected unusual traffic from your computer network.`, true},
		{"cloudflare interstitial", `<title>Just a moment...</title><div id="cf-browser-verification"></div>`, true},

		{"ordinary result page", `<div id="links" class="results"><div class="result__body">hi</div></div>`, false},
		{"empty body", ``, false},
		// A page may legitimately discuss captchas without being one; the markers
		// are deliberately specific enough not to fire on prose.
		{"prose mentioning captcha", `<p>This article explains how a captcha works.</p>`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsChallengePage([]byte(tc.body)); got != tc.want {
				t.Fatalf("IsChallengePage() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBlockErrorsWrapErrBlocked(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"challenge", ErrChallenge("mojeek")},
		{"missing container", ErrMissingResultsContainer("yahoo", "#web")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !errors.Is(tc.err, search.ErrBlocked) {
				t.Fatalf("err = %v, want errors.Is search.ErrBlocked", tc.err)
			}
		})
	}
}
