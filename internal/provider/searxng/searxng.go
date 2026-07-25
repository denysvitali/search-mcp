package searxng

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/denysvitali/search-mcp/internal/provider"
	"github.com/denysvitali/search-mcp/internal/provider/common"
	"github.com/denysvitali/search-mcp/internal/search"
)

func init() {
	provider.Register("searxng", func(_, endpoint string) (search.Provider, error) {
		return NewSearXNGChecked(endpoint)
	})
}

// SearXNG queries a self-hosted SearXNG instance's JSON API. The instance URL
// is mandatory: there is no default public endpoint.
type SearXNG struct {
	baseURL string
	client  *http.Client
}

var _ provider.Provider = (*SearXNG)(nil)

// NewSearXNGChecked constructs a SearXNG provider, validating the instance URL.
func NewSearXNGChecked(baseURL string) (*SearXNG, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("searxng url is required")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("invalid searxng url %q", baseURL)
	}
	return &SearXNG{baseURL: baseURL, client: common.NewHTTPClient()}, nil
}

func (s *SearXNG) Name() string {
	return "searxng"
}

// searxngTimeRange maps the request freshness to SearXNG's time_range values.
// Both SearXNG's own names and Brave-style pd/pw/pm/py shorthands are accepted.
func searxngTimeRange(freshness string) string {
	switch strings.ToLower(strings.TrimSpace(freshness)) {
	case "day", "pd":
		return "day"
	case "week", "pw":
		return "week"
	case "month", "pm":
		return "month"
	case "year", "py":
		return "year"
	}
	return ""
}

// searxngMaxPages caps how deep a single search pages through an instance. Most
// public instances are volunteer-run and rate limit aggressively.
const searxngMaxPages = 3

// Search pages through the instance's results until it has req.Count of them.
// A SearXNG page carries roughly ten results, so without paging a larger request
// silently came back short.
func (s *SearXNG) Search(ctx context.Context, req search.Request) (search.Response, error) {
	count := req.Count
	if count <= 0 {
		count = 10
	}

	var results []search.Result
	seen := make(map[string]struct{}, count)
	for page := 1; page <= searxngMaxPages && len(results) < count; page++ {
		pageResults, err := s.searchPage(ctx, req, page)
		if err != nil {
			if page > 1 && len(results) > 0 {
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

	return search.Response{Query: req.Query, Provider: s.Name(), Results: results}, nil
}

func (s *SearXNG) searchPage(ctx context.Context, req search.Request, page int) ([]search.Result, error) {
	values := url.Values{}
	values.Set("q", req.Query)
	values.Set("format", "json")
	if req.Language != "" {
		values.Set("language", req.Language)
	}
	switch strings.ToLower(req.SafeSearch) {
	case "off":
		values.Set("safesearch", "0")
	case "moderate":
		values.Set("safesearch", "1")
	case "strict":
		values.Set("safesearch", "2")
	}
	if timeRange := searxngTimeRange(req.Freshness); timeRange != "" {
		values.Set("time_range", timeRange)
	}
	if page > 1 {
		values.Set("pageno", strconv.Itoa(page))
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/search?"+values.Encode(), nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "application/json")
	common.ApplyExtraHeaders(httpReq, req)

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("searxng returned http 429: %w", search.NewRateLimitedError(resp.Header))
	case resp.StatusCode == http.StatusForbidden:
		// Instances without the json format enabled answer 403; treat it as
		// blocked so the service can fall through to another provider.
		return nil, fmt.Errorf("searxng returned http 403 (is format=json enabled?): %w", provider.ErrBlocked)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return nil, fmt.Errorf("searxng search failed: %s", resp.Status)
	}

	var payload searxngResponse
	if err := json.NewDecoder(common.LimitedBody(resp.Body)).Decode(&payload); err != nil {
		return nil, err
	}

	results := make([]search.Result, 0, len(payload.Results))
	for _, item := range payload.Results {
		results = append(results, search.Result{
			Title:       item.Title,
			URL:         item.URL,
			Description: item.Content,
			Source:      s.Name(),
			Published:   item.PublishedDate,
		})
	}

	return results, nil
}

type searxngResponse struct {
	Results []struct {
		Title         string `json:"title"`
		URL           string `json:"url"`
		Content       string `json:"content"`
		PublishedDate string `json:"publishedDate"`
	} `json:"results"`
}
