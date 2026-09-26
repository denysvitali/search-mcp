package crossref

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
		if q.Get("query.bibliographic") != "transformer" || q.Get("rows") != "50" || !strings.Contains(q.Get("filter"), "from-pub-date:") || !strings.HasPrefix(q.Get("filter"), "type:journal-article,type:proceedings-article,type:posted-content") || r.Header.Get("Authorization") != "" {
			t.Errorf("bad request %s", r.URL)
		}
		fmt.Fprint(w, `{"status":"ok","message":{"items":[{"DOI":"10.1234/a","title":["Attention &amp; learning"],"author":[{"given":"Ada","family":"Lovelace"}],"abstract":"<jats:p>An <jats:italic>abstract</jats:italic>.</jats:p>","container-title":["Journal"],"publisher":"Publisher","published":{"date-parts":[[2025,9]]}},{"DOI":"10.1234/b","title":["Second"],"published":{"date-parts":[[2024]]}}]}}`)
	}))
	defer server.Close()
	resp, err := New(server.URL).Search(context.Background(), search.Request{Query: "transformer", Count: 100, Freshness: "py"})
	if err != nil || len(resp.Results) != 2 {
		t.Fatalf("%+v %v", resp, err)
	}
	hit := resp.Results[0]
	if hit.Title != "Attention & learning" || hit.URL != "https://doi.org/10.1234/a" || hit.Published != "2025-09" || !strings.Contains(hit.Description, "Ada Lovelace") || strings.Contains(hit.Description, "<jats") {
		t.Fatalf("metadata: %+v", hit)
	}
	if resp.Results[1].Published != "2024" {
		t.Fatal("invented date precision")
	}
}

func TestEnvelopes(t *testing.T) {
	for _, body := range []string{`{}`, `{"status":"error"}`, `{"status":"ok","message":{"items":null}}`} {
		if _, err := decode([]byte(body)); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	rows, err := decode([]byte(`{"status":"ok","message":{"items":[]}}`))
	if err != nil || len(rows) != 0 {
		t.Fatalf("%+v %v", rows, err)
	}
}
