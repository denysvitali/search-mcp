package duckduckgo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/denysvitali/search-mcp/internal/provider"
	"github.com/denysvitali/search-mcp/internal/search"
)

// duckFixture mirrors the real page shape: result rows live inside
// div#links.results. The container matters — its absence is how the provider
// tells a challenge page apart from a query that matched nothing.
const duckFixture = `<!DOCTYPE html><html><body>
<div id="links" class="results">
<div class="result results_links results_links_deep web-result">
  <div class="result__body links_main links_deep">
    <h2 class="result__title">
      <a class="result__a" href="https://example.test/ddg">Example DDG Result</a>
    </h2>
    <a class="result__snippet" href="https://example.test/ddg">A snippet describing the result.</a>
  </div>
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

// serveFixture replays a captured DuckDuckGo response so the parser is
// exercised against real markup rather than a hand-written approximation.
func serveFixture(t *testing.T, name string, status int) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
}

// TestDuckDuckGoParsesCapturedSERP guards against upstream markup drift: it runs
// the parser over a real captured result page rather than a synthetic one.
func TestDuckDuckGoParsesCapturedSERP(t *testing.T) {
	server := serveFixture(t, "results.html", http.StatusOK)
	defer server.Close()

	resp, err := NewDuckDuckGo(server.URL).Search(context.Background(), search.Request{Query: "x", Count: 20})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(resp.Results) < 5 {
		t.Fatalf("results = %d, want the captured page's full row set", len(resp.Results))
	}
	for i, r := range resp.Results {
		if r.Title == "" || r.URL == "" {
			t.Fatalf("result %d incomplete: %+v", i, r)
		}
		if !strings.HasPrefix(r.URL, "http") {
			t.Fatalf("result %d url not unwrapped: %q", i, r.URL)
		}
	}
}

// TestDuckDuckGoCapturedAnomalyIsBlocked replays the real anomaly interstitial.
func TestDuckDuckGoCapturedAnomalyIsBlocked(t *testing.T) {
	server := serveFixture(t, "anomaly.html", http.StatusOK)
	defer server.Close()

	_, err := NewDuckDuckGo(server.URL).Search(context.Background(), search.Request{Query: "x"})
	if !errors.Is(err, provider.ErrBlocked) {
		t.Fatalf("error = %v, want ErrBlocked", err)
	}
}

// TestDuckDuckGoCapturedNoResultsIsNotBlocked is the counterpart: a query that
// genuinely matched nothing still renders the results container, and must come
// back as an empty success rather than a block.
func TestDuckDuckGoCapturedNoResultsIsNotBlocked(t *testing.T) {
	server := serveFixture(t, "no_results.html", http.StatusOK)
	defer server.Close()

	resp, err := NewDuckDuckGo(server.URL).Search(context.Background(), search.Request{Query: "x"})
	if err != nil {
		t.Fatalf("a genuine zero-match page must not be reported as an error: %v", err)
	}
	if len(resp.Results) != 0 {
		t.Fatalf("results = %d, want 0", len(resp.Results))
	}
}

// TestDuckDuckGoMissingContainerIsBlocked covers markup drift: a 200 that parses
// fine but has no results container must fail loudly instead of silently
// reporting zero results.
func TestDuckDuckGoMissingContainerIsBlocked(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body><div id="something-else"></div></body></html>`))
	}))
	defer server.Close()

	_, err := NewDuckDuckGo(server.URL).Search(context.Background(), search.Request{Query: "x"})
	if !errors.Is(err, provider.ErrBlocked) {
		t.Fatalf("error = %v, want ErrBlocked when the results container is gone", err)
	}
}

func TestDuckDuckGoRegion(t *testing.T) {
	for _, tc := range []struct {
		name              string
		country, language string
		want              string
	}{
		{"both", "uk", "en", "en-uk"},
		{"country only", "de", "", "en-de"},
		{"language only", "", "fr", "fr-wt"},
		{"neither", "", "", ""},
		{"whitespace and case", " UK ", " EN ", "en-uk"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := duckRegion(tc.country, tc.language); got != tc.want {
				t.Fatalf("duckRegion(%q, %q) = %q, want %q", tc.country, tc.language, got, tc.want)
			}
		})
	}
}

func TestDuckDuckGoSendsSafeSearch(t *testing.T) {
	for _, tc := range []struct{ mode, want string }{
		{"off", "-2"},
		{"moderate", "-1"},
		{"strict", "1"},
		{"", ""},
		{"nonsense", ""},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			var got string
			var present bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				got = r.PostForm.Get("kp")
				_, present = r.PostForm["kp"]
				_, _ = w.Write([]byte(duckFixture))
			}))
			defer server.Close()

			if _, err := NewDuckDuckGo(server.URL).Search(context.Background(), search.Request{Query: "x", SafeSearch: tc.mode}); err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if present {
					t.Fatalf("kp = %q, want the parameter to be absent", got)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("kp = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDuckDuckGoLiftsPublishedDateFromSnippet(t *testing.T) {
	const dated = `<!DOCTYPE html><html><body><div id="links" class="results">
<div class="result__body">
  <h2 class="result__title"><a class="result__a" href="https://example.test/dated">Dated Result</a></h2>
  <a class="result__snippet">Jul 16, 2025 · Body of the snippet.</a>
</div></div></body></html>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(dated))
	}))
	defer server.Close()

	resp, err := NewDuckDuckGo(server.URL).Search(context.Background(), search.Request{Query: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(resp.Results))
	}
	if resp.Results[0].Published != "2025-07-16" {
		t.Fatalf("published = %q, want 2025-07-16", resp.Results[0].Published)
	}
	if resp.Results[0].Description != "Body of the snippet." {
		t.Fatalf("description = %q, want the date stripped", resp.Results[0].Description)
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
