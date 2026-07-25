package yahoo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/denysvitali/search-mcp/internal/provider"
	"github.com/denysvitali/search-mcp/internal/search"
)

// yahooFixture mirrors the real page shape: organic rows live inside div#web.
// The container matters — its absence is how the provider tells a challenge page
// apart from a query that matched nothing.
const yahooFixture = `<html><body><div id="web"><div class="dd algo algo-sr relsrch Sr">
<div class="compTitle"><a href="https://r.search.yahoo.com/x/RU=https%3a%2f%2fexample.test%2fissue%3fid%3d7/RK=2/RS=x"><h3 class="title"><span>Useful Result</span></h3></a></div>
<div class="compText aAbs"><p>Useful <b>search</b> snippet.</p></div></div></div></body></html>`

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

// TestYahooParsesCapturedSERP guards against upstream markup drift by running
// the parser over a real captured Yahoo result page.
func TestYahooParsesCapturedSERP(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "results.html"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(body)
	}))
	defer server.Close()

	resp, err := NewYahoo(server.URL).Search(context.Background(), search.Request{Query: "x", Count: 20})
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
		if strings.Contains(r.URL, "r.search.yahoo.com") {
			t.Fatalf("result %d url not unwrapped: %q", i, r.URL)
		}
	}
}

// TestYahooMissingContainerIsBlocked covers markup drift and unrecognised
// interstitials: a 200 without div#web must fail loudly, not report zero results.
func TestYahooMissingContainerIsBlocked(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body><div id="something-else"></div></body></html>`))
	}))
	defer server.Close()

	_, err := NewYahoo(server.URL).Search(context.Background(), search.Request{Query: "x"})
	if !errors.Is(err, provider.ErrBlocked) {
		t.Fatalf("error = %v, want ErrBlocked when the results container is gone", err)
	}
}

// TestYahooPagesUntilCountReached checks that a request for more results than a
// single SERP holds walks Yahoo's 1-based `b` offset and deduplicates the
// overlap between pages, instead of silently returning one page's worth.
func TestYahooPagesUntilCountReached(t *testing.T) {
	var offsets []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset := r.URL.Query().Get("b")
		offsets = append(offsets, offset)

		var rows strings.Builder
		rows.WriteString(`<html><body><div id="web">`)
		// Each page yields 5 rows; row 0 repeats across pages to exercise dedup.
		for i := range 5 {
			id := offset + "-" + strconv.Itoa(i)
			if i == 0 {
				id = "shared"
			}
			rows.WriteString(`<div class="algo-sr"><div class="compTitle"><a href="https://example.test/` + id +
				`"><h3 class="title"><span>Result ` + id + `</span></h3></a></div>` +
				`<div class="compText"><p>snippet</p></div></div>`)
		}
		rows.WriteString(`</div></body></html>`)
		_, _ = w.Write([]byte(rows.String()))
	}))
	defer server.Close()

	resp, err := NewYahoo(server.URL).Search(context.Background(), search.Request{Query: "x", Count: 12})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"", "11", "21"}; !slices.Equal(offsets, want) {
		t.Fatalf("offsets = %v, want %v", offsets, want)
	}
	// 3 pages x 5 rows, minus the shared row counted twice = 13, capped at 12.
	if len(resp.Results) != 12 {
		t.Fatalf("results = %d, want 12", len(resp.Results))
	}
	seen := make(map[string]bool, len(resp.Results))
	for _, r := range resp.Results {
		if seen[r.URL] {
			t.Fatalf("duplicate url across pages: %q", r.URL)
		}
		seen[r.URL] = true
	}
}

// TestYahooKeepsEarlierPagesWhenLaterOneFails checks that a block on page 2 does
// not throw away the results page 1 already produced.
func TestYahooKeepsEarlierPagesWhenLaterOneFails(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls > 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(yahooFixture))
	}))
	defer server.Close()

	resp, err := NewYahoo(server.URL).Search(context.Background(), search.Request{Query: "x", Count: 20})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("results = %d, want the single row page 1 returned", len(resp.Results))
	}
}

// TestYahooFirstPageFailurePropagates is the counterpart: with nothing banked
// yet, the error must reach the caller so the service can fall back.
func TestYahooFirstPageFailurePropagates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	_, err := NewYahoo(server.URL).Search(context.Background(), search.Request{Query: "x", Count: 20})
	if !errors.Is(err, provider.ErrRateLimited) {
		t.Fatalf("error = %v, want ErrRateLimited", err)
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
