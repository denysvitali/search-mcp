package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/denysvitali/search-mcp/internal/search"
)

const braveEndpoint = "https://api.search.brave.com/res/v1/web/search"

type Brave struct {
	apiKey   string
	endpoint string
	client   *http.Client
}

func NewBrave(apiKey string, endpoint ...string) *Brave {
	target := braveEndpoint
	if len(endpoint) > 0 && endpoint[0] != "" {
		target = endpoint[0]
	}
	return &Brave{
		apiKey:   apiKey,
		endpoint: target,
		client:   &http.Client{Timeout: 15 * time.Second},
	}
}

func (b *Brave) Name() string {
	return "brave"
}

func (b *Brave) Search(ctx context.Context, req search.Request) (search.Response, error) {
	if b.apiKey == "" {
		return search.Response{}, fmt.Errorf("brave API key is required")
	}

	values := url.Values{}
	values.Set("q", req.Query)
	values.Set("count", strconv.Itoa(req.Count))
	if req.Country != "" {
		values.Set("country", req.Country)
	}
	if req.Language != "" {
		values.Set("search_lang", req.Language)
	}
	if req.SafeSearch != "" {
		values.Set("safesearch", req.SafeSearch)
	}
	if req.Freshness != "" {
		values.Set("freshness", req.Freshness)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, b.endpoint+"?"+values.Encode(), nil)
	if err != nil {
		return search.Response{}, err
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("X-Subscription-Token", b.apiKey)

	resp, err := b.client.Do(httpReq)
	if err != nil {
		return search.Response{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return search.Response{}, fmt.Errorf("brave search failed: %s", resp.Status)
	}

	var payload braveResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return search.Response{}, err
	}

	results := make([]search.Result, 0, len(payload.Web.Results))
	for _, item := range payload.Web.Results {
		results = append(results, search.Result{
			Title:       item.Title,
			URL:         item.URL,
			Description: item.Description,
			Source:      b.Name(),
			Published:   item.Age,
		})
	}

	return search.Response{Query: req.Query, Provider: b.Name(), Results: results}, nil
}

type braveResponse struct {
	Web struct {
		Results []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
			Age         string `json:"age"`
		} `json:"results"`
	} `json:"web"`
}
