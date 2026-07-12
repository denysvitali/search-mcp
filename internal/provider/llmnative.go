package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/denysvitali/search-mcp/internal/search"
)

const (
	kagiEndpoint   = "https://kagi.com/api/v1/search"
	exaEndpoint    = "https://api.exa.ai/search"
	tavilyEndpoint = "https://api.tavily.com/search"
)

// Kagi, Exa, and Tavily are opt-in API-backed providers. They share the same
// key validation convention as Brave, so a missing key is reported clearly.
type Kagi struct{ apiProvider }
type Exa struct{ apiProvider }
type Tavily struct{ apiProvider }

type apiProvider struct {
	name, apiKey, endpoint string
	client                 *http.Client
	keyErr                 error
}

func newAPIProvider(name, key, endpoint string) apiProvider {
	p := apiProvider{name: name, apiKey: key, endpoint: endpoint, client: newHTTPClient()}
	if strings.TrimSpace(key) == "" {
		p.keyErr = fmt.Errorf("%s api key is required", name)
	}
	return p
}

func NewKagi(key string, endpoint ...string) *Kagi {
	target := kagiEndpoint
	if len(endpoint) > 0 && endpoint[0] != "" {
		target = endpoint[0]
	}
	return &Kagi{newAPIProvider("kagi", key, target)}
}
func NewExa(key string, endpoint ...string) *Exa {
	target := exaEndpoint
	if len(endpoint) > 0 && endpoint[0] != "" {
		target = endpoint[0]
	}
	return &Exa{newAPIProvider("exa", key, target)}
}
func NewTavily(key string, endpoint ...string) *Tavily {
	target := tavilyEndpoint
	if len(endpoint) > 0 && endpoint[0] != "" {
		target = endpoint[0]
	}
	return &Tavily{newAPIProvider("tavily", key, target)}
}
func NewKagiChecked(key string, endpoint ...string) (*Kagi, error) {
	p := NewKagi(key, endpoint...)
	if p.keyErr != nil {
		return nil, p.keyErr
	}
	return p, nil
}
func NewExaChecked(key string, endpoint ...string) (*Exa, error) {
	p := NewExa(key, endpoint...)
	if p.keyErr != nil {
		return nil, p.keyErr
	}
	return p, nil
}
func NewTavilyChecked(key string, endpoint ...string) (*Tavily, error) {
	p := NewTavily(key, endpoint...)
	if p.keyErr != nil {
		return nil, p.keyErr
	}
	return p, nil
}
func (p *apiProvider) Name() string { return p.name }

func (p *Kagi) Search(ctx context.Context, req search.Request) (search.Response, error) {
	if p.keyErr != nil {
		return search.Response{}, p.keyErr
	}
	values := url.Values{"q": {req.Query}, "limit": {fmt.Sprint(req.Count)}}
	h, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint+"?"+values.Encode(), nil)
	if err != nil {
		return search.Response{}, err
	}
	h.Header.Set("Authorization", "Bot "+p.apiKey)
	h.Header.Set("Accept", "application/json")
	applyExtraHeaders(h, req)
	resp, err := p.client.Do(h)
	if err != nil {
		return search.Response{}, err
	}
	defer resp.Body.Close()
	if err := checkAPIStatus(p.name, resp); err != nil {
		return search.Response{}, err
	}
	var payload struct {
		Data []struct {
			Title      string `json:"title"`
			ShortTitle string `json:"t"`
			URL        string `json:"url"`
			Snippet    string `json:"snippet"`
			Published  string `json:"published"`
		} `json:"data"`
	}
	if err := json.NewDecoder(limitedBody(resp.Body)).Decode(&payload); err != nil {
		return search.Response{}, err
	}
	results := make([]search.Result, 0, len(payload.Data))
	for _, r := range payload.Data {
		title := r.Title
		if title == "" {
			title = r.ShortTitle
		}
		results = append(results, search.Result{Title: title, URL: r.URL, Description: r.Snippet, Published: r.Published, Source: p.Name()})
	}
	return search.Response{Query: req.Query, Provider: p.Name(), Results: results}, nil
}

func (p *Exa) Search(ctx context.Context, req search.Request) (search.Response, error) {
	if p.keyErr != nil {
		return search.Response{}, p.keyErr
	}
	body, err := json.Marshal(map[string]any{"query": req.Query, "type": "auto", "numResults": req.Count, "contents": map[string]bool{"highlights": true}})
	if err != nil {
		return search.Response{}, err
	}
	h, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return search.Response{}, err
	}
	h.Header.Set("x-api-key", p.apiKey)
	h.Header.Set("Content-Type", "application/json")
	h.Header.Set("Accept", "application/json")
	applyExtraHeaders(h, req)
	resp, err := p.client.Do(h)
	if err != nil {
		return search.Response{}, err
	}
	defer resp.Body.Close()
	if err := checkAPIStatus(p.name, resp); err != nil {
		return search.Response{}, err
	}
	var payload struct {
		Results []struct {
			Title      string   `json:"title"`
			URL        string   `json:"url"`
			Highlights []string `json:"highlights"`
			Text       string   `json:"text"`
			Published  string   `json:"publishedDate"`
		} `json:"results"`
	}
	if err := json.NewDecoder(limitedBody(resp.Body)).Decode(&payload); err != nil {
		return search.Response{}, err
	}
	results := make([]search.Result, 0, len(payload.Results))
	for _, r := range payload.Results {
		desc := strings.Join(r.Highlights, "\n")
		if desc == "" {
			desc = r.Text
		}
		results = append(results, search.Result{Title: r.Title, URL: r.URL, Description: desc, Published: r.Published, Source: p.Name()})
	}
	return search.Response{Query: req.Query, Provider: p.Name(), Results: results}, nil
}

func (p *Tavily) Search(ctx context.Context, req search.Request) (search.Response, error) {
	if p.keyErr != nil {
		return search.Response{}, p.keyErr
	}
	payload := map[string]any{"query": req.Query, "max_results": req.Count, "search_depth": "basic"}
	if req.Freshness != "" {
		payload["time_range"] = req.Freshness
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return search.Response{}, err
	}
	h, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return search.Response{}, err
	}
	h.Header.Set("Authorization", "Bearer "+p.apiKey)
	h.Header.Set("Content-Type", "application/json")
	h.Header.Set("Accept", "application/json")
	applyExtraHeaders(h, req)
	resp, err := p.client.Do(h)
	if err != nil {
		return search.Response{}, err
	}
	defer resp.Body.Close()
	if err := checkAPIStatus(p.name, resp); err != nil {
		return search.Response{}, err
	}
	var out struct {
		Results []struct {
			Title     string `json:"title"`
			URL       string `json:"url"`
			Content   string `json:"content"`
			Published string `json:"published_date"`
		} `json:"results"`
	}
	if err := json.NewDecoder(limitedBody(resp.Body)).Decode(&out); err != nil {
		return search.Response{}, err
	}
	results := make([]search.Result, 0, len(out.Results))
	for _, r := range out.Results {
		results = append(results, search.Result{Title: r.Title, URL: r.URL, Description: r.Content, Published: r.Published, Source: p.Name()})
	}
	return search.Response{Query: req.Query, Provider: p.Name(), Results: results}, nil
}

func checkAPIStatus(name string, resp *http.Response) error {
	if resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("%s returned http 429: %w", name, search.NewRateLimitedError(resp.Header))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s search failed: %s", name, resp.Status)
	}
	return nil
}
