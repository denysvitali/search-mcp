package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	cycletls "github.com/Danny-Dasilva/CycleTLS/cycletls"
	"github.com/denysvitali/search-mcp/internal/search"
	"golang.org/x/net/html"
)

const (
	duckDuckGoEndpoint  = "https://html.duckduckgo.com/html/"
	duckDuckGoUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/147.0.0.0 Safari/537.36"
	duckDuckGoJa3       = "771,4865-4866-4867-49195-49199-49196-49200-52393-52392-49171-49172-156-157-47-53,0-23-65281-10-11-35-16-5-13-18-51-45-43-27-21,29-23-24,0"
)

type DuckDuckGo struct {
	endpoint string
	client   cycletls.CycleTLS
}

func NewDuckDuckGo(endpoint ...string) *DuckDuckGo {
	target := duckDuckGoEndpoint
	if len(endpoint) > 0 && endpoint[0] != "" {
		target = endpoint[0]
	}
	return &DuckDuckGo{endpoint: target, client: cycletls.Init()}
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

	timeout := 15
	if dl, ok := ctx.Deadline(); ok {
		if remaining := time.Until(dl); remaining > 0 {
			timeout = int(remaining/time.Second) + 1
		}
	}

	type doResult struct {
		resp cycletls.Response
		err  error
	}
	resultCh := make(chan doResult, 1)
	go func() {
		resp, err := d.client.Do(d.endpoint, cycletls.Options{
			Body:      form.Encode(),
			Ja3:       duckDuckGoJa3,
			UserAgent: duckDuckGoUserAgent,
			Headers:   duckDuckGoHeaders(),
			Timeout:   timeout,
		}, "POST")
		resultCh <- doResult{resp, err}
	}()

	var r doResult
	select {
	case <-ctx.Done():
		return search.Response{}, ctx.Err()
	case r = <-resultCh:
	}
	if r.err != nil {
		return search.Response{}, fmt.Errorf("duckduckgo request: %w", r.err)
	}
	if r.resp.Status < 200 || r.resp.Status > 299 {
		return search.Response{}, fmt.Errorf("duckduckgo search failed: status %d", r.resp.Status)
	}

	body := []byte(r.resp.Body)
	if isDuckAnomaly(body) {
		return search.Response{}, errors.New("duckduckgo anti-bot challenge served (anomaly page); the source IP is being rate-limited or fingerprinted")
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
		"accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7",
		"accept-language":           "en-US,en;q=0.9",
		"cache-control":             "no-cache",
		"content-type":              "application/x-www-form-urlencoded",
		"origin":                    "https://html.duckduckgo.com",
		"pragma":                    "no-cache",
		"priority":                  "u=0, i",
		"referer":                   "https://html.duckduckgo.com/",
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
		if n.Type == html.ElementNode && n.Data == "div" && hasClass(n, "result__body") {
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
	titleAnchor := findElement(node, func(n *html.Node) bool {
		return n.Data == "a" && hasClass(n, "result__a")
	})
	if titleAnchor == nil {
		return search.Result{}, false
	}
	href := unwrapDuckURL(attr(titleAnchor, "href"))
	if href == "" || strings.HasPrefix(href, "https://duckduckgo.com/y.js") {
		return search.Result{}, false
	}
	title := strings.TrimSpace(textContent(titleAnchor))
	if title == "" {
		return search.Result{}, false
	}

	snippetNode := findElement(node, func(n *html.Node) bool {
		return n.Data == "a" && hasClass(n, "result__snippet")
	})
	var snippet string
	if snippetNode != nil {
		snippet = strings.TrimSpace(textContent(snippetNode))
	} else if div := findElement(node, func(n *html.Node) bool {
		return n.Data == "div" && hasClass(n, "result__snippet")
	}); div != nil {
		snippet = strings.TrimSpace(textContent(div))
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

func hasClass(n *html.Node, class string) bool {
	for _, a := range n.Attr {
		if a.Key != "class" {
			continue
		}
		for _, c := range strings.Fields(a.Val) {
			if c == class {
				return true
			}
		}
	}
	return false
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func findElement(root *html.Node, match func(*html.Node) bool) *html.Node {
	if root == nil {
		return nil
	}
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && match(c) {
			return c
		}
		if found := findElement(c, match); found != nil {
			return found
		}
	}
	return nil
}

func textContent(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n == nil {
			return
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}
