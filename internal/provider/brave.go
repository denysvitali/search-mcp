package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/denysvitali/search-mcp/internal/search"
)

const braveEndpoint = "https://api.search.brave.com/res/v1/web/search"

type Brave struct {
	apiKey   string
	endpoint string
	client   *http.Client
	// keyErr records an invalid (empty) API key detected in the constructor so
	// Search can fail fast with a stable error.
	keyErr error
}

// NewBrave constructs a Brave provider. The constructor signature is kept
// backward compatible (it cannot return an error), so an empty/whitespace API
// key is validated here and recorded; the resulting error is surfaced at the
// earliest point Search is invoked. Prefer NewBraveChecked when you want the
// validation failure up front.
func NewBrave(apiKey string, endpoint ...string) *Brave {
	target := braveEndpoint
	if len(endpoint) > 0 && endpoint[0] != "" {
		target = endpoint[0]
	}
	b := &Brave{
		apiKey:   apiKey,
		endpoint: target,
		client:   newHTTPClient(),
	}
	if strings.TrimSpace(apiKey) == "" {
		b.keyErr = fmt.Errorf("brave api key is required")
	}
	return b
}

// NewBraveChecked is like NewBrave but returns the API-key validation error
// directly from the constructor for callers that can handle it.
func NewBraveChecked(apiKey string, endpoint ...string) (*Brave, error) {
	b := NewBrave(apiKey, endpoint...)
	if b.keyErr != nil {
		return nil, b.keyErr
	}
	return b, nil
}

func (b *Brave) Name() string {
	return "brave"
}

func (b *Brave) Search(ctx context.Context, req search.Request) (search.Response, error) {
	if b.keyErr != nil {
		return search.Response{}, b.keyErr
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
	applyExtraHeaders(httpReq, req)

	resp, err := b.client.Do(httpReq)
	if err != nil {
		return search.Response{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return search.Response{}, fmt.Errorf("brave returned http 429: %w", search.NewRateLimitedError(resp.Header))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return search.Response{}, fmt.Errorf("brave search failed: %s", resp.Status)
	}

	var payload braveResponse
	if err := json.NewDecoder(limitedBody(resp.Body)).Decode(&payload); err != nil {
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
