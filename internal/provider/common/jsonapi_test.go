package common

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/denysvitali/search-mcp/internal/search"
)

func TestJSONAPIStatusAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   error
	}{
		{429, `{}`, search.ErrRateLimited}, {403, `{}`, search.ErrBlocked}, {200, `<html>verify you are human</html>`, search.ErrBlocked},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "5")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			p := JSONAPI{Name: "test", Endpoint: server.URL, Decode: func([]byte) ([]search.Result, error) { t.Error("decoded failure as results"); return nil, nil }}
			_, err := p.Search(context.Background(), search.Request{Query: "q"})
			if !errors.Is(err, tc.want) {
				t.Fatalf("%v", err)
			}
			if tc.status == 429 {
				var limited *search.RateLimitedError
				if !errors.As(err, &limited) || limited.RetryAfter != 5*time.Second {
					t.Fatal("lost Retry-After")
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, err = p.Search(ctx, search.Request{Query: "q"})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
		})
	}
}

func TestHTMLSnippetAndFreshness(t *testing.T) {
	got := HTMLSnippet(`<p>Use <code>ctx.Done()</code>.</p><p>Go &amp; Rust.</p><script>noise()</script>`)
	if got != "Use ctx.Done(). Go & Rust." {
		t.Fatalf("snippet = %q", got)
	}
	long := HTMLSnippet(strings.Repeat("界", 900))
	if !utf8.ValidString(long) || len([]rune(long)) != 801 {
		t.Fatal("snippet truncation split Unicode")
	}
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	for _, f := range []string{"pd", "pw", "pm", "py", "hour", "day", "week", "month", "year"} {
		since, err := FreshnessSince(f, now)
		if err != nil || !since.Before(now) {
			t.Fatalf("%s: %v %v", f, since, err)
		}
	}
	if _, err := FreshnessSince("invalid", now); err == nil {
		t.Fatal("invalid freshness accepted")
	}
}
