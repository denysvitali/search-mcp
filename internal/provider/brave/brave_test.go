package brave

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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

// TestBraveClampsCount pins the fix for a request that Brave rejects outright:
// its web search API caps count at 20, and anything larger came back as a 422
// that the service could not distinguish from a real failure.
func TestBraveClampsCount(t *testing.T) {
	for _, tc := range []struct{ requested, want string }{
		{"0", "10"},
		{"5", "5"},
		{"20", "20"},
		{"50", "20"},
		{"100", "20"},
	} {
		t.Run(tc.requested, func(t *testing.T) {
			var got string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.URL.Query().Get("count")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"web":{"results":[{"title":"t","url":"https://example.test/1","description":"d","age":"2025-07-16"}]}}`))
			}))
			defer server.Close()

			count, err := strconv.Atoi(tc.requested)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := NewBrave("key", server.URL).Search(context.Background(), search.Request{Query: "x", Count: count}); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("count = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBraveSendsQueryParameters covers the request shape that had no coverage:
// country, language, safesearch and freshness all reaching the wire.
func TestBraveSendsQueryParameters(t *testing.T) {
	var got url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"web":{"results":[{"title":"t","url":"https://example.test/1","description":"d","age":"2025-07-16"}]}}`))
	}))
	defer server.Close()

	resp, err := NewBrave("key", server.URL).Search(context.Background(), search.Request{
		Query:      "golang",
		Count:      5,
		Country:    "de",
		Language:   "en",
		SafeSearch: "moderate",
		Freshness:  "pw",
	})
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"q":           "golang",
		"country":     "de",
		"search_lang": "en",
		"safesearch":  "moderate",
		"freshness":   "pw",
	} {
		if got.Get(key) != want {
			t.Errorf("%s = %q, want %q", key, got.Get(key), want)
		}
	}
	// A single page must not send an offset at all.
	if _, present := got["offset"]; present {
		t.Error("offset should be absent on the first page")
	}
	if len(resp.Results) != 1 || resp.Results[0].Published != "2025-07-16" {
		t.Fatalf("results = %+v, want age mapped to Published", resp.Results)
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
