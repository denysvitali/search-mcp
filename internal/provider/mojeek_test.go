package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/denysvitali/search-mcp/internal/search"
)

const mojeekFixture = `<!DOCTYPE html><html><body>
<div class="results">
<ul class="results-standard">
<!--ls-->
<li class="r1"><a title="https://example.test/first" href="https://example.test/first" class="ob"><p class="i"><span class="url">https://example.test</span></p></a><h2><a class="title" title="https://example.test/first" href="https://example.test/first">First Result</a></h2><p class="s">First <strong>snippet</strong> with highlights.</p></li>
<li class="r2 clu-result"><a title="https://example.test/second" href="https://example.test/second" class="ob"><p class="i"><span class="url">https://example.test</span></p></a><h2><a class="title" title="https://example.test/second" href="https://example.test/second">Second Result</a></h2><p class="s">Second snippet.</p><p class="more"><a href="/search?q=site%3Aexample.test">See more &raquo;</a></p></li>
</ul>
</div>
</body></html>`

func TestMojeekSearchParsesResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if got := r.URL.Query().Get("q"); got != "model context protocol" {
			t.Errorf("q = %q, want model context protocol", got)
		}
		if got := r.URL.Query().Get("safe"); got != "1" {
			t.Errorf("safe = %q, want 1", got)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(mojeekFixture))
	}))
	defer server.Close()

	m := NewMojeek(server.URL)
	resp, err := m.Search(context.Background(), search.Request{Query: "model context protocol", Count: 5})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if resp.Provider != "mojeek" {
		t.Fatalf("provider = %q, want mojeek", resp.Provider)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(resp.Results))
	}
	if resp.Results[0].Title != "First Result" {
		t.Fatalf("first title = %q, want First Result", resp.Results[0].Title)
	}
	if resp.Results[0].URL != "https://example.test/first" {
		t.Fatalf("first url = %q, want https://example.test/first", resp.Results[0].URL)
	}
	if resp.Results[0].Description != "First snippet with highlights." {
		t.Fatalf("first desc = %q, want flattened snippet", resp.Results[0].Description)
	}
	if resp.Results[1].URL != "https://example.test/second" {
		t.Fatalf("second url = %q, want https://example.test/second", resp.Results[1].URL)
	}
}

func TestMojeekSearchHonoursCount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(mojeekFixture))
	}))
	defer server.Close()

	m := NewMojeek(server.URL)
	resp, err := m.Search(context.Background(), search.Request{Query: "x", Count: 1})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(resp.Results))
	}
}

func TestMojeekSearchPassesLanguageAndCountry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("lb"); got != "en" {
			t.Errorf("lb = %q, want en", got)
		}
		if got := r.URL.Query().Get("arc"); got != "gb" {
			t.Errorf("arc = %q, want gb", got)
		}
		_, _ = w.Write([]byte(mojeekFixture))
	}))
	defer server.Close()

	m := NewMojeek(server.URL)
	if _, err := m.Search(context.Background(), search.Request{Query: "x", Language: "EN", Country: "GB"}); err != nil {
		t.Fatal(err)
	}
}

func TestMojeekSearchSurfacesRateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	m := NewMojeek(server.URL)
	_, err := m.Search(context.Background(), search.Request{Query: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("error = %q, want rate limit mention", err.Error())
	}
}

func TestMojeekSinceFreshness(t *testing.T) {
	if got := mojeekSince(""); got != "" {
		t.Errorf("empty freshness = %q, want empty", got)
	}
	if got := mojeekSince("week"); len(got) != 8 {
		t.Errorf("week = %q, want YYYYMMDD", got)
	}
}
