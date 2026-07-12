package reader

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// articlePage builds an HTML page with heavy navigation boilerplate around a
// long article body, long enough for readability to trust the extraction.
func articlePage() string {
	var paragraphs strings.Builder
	for i := range 12 {
		fmt.Fprintf(&paragraphs, "<p>Paragraph %d of the article body, padded with enough prose that the readability extractor considers this the main content of the page and keeps it around.</p>\n", i)
	}
	return `<!DOCTYPE html><html><head><title>Article Title</title></head><body>
<nav><ul><li><a href="/home">Home</a></li><li><a href="/about">About</a></li><li><a href="/pricing">Pricing</a></li></ul></nav>
<div class="cookie-banner">We use cookies. <a href="/cookies">Learn more</a></div>
<article><h1>Article Title</h1>` + paragraphs.String() + `</article>
<footer><a href="/imprint">Imprint</a> | <a href="/privacy">Privacy</a> | © 2026 Example Corp</footer>
</body></html>`
}

func TestReadStripsBoilerplateViaReadability(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(articlePage()))
	}))
	defer server.Close()

	got, err := Read(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !strings.Contains(got, "Paragraph 3 of the article body") {
		t.Errorf("article body missing:\n%s", got)
	}
	if !strings.Contains(got, "# Article Title") {
		t.Errorf("title heading missing:\n%s", got)
	}
	for _, boilerplate := range []string{"Pricing", "cookies", "Imprint"} {
		if strings.Contains(got, boilerplate) {
			t.Errorf("boilerplate %q leaked into output:\n%s", boilerplate, got)
		}
	}
}

func TestReadFallsBackForShortPages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><body><p>tiny page</p></body></html>`))
	}))
	defer server.Close()

	got, err := Read(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !strings.Contains(got, "tiny page") {
		t.Errorf("fallback content missing: %q", got)
	}
}
