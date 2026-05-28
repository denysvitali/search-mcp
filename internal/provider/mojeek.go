package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/denysvitali/search-mcp/internal/htmlutil"
	"github.com/denysvitali/search-mcp/internal/search"
	"golang.org/x/net/html"
)

const (
	mojeekEndpoint  = "https://www.mojeek.com/search"
	mojeekUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.0.0 Safari/537.36"
)

type Mojeek struct {
	endpoint string
	client   *http.Client
}

func NewMojeek(endpoint ...string) *Mojeek {
	target := mojeekEndpoint
	if len(endpoint) > 0 && endpoint[0] != "" {
		target = endpoint[0]
	}
	return &Mojeek{endpoint: target, client: newHTTPClient(defaultHTTPTimeout)}
}

func (m *Mojeek) Name() string {
	return "mojeek"
}

func (m *Mojeek) Search(ctx context.Context, req search.Request) (search.Response, error) {
	values := url.Values{}
	values.Set("q", req.Query)
	values.Set("safe", mojeekSafeSearch(req.SafeSearch))
	if lang := strings.ToLower(strings.TrimSpace(req.Language)); lang != "" {
		values.Set("lb", lang)
	}
	if country := strings.ToLower(strings.TrimSpace(req.Country)); country != "" {
		values.Set("arc", country)
	}
	if since := mojeekSince(req.Freshness); since != "" {
		values.Set("since", since)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, m.endpoint+"?"+values.Encode(), nil)
	if err != nil {
		return search.Response{}, err
	}
	httpReq.Header.Set("User-Agent", mojeekUserAgent)
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	httpReq.Header.Set("Accept-Language", "en-US,en;q=0.9")
	applyExtraHeaders(httpReq, req)

	resp, err := m.client.Do(httpReq)
	if err != nil {
		return search.Response{}, fmt.Errorf("mojeek request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return search.Response{}, fmt.Errorf("mojeek returned http 429; back off and retry later: %w", ErrRateLimited)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return search.Response{}, fmt.Errorf("mojeek search failed: status %d", resp.StatusCode)
	}

	doc, err := html.Parse(resp.Body)
	if err != nil {
		return search.Response{}, fmt.Errorf("parse mojeek html: %w", err)
	}

	count := req.Count
	if count <= 0 {
		count = 10
	}
	results := extractMojeekResults(doc, count)

	return search.Response{Query: req.Query, Provider: m.Name(), Results: results}, nil
}

func mojeekSafeSearch(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "off", "0", "none":
		return "0"
	default:
		return "1"
	}
}

func mojeekSince(freshness string) string {
	now := time.Now()
	var t time.Time
	switch strings.ToLower(strings.TrimSpace(freshness)) {
	case "day", "d", "pd":
		t = now.AddDate(0, 0, -1)
	case "week", "w", "pw":
		t = now.AddDate(0, 0, -7)
	case "month", "m", "pm":
		t = now.AddDate(0, -1, 0)
	case "year", "y", "py":
		t = now.AddDate(-1, 0, 0)
	default:
		return ""
	}
	return t.Format("20060102")
}

func extractMojeekResults(root *html.Node, limit int) []search.Result {
	resultsList := htmlutil.FindElement(root, func(n *html.Node) bool {
		return n.Data == "ul" && htmlutil.HasClass(n, "results-standard")
	})
	if resultsList == nil {
		return nil
	}

	results := make([]search.Result, 0, limit)
	for c := resultsList.FirstChild; c != nil && len(results) < limit; c = c.NextSibling {
		if c.Type != html.ElementNode || c.Data != "li" {
			continue
		}
		if r, ok := parseMojeekResult(c); ok {
			results = append(results, r)
		}
	}
	return results
}

func parseMojeekResult(li *html.Node) (search.Result, bool) {
	urlAnchor := htmlutil.FindElement(li, func(n *html.Node) bool {
		return n.Data == "a" && htmlutil.HasClass(n, "ob")
	})
	if urlAnchor == nil {
		return search.Result{}, false
	}
	href := strings.TrimSpace(htmlutil.Attr(urlAnchor, "href"))
	if href == "" {
		return search.Result{}, false
	}

	titleAnchor := htmlutil.FindElement(li, func(n *html.Node) bool {
		return n.Data == "a" && htmlutil.HasClass(n, "title")
	})
	if titleAnchor == nil {
		return search.Result{}, false
	}
	title := strings.TrimSpace(htmlutil.TextContent(titleAnchor))
	if title == "" {
		return search.Result{}, false
	}

	snippet := ""
	if p := htmlutil.FindElement(li, func(n *html.Node) bool {
		return n.Data == "p" && htmlutil.HasClass(n, "s")
	}); p != nil {
		snippet = htmlutil.CollapseWhitespace(htmlutil.TextContent(p))
	}

	return search.Result{
		Title:       title,
		URL:         href,
		Description: snippet,
		Source:      "mojeek",
	}, true
}
