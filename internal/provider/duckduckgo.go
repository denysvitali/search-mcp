package provider

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/denysvitali/search-mcp/internal/htmlutil"
	"github.com/denysvitali/search-mcp/internal/search"
	"golang.org/x/net/html"
)

const (
	duckDuckGoEndpoint  = "https://html.duckduckgo.com/html/"
	duckDuckGoUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.0.0 Safari/537.36"
)

type DuckDuckGo struct {
	endpoint string
	client   *http.Client
}

func NewDuckDuckGo(endpoint ...string) *DuckDuckGo {
	target := duckDuckGoEndpoint
	if len(endpoint) > 0 && endpoint[0] != "" {
		target = endpoint[0]
	}
	return &DuckDuckGo{endpoint: target, client: newHTTPClient(defaultHTTPTimeout)}
}

func (d *DuckDuckGo) Name() string {
	return "duckduckgo"
}

func (d *DuckDuckGo) Search(ctx context.Context, req search.Request) (search.Response, error) {
	form := url.Values{}
	form.Set("q", req.Query)
	form.Set("b", "")
	if region := duckRegion(req.Country, req.Language); region != "" {
		form.Set("kl", region)
	}
	if df := duckFreshness(req.Freshness); df != "" {
		form.Set("df", df)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, d.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return search.Response{}, err
	}
	for k, v := range duckDuckGoHeaders() {
		httpReq.Header.Set(k, v)
	}
	applyExtraHeaders(httpReq, req)

	resp, err := d.client.Do(httpReq)
	if err != nil {
		return search.Response{}, fmt.Errorf("duckduckgo request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return search.Response{}, fmt.Errorf("read duckduckgo response: %w", err)
	}

	// Status 202 with an "anomaly" body is DDG's soft block; the more common
	// failure mode is a 200 that still contains anomaly.js. Treat both as the
	// same condition so callers fall back to another provider.
	if isDuckAnomaly(body) {
		return search.Response{}, fmt.Errorf("duckduckgo served anomaly page; source ip rate-limited or fingerprinted: %w", ErrBlocked)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return search.Response{}, fmt.Errorf("duckduckgo search failed: status %d", resp.StatusCode)
	}

	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return search.Response{}, fmt.Errorf("parse duckduckgo html: %w", err)
	}

	count := req.Count
	if count <= 0 {
		count = 10
	}
	results := extractDuckResults(doc, count)

	return search.Response{Query: req.Query, Provider: d.Name(), Results: results}, nil
}

func duckDuckGoHeaders() map[string]string {
	return map[string]string{
		"User-Agent":                duckDuckGoUserAgent,
		"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7",
		"Accept-Language":           "en-US,en;q=0.9",
		"Content-Type":              "application/x-www-form-urlencoded",
		"Origin":                    "https://html.duckduckgo.com",
		"Referer":                   "https://html.duckduckgo.com/",
		"sec-ch-ua":                 `"Chromium";v="147", "Not.A/Brand";v="8"`,
		"sec-ch-ua-mobile":          "?0",
		"sec-ch-ua-platform":        `"Linux"`,
		"sec-fetch-dest":            "document",
		"sec-fetch-mode":            "navigate",
		"sec-fetch-site":            "same-origin",
		"sec-fetch-user":            "?1",
		"upgrade-insecure-requests": "1",
	}
}

func isDuckAnomaly(body []byte) bool {
	return bytes.Contains(body, []byte("anomaly.js")) || bytes.Contains(body, []byte("/anomaly/"))
}

func duckRegion(country, language string) string {
	country = strings.ToLower(strings.TrimSpace(country))
	language = strings.ToLower(strings.TrimSpace(language))
	switch {
	case country != "" && language != "":
		return language + "-" + country
	case country != "":
		return "us-" + country
	case language != "":
		return language + "-us"
	}
	return ""
}

func duckFreshness(freshness string) string {
	switch strings.ToLower(strings.TrimSpace(freshness)) {
	case "day", "d", "pd":
		return "d"
	case "week", "w", "pw":
		return "w"
	case "month", "m", "pm":
		return "m"
	case "year", "y", "py":
		return "y"
	}
	return ""
}

func extractDuckResults(root *html.Node, limit int) []search.Result {
	results := make([]search.Result, 0, limit)
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if len(results) >= limit {
			return
		}
		if n.Type == html.ElementNode && n.Data == "div" && htmlutil.HasClass(n, "result__body") {
			if r, ok := parseDuckResult(n); ok {
				results = append(results, r)
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return results
}

func parseDuckResult(node *html.Node) (search.Result, bool) {
	titleAnchor := htmlutil.FindElement(node, func(n *html.Node) bool {
		return n.Data == "a" && htmlutil.HasClass(n, "result__a")
	})
	if titleAnchor == nil {
		return search.Result{}, false
	}
	href := unwrapDuckURL(htmlutil.Attr(titleAnchor, "href"))
	if href == "" || strings.HasPrefix(href, "https://duckduckgo.com/y.js") {
		return search.Result{}, false
	}
	title := strings.TrimSpace(htmlutil.TextContent(titleAnchor))
	if title == "" {
		return search.Result{}, false
	}

	snippetNode := htmlutil.FindElement(node, func(n *html.Node) bool {
		return n.Data == "a" && htmlutil.HasClass(n, "result__snippet")
	})
	var snippet string
	if snippetNode != nil {
		snippet = strings.TrimSpace(htmlutil.TextContent(snippetNode))
	} else if div := htmlutil.FindElement(node, func(n *html.Node) bool {
		return n.Data == "div" && htmlutil.HasClass(n, "result__snippet")
	}); div != nil {
		snippet = strings.TrimSpace(htmlutil.TextContent(div))
	}

	return search.Result{
		Title:       title,
		URL:         href,
		Description: snippet,
		Source:      "duckduckgo",
	}, true
}

func unwrapDuckURL(href string) string {
	if href == "" {
		return ""
	}
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return href
	}
	if strings.HasSuffix(u.Host, "duckduckgo.com") && strings.HasPrefix(u.Path, "/l/") {
		if uddg := u.Query().Get("uddg"); uddg != "" {
			if decoded, err := url.QueryUnescape(uddg); err == nil {
				return decoded
			}
			return uddg
		}
	}
	return href
}
