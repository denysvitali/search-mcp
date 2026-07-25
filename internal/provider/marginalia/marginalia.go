// Package marginalia queries the Marginalia search index.
//
// Marginalia is an independent, non-commercial crawler that favours small,
// text-heavy, non-SEO-optimised sites. Its recall on mainstream queries is far
// below the mass-market engines, but it is the only backend here that serves a
// documented JSON API without an API key and without an anti-bot wall, which
// makes it a dependable floor when the HTML scrapers are being challenged.
package marginalia

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/denysvitali/search-mcp/internal/provider"
	"github.com/denysvitali/search-mcp/internal/provider/common"
	"github.com/denysvitali/search-mcp/internal/search"
)

func init() {
	provider.Register("marginalia", func(_, endpoint string) (search.Provider, error) {
		return NewMarginalia(endpoint), nil
	})
}

// marginaliaEndpoint is the keyless public API. The query is appended as a path
// segment, not a query parameter.
const marginaliaEndpoint = "https://api.marginalia.nu/public/search"

// Marginalia searches the Marginalia index via its public JSON API.
type Marginalia struct {
	endpoint string
	client   *http.Client
}

var _ provider.Provider = (*Marginalia)(nil)

func NewMarginalia(endpoint ...string) *Marginalia {
	target := marginaliaEndpoint
	if len(endpoint) > 0 && endpoint[0] != "" {
		target = strings.TrimRight(endpoint[0], "/")
	}
	return &Marginalia{endpoint: target, client: common.NewHTTPClient()}
}

func (m *Marginalia) Name() string { return "marginalia" }

func (m *Marginalia) Search(ctx context.Context, req search.Request) (search.Response, error) {
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return search.Response{}, fmt.Errorf("marginalia: query is required")
	}

	// The API takes the query as a path segment, so it must be path-escaped
	// rather than form-encoded (a form-encoded space would arrive as a literal
	// "+" in the query text).
	target := m.endpoint + "/" + url.PathEscape(query)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return search.Response{}, err
	}
	httpReq.Header.Set("Accept", "application/json")
	common.ApplyExtraHeaders(httpReq, req)

	resp, err := m.client.Do(httpReq)
	if err != nil {
		return search.Response{}, fmt.Errorf("marginalia request: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		return search.Response{}, fmt.Errorf("marginalia returned http 429: %w", search.NewRateLimitedError(resp.Header))
	case http.StatusForbidden:
		return search.Response{}, fmt.Errorf("marginalia returned http 403; request blocked by upstream: %w", provider.ErrBlocked)
	case http.StatusServiceUnavailable:
		// The public instance sheds load rather than queueing; another provider
		// will do better than retrying immediately.
		return search.Response{}, fmt.Errorf("marginalia is unavailable (http 503): %w", provider.ErrRateLimited)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return search.Response{}, fmt.Errorf("marginalia search failed: %s", resp.Status)
	}

	var payload marginaliaResponse
	if err := json.NewDecoder(common.LimitedBody(resp.Body)).Decode(&payload); err != nil {
		return search.Response{}, fmt.Errorf("decode marginalia response: %w", err)
	}

	count := req.Count
	if count <= 0 {
		count = 10
	}
	results := make([]search.Result, 0, min(count, len(payload.Results)))
	for _, item := range payload.Results {
		if len(results) >= count {
			break
		}
		if item.URL == "" || item.Title == "" {
			continue
		}
		results = append(results, search.Result{
			Title:       item.Title,
			URL:         item.URL,
			Description: strings.TrimSpace(item.Description),
			Source:      m.Name(),
		})
	}

	return search.Response{Query: req.Query, Provider: m.Name(), Results: results}, nil
}

type marginaliaResponse struct {
	Query   string `json:"query"`
	Results []struct {
		URL         string `json:"url"`
		Title       string `json:"title"`
		Description string `json:"description"`
	} `json:"results"`
}
