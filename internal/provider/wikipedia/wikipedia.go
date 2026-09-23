// Package wikipedia searches Wikipedia through the public MediaWiki Action API.
package wikipedia

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/denysvitali/search-mcp/internal/htmlutil"
	"github.com/denysvitali/search-mcp/internal/provider"
	"github.com/denysvitali/search-mcp/internal/provider/common"
	"github.com/denysvitali/search-mcp/internal/search"
	"golang.org/x/net/html"
)

const defaultEndpoint = "https://en.wikipedia.org/w/api.php"

var languageCode = regexp.MustCompile(`^[a-z]{2,12}(?:-[a-z]{2,12})?$`)

func init() {
	provider.Register("wikipedia", func(_, endpoint string) (search.Provider, error) {
		return NewWikipedia(endpoint), nil
	})
}

type Wikipedia struct {
	endpoint string
	client   *http.Client
}

var _ provider.Provider = (*Wikipedia)(nil)

func NewWikipedia(endpoint string) *Wikipedia {
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	return &Wikipedia{endpoint: endpoint, client: common.NewHTTPClient()}
}

func (w *Wikipedia) Name() string { return "wikipedia" }

func (w *Wikipedia) Search(ctx context.Context, req search.Request) (search.Response, error) {
	if strings.TrimSpace(req.Query) == "" {
		return search.Response{}, fmt.Errorf("wikipedia: query is required")
	}
	endpoint := w.endpoint
	if endpoint == defaultEndpoint && req.Language != "" {
		language := strings.ToLower(strings.TrimSpace(req.Language))
		if !languageCode.MatchString(language) {
			return search.Response{}, fmt.Errorf("wikipedia: invalid language code %q", req.Language)
		}
		endpoint = "https://" + language + ".wikipedia.org/w/api.php"
	}
	count := req.Count
	if count <= 0 {
		count = 10
	}
	count = min(count, 50) // MediaWiki's limit for anonymous clients.
	values := url.Values{
		"action":        {"query"},
		"list":          {"search"},
		"srsearch":      {req.Query},
		"srnamespace":   {"0"},
		"srlimit":       {fmt.Sprint(count)},
		"format":        {"json"},
		"formatversion": {"2"},
	}
	target, err := common.URLWithQuery(endpoint, values)
	if err != nil {
		return search.Response{}, fmt.Errorf("wikipedia endpoint: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return search.Response{}, err
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", "search-mcp/1.0 (https://github.com/denysvitali/search-mcp)")
	common.ApplyExtraHeaders(httpReq, req)
	resp, err := w.client.Do(httpReq)
	if err != nil {
		return search.Response{}, fmt.Errorf("wikipedia request: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		return search.Response{}, fmt.Errorf("wikipedia returned http 429: %w", search.NewRateLimitedError(resp.Header))
	case http.StatusForbidden:
		return search.Response{}, fmt.Errorf("wikipedia returned http 403: %w", provider.ErrBlocked)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return search.Response{}, fmt.Errorf("wikipedia search failed: status %d", resp.StatusCode)
	}
	var payload struct {
		Error *struct {
			Info string `json:"info"`
		} `json:"error"`
		Query *struct {
			Search []struct {
				Title   string `json:"title"`
				PageID  int64  `json:"pageid"`
				Snippet string `json:"snippet"`
			} `json:"search"`
		} `json:"query"`
	}
	if err := json.NewDecoder(common.LimitedBody(resp.Body)).Decode(&payload); err != nil {
		return search.Response{}, fmt.Errorf("decode wikipedia response: %w", err)
	}
	if payload.Error != nil {
		return search.Response{}, fmt.Errorf("wikipedia API: %s", payload.Error.Info)
	}
	if payload.Query == nil {
		return search.Response{}, fmt.Errorf("wikipedia response missing query data")
	}
	base, err := url.Parse(endpoint)
	if err != nil {
		return search.Response{}, err
	}
	results := make([]search.Result, 0, len(payload.Query.Search))
	for _, item := range payload.Query.Search {
		if item.Title == "" || item.PageID <= 0 {
			continue
		}
		article := &url.URL{Scheme: base.Scheme, Host: base.Host, Path: "/wiki/" + strings.ReplaceAll(item.Title, " ", "_")}
		fragment, err := html.ParseFragment(strings.NewReader(item.Snippet), nil)
		description := ""
		if err == nil {
			for _, node := range fragment {
				description += htmlutil.TextContent(node)
			}
			description = htmlutil.CollapseWhitespace(description)
		}
		results = append(results, search.Result{Title: item.Title, URL: article.String(), Description: description, Source: w.Name()})
	}
	return search.Response{Query: req.Query, Provider: w.Name(), Results: results}, nil
}
