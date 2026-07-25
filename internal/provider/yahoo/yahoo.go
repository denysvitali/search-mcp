package yahoo

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/denysvitali/search-mcp/internal/htmlutil"
	"github.com/denysvitali/search-mcp/internal/provider"
	"github.com/denysvitali/search-mcp/internal/provider/common"
	"github.com/denysvitali/search-mcp/internal/search"
	"golang.org/x/net/html"
)

func init() {
	provider.Register("yahoo", func(_, endpoint string) (search.Provider, error) {
		return NewYahoo(endpoint), nil
	})
}

const (
	yahooEndpoint  = "https://search.yahoo.com/search"
	yahooUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.0.0 Safari/537.36"

	// yahooResultsPerPage is how many organic rows a single Yahoo SERP holds.
	// It drives the 1-based `b` offset used to page through results.
	yahooResultsPerPage = 10
	// yahooMaxPages caps how deep a single search will page. Yahoo returns
	// progressively less relevant results and each extra page is another request
	// against an endpoint that rate-limits aggressively.
	yahooMaxPages = 3
	// yahooPageDelay spaces out consecutive page fetches. The service's rate
	// limiter is applied once per Search call, so without this the extra pages
	// would burst through unthrottled.
	yahooPageDelay = 250 * time.Millisecond
)

// Yahoo searches Yahoo's public HTML results. It provides a useful third
// no-key fallback without pretending that Bing's low-quality RSS endpoint is
// an API. Yahoo may change this markup, so parsing is deliberately scoped to
// its organic result containers.
type Yahoo struct {
	endpoint string
	client   *http.Client
}

var _ provider.Provider = (*Yahoo)(nil)

func NewYahoo(endpoint ...string) *Yahoo {
	target := yahooEndpoint
	if len(endpoint) > 0 && endpoint[0] != "" {
		target = endpoint[0]
	}
	return &Yahoo{endpoint: target, client: common.NewHTTPClient()}
}

func (y *Yahoo) Name() string { return "yahoo" }

// Search pages through Yahoo's SERP until it has req.Count results. A single
// page only carries around eight organic rows, so without paging any request for
// more than that silently came back short.
func (y *Yahoo) Search(ctx context.Context, req search.Request) (search.Response, error) {
	count := req.Count
	if count <= 0 {
		count = 10
	}

	var results []search.Result
	seen := make(map[string]struct{}, count)
	for page := 0; page < yahooMaxPages && len(results) < count; page++ {
		if page > 0 {
			select {
			case <-ctx.Done():
				return search.Response{}, ctx.Err()
			case <-time.After(yahooPageDelay):
			}
		}

		pageResults, err := y.searchPage(ctx, req, page, count)
		if err != nil {
			// Later pages are a bonus: keep whatever the earlier ones produced
			// rather than failing a search that already has usable results.
			if page > 0 && len(results) > 0 {
				break
			}
			return search.Response{}, err
		}
		if len(pageResults) == 0 {
			break
		}
		for _, r := range pageResults {
			if _, dup := seen[r.URL]; dup {
				continue
			}
			seen[r.URL] = struct{}{}
			results = append(results, r)
			if len(results) >= count {
				break
			}
		}
	}

	return search.Response{Query: req.Query, Provider: y.Name(), Results: results}, nil
}

// searchPage fetches a single SERP. page is 0-based; Yahoo's `b` offset is
// 1-based, so page 1 starts at b=11.
func (y *Yahoo) searchPage(ctx context.Context, req search.Request, page, count int) ([]search.Result, error) {
	values := url.Values{"p": {req.Query}}
	if req.Language != "" {
		values.Set("vl", strings.ToLower(strings.TrimSpace(req.Language)))
	}
	if page > 0 {
		values.Set("b", strconv.Itoa(page*yahooResultsPerPage+1))
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, y.endpoint+"?"+values.Encode(), nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("User-Agent", yahooUserAgent)
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	httpReq.Header.Set("Accept-Language", "en-US,en;q=0.9")
	common.ApplyExtraHeaders(httpReq, req)

	resp, err := y.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("yahoo request: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusForbidden:
		return nil, fmt.Errorf("yahoo returned http 403; request blocked by upstream: %w", provider.ErrBlocked)
	case http.StatusTooManyRequests:
		return nil, fmt.Errorf("yahoo returned http 429: %w", search.NewRateLimitedError(resp.Header))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("yahoo search failed: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(common.LimitedBody(resp.Body))
	if err != nil {
		return nil, fmt.Errorf("read yahoo response: %w", err)
	}
	if common.IsChallengePage(body) {
		return nil, common.ErrChallenge("yahoo")
	}

	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse yahoo html: %w", err)
	}
	results, found := extractYahooResults(doc, count)
	if !found {
		return nil, common.ErrMissingResultsContainer("yahoo", "#web")
	}
	return results, nil
}

// extractYahooResults parses the organic result rows and reports whether the
// web-results container was present, so a challenge page or markup drift is
// distinguishable from a query that genuinely matched nothing.
func extractYahooResults(root *html.Node, limit int) ([]search.Result, bool) {
	container := htmlutil.FindElement(root, func(n *html.Node) bool {
		return htmlutil.Attr(n, "id") == "web"
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
		if n.Type == html.ElementNode && n.Data == "div" && htmlutil.HasClass(n, "algo-sr") {
			if result, ok := parseYahooResult(n); ok {
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

func parseYahooResult(node *html.Node) (search.Result, bool) {
	titleNode := htmlutil.FindElement(node, func(n *html.Node) bool {
		return n.Data == "h3" && htmlutil.HasClass(n, "title")
	})
	if titleNode == nil {
		return search.Result{}, false
	}
	anchor := titleNode.Parent
	if anchor == nil || anchor.Data != "a" {
		return search.Result{}, false
	}
	title := htmlutil.CollapseWhitespace(htmlutil.TextContent(titleNode))
	href := unwrapYahooURL(htmlutil.Attr(anchor, "href"))
	if title == "" || href == "" {
		return search.Result{}, false
	}

	snippet := ""
	if text := htmlutil.FindElement(node, func(n *html.Node) bool {
		return n.Data == "div" && htmlutil.HasClass(n, "compText")
	}); text != nil {
		snippet = htmlutil.CollapseWhitespace(htmlutil.TextContent(text))
	}
	// Yahoo prefixes the snippet with the page's date ("Jul 16, 2025 · …")
	// instead of exposing it as a field; lift it into Published so callers can
	// judge how current a result is.
	published, snippet := search.SplitPublished(snippet)
	return search.Result{Title: title, URL: href, Description: snippet, Source: "yahoo", Published: published}, true
}

func unwrapYahooURL(href string) string {
	u, err := url.Parse(href)
	if err != nil || !strings.HasSuffix(strings.ToLower(u.Host), "search.yahoo.com") {
		return href
	}
	const marker = "/RU="
	start := strings.Index(u.Path, marker)
	if start < 0 {
		return href
	}
	encoded := u.Path[start+len(marker):]
	if end := strings.Index(encoded, "/RK="); end >= 0 {
		encoded = encoded[:end]
	}
	if decoded, err := url.QueryUnescape(encoded); err == nil && decoded != "" {
		return decoded
	}
	return href
}
