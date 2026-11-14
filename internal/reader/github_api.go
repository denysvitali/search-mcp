package reader

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type gitHubAPIError struct {
	Message string `json:"message"`
}

// checkGitHubRateLimit inspects a response for rate-limit / abuse responses and
// returns a clear, non-retryable error if the request was throttled. It returns
// nil for any other status (including success).
func checkGitHubRateLimit(resp *http.Response) error {
	rateLimited := resp.StatusCode == http.StatusTooManyRequests
	if resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0" {
		rateLimited = true
	}
	if !rateLimited {
		return nil
	}

	detail := "github api rate limit exceeded"
	if reset := resp.Header.Get("X-RateLimit-Reset"); reset != "" {
		detail = fmt.Sprintf("%s (resets at unix %s)", detail, reset)
	}
	if retry := resp.Header.Get("Retry-After"); retry != "" {
		detail = fmt.Sprintf("%s (retry after %ss)", detail, retry)
	}
	return fmt.Errorf("%s: HTTP %d", detail, resp.StatusCode)
}

// doGitHubRequest issues a single GitHub API GET, returning the response so the
// caller can decode it. It surfaces rate-limit and non-200 errors uniformly.
func doGitHubRequest(ctx context.Context, client *http.Client, endpoint string) (*http.Response, error) {
	req, err := newRequest(ctx, endpoint, "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-GitHub-Api-Version", gitHubAPIVersion)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github request failed: %w", err)
	}

	if err := checkGitHubRateLimit(resp); err != nil {
		resp.Body.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("github api request failed: HTTP %d: %s", resp.StatusCode, decodeGitHubAPIError(resp.Body))
		resp.Body.Close()
		return nil, err
	}
	return resp, nil
}

func fetchGitHubJSON(ctx context.Context, client *http.Client, endpoint string, target interface{}) error {
	resp, err := doGitHubRequest(ctx, client, endpoint)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if err := json.NewDecoder(limitedBody(resp.Body)).Decode(target); err != nil {
		return fmt.Errorf("failed to decode github response from %s: %w", endpoint, err)
	}
	return nil
}

// fetchGitHubPaginated retrieves every page of a list endpoint, following the
// Link header's rel="next" relation, up to gitHubMaxPages. Each page is decoded
// into a fresh []T and appended to the accumulated result.
func fetchGitHubPaginated[T any](ctx context.Context, client *http.Client, endpoint string) ([]T, error) {
	var all []T

	next := withPerPage(endpoint, gitHubPerPage)
	for page := 0; page < gitHubMaxPages && next != ""; page++ {
		resp, err := doGitHubRequest(ctx, client, next)
		if err != nil {
			return nil, err
		}

		var batch []T
		decodeErr := json.NewDecoder(limitedBody(resp.Body)).Decode(&batch)
		nextURL := parseGitHubNextLink(resp.Header.Get("Link"))
		resp.Body.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("failed to decode github response from %s: %w", next, decodeErr)
		}

		all = append(all, batch...)

		// Stop early when the page is short: there is no further data even
		// if a Link header is (unexpectedly) present.
		if len(batch) < gitHubPerPage {
			break
		}
		next = nextURL
	}

	return all, nil
}

// withPerPage adds a per_page query parameter to a GitHub list endpoint,
// preserving any existing query values.
func withPerPage(endpoint string, perPage int) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	query := parsed.Query()
	query.Set("per_page", strconv.Itoa(perPage))
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// parseGitHubNextLink extracts the rel="next" URL from a GitHub Link header,
// returning "" when there is no next page.
func parseGitHubNextLink(header string) string {
	if strings.TrimSpace(header) == "" {
		return ""
	}
	for _, part := range strings.Split(header, ",") {
		section := strings.Split(strings.TrimSpace(part), ";")
		if len(section) < 2 {
			continue
		}
		linkURL := strings.TrimSpace(section[0])
		if !strings.HasPrefix(linkURL, "<") || !strings.HasSuffix(linkURL, ">") {
			continue
		}
		linkURL = linkURL[1 : len(linkURL)-1]
		for _, param := range section[1:] {
			param = strings.TrimSpace(param)
			if param == `rel="next"` || param == "rel=next" {
				return linkURL
			}
		}
	}
	return ""
}

func decodeGitHubAPIError(body io.Reader) string {
	payload, err := io.ReadAll(io.LimitReader(body, 4096))
	if err != nil {
		return "unable to read error body"
	}
	trimmed := strings.TrimSpace(string(payload))
	if trimmed == "" {
		return "empty error response"
	}

	var apiErr gitHubAPIError
	if err := json.Unmarshal(payload, &apiErr); err == nil && strings.TrimSpace(apiErr.Message) != "" {
		return apiErr.Message
	}
	return trimmed
}
