package yahoo

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

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

func (y *Yahoo) Search(ctx context.Context, req search.Request) (search.Response, error) {
	values := url.Values{"p": {req.Query}}
	if req.Language != "" {
		values.Set("vl", strings.ToLower(strings.TrimSpace(req.Language)))
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, y.endpoint+"?"+values.Encode(), nil)
	if err != nil {
		return search.Response{}, err
	}
	httpReq.Header.Set("User-Agent", yahooUserAgent)
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	httpReq.Header.Set("Accept-Language", "en-US,en;q=0.9")
	common.ApplyExtraHeaders(httpReq, req)

	resp, err := y.client.Do(httpReq)
	if err != nil {
		return search.Response{}, fmt.Errorf("yahoo request: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusForbidden:
		return search.Response{}, fmt.Errorf("yahoo returned http 403; request blocked by upstream: %w", provider.ErrBlocked)
	case http.StatusTooManyRequests:
		return search.Response{}, fmt.Errorf("yahoo returned http 429: %w", search.NewRateLimitedError(resp.Header))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return search.Response{}, fmt.Errorf("yahoo search failed: status %d", resp.StatusCode)
	}

	doc, err := html.Parse(common.LimitedBody(resp.Body))
	if err != nil {
		return search.Response{}, fmt.Errorf("parse yahoo html: %w", err)
	}
	count := req.Count
	if count <= 0 {
		count = 10
	}
	return search.Response{Query: req.Query, Provider: y.Name(), Results: extractYahooResults(doc, count)}, nil
}

func extractYahooResults(root *html.Node, limit int) []search.Result {
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
	walk(root)
	return results
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
	return search.Result{Title: title, URL: href, Description: snippet, Source: "yahoo"}, true
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
