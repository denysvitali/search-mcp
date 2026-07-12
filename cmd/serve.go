package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/denysvitali/search-mcp/internal/observability"
	"github.com/denysvitali/search-mcp/internal/reader"
	"github.com/denysvitali/search-mcp/internal/resilience"
	searchdomain "github.com/denysvitali/search-mcp/internal/search"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	// toolSearch is the MCP tool name for web search.
	toolSearch = "search"
	// toolSearchBatch is the MCP tool name for parallel multi-query search.
	toolSearchBatch = "search_batch"
	// toolWebRead is the MCP tool name for fetching a URL as Markdown.
	toolWebRead = "web_read"
	// toolReadPDF is the MCP tool name for targeted PDF extraction.
	toolReadPDF = "read_pdf"
	// toolProviderStatus is the MCP tool name for provider health inspection.
	toolProviderStatus = "provider_status"

	// maxResultCount caps the requested result count to a sane upper bound.
	maxResultCount = 100
	// maxPDFContextLines bounds surrounding lines returned for each PDF match.
	maxPDFContextLines = 10
	// maxPDFResults bounds pages or matches returned by read_pdf.
	maxPDFResults = 50
)

// searchArgs is the typed input for the search tool; the SDK derives the
// input schema from it and validates arguments before the handler runs.
type searchArgs struct {
	Query      string `json:"query" jsonschema:"Search query"`
	Provider   string `json:"provider,omitempty" jsonschema:"Provider name: duckduckgo, mojeek, brave, searxng, kagi, exa, tavily, or all to fan out to every provider and merge rankings"`
	Count      *int   `json:"count,omitempty" jsonschema:"Maximum number of results"`
	Country    string `json:"country,omitempty" jsonschema:"Provider country code"`
	Language   string `json:"language,omitempty" jsonschema:"Provider language code"`
	SafeSearch string `json:"safe_search,omitempty" jsonschema:"Provider safe search mode"`
	Freshness  string `json:"freshness,omitempty" jsonschema:"Provider freshness filter"`
}

// batchSearchArgs is the typed input for the search_batch tool.
type batchSearchArgs struct {
	Queries    []string `json:"queries" jsonschema:"Search queries to run in parallel, maximum 10"`
	Provider   string   `json:"provider,omitempty" jsonschema:"Provider name: duckduckgo, mojeek, brave, searxng, kagi, exa, tavily, or all"`
	Count      *int     `json:"count,omitempty" jsonschema:"Maximum number of results per query"`
	Country    string   `json:"country,omitempty" jsonschema:"Provider country code"`
	Language   string   `json:"language,omitempty" jsonschema:"Provider language code"`
	SafeSearch string   `json:"safe_search,omitempty" jsonschema:"Provider safe search mode"`
	Freshness  string   `json:"freshness,omitempty" jsonschema:"Provider freshness filter"`
}

// batchSearchResult groups the per-query responses; failed queries land in
// Errors keyed by query instead of failing the whole batch.
type batchSearchResult struct {
	Responses []searchdomain.Response `json:"responses"`
	Errors    map[string]string       `json:"errors,omitempty"`
}

// maxBatchQueries caps how many queries one search_batch call may fan out.
const maxBatchQueries = 10

// runBatchSearch executes every query concurrently through the service; the
// per-provider rate limiters still pace the actual provider calls.
func runBatchSearch(ctx context.Context, service *searchdomain.Service, queries []string, base searchdomain.Request) batchSearchResult {
	type outcome struct {
		query string
		resp  searchdomain.Response
		err   error
	}
	outcomes := make([]outcome, len(queries))

	var wg sync.WaitGroup
	for i, query := range queries {
		wg.Add(1)
		go func(i int, query string) {
			defer wg.Done()
			req := base
			req.Query = query
			resp, err := service.Search(ctx, req)
			outcomes[i] = outcome{query: query, resp: resp, err: err}
		}(i, query)
	}
	wg.Wait()

	result := batchSearchResult{}
	for _, o := range outcomes {
		if o.err != nil {
			if result.Errors == nil {
				result.Errors = make(map[string]string)
			}
			result.Errors[o.query] = o.err.Error()
			continue
		}
		result.Responses = append(result.Responses, o.resp)
	}
	return result
}

