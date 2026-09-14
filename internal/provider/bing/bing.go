// Package bing scrapes Bing's public HTML results page. It deliberately uses
// the browser-facing page rather than the retired Bing Search API, keeping the
// provider free and keyless while treating markup changes and bot challenges
// as provider failures.
package bing

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/denysvitali/search-mcp/internal/htmlutil"
	"github.com/denysvitali/search-mcp/internal/provider"
	"github.com/denysvitali/search-mcp/internal/provider/common"
	"github.com/denysvitali/search-mcp/internal/search"
	"golang.org/x/net/html"
)

func init() {
	provider.Register("bing", func(_, endpoint string) (search.Provider, error) {
		return NewBing(endpoint), nil
	})
}

const (
	bingEndpoint  = "https://www.bing.com/search"
	bingPageSize  = 10
	bingMaxPages  = 3
	bingUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.0.0 Safari/537.36"
)

// Bing is a keyless HTML scraper. Bing occasionally changes its SERP markup or
// serves a consent/challenge page, so the parser fails loudly when b_results is
// absent instead of returning a misleading empty search.
type Bing struct {
	endpoint string
	client   *http.Client
}

var _ provider.Provider = (*Bing)(nil)

func NewBing(endpoint ...string) *Bing {
	target := bingEndpoint
	if len(endpoint) > 0 && endpoint[0] != "" {
		target = endpoint[0]
	}
	return &Bing{endpoint: target, client: common.NewHTTPClient()}
}

func (b *Bing) Name() string { return "bing" }

// Search pages through a maximum of three browser-facing SERPs. The free HTML
// endpoint is best effort: it usually yields ten rows per page and may return
// fewer results when Bing changes ranking or challenges the caller.
func (b *Bing) Search(ctx context.Context, req search.Request) (search.Response, error) {
	count := req.Count
	if count <= 0 {
		count = 10
	}

	results := make([]search.Result, 0, min(count, bingPageSize*bingMaxPages))
	seen := make(map[string]struct{}, count)
	for page := 0; page < bingMaxPages && len(results) < count; page++ {
		pageResults, err := b.searchPage(ctx, req, page)
		if err != nil {
			if page > 0 && len(results) > 0 {
				break
			}
			return search.Response{}, err
		}
		if len(pageResults) == 0 {
			break
		}
		for _, result := range pageResults {
			key := search.NormalizeResultURL(result.URL)
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			results = append(results, result)
			if len(results) >= count {
				break
			}
		}
		if len(pageResults) < bingPageSize {
			break
		}
	}

	return search.Response{Query: req.Query, Provider: b.Name(), Results: results}, nil
}

