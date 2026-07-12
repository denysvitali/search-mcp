package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/denysvitali/search-mcp/internal/search"
)

func TestSearXNGSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if got := q.Get("q"); got != "golang" {
			t.Errorf("q = %q", got)
		}
		if got := q.Get("format"); got != "json" {
			t.Errorf("format = %q", got)
		}
		if got := q.Get("time_range"); got != "week" {
			t.Errorf("time_range = %q", got)
		}
		if got := q.Get("safesearch"); got != "1" {
			t.Errorf("safesearch = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[
			{"title":"Go","url":"https://go.dev","content":"The Go language","publishedDate":"2026-01-01"},
			{"title":"Two","url":"https://two.test","content":"second"},
			{"title":"Three","url":"https://three.test","content":"third"}
		]}`))
	}))
	defer server.Close()

	p, err := NewSearXNGChecked(server.URL)
	if err != nil {
		t.Fatalf("NewSearXNGChecked: %v", err)
	}
	resp, err := p.Search(context.Background(), search.Request{
		Query: "golang", Count: 2, SafeSearch: "moderate", Freshness: "pw",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("results = %d, want 2 (count cap)", len(resp.Results))
	}
	first := resp.Results[0]
	if first.Title != "Go" || first.URL != "https://go.dev" || first.Source != "searxng" || first.Published != "2026-01-01" {
		t.Errorf("first result = %+v", first)
	}
}

func TestSearXNGErrors(t *testing.T) {
	status := http.StatusTooManyRequests
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	}))
	defer server.Close()

	p, err := NewSearXNGChecked(server.URL)
	if err != nil {
		t.Fatalf("NewSearXNGChecked: %v", err)
	}

	_, err = p.Search(context.Background(), search.Request{Query: "x"})
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("429 err = %v, want ErrRateLimited", err)
	}

	status = http.StatusForbidden
	_, err = p.Search(context.Background(), search.Request{Query: "x"})
	if !errors.Is(err, ErrBlocked) {
		t.Errorf("403 err = %v, want ErrBlocked", err)
	}
}

func TestNewSearXNGCheckedValidation(t *testing.T) {
	if _, err := NewSearXNGChecked(""); err == nil {
		t.Error("empty url should error")
	}
	if _, err := NewSearXNGChecked("ftp://x"); err == nil {
		t.Error("non-http scheme should error")
	}
}