// providerStatus reports one provider's health.
type providerStatus struct {
	Name                string  `json:"name"`
	Breaker             string  `json:"breaker"`
	ConsecutiveFailures int     `json:"consecutive_failures"`
	CooldownRemaining   string  `json:"cooldown_remaining,omitempty"`
	RateLimitTokens     float64 `json:"rate_limit_tokens"`
}

type providerStatusResult struct {
	Providers []providerStatus `json:"providers"`
}

// collectProviderStatus snapshots breaker state and rate-limit headroom for
// every configured provider.
func collectProviderStatus(service *searchdomain.Service) providerStatusResult {
	result := providerStatusResult{}
	for _, name := range service.ProviderNames() {
		status := providerStatus{Name: name, Breaker: "unknown"}
		if breaker := resilience.FindBreaker(service.Provider(name)); breaker != nil {
			snapshot := breaker.Status()
			status.Breaker = snapshot.State
			status.ConsecutiveFailures = snapshot.ConsecutiveFailures
			if snapshot.CooldownRemaining > 0 {
				status.CooldownRemaining = snapshot.CooldownRemaining.Round(time.Millisecond).String()
			}
		}
		if tokens, ok := service.RateLimitTokens(name); ok {
			status.RateLimitTokens = tokens
		}
		result.Providers = append(result.Providers, status)
	}
	return result
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

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func newMCPServer(service *searchdomain.Service) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "search-mcp", Version: version}, nil)

	mcp.AddTool(s, &mcp.Tool{
		Name:        toolSearch,
		Description: "Search the web using a configured provider.",
		Annotations: readOnlyOpenWorld(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args searchArgs) (*mcp.CallToolResult, searchdomain.Response, error) {
		count, err := clampCount(intOrDefault(args.Count, viper.GetInt("count")))
		if err != nil {
			return nil, searchdomain.Response{}, err
		}
		resp, err := service.Search(ctx, searchdomain.Request{
			Query:      args.Query,
			Provider:   valueOrDefault(args.Provider, viper.GetString("provider")),
			Count:      count,
			Country:    valueOrDefault(args.Country, viper.GetString("country")),
			Language:   valueOrDefault(args.Language, viper.GetString("language")),
			SafeSearch: valueOrDefault(args.SafeSearch, viper.GetString("safe_search")),
			Freshness:  valueOrDefault(args.Freshness, viper.GetString("freshness")),
		})
		if err != nil {
			return nil, searchdomain.Response{}, err
		}
		data, err := json.MarshalIndent(resp, "", "  ")
		if err != nil {
			return nil, searchdomain.Response{}, err
		}
		return textResult(string(data)), resp, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        toolSearchBatch,
		Description: "Run several web searches in parallel and return grouped results. Ideal for research fan-out; failed queries are reported per query without failing the batch.",
		Annotations: readOnlyOpenWorld(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args batchSearchArgs) (*mcp.CallToolResult, batchSearchResult, error) {
		if len(args.Queries) == 0 {
			return nil, batchSearchResult{}, fmt.Errorf("queries must not be empty")
		}
		if len(args.Queries) > maxBatchQueries {
			return nil, batchSearchResult{}, fmt.Errorf("at most %d queries per batch, got %d", maxBatchQueries, len(args.Queries))
		}
		count, err := clampCount(intOrDefault(args.Count, viper.GetInt("count")))
		if err != nil {
			return nil, batchSearchResult{}, err
		}
		result := runBatchSearch(ctx, service, args.Queries, searchdomain.Request{
			Provider:   valueOrDefault(args.Provider, viper.GetString("provider")),
			Count:      count,
			Country:    valueOrDefault(args.Country, viper.GetString("country")),
			Language:   valueOrDefault(args.Language, viper.GetString("language")),
			SafeSearch: valueOrDefault(args.SafeSearch, viper.GetString("safe_search")),
			Freshness:  valueOrDefault(args.Freshness, viper.GetString("freshness")),
		})
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return nil, batchSearchResult{}, err
		}
		return textResult(string(data)), result, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        toolWebRead,
		Description: "Fetch a URL and return its content as Markdown. GitHub repo / issue / pull-request URLs and Reddit comment threads are pulled from their JSON APIs; everything else is fetched as HTML and converted. Use max_length/start_index for chunked reads of long pages, or query to grep within the page.",
		Annotations: readOnlyOpenWorld(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args webReadArgs) (*mcp.CallToolResult, webReadResult, error) {
		if args.Links != nil && *args.Links {
			content, err := reader.ExtractLinks(ctx, args.URL)
			if err != nil {
				return nil, webReadResult{}, err
			}
			return textResult(content), webReadResult{Content: content}, nil
		}
		var query string
		if args.Query != nil {
			query = *args.Query
		}
		content, err := reader.ReadWithOptions(ctx, args.URL, reader.ReadOptions{
			MaxLength:    intOrDefault(args.MaxLength, 0),
			StartIndex:   intOrDefault(args.StartIndex, 0),
			Query:        query,
			ContextLines: intOrDefault(args.Context, 0),
			MaxMatches:   intOrDefault(args.MaxMatches, 0),
		})
		if err != nil {
			return nil, webReadResult{}, err
		}
		return textResult(content), webReadResult{Content: content}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        toolProviderStatus,
		Description: "Report the health of every configured search provider: circuit-breaker state, consecutive failures, cooldown remaining, and rate-limit headroom. Use it to pick a healthy provider explicitly.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, providerStatusResult, error) {
		result := collectProviderStatus(service)
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return nil, providerStatusResult{}, err
		}
		return textResult(string(data)), result, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        toolReadPDF,
		Description: "Read selected pages or search a PDF and return page-numbered text. Use pages for 1-based ranges such as 1-3,17, or query to find matching text. With neither, returns the PDF's metadata, page count, and outline so you can target pages. Results never include PDF bytes.",
		Annotations: readOnlyOpenWorld(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args readPDFArgs) (*mcp.CallToolResult, readPDFResult, error) {
		contextLines, err := clampPDFNumber(intOrDefault(args.Context, 2), maxPDFContextLines, "context")
		if err != nil {
			return nil, readPDFResult{}, err
		}
		maxResults, err := clampPDFNumber(intOrDefault(args.MaxResults, 20), maxPDFResults, "max_results")
		if err != nil {
			return nil, readPDFResult{}, err
		}
		content, err := reader.ReadPDF(ctx, args.URL, args.Pages, args.Query, contextLines, maxResults)
		if err != nil {
			return nil, readPDFResult{}, err
		}
		return textResult(content), readPDFResult{Content: content}, nil
	})

	return s
}

// valueOrDefault returns v unless it is empty, in which case def is used.
func valueOrDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the MCP server (stdio by default, streamable HTTP with --http)",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()

		shutdown, err := observability.Setup(ctx, observability.Config{
			Enabled:     viper.GetBool("otel"),
			ServiceName: "search-mcp",
			Exporter:    viper.GetString("otel_exporter"),
			Endpoint:    viper.GetString("otel_endpoint"),
			Writer:      os.Stderr,
		})
		if err != nil {
			return err
		}
		defer func() { _ = shutdown(context.Background()) }()

		service, err := newSearchService(newLogger())
		if err != nil {
			return err
		}

		s := newMCPServer(service)

		if addr := viper.GetString("http"); addr != "" {
			return serveHTTP(ctx, s, addr)
		}

		// Run the stdio server with the signal-aware context so SIGINT/SIGTERM
		// trigger a clean shutdown.
		if err := s.Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
			return err
		}
		return nil
	},
}

func init() {
	serveCmd.Flags().String("http", "", "listen address for the streamable HTTP transport (e.g. :8080); empty uses stdio")
	_ = viper.BindPFlag("http", serveCmd.Flags().Lookup("http"))
}

// serveHTTP exposes the MCP server over the streamable HTTP transport until
// ctx is cancelled.
func serveHTTP(ctx context.Context, s *mcp.Server, addr string) error {
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.ListenAndServe() }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
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
