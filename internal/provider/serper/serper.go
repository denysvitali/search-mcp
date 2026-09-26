// Package serper provides general Google web results through Serper's API.
// Serper requires an API key; its free account does not require a credit card.
package serper

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

const defaultEndpoint = "https://google.serper.dev/search"

// Serper returns organic Google results. It never requests generated answers
// or follows up with extra paid content-extraction calls.
type Serper struct {
	key, endpoint string
	client        *http.Client
}

func init() {
	provider.Register("serper", func(key, endpoint string) (search.Provider, error) { return New(key, endpoint) })
}

// New constructs a provider using an explicitly configured API key.
func New(key, endpoint string) (*Serper, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, fmt.Errorf("serper API key is required (a free account is available)")
	}
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	if _, err := common.URLWithQuery(endpoint, nil); err != nil {
		return nil, err
	}
	return &Serper{key: key, endpoint: endpoint, client: common.NewHTTPClient()}, nil
}
func (s *Serper) Name() string { return "serper" }

func (s *Serper) Search(ctx context.Context, req search.Request) (search.Response, error) {
	count := req.Count
	if count <= 0 {
		count = 10
	}
	// One request per search; do not spend additional credits on pagination.
	count = min(count, 100)
	body := map[string]any{"q": req.Query, "num": count}
	if req.Country != "" {
		body["gl"] = strings.ToLower(strings.TrimSpace(req.Country))
	}
	if req.Language != "" {
		body["hl"] = strings.ToLower(strings.TrimSpace(req.Language))
	}
	if req.Freshness != "" {
		switch strings.ToLower(strings.TrimSpace(req.Freshness)) {
		case "pd", "day", "d":
			body["tbs"] = "qdr:d"
		case "pw", "week", "w":
			body["tbs"] = "qdr:w"
		case "pm", "month", "m":
			body["tbs"] = "qdr:m"
		case "py", "year", "y":
			body["tbs"] = "qdr:y"
		default:
			return search.Response{}, fmt.Errorf("serper: unsupported freshness %q", req.Freshness)
		}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return search.Response{}, err
	}
	h, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(payload))
	if err != nil {
		return search.Response{}, err
	}
	h.Header.Set("X-API-KEY", s.key)
	h.Header.Set("Content-Type", "application/json")
	h.Header.Set("Accept", "application/json")
	common.ApplyExtraHeaders(h, req)
	resp, err := s.client.Do(h)
	if err != nil {
		return search.Response{}, fmt.Errorf("serper request: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		return search.Response{}, fmt.Errorf("serper: %w", search.NewRateLimitedError(resp.Header))
	case http.StatusUnauthorized, http.StatusForbidden:
		return search.Response{}, fmt.Errorf("serper rejected the API key: %w", search.ErrBlocked)
	case http.StatusPaymentRequired:
		return search.Response{}, fmt.Errorf("serper credits exhausted: %w", search.ErrBlocked)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return search.Response{}, fmt.Errorf("serper search failed: HTTP %d", resp.StatusCode)
	}
	var data struct {
		Organic *[]struct {
			Title   string `json:"title"`
			Link    string `json:"link"`
			Snippet string `json:"snippet"`
			Date    string `json:"date"`
		} `json:"organic"`
	}
	if err := json.NewDecoder(common.LimitedBody(resp.Body)).Decode(&data); err != nil {
		return search.Response{}, fmt.Errorf("decode serper: %w", err)
	}
	if data.Organic == nil {
		return search.Response{}, fmt.Errorf("serper response missing organic results")
	}
	results := make([]search.Result, 0, min(count, len(*data.Organic)))
	for _, r := range *data.Organic {
		if strings.TrimSpace(r.Link) == "" {
			continue
		}
		results = append(results, search.Result{Title: r.Title, URL: r.Link, Description: r.Snippet, Published: r.Date, Source: s.Name()})
		if len(results) >= count {
			break
		}
	}
	return search.Response{Query: req.Query, Provider: s.Name(), Results: results}, nil
}
