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

const duckDuckGoEndpoint = "https://api.duckduckgo.com/"

type DuckDuckGo struct {
	endpoint string
	client   *http.Client
}

func NewDuckDuckGo(endpoint ...string) *DuckDuckGo {
	target := duckDuckGoEndpoint
	if len(endpoint) > 0 && endpoint[0] != "" {
		target = endpoint[0]
	}
	return &DuckDuckGo{endpoint: target, client: &http.Client{Timeout: 15 * time.Second}}
}

func (d *DuckDuckGo) Name() string {
	return "duckduckgo"
}

func (d *DuckDuckGo) Search(ctx context.Context, req search.Request) (search.Response, error) {
	values := url.Values{}
	values.Set("q", req.Query)
	values.Set("format", "json")
	values.Set("no_redirect", "1")
	values.Set("no_html", "1")
	values.Set("skip_disambig", "1")
	values.Set("t", "search-mcp")

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, d.endpoint+"?"+values.Encode(), nil)
	if err != nil {
		return search.Response{}, err
	}
	httpReq.Header.Set("Accept", "application/json")

	resp, err := d.client.Do(httpReq)
	if err != nil {
		return search.Response{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return search.Response{}, fmt.Errorf("duckduckgo search failed: %s", resp.Status)
	}

	var payload duckDuckGoResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return search.Response{}, err
	}

	results := make([]search.Result, 0, req.Count)
	if payload.AbstractURL != "" || payload.AbstractText != "" {
		results = append(results, search.Result{
			Title:       firstNonEmpty(payload.Heading, req.Query),
			URL:         payload.AbstractURL,
			Description: payload.AbstractText,
			Source:      d.Name(),
		})
	}
	flattenDuckTopics(payload.RelatedTopics, &results, req.Count)

	if len(results) > req.Count {
		results = results[:req.Count]
	}

	return search.Response{Query: req.Query, Provider: d.Name(), Results: results}, nil
}

type duckDuckGoResponse struct {
	Heading       string     `json:"Heading"`
	AbstractText  string     `json:"AbstractText"`
	AbstractURL   string     `json:"AbstractURL"`
	RelatedTopics []ddgTopic `json:"RelatedTopics"`
	Results       []ddgTopic `json:"Results"`
}

type ddgTopic struct {
	FirstURL string     `json:"FirstURL"`
	Text     string     `json:"Text"`
	Name     string     `json:"Name"`
	Topics   []ddgTopic `json:"Topics"`
}

func flattenDuckTopics(topics []ddgTopic, results *[]search.Result, limit int) {
	for _, topic := range topics {
		if len(*results) >= limit {
			return
		}
		if len(topic.Topics) > 0 {
			flattenDuckTopics(topic.Topics, results, limit)
			continue
		}
		if topic.FirstURL == "" && topic.Text == "" {
			continue
		}
		*results = append(*results, search.Result{
			Title:       firstNonEmpty(topic.Name, "DuckDuckGo result "+strconv.Itoa(len(*results)+1)),
			URL:         topic.FirstURL,
			Description: topic.Text,
			Source:      "duckduckgo",
		})
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
