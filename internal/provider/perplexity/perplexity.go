// Package perplexity integrates the Perplexity Search API's ranked web results.
package perplexity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/denysvitali/search-mcp/internal/provider"
	"github.com/denysvitali/search-mcp/internal/provider/common"
	"github.com/denysvitali/search-mcp/internal/search"
)

const defaultEndpoint = "https://api.perplexity.ai/search"

// Perplexity returns web results with query-relevant excerpts, not generated answers.
type Perplexity struct {
	key, endpoint string
	client        *http.Client
}

func init() {
	provider.Register("perplexity", func(key, endpoint string) (search.Provider, error) {
		return New(key, endpoint)
	})
}

// New constructs the opt-in provider. An API key is required.
func New(key, endpoint string) (*Perplexity, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("perplexity api key is required")
	}
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	if _, err := common.URLWithQuery(endpoint, nil); err != nil {
		return nil, err
	}
	return &Perplexity{key: key, endpoint: endpoint, client: common.NewHTTPClient()}, nil
}

func (p *Perplexity) Name() string { return "perplexity" }

func (p *Perplexity) Search(ctx context.Context, req search.Request) (search.Response, error) {
	count := req.Count
	if count <= 0 {
		count = 10
	}
	// The web API caps at 20 and has no pagination; never request people
	// search merely to obtain a larger result limit.
	body := map[string]any{
		"query": req.Query, "max_results": min(count, 20),
		"max_tokens_per_page": 512,
	}
	if req.Country != "" {
		body["country"] = strings.ToUpper(req.Country)
	}
	if req.Language != "" {
		body["search_language_filter"] = []string{strings.ToLower(req.Language)}
	}
	if req.Freshness != "" {
		freshness := strings.ToLower(req.Freshness)
		aliases := map[string]string{"pd": "day", "pw": "week", "pm": "month", "py": "year"}
		if alias, ok := aliases[freshness]; ok {
			freshness = alias
		}
		switch freshness {
		case "hour", "day", "week", "month", "year":
			body["search_recency_filter"] = freshness
		default:
			return search.Response{}, fmt.Errorf("perplexity: unsupported freshness %q", req.Freshness)
		}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return search.Response{}, err
	}
	h, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(payload))
	if err != nil {
		return search.Response{}, err
	}
	h.Header.Set("Authorization", "Bearer "+p.key)
	h.Header.Set("Content-Type", "application/json")
	h.Header.Set("Accept", "application/json")
	common.ApplyExtraHeaders(h, req)
	resp, err := p.client.Do(h)
	if err != nil {
		return search.Response{}, fmt.Errorf("perplexity search: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return search.Response{}, fmt.Errorf("perplexity: %w", search.NewRateLimitedError(resp.Header))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return search.Response{}, fmt.Errorf("perplexity search: HTTP %d", resp.StatusCode)
	}
	var data struct {
		Results *[]struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Snippet string `json:"snippet"`
			Date    string `json:"date"`
		} `json:"results"`
	}
	if err := json.NewDecoder(common.LimitedBody(resp.Body)).Decode(&data); err != nil {
		return search.Response{}, fmt.Errorf("perplexity decode: %w", err)
	}
	if data.Results == nil {
		return search.Response{}, fmt.Errorf("perplexity: response missing results array")
	}
	results := make([]search.Result, 0, min(count, len(*data.Results)))
	for _, r := range *data.Results {
		if strings.TrimSpace(r.URL) == "" {
			continue
		}
		results = append(results, search.Result{
			Title: r.Title, URL: r.URL, Description: r.Snippet,
			Published: r.Date, Source: p.Name(),
		})
		if len(results) >= min(count, 20) {
			break
		}
	}
	return search.Response{Query: req.Query, Provider: p.Name(), Results: results}, nil
}
