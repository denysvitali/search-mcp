package google

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/denysvitali/search-mcp/internal/provider"
	"github.com/denysvitali/search-mcp/internal/search"
)

func TestGoogleSearchParsesResultsAndOptions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		for key, want := range map[string]string{
			"q": "go mcp", "num": "10", "gbv": "1", "filter": "0", "hl": "de", "gl": "de", "safe": "active", "tbs": "qdr:w",
		} {
			if got := query.Get(key); got != want {
				t.Errorf("%s = %q, want %q", key, got, want)
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body><div id="search">
<div class="MjjYud"><h3><a href="https://example.test/one">Google Result</a></h3><div class="VwiC3b">Jan 2, 2026 · Useful snippet.</div></div>
<div class="MjjYud"><h3><a href="https://example.test/two">Second Result</a></h3><div class="VwiC3b">Second snippet.</div></div>
</div></body></html>`))
	}))
	defer server.Close()

	resp, err := NewGoogle(server.URL).Search(context.Background(), search.Request{
		Query: "go mcp", Count: 2, Country: "DE", Language: "DE", SafeSearch: "strict", Freshness: "week",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(resp.Results))
	}
	if got := resp.Results[0]; got.Title != "Google Result" || got.URL != "https://example.test/one" || got.Published != "2026-01-02" || got.Description != "Useful snippet." {
		t.Fatalf("first result = %+v", got)
	}
}

func TestGoogleUnwrapsRedirectURL(t *testing.T) {
	got := unwrapGoogleURL("https://www.google.com/url?q=https%3A%2F%2Fexample.test%2Fresult%3Fid%3D1")
	if got != "https://example.test/result?id=1" {
		t.Fatalf("unwrapped url = %q", got)
	}
}

func TestGoogleMissingResultsContainerIsBlocked(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body>not a results page</body></html>`))
	}))
	defer server.Close()

	_, err := NewGoogle(server.URL).Search(context.Background(), search.Request{Query: "x"})
	if !errors.Is(err, provider.ErrBlocked) {
		t.Fatalf("error = %v, want ErrBlocked", err)
	}
}

func TestGoogleChallengeIsBlocked(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><head><meta content="0;url=/httpservice/retry/enablejs"></head></html>`))
	}))
	defer server.Close()

	_, err := NewGoogle(server.URL).Search(context.Background(), search.Request{Query: "x"})
	if !errors.Is(err, provider.ErrBlocked) {
		t.Fatalf("error = %v, want ErrBlocked", err)
	}
}
