package brave

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
	provider.Register("brave", func(key, endpoint string) (search.Provider, error) {
		return NewBraveChecked(key, endpoint)
	})
}

const braveEndpoint = "https://api.search.brave.com/res/v1/web/search"

// braveMaxCount is the largest `count` Brave's web search API accepts. Sending
// more is rejected with a 422, so the value is clamped rather than passed
// through — a caller asking for 50 should get Brave's maximum, not an error.
const braveMaxCount = 20

// braveMaxOffset is the highest page offset Brave accepts (0-9).
const braveMaxOffset = 9

type Brave struct {
	apiKey   string
	endpoint string
	client   *http.Client
	// keyErr records an invalid (empty) API key detected in the constructor so
	// Search can fail fast with a stable error.
	keyErr error
}

var _ provider.Provider = (*Brave)(nil)

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
		client:   common.NewHTTPClient(),
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

// braveCount clamps a requested result count into the range Brave's API accepts.
func braveCount(count int) int {
	if count <= 0 {
		return 10
	}
	return min(count, braveMaxCount)
}

// Search fetches results from Brave, paging with `offset` when the caller wants
// more than one page holds. Each page is a separate billed API call, so paging
// only kicks in above braveMaxCount — an ordinary request stays a single call.
func (b *Brave) Search(ctx context.Context, req search.Request) (search.Response, error) {
	if b.keyErr != nil {
		return search.Response{}, b.keyErr
	}

	count := req.Count
	if count <= 0 {
		count = 10
	}

	var results []search.Result
	seen := make(map[string]struct{}, count)
	for offset := 0; offset <= braveMaxOffset && len(results) < count; offset++ {
		page, err := b.searchPage(ctx, req, offset)
		if err != nil {
			if offset > 0 && len(results) > 0 {
				break
			}
			return search.Response{}, err
		}
		for _, r := range page {
			if _, dup := seen[r.URL]; dup {
				continue
			}
			seen[r.URL] = struct{}{}
			results = append(results, r)
			if len(results) >= count {
				break
			}
		}
		// A page shorter than the page size means the index is exhausted.
		// Continuing would spend billed API calls on empty responses.
		if len(page) < braveCount(req.Count) {
			break
		}
	}

	return search.Response{Query: req.Query, Provider: b.Name(), Results: results}, nil
}

func (b *Brave) searchPage(ctx context.Context, req search.Request, offset int) ([]search.Result, error) {
	values := url.Values{}
	values.Set("q", req.Query)
	values.Set("count", strconv.Itoa(braveCount(req.Count)))
	if offset > 0 {
		values.Set("offset", strconv.Itoa(offset))
	}
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
		return nil, err
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("X-Subscription-Token", b.apiKey)
	common.ApplyExtraHeaders(httpReq, req)

	resp, err := b.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("brave returned http 429: %w", search.NewRateLimitedError(resp.Header))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("brave search failed: %s", resp.Status)
	}

	var payload braveResponse
	if err := json.NewDecoder(common.LimitedBody(resp.Body)).Decode(&payload); err != nil {
		return nil, err
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

	return results, nil
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
