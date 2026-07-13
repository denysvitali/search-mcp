package duckduckgo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/denysvitali/search-mcp/internal/provider"
	"github.com/denysvitali/search-mcp/internal/search"
)

const duckFixture = `<!DOCTYPE html><html><body>
<div class="result results_links results_links_deep web-result">
  <div class="result__body links_main links_deep">
    <h2 class="result__title">
      <a class="result__a" href="https://example.test/ddg">Example DDG Result</a>
    </h2>
    <a class="result__snippet" href="https://example.test/ddg">A snippet describing the result.</a>
  </div>
</div>
</body></html>`

func TestDuckDuckGoSearchParsesResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(duckFixture))
	}))
	defer server.Close()

	d := NewDuckDuckGo(server.URL)
	resp, err := d.Search(context.Background(), search.Request{Query: "x", Count: 5})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(resp.Results))
	}
	if resp.Results[0].Title != "Example DDG Result" {
		t.Fatalf("title = %q", resp.Results[0].Title)
	}
	if resp.Results[0].URL != "https://example.test/ddg" {
		t.Fatalf("url = %q", resp.Results[0].URL)
	}
}

func TestDuckDuckGoSearchDetectsAnomaly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`<html><head><script src="/dist/anomaly.js"></script></head><body></body></html>`))
	}))
	defer server.Close()

	d := NewDuckDuckGo(server.URL)
	_, err := d.Search(context.Background(), search.Request{Query: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, provider.ErrBlocked) {
		t.Fatalf("error = %v, want ErrBlocked", err)
	}
}

func TestDuckDuckGoSearchPropagatesExtraHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Test-Header"); got != "ddg-val" {
			t.Errorf("X-Test-Header = %q, want ddg-val", got)
		}
		_, _ = w.Write([]byte(duckFixture))
	}))
	defer server.Close()

	d := NewDuckDuckGo(server.URL)
	_, err := d.Search(context.Background(), search.Request{
		Query:        "x",
		ExtraHeaders: map[string]string{"X-Test-Header": "ddg-val"},
	})
	if err != nil {
		t.Fatal(err)
	}
}
