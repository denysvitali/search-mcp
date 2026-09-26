package common

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/denysvitali/search-mcp/internal/search"
)

// JSONAPI handles bounded GET requests for keyless public search APIs.
type JSONAPI struct {
	// Name is the provider name used in Result.Source and error messages.
	Name string
	// Endpoint is the absolute http(s) URL to GET.
	Endpoint string
	// Client performs the request. A nil Client falls back to NewHTTPClient.
	Client *http.Client
	// BuildQuery returns the request's query parameters. It is skipped entirely
	// when nil (endpoints that take the query as a path segment).
	BuildQuery func(req search.Request, count int) map[string]string
	// BuildHeaders sets request headers. It is skipped when nil.
	BuildHeaders func(httpReq *http.Request)
	// Decode converts the decoded top-level JSON into result rows. Returning a
	// non-nil error fails the search rather than yielding an empty result set.
	Decode func(payload []byte) ([]search.Result, error)
}

// Search performs one JSON API request and returns its decoded rows.
func (j *JSONAPI) Search(ctx context.Context, req search.Request) (search.Response, error) {
	if j.Name == "" || j.Endpoint == "" || j.Decode == nil {
		return search.Response{}, fmt.Errorf("jsonapi: provider %q is not configured", j.Name)
	}
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		return search.Response{}, fmt.Errorf("%s: query is required", j.Name)
	}
	if _, err := FreshnessSince(req.Freshness, time.Now()); err != nil {
		return search.Response{}, err
	}
	count := req.Count
	if count <= 0 {
		count = 10
	}

	target := j.Endpoint
	if j.BuildQuery != nil {
		var err error
		target, err = URLWithQuery(j.Endpoint, buildValues(j.BuildQuery(req, count)))
		if err != nil {
			return search.Response{}, fmt.Errorf("%s endpoint: %w", j.Name, err)
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return search.Response{}, fmt.Errorf("%s request: %w", j.Name, err)
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", "search-mcp/1.0 (https://github.com/denysvitali/search-mcp)")
	if j.BuildHeaders != nil {
		j.BuildHeaders(httpReq)
	}
	ApplyExtraHeaders(httpReq, req)

	client := j.Client
	if client == nil {
		client = NewHTTPClient()
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return search.Response{}, fmt.Errorf("%s request: %w", j.Name, err)
	}
	defer resp.Body.Close()

	if err := classifyStatus(j.Name, resp); err != nil {
		// Stack Exchange uses HTTP 400 for typed API errors, including a
		// method throttle. Decode that envelope to record its cooldown.
		if resp.StatusCode == http.StatusBadRequest {
			body, readErr := io.ReadAll(LimitedBody(resp.Body))
			if readErr == nil && json.Valid(body) {
				if _, decodeErr := j.Decode(body); decodeErr != nil {
					return search.Response{}, fmt.Errorf("%s HTTP 400: %w", j.Name, decodeErr)
				}
			}
		}
		return search.Response{}, err
	}

	body, err := io.ReadAll(LimitedBody(resp.Body))
	if err != nil {
		return search.Response{}, fmt.Errorf("read %s response: %w", j.Name, err)
	}
	// A JSON API can still be fronted by a bot wall or a login portal that
	// answers 200 with HTML. Classifying that as blocked keeps a failed backend
	// from looking like a healthy search that simply found nothing.
	if !json.Valid(body) && IsChallengePage(body) {
		return search.Response{}, ErrChallenge(j.Name)
	}

	results, err := j.Decode(body)
	if err != nil {
		return search.Response{}, fmt.Errorf("%s: %w", j.Name, err)
	}
	if len(results) > count {
		results = results[:count]
	}
	return search.Response{Query: req.Query, Provider: j.Name, Results: results}, nil
}

// buildValues turns a provider's query map into url.Values, dropping entries
// whose value is empty so a backend can pass optional filters without emitting
// meaningless empty parameters.
func buildValues(params map[string]string) url.Values {
	values := make(url.Values, len(params))
	for key, value := range params {
		if value == "" {
			continue
		}
		values.Set(key, value)
	}
	return values
}

// classifyStatus maps an HTTP status onto the shared search sentinels so the
// service's fallback and the retry decorator both classify a failure correctly.
func classifyStatus(name string, resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		return fmt.Errorf("%s returned http 429: %w", name, search.NewRateLimitedError(resp.Header))
	case http.StatusForbidden, http.StatusUnauthorized:
		return fmt.Errorf("%s returned http %d; request blocked by upstream: %w", name, resp.StatusCode, search.ErrBlocked)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s request failed: status %d", name, resp.StatusCode)
	}
	return nil
}
