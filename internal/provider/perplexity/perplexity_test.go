package perplexity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/denysvitali/search-mcp/internal/search"
)

func TestSearchRequestAndResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer test-key" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("incorrect request method or headers")
		}
		var body struct {
			Query     string   `json:"query"`
			Count     int      `json:"max_results"`
			Country   string   `json:"country"`
			Languages []string `json:"search_language_filter"`
			Freshness string   `json:"search_recency_filter"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body.Query != "Go concurrency" || body.Count != 20 || body.Country != "US" || len(body.Languages) != 1 || body.Languages[0] != "en" || body.Freshness != "week" {
			t.Errorf("unexpected payload: %+v", body)
		}
		fmt.Fprint(w, `{"results":[{"title":"Go docs","url":"https://go.dev/doc/","snippet":"Channels and goroutines","date":"2026-09-01","last_updated":"2026-09-25"},{"title":"Invalid"}]}`)
	}))
	defer server.Close()
	p, err := New("test-key", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Search(context.Background(), search.Request{Query: "Go concurrency", Count: 50, Country: "us", Language: "EN", Freshness: "pw"})
	if err != nil || len(resp.Results) != 1 {
		t.Fatalf("%+v, %v", resp, err)
	}
	got := resp.Results[0]
	if got.Source != "perplexity" || got.Description != "Channels and goroutines" || got.Published != "2026-09-01" {
		t.Fatalf("incorrect result metadata: %+v", got)
	}
}

func TestResponseFailures(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		wantError bool
	}{
		{"rate limit", 429, "", true},
		{"auth", 401, "", true},
		{"upstream", 503, "", true},
		{"invalid JSON", 200, "<html>challenge</html>", true},
		{"error envelope", 200, `{"error":"unavailable"}`, true},
		{"null results", 200, `{"results":null}`, true},
		{"no matches", 200, `{"results":[]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "2")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			p, err := New("key", server.URL)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := p.Search(context.Background(), search.Request{Query: "q"})
			if (err != nil) != tc.wantError {
				t.Fatalf("response %+v, error %v", resp, err)
			}
			if tc.status == 429 {
				var limited *search.RateLimitedError
				if !errors.Is(err, search.ErrRateLimited) || !errors.As(err, &limited) || limited.RetryAfter != 2*time.Second {
					t.Fatalf("lost Retry-After: %v", err)
				}
			}
		})
	}
}

func TestValidationAndCancellation(t *testing.T) {
	if _, err := New(" ", ""); err == nil {
		t.Fatal("accepted empty key")
	}
	if _, err := New("key", "relative/path"); err == nil {
		t.Fatal("accepted relative endpoint")
	}
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("invalid/canceled request reached server")
	}))
	defer server.Close()
	p, err := New("key", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Search(ctx, search.Request{Query: "q"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
	if _, err := p.Search(context.Background(), search.Request{Query: "q", Freshness: "invalid"}); err == nil {
		t.Fatal("accepted unsupported freshness")
	}
}
