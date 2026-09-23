package wikipedia

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/denysvitali/search-mcp/internal/search"
)

func TestSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("action") != "query" || q.Get("list") != "search" || q.Get("srsearch") != "Go language" || q.Get("srlimit") != "3" {
			t.Errorf("unexpected query: %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"query":{"search":[{"title":"Go (programming language)","pageid":123,"snippet":"A <span class=\"searchmatch\">compiled</span> language &amp; tool."}]}}`)
	}))
	defer server.Close()
	resp, err := NewWikipedia(server.URL).Search(context.Background(), search.Request{Query: "Go language", Count: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("results = %+v", resp.Results)
	}
	got := resp.Results[0]
	if got.URL != server.URL+"/wiki/Go_%28programming_language%29" || got.Description != "A compiled language & tool." || got.Source != "wikipedia" {
		t.Fatalf("result = %+v", got)
	}
}

func TestInvalidLanguage(t *testing.T) {
	_, err := NewWikipedia("").Search(context.Background(), search.Request{Query: "Go", Language: "en/../evil"})
	if err == nil {
		t.Fatal("expected invalid language error")
	}
}

func TestAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"error":{"info":"bad search"}}`)
	}))
	defer server.Close()
	_, err := NewWikipedia(server.URL).Search(context.Background(), search.Request{Query: "Go"})
	if err == nil {
		t.Fatal("expected API error")
	}
}
