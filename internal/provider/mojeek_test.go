package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("error = %v, want ErrRateLimited", err)
	}
}

func TestMojeekSearchClassifiesForbiddenAsBlocked(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	m := NewMojeek(server.URL)
	_, err := m.Search(context.Background(), search.Request{Query: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("error = %v, want ErrBlocked", err)
	}
}

func TestMojeekSafeSearch(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"off", "0"},
		{"0", "0"},
		{"none", "0"},
		{"OFF", "0"},
		{" off ", "0"},
		{"", "1"},
		{"strict", "1"},
		{"1", "1"},
		{"on", "1"},
	}
	for _, c := range cases {
		if got := mojeekSafeSearch(c.in); got != c.want {
			t.Errorf("mojeekSafeSearch(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMojeekSinceFreshness(t *testing.T) {
	empties := []string{"", "   ", "bogus", "lastdecade"}
	for _, in := range empties {
		if got := mojeekSince(in); got != "" {
			t.Errorf("mojeekSince(%q) = %q, want empty", in, got)
		}
	}
	nonEmpty := []string{"day", "d", "pd", "week", "w", "pw", "month", "m", "pm", "year", "y", "py", "WEEK"}
	for _, in := range nonEmpty {
		if got := mojeekSince(in); len(got) != 8 {
			t.Errorf("mojeekSince(%q) = %q, want YYYYMMDD", in, got)
		}
	}
}

func TestMojeekSearchPropagatesExtraHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Test-Header"); got != "abc123" {
			t.Errorf("X-Test-Header = %q, want abc123", got)
		}
		_, _ = w.Write([]byte(mojeekFixture))
	}))
	defer server.Close()

	m := NewMojeek(server.URL)
	_, err := m.Search(context.Background(), search.Request{
		Query:        "x",
		ExtraHeaders: map[string]string{"X-Test-Header": "abc123"},
	})
	if err != nil {
		t.Fatal(err)
	}
}