func (b *Bing) searchPage(ctx context.Context, req search.Request, page int) ([]search.Result, error) {
	values := url.Values{"q": {req.Query}, "count": {strconv.Itoa(bingPageSize)}}
	if page > 0 {
		values.Set("first", strconv.Itoa(page*bingPageSize+1))
	}
	if country := strings.TrimSpace(req.Country); country != "" {
		values.Set("cc", strings.ToLower(country))
	}
	if language := strings.TrimSpace(req.Language); language != "" {
		values.Set("setlang", strings.ToLower(language))
	}
	if safe := bingSafeSearch(req.SafeSearch); safe != "" {
		values.Set("adlt", safe)
	}
	if freshness := bingFreshness(req.Freshness); freshness != "" {
		values.Set("filters", freshness)
	}

	target, err := common.URLWithQuery(b.endpoint, values)
	if err != nil {
		return nil, fmt.Errorf("build bing request URL: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("User-Agent", bingUserAgent)
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	httpReq.Header.Set("Accept-Language", "en-US,en;q=0.9")
	common.ApplyExtraHeaders(httpReq, req)

	resp, err := b.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("bing request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(common.LimitedBody(resp.Body))
	if err != nil {
		return nil, fmt.Errorf("read bing response: %w", err)
	}
	if common.IsChallengePage(body) {
		return nil, common.ErrChallenge("bing")
	}
	switch resp.StatusCode {
	case http.StatusForbidden:
		return nil, fmt.Errorf("bing returned http 403; request blocked by upstream: %w", provider.ErrBlocked)
	case http.StatusTooManyRequests:
		return nil, fmt.Errorf("bing returned http 429: %w", search.NewRateLimitedError(resp.Header))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("bing search failed: status %d", resp.StatusCode)
	}

	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse bing html: %w", err)
	}
	results, found := extractBingResults(doc, bingPageSize)
	if !found {
		return nil, common.ErrMissingResultsContainer("bing", "#b_results")
	}
	return results, nil
}

func bingSafeSearch(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "off", "0", "none":
		return "off"
	case "moderate", "1", "medium":
		return "moderate"
	case "strict", "2", "on", "high":
		return "strict"
	default:
		return ""
	}
}

// Bing's browser filter syntax uses ez* values for relative freshness windows.
func bingFreshness(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "day", "d", "pd":
		return `ex1:"ez1"`
	case "week", "w", "pw":
		return `ex1:"ez2"`
	case "month", "m", "pm":
		return `ex1:"ez3"`
	case "year", "y", "py":
		return `ex1:"ez5"`
	default:
		return ""
	}
}

func extractBingResults(root *html.Node, limit int) ([]search.Result, bool) {
	container := htmlutil.FindElement(root, func(n *html.Node) bool {
		return htmlutil.Attr(n, "id") == "b_results"
	})
	if container == nil {
		return nil, false
	}

	results := make([]search.Result, 0, limit)
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if len(results) >= limit {
			return
		}
		if n.Type == html.ElementNode && n.Data == "li" && htmlutil.HasClass(n, "b_algo") {
			if result, ok := parseBingResult(n); ok {
				results = append(results, result)
			}
			return
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(container)
	return results, true
}

func parseBingResult(node *html.Node) (search.Result, bool) {
	titleNode := htmlutil.FindElement(node, func(n *html.Node) bool {
		return n.Data == "h2"
	})
	if titleNode == nil {
		return search.Result{}, false
	}
	anchor := htmlutil.FindElement(titleNode, func(n *html.Node) bool {
		return n.Data == "a"
	})
	if anchor == nil {
		return search.Result{}, false
	}
	href := unwrapBingURL(htmlutil.Attr(anchor, "href"))
	if href == "" {
		return search.Result{}, false
	}
	parsed, err := url.Parse(href)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return search.Result{}, false
	}
	title := htmlutil.CollapseWhitespace(htmlutil.TextContent(titleNode))
	if title == "" {
		return search.Result{}, false
	}

	description := ""
	if caption := htmlutil.FindElement(node, func(n *html.Node) bool {
		return n.Data == "div" && htmlutil.HasClass(n, "b_caption")
	}); caption != nil {
		description = htmlutil.CollapseWhitespace(htmlutil.TextContent(caption))
	}
	published, description := search.SplitPublished(description)
	return search.Result{Title: title, URL: href, Description: description, Published: published, Source: "bing"}, true
}

// unwrapBingURL decodes the browser click redirect when Bing uses /ck/a. The
// direct URL form is retained for simpler regional SERPs and test fixtures.
func unwrapBingURL(href string) string {
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil || !strings.HasSuffix(strings.ToLower(u.Hostname()), "bing.com") || !strings.HasPrefix(u.Path, "/ck/a") {
		return href
	}
	encoded := u.Query().Get("u")
	if !strings.HasPrefix(encoded, "a1") {
		return href
	}
	encoded = encoded[2:]
	for _, encoding := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding} {
		decoded, err := encoding.DecodeString(encoded)
		if err == nil && strings.HasPrefix(string(decoded), "http") {
			return string(decoded)
		}
	}
	return href
}
