package cmd

import (
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/viper"
)

// searchArgs is the typed input for the search tool; the SDK derives the
// input schema from it and validates arguments before the handler runs.
type searchArgs struct {
	Query          string   `json:"query" jsonschema:"Search query"`
	Provider       string   `json:"provider,omitempty" jsonschema:"Configured provider name. Omit (recommended) to fan out to every configured provider and merge their rankings, which is the most resilient option; name one only to target a specific backend."`
	Count          *int     `json:"count,omitempty" jsonschema:"Best-effort maximum number of results, up to 50. Keyless HTML backends carry about ten results per page and page three deep, so large values may return fewer."`
	Country        string   `json:"country,omitempty" jsonschema:"Provider country code"`
	Language       string   `json:"language,omitempty" jsonschema:"Provider language code"`
	SafeSearch     string   `json:"safe_search,omitempty" jsonschema:"Provider safe search mode"`
	Freshness      string   `json:"freshness,omitempty" jsonschema:"Provider freshness filter"`
	IncludeDomains []string `json:"include_domains,omitempty" jsonschema:"Only return results from these hostnames and subdomains. Applied locally; count is best effort. An empty array clears the configured filter."`
	ExcludeDomains []string `json:"exclude_domains,omitempty" jsonschema:"Exclude results from these hostnames and subdomains. An empty array clears the configured filter."`
	MaxPerHost     *int     `json:"max_per_host,omitempty" jsonschema:"Maximum results per hostname; 0 disables the limit. Use 2 for source diversity."`
}

type webReadArgs struct {
	URL        string  `json:"url" jsonschema:"The URL to fetch"`
	MaxLength  *int    `json:"max_length,omitempty" jsonschema:"Maximum characters to return; content beyond it is truncated with a marker showing how to continue. 0 or omitted returns everything"`
	StartIndex *int    `json:"start_index,omitempty" jsonschema:"Character offset to start from, for chunked reads of long pages"`
	Query      *string `json:"query,omitempty" jsonschema:"Optional case-insensitive text to search for in the page; returns matching lines with context instead of the full content"`
	Context    *int    `json:"context,omitempty" jsonschema:"Lines of context around each query match, default 2, maximum 10"`
	MaxMatches *int    `json:"max_matches,omitempty" jsonschema:"Maximum query matches to return, default 20, maximum 100"`
	Links      *bool   `json:"links,omitempty" jsonschema:"When true, return the page's links (anchor text plus absolute URL) instead of its content"`
}

// webReadResult is returned as structured content alongside the Markdown text.
type webReadResult struct {
	Content string `json:"content"`
}

// readPDFResult is returned as structured content alongside the Markdown text.
type readPDFResult struct {
	Content string `json:"content"`
}

type readPDFArgs struct {
	URL        string `json:"url" jsonschema:"The PDF URL to fetch"`
	Pages      string `json:"pages,omitempty" jsonschema:"Optional 1-based page ranges, for example 1-3,17"`
	Query      string `json:"query,omitempty" jsonschema:"Optional case-insensitive text to search for"`
	Context    *int   `json:"context,omitempty" jsonschema:"Lines of context around each match, default 2, maximum 10"`
	MaxResults *int   `json:"max_results,omitempty" jsonschema:"Maximum pages or matches to return, default 20, maximum 50"`
}

// clampCount validates and clamps an MCP-supplied result count, rejecting
// negative values and capping absurdly large ones.
func clampCount(v int) (int, error) {
	if v < 0 {
		return 0, fmt.Errorf("count must not be negative, got %d", v)
	}
	if v > maxResultCount {
		return maxResultCount, nil
	}
	return v, nil
}

// domainsOrDefault distinguishes omitted filters from explicit empty arrays.
func domainsOrDefault(domains []string, key string) []string {
	if domains == nil {
		return viper.GetStringSlice(key)
	}
	return domains
}

// intOrDefault dereferences an optional integer argument, falling back to the
// given default when the client omitted it.
func intOrDefault(v *int, def int) int {
	if v == nil {
		return def
	}
	return *v
}

// readOnlyOpenWorld marks a tool as non-mutating but interacting with the
// open web, so clients can treat calls as safe to repeat.
func readOnlyOpenWorld() *mcp.ToolAnnotations {
	openWorld := true
	return &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &openWorld}
}

// structuredResult leaves Content unset so the MCP SDK can add its standards-
// compliant JSON text fallback alongside StructuredContent. Older clients can
// therefore still consume tool output, while modern clients use the typed
// structured payload.
func structuredResult() *mcp.CallToolResult {
	return &mcp.CallToolResult{}
}

// valueOrDefault returns v unless it is empty, in which case def is used.
func valueOrDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func clampPDFNumber(value, maximum int, name string) (int, error) {
	if value < 0 {
		return 0, fmt.Errorf("%s must not be negative, got %d", name, value)
	}
	if value > maximum {
		return maximum, nil
	}
	return value, nil
}
