package bing

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/denysvitali/search-mcp/internal/provider"
	"github.com/denysvitali/search-mcp/internal/search"
)

func TestBingSearchParsesResultsAndOptions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		for key, want := range map[string]string{
			"q": "go mcp", "count": "10", "cc": "de", "setlang": "de", "adlt": "strict", "filters": `ex1:"ez2"`,
		} {
			if got := query.Get(key); got != want {
				t.Errorf("%s = %q, want %q", key, got, want)
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body><ol id="b_results">
<li class="b_algo"><h2><a href="https://example.test/one?utm_source=bing">Bing Result</a></h2><div class="b_caption"><p>Jan 2, 2026 · Useful snippet.</p></div></li>
<li class="b_algo"><h2><a href="https://example.test/two">Second Result</a></h2><div class="b_caption"><p>Second snippet.</p></div></li>
</ol></body></html>`))
	}))
	defer server.Close()

	resp, err := NewBing(server.URL).Search(context.Background(), search.Request{
		Query: "go mcp", Count: 2, Country: "DE", Language: "DE", SafeSearch: "strict", Freshness: "week",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(resp.Results))
	}
	if got := resp.Results[0]; got.Title != "Bing Result" || got.URL != "https://example.test/one?utm_source=bing" || got.Published != "2026-01-02" || got.Description != "Useful snippet." {
		t.Fatalf("first result = %+v", got)
	}
}

func TestBingUnwrapsClickURL(t *testing.T) {
	encoded := "a1" + "aHR0cHM6Ly9leGFtcGxlLnRlc3QvY2xpY2s"
	got := unwrapBingURL("https://www.bing.com/ck/a?u=" + encoded)
	if got != "https://example.test/click" {
		t.Fatalf("unwrapped url = %q", got)
	}
}

func TestBingMissingResultsContainerIsBlocked(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body>not a results page</body></html>`))
	}))
	defer server.Close()

	_, err := NewBing(server.URL).Search(context.Background(), search.Request{Query: "x"})
	if !errors.Is(err, provider.ErrBlocked) {
		t.Fatalf("error = %v, want ErrBlocked", err)
	}
}

func TestBingPagesAndDeduplicates(t *testing.T) {
	var firstSeen bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("first")
		if page == "" {
			firstSeen = true
		} else if !firstSeen || page != "11" {
			t.Errorf("unexpected first-page sequence: first=%q seen=%v", page, firstSeen)
		}
		var rows strings.Builder
		rows.WriteString(`<ol id="b_results">`)
		for i := 0; i < 10; i++ {
			id := page + "-" + string(rune('a'+i))
			if i == 0 {
				id = "shared"
			}
			rows.WriteString(`<li class="b_algo"><h2><a href="https://example.test/` + id + `">` + id + `</a></h2></li>`)
		}
		rows.WriteString(`</ol>`)
		_, _ = w.Write([]byte(rows.String()))
	}))
	defer server.Close()

	resp, err := NewBing(server.URL).Search(context.Background(), search.Request{Query: "x", Count: 12})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 12 {
		t.Fatalf("results = %d, want 12", len(resp.Results))
	}
}

func TestBingRejectsUnrelatedResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<ol id="b_results"><li class="b_algo"><h2><a href="https://maps.example/city">City maps</a></h2><div class="b_caption">Find directions nearby</div></li></ol>`))
	}))
	defer server.Close()
	_, err := NewBing(server.URL).Search(context.Background(), search.Request{Query: "golang context cancellation"})
	if !errors.Is(err, provider.ErrBlocked) {
		t.Fatalf("error = %v, want ErrBlocked", err)
	}
}

func TestBingSiteRestriction(t *testing.T) {
	results := []search.Result{{Title: "Context in Go", URL: "https://go.dev/blog/context"}}
	if !bingResultsRelevant("site:go.dev/blog context", results) {
		t.Fatal("matching site and query was rejected")
	}
	results[0].URL = "https://other.example/blog/context"
	if bingResultsRelevant("site:go.dev/blog context", results) {
		t.Fatal("off-site result was accepted")
	}
}
