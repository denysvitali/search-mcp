// Package google scrapes Google's browser-facing HTML results page. Google
// does not provide a free general-purpose server-side search API for new
// consumers, so this provider is best effort and intentionally opt-in by
// default: Google challenges datacenter clients more aggressively than the
// other free providers.
package google

import (
	"bytes"
	"context"
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
	provider.Register("google", func(_, endpoint string) (search.Provider, error) {
		return NewGoogle(endpoint), nil
	})
}

const (
	googleEndpoint  = "https://www.google.com/search"
	googlePageSize  = 10
	googleMaxPages  = 3
	googleUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.0.0 Safari/537.36"
)

// Google is a keyless HTML scraper. It is not enabled in the default provider
// set because a challenge is common from hosted/datacenter IP ranges.
type Google struct {
	endpoint string
	client   *http.Client
}

var _ provider.Provider = (*Google)(nil)

func NewGoogle(endpoint ...string) *Google {
	target := googleEndpoint
	if len(endpoint) > 0 && endpoint[0] != "" {
		target = endpoint[0]
	}
	return &Google{endpoint: target, client: common.NewHTTPClient()}
}

func (g *Google) Name() string { return "google" }

// Search pages through at most three public HTML result pages. A successful
// Google page may still contain fewer rows due to filters or regional layout.
func (g *Google) Search(ctx context.Context, req search.Request) (search.Response, error) {
	count := req.Count
	if count <= 0 {
		count = 10
	}
	results := make([]search.Result, 0, min(count, googlePageSize*googleMaxPages))
	seen := make(map[string]struct{}, count)
	for page := 0; page < googleMaxPages && len(results) < count; page++ {
		pageResults, err := g.searchPage(ctx, req, page)
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
		if len(pageResults) < googlePageSize {
			break
		}
	}
	return search.Response{Query: req.Query, Provider: g.Name(), Results: results}, nil
}

func (g *Google) searchPage(ctx context.Context, req search.Request, page int) ([]search.Result, error) {
	values := url.Values{
		"q":      {req.Query},
		"num":    {strconv.Itoa(googlePageSize)},
		"gbv":    {"1"},
		"filter": {"0"},
	}
	if page > 0 {
		values.Set("start", strconv.Itoa(page*googlePageSize))
	}
	if language := strings.TrimSpace(req.Language); language != "" {
		values.Set("hl", strings.ToLower(language))
	}
	if country := strings.TrimSpace(req.Country); country != "" {
		values.Set("gl", strings.ToLower(country))
	}
	if safe := googleSafeSearch(req.SafeSearch); safe != "" {
		values.Set("safe", safe)
	}
	if freshness := googleFreshness(req.Freshness); freshness != "" {
		values.Set("tbs", freshness)
	}

	target, err := common.URLWithQuery(g.endpoint, values)
	if err != nil {
		return nil, fmt.Errorf("build google request URL: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("User-Agent", googleUserAgent)
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	httpReq.Header.Set("Accept-Language", "en-US,en;q=0.9")
	common.ApplyExtraHeaders(httpReq, req)

	resp, err := g.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("google request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(common.LimitedBody(resp.Body))
	if err != nil {
		return nil, fmt.Errorf("read google response: %w", err)
	}
	if common.IsChallengePage(body) {
		return nil, common.ErrChallenge("google")
	}
	switch resp.StatusCode {
	case http.StatusForbidden:
		return nil, fmt.Errorf("google returned http 403; request blocked by upstream: %w", provider.ErrBlocked)
	case http.StatusTooManyRequests:
		return nil, fmt.Errorf("google returned http 429: %w", search.NewRateLimitedError(resp.Header))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("google search failed: status %d", resp.StatusCode)
	}

	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse google html: %w", err)
	}
	results, found := extractGoogleResults(doc, googlePageSize)
	if !found {
		return nil, common.ErrMissingResultsContainer("google", "#search")
	}
	return results, nil
}

func googleSafeSearch(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "off", "0", "none":
		return "off"
	case "moderate", "1", "medium", "strict", "2", "on", "high":
		return "active"
	default:
		return ""
	}
}

func googleFreshness(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "day", "d", "pd":
		return "qdr:d"
	case "week", "w", "pw":
		return "qdr:w"
	case "month", "m", "pm":
		return "qdr:m"
	case "year", "y", "py":
		return "qdr:y"
	default:
		return ""
	}
}

func extractGoogleResults(root *html.Node, limit int) ([]search.Result, bool) {
	container := htmlutil.FindElement(root, func(n *html.Node) bool {
		return htmlutil.Attr(n, "id") == "search"
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
		if n.Type == html.ElementNode && n.Data == "h3" {
			if result, ok := parseGoogleResult(n); ok {
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

func parseGoogleResult(titleNode *html.Node) (search.Result, bool) {
	anchor := htmlutil.FindElement(titleNode, func(n *html.Node) bool { return n.Data == "a" })
	if anchor == nil && titleNode.Parent != nil && titleNode.Parent.Data == "a" {
		anchor = titleNode.Parent
	}
	if anchor == nil {
		return search.Result{}, false
	}
	href := unwrapGoogleURL(htmlutil.Attr(anchor, "href"))
	parsed, err := url.Parse(href)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return search.Result{}, false
	}
	title := htmlutil.CollapseWhitespace(htmlutil.TextContent(titleNode))
	if title == "" {
		return search.Result{}, false
	}

	description := ""
	for node := titleNode.Parent; node != nil; node = node.Parent {
		if node.Type == html.ElementNode && (htmlutil.HasClass(node, "MjjYud") || htmlutil.HasClass(node, "g")) {
			if snippet := htmlutil.FindElement(node, func(n *html.Node) bool {
				return htmlutil.HasClass(n, "VwiC3b") || htmlutil.HasClass(n, "IsZvec") || htmlutil.HasClass(n, "aCOpRe")
			}); snippet != nil {
				description = htmlutil.CollapseWhitespace(htmlutil.TextContent(snippet))
			}
			break
		}
	}
	published, description := search.SplitPublished(description)
	return search.Result{Title: title, URL: href, Description: description, Published: published, Source: "google"}, true
}

func unwrapGoogleURL(href string) string {
	if strings.HasPrefix(href, "/") {
		href = "https://www.google.com" + href
	}
	u, err := url.Parse(href)
	if err != nil || !strings.HasSuffix(strings.ToLower(u.Hostname()), "google.com") {
		return href
	}
	if u.Path != "/url" && u.Path != "/url/" {
		return href
	}
	for _, key := range []string{"q", "url", "u"} {
		if target := u.Query().Get(key); target != "" {
			return target
		}
	}
	return href
}
