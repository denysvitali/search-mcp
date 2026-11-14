package cmd

import (
	"context"
	"sync"
	"time"

	"github.com/denysvitali/search-mcp/internal/resilience"
	searchdomain "github.com/denysvitali/search-mcp/internal/search"
)

// batchSearchArgs is the typed input for the search_batch tool.
type batchSearchArgs struct {
	Queries        []string `json:"queries" jsonschema:"Search queries to run in parallel, maximum 10"`
	Provider       string   `json:"provider,omitempty" jsonschema:"Configured provider name. Omit (recommended) to fan out to every configured provider and merge their rankings; name one only to target a specific backend."`
	Count          *int     `json:"count,omitempty" jsonschema:"Best-effort maximum number of results per query, up to 50. Keyless HTML backends carry about ten results per page, so large values may return fewer."`
	Country        string   `json:"country,omitempty" jsonschema:"Provider country code"`
	Language       string   `json:"language,omitempty" jsonschema:"Provider language code"`
	SafeSearch     string   `json:"safe_search,omitempty" jsonschema:"Provider safe search mode"`
	Freshness      string   `json:"freshness,omitempty" jsonschema:"Provider freshness filter"`
	IncludeDomains []string `json:"include_domains,omitempty" jsonschema:"Only return results from these hostnames and subdomains. Applied locally; count is best effort. An empty array clears the configured filter."`
	ExcludeDomains []string `json:"exclude_domains,omitempty" jsonschema:"Exclude results from these hostnames and subdomains. An empty array clears the configured filter."`
	MaxPerHost     *int     `json:"max_per_host,omitempty" jsonschema:"Maximum results per hostname; 0 disables the limit. Use 2 for source diversity."`
}

// batchSearchItem preserves one outcome for every input query, including
// duplicates and blank queries. Keeping the input order makes batch results
// straightforward to join back to the caller's work list.
type batchSearchItem struct {
	Query    string                 `json:"query"`
	Response *searchdomain.Response `json:"response,omitempty"`
	Error    string                 `json:"error,omitempty"`
}

// batchSearchResult groups one lossless outcome per input query.
type batchSearchResult struct {
	Items []batchSearchItem `json:"items"`
	// Responses and Errors retain the original shape for older clients. New
	// clients should consume Items because the legacy map cannot represent
	// duplicate query strings without data loss.
	Responses []searchdomain.Response `json:"responses,omitempty"`
	Errors    map[string]string       `json:"errors,omitempty"`
}

// maxBatchQueries caps how many queries one search_batch call may fan out.
const maxBatchQueries = 10

// maxBatchConcurrency avoids sending a burst of fallback requests when a
// public HTML provider starts challenging clients.
const maxBatchConcurrency = 2

// runBatchSearch executes every query concurrently through the service; the
// per-provider rate limiters still pace the actual provider calls.
func runBatchSearch(ctx context.Context, service *searchdomain.Service, queries []string, base searchdomain.Request) batchSearchResult {
	type outcome struct {
		query string
		resp  searchdomain.Response
		err   error
	}
	outcomes := make([]outcome, len(queries))
	sem := make(chan struct{}, maxBatchConcurrency)

	var wg sync.WaitGroup
	for i, query := range queries {
		wg.Add(1)
		go func(i int, query string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				outcomes[i] = outcome{query: query, err: ctx.Err()}
				return
			}
			req := base
			req.Query = query
			resp, err := service.Search(ctx, req)
			outcomes[i] = outcome{query: query, resp: resp, err: err}
		}(i, query)
	}
	wg.Wait()

	result := batchSearchResult{Items: make([]batchSearchItem, len(outcomes))}
	for i, o := range outcomes {
		item := batchSearchItem{Query: o.query}
		if o.err != nil {
			item.Error = o.err.Error()
			if result.Errors == nil {
				result.Errors = make(map[string]string)
			}
			if _, exists := result.Errors[o.query]; !exists {
				result.Errors[o.query] = o.err.Error()
			}
			result.Items[i] = item
			continue
		}
		resp := o.resp
		item.Response = &resp
		result.Responses = append(result.Responses, resp)
		result.Items[i] = item
	}
	return result
}

// providerStatus reports one provider's health.
type providerStatus struct {
	Name string `json:"name"`
	// Healthy is false once the provider has a failure streak, so a caller can
	// pick a working backend without interpreting breaker states.
	Healthy             bool   `json:"healthy"`
	Breaker             string `json:"breaker"`
	ConsecutiveFailures int    `json:"consecutive_failures"`
	CooldownRemaining   string `json:"cooldown_remaining,omitempty"`
	// LastError explains why a provider is degraded — an anti-bot challenge
	// reads very differently from a transport timeout.
	LastError       string  `json:"last_error,omitempty"`
	RateLimitTokens float64 `json:"rate_limit_tokens"`
}

type providerStatusResult struct {
	Providers []providerStatus `json:"providers"`
}

// collectProviderStatus snapshots breaker state and rate-limit headroom for
// every configured provider.
func collectProviderStatus(service *searchdomain.Service) providerStatusResult {
	result := providerStatusResult{}
	for _, name := range service.ProviderNames() {
		status := providerStatus{Name: name, Breaker: "unknown", Healthy: true}
		if breaker := resilience.FindBreaker(service.Provider(name)); breaker != nil {
			snapshot := breaker.Status()
			status.Breaker = snapshot.State
			status.ConsecutiveFailures = snapshot.ConsecutiveFailures
			status.Healthy = snapshot.State == "closed" && snapshot.ConsecutiveFailures == 0
			if snapshot.CooldownRemaining > 0 {
				status.CooldownRemaining = snapshot.CooldownRemaining.Round(time.Millisecond).String()
			}
			if snapshot.LastError != nil {
				status.LastError = snapshot.LastError.Error()
			}
		}
		if tokens, ok := service.RateLimitTokens(name); ok {
			status.RateLimitTokens = tokens
		}
		result.Providers = append(result.Providers, status)
	}
	return result
}
