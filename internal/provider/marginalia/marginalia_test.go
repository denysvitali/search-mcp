package marginalia

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

// marginaliaFixture mirrors a real api.marginalia.nu/public/search response.
const marginaliaFixture = `{
  "license": "CC-BY-NC-SA 4.0",
  "page": 1,
  "pages": 11,
  "query": "kubernetes operator",
  "results": [
    {"url":"https://oracle.github.io/weblogic-kubernetes-operator/","title":"WebLogic Kubernetes Operator","description":"The WebLogic Kubernetes Operator supports running your domains on Kubernetes.","quality":3.13,"format":"html","resultsFromDomain":904,"details":[[]]},
    {"url":"https://example.test/second","title":"Second","description":"  padded description  ","quality":1.0,"format":"html","resultsFromDomain":2,"details":[[]]},
    {"url":"","title":"No URL","description":"skipped","quality":0.0,"format":"html","resultsFromDomain":0,"details":[[]]}
  ]
}`

func TestMarginaliaSearchParsesResults(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q, want application/json", got)
		}
		// The public API needs no credentials; assert we send none.
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Api-Key") != "" {
			t.Error("marginalia is keyless; no credential header should be sent")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(marginaliaFixture))
	}))
	defer server.Close()

	resp, err := NewMarginalia(server.URL).Search(context.Background(), search.Request{Query: "kubernetes operator", Count: 10})
	if err != nil {
		t.Fatal(err)
	}
	// The query travels as a path segment, so a space must be percent-escaped
	// rather than turned into "+".
	if want := "/kubernetes%20operator"; gotPath != want {
		t.Fatalf("path = %q, want %q", gotPath, want)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("results = %d, want 2 (the entry without a url is dropped)", len(resp.Results))
	}
	if resp.Results[0].Title != "WebLogic Kubernetes Operator" {
		t.Fatalf("title = %q", resp.Results[0].Title)
	}
	if resp.Results[0].Source != "marginalia" {
		t.Fatalf("source = %q, want marginalia", resp.Results[0].Source)
	}
	if resp.Results[1].Description != "padded description" {
		t.Fatalf("description = %q, want it trimmed", resp.Results[1].Description)
	}
}

func TestMarginaliaSearchRespectsCount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(marginaliaFixture))
	}))
	defer server.Close()

	resp, err := NewMarginalia(server.URL).Search(context.Background(), search.Request{Query: "x", Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(resp.Results))
	}
}

func TestMarginaliaClassifiesFailures(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{
		{http.StatusTooManyRequests, provider.ErrRateLimited},
		{http.StatusForbidden, provider.ErrBlocked},
		{http.StatusServiceUnavailable, provider.ErrRateLimited},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer server.Close()

			_, err := NewMarginalia(server.URL).Search(context.Background(), search.Request{Query: "x"})
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestMarginaliaRejectsEmptyQuery(t *testing.T) {
	_, err := NewMarginalia("http://unused.test").Search(context.Background(), search.Request{Query: "   "})
	if err == nil || !strings.Contains(err.Error(), "query is required") {
		t.Fatalf("error = %v, want a query-required error", err)
	}
}
