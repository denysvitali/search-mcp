package brave

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/denysvitali/search-mcp/internal/provider"
	"github.com/denysvitali/search-mcp/internal/search"
)

func TestNewBraveCheckedRejectsEmptyKey(t *testing.T) {
	if _, err := NewBraveChecked(""); err == nil {
		t.Fatal("expected error for empty key")
	}
	if _, err := NewBraveChecked("   "); err == nil {
		t.Fatal("expected error for whitespace key")
	}
	if _, err := NewBraveChecked("real-key"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBraveSearchFailsFastWithoutKey(t *testing.T) {
	b := NewBrave("")
	_, err := b.Search(context.Background(), search.Request{Query: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestBraveSearchSurfacesRateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	b := NewBrave("key", server.URL)
	_, err := b.Search(context.Background(), search.Request{Query: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, provider.ErrRateLimited) {
		t.Fatalf("error = %v, want ErrRateLimited", err)
	}
}

func TestBraveSearchPropagatesExtraHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Test-Header"); got != "brave-val" {
			t.Errorf("X-Test-Header = %q, want brave-val", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"web":{"results":[]}}`))
	}))
	defer server.Close()

	b := NewBrave("key", server.URL)
	_, err := b.Search(context.Background(), search.Request{
		Query:        "x",
		ExtraHeaders: map[string]string{"X-Test-Header": "brave-val"},
	})
	if err != nil {
		t.Fatal(err)
	}
}
