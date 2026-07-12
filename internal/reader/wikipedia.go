package reader

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// wikipediaAPIBaseURL overrides the per-language MediaWiki API endpoint. It
// is empty in production (the endpoint is derived from the article host) and
// set by tests to point at an httptest server.
var wikipediaAPIBaseURL = ""

type wikipediaQueryResponse struct {
	Query struct {
		Pages []struct {
			Title   string `json:"title"`
			Extract string `json:"extract"`
			FullURL string `json:"fullurl"`
			Missing bool   `json:"missing"`
		} `json:"pages"`
	} `json:"query"`
}

func isWikipediaArticleURL(parsedURL *url.URL) bool {
	host := strings.ToLower(parsedURL.Hostname())
	if !strings.HasSuffix(host, ".wikipedia.org") {
		return false
	}
	segments := pathSegments(parsedURL.Path)
	return len(segments) == 2 && segments[0] == "wiki" && segments[1] != ""
}

// fetchWikipediaContentAsMarkdown pulls the article's plain-text extract from
// the MediaWiki action API instead of scraping the rendered page.
func fetchWikipediaContentAsMarkdown(ctx context.Context, client *http.Client, parsedURL *url.URL) (string, error) {
	segments := pathSegments(parsedURL.Path)
	title := segments[1]

	apiBase := wikipediaAPIBaseURL
	if apiBase == "" {
		apiBase = "https://" + parsedURL.Hostname() + "/w/api.php"
	}
	params := url.Values{
		"action":        {"query"},
		"prop":          {"extracts|info"},
		"inprop":        {"url"},
		"explaintext":   {"1"},
		"redirects":     {"1"},
		"format":        {"json"},
		"formatversion": {"2"},
		"titles":        {title},
	}

	req, err := newRequest(ctx, apiBase+"?"+params.Encode(), "application/json")
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("wikipedia request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("wikipedia request failed: HTTP %d: %s", resp.StatusCode, readErrorBody(resp.Body))
	}

	var decoded wikipediaQueryResponse
	if err := json.NewDecoder(limitedBody(resp.Body)).Decode(&decoded); err != nil {
		return "", fmt.Errorf("failed to decode wikipedia response: %w", err)
	}
	if len(decoded.Query.Pages) == 0 || decoded.Query.Pages[0].Missing {
		return "", fmt.Errorf("wikipedia article %q not found", title)
	}

	page := decoded.Query.Pages[0]
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", page.Title)
	link := page.FullURL
	if link == "" {
		link = parsedURL.String()
	}
	fmt.Fprintf(&b, "- Link: %s\n\n", link)
	if strings.TrimSpace(page.Extract) == "" {
		b.WriteString("_No article text available._\n")
	} else {
		b.WriteString(strings.TrimSpace(page.Extract))
		b.WriteString("\n")
	}
	return cleanMarkdown(b.String()), nil
}
