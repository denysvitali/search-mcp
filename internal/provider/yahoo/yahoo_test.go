package yahoo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/denysvitali/search-mcp/internal/provider"
	"github.com/denysvitali/search-mcp/internal/search"
)

const yahooFixture = `<html><body><div class="dd algo algo-sr relsrch Sr">
<div class="compTitle"><a href="https://r.search.yahoo.com/x/RU=https%3a%2f%2fexample.test%2fissue%3fid%3d7/RK=2/RS=x"><h3 class="title"><span>Useful Result</span></h3></a></div>
<div class="compText aAbs"><p>Useful <b>search</b> snippet.</p></div></div></body></html>`

func TestYahooSearchParsesAndUnwrapsResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("p"); got != "test query" {
			t.Errorf("p = %q, want test query", got)
		}
		_, _ = w.Write([]byte(yahooFixture))
	}))
	defer server.Close()

	resp, err := NewYahoo(server.URL).Search(context.Background(), search.Request{Query: "test query", Count: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(resp.Results))
	}
	got := resp.Results[0]
	if got.Title != "Useful Result" || got.URL != "https://example.test/issue?id=7" || got.Description != "Useful search snippet." {
		t.Fatalf("result = %#v", got)
	}
}

func TestYahooSearchClassifiesBlocking(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer server.Close()
			_, err := NewYahoo(server.URL).Search(context.Background(), search.Request{Query: "x"})
			if err == nil || (!errors.Is(err, provider.ErrBlocked) && !errors.Is(err, provider.ErrRateLimited)) {
				t.Fatalf("error = %v, want blocked or rate limited", err)
			}
		})
	}
}
