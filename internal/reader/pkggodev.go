package reader

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// pkgGoDevBaseURL is the pkg.go.dev origin. It is a var so tests can point it
// at an httptest server.
var pkgGoDevBaseURL = "https://pkg.go.dev"

// isPkgGoDevURL matches package documentation pages on pkg.go.dev.
func isPkgGoDevURL(parsedURL *url.URL) bool {
	host := strings.ToLower(parsedURL.Hostname())
	if host != "pkg.go.dev" {
		return false
	}
	return len(pathSegments(parsedURL.Path)) > 0
}

// fetchPkgGoDevContentAsMarkdown fetches the package page and extracts just
// the documentation section, skipping the site chrome and index boilerplate.
func fetchPkgGoDevContentAsMarkdown(ctx context.Context, client *http.Client, parsedURL *url.URL) (string, error) {
	pageURL := strings.TrimRight(pkgGoDevBaseURL, "/") + parsedURL.RequestURI()
	req, err := newRequest(ctx, pageURL, defaultAccept)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("pkg.go.dev request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("pkg.go.dev request failed: HTTP %d: %s", resp.StatusCode, readErrorBody(resp.Body))
	}

	doc, err := goquery.NewDocumentFromReader(limitedBody(resp.Body))
	if err != nil {
		return "", fmt.Errorf("failed to parse pkg.go.dev page: %w", err)
	}

	section := doc.Find("div.Documentation").First()
	if section.Length() == 0 {
		section = doc.Find(".Documentation-content").First()
	}
	if section.Length() == 0 {
		// Not a documentation page (e.g. search results); fall back to the
		// generic conversion of the whole page.
		html, err := doc.Html()
		if err != nil {
			return "", fmt.Errorf("failed to serialize pkg.go.dev page: %w", err)
		}
		markdown, err := convertHTMLToMarkdown(html)
		if err != nil {
			return "", err
		}
		return cleanMarkdown(markdown), nil
	}

	html, err := goquery.OuterHtml(section)
	if err != nil {
		return "", fmt.Errorf("failed to serialize pkg.go.dev documentation: %w", err)
	}
	markdown, err := convertHTMLToMarkdown(html)
	if err != nil {
		return "", err
	}
	header := fmt.Sprintf("# %s\n\n- Link: %s\n\n", strings.Join(pathSegments(parsedURL.Path), "/"), pageURL)
	return cleanMarkdown(header + markdown), nil
}
