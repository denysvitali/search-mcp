package reader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExtractLinks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body>
<a href="/docs">Documentation</a>
<a href="/docs">Documentation duplicate</a>
<a href="https://other.test/page#section">External</a>
<a href="#top">Skip fragment</a>
<a href="mailto:x@example.test">Skip mailto</a>
<a href="javascript:void(0)">Skip js</a>
</body></html>`))
	}))
	defer server.Close()

	got, err := ExtractLinks(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("ExtractLinks: %v", err)
	}
	for _, want := range []string{"[Documentation](" + server.URL + "/docs)", "[External](https://other.test/page)"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"mailto:", "javascript:", "#section", "duplicate"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("unwanted %q in:\n%s", unwanted, got)
		}
	}
}

func TestExtractLinksRejectsNonHTML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	if _, err := ExtractLinks(context.Background(), server.URL); err == nil {
		t.Error("expected error for non-HTML content")
	}
}
