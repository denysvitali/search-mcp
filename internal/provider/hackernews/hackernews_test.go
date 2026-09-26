package hackernews

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/denysvitali/search-mcp/internal/search"
)

func TestSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("query") != "Go context" || q.Get("tags") != "story" || q.Get("hitsPerPage") != "2" || !strings.HasPrefix(q.Get("numericFilters"), "created_at_i>=") || r.Header.Get("Authorization") != "" {
			t.Errorf("bad keyless request: %s", r.URL)
		}
		fmt.Fprint(w, `{"hits":[{"title":"Go &amp; contexts","url":"https://example.com/go","points":42,"num_comments":7,"created_at":"2026-01-02T03:04:05Z"},{"title":"Ask HN","objectID":"123","story_text":"<p>How to <b>verify you are human</b>?</p>"}]}`)
	}))
	defer server.Close()
	resp, err := NewHN(server.URL).Search(context.Background(), search.Request{Query: "Go context", Count: 2, Freshness: "pw"})
	if err != nil || len(resp.Results) != 2 {
		t.Fatalf("%+v %v", resp, err)
	}
	if resp.Results[0].Title != "Go & contexts" || resp.Results[0].Published != "2026-01-02" || !strings.Contains(resp.Results[0].Description, "42 points") {
		t.Fatalf("metadata: %+v", resp.Results[0])
	}
	if resp.Results[1].URL != "https://news.ycombinator.com/item?id=123" || resp.Results[1].Description != "How to verify you are human?" {
		t.Fatalf("self post: %+v", resp.Results[1])
	}
}

func TestDecodeErrorsAndEmpty(t *testing.T) {
	for _, body := range []string{`{}`, `{"hits":null}`, `<html>error</html>`} {
		if _, err := decodeStories([]byte(body)); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	rows, err := decodeStories([]byte(`{"hits":[]}`))
	if err != nil || len(rows) != 0 {
		t.Fatalf("%+v %v", rows, err)
	}
}
