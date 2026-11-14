package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/denysvitali/search-mcp/internal/observability"
	"github.com/denysvitali/search-mcp/internal/reader"
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

	// maxResultCount caps the requested result count. The keyless HTML backends
	// carry roughly ten organic rows per page and page at most three deep, so a
	// far higher ceiling would only advertise results that cannot be delivered.
	maxResultCount = searchdomain.MaxResultCount
	// maxPDFContextLines bounds surrounding lines returned for each PDF match.
	maxPDFContextLines = 10
	// maxPDFResults bounds pages or matches returned by read_pdf.
	maxPDFResults = 50
)

func newMCPServer(service *searchdomain.Service) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "search-mcp", Version: version}, nil)

	mcp.AddTool(s, &mcp.Tool{
		Name:        toolSearch,
		Description: "Search the web using configured providers. Omit provider to fan out and merge rankings; partial provider failures are returned in degraded.",
		Annotations: readOnlyOpenWorld(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args searchArgs) (*mcp.CallToolResult, searchdomain.Response, error) {
		count, err := clampCount(intOrDefault(args.Count, viper.GetInt("count")))
		if err != nil {
			return nil, searchdomain.Response{}, err
		}
		searchCtx, cancel := withConfiguredTimeout(ctx, viper.GetDuration("search_timeout"))
		defer cancel()
		resp, err := service.Search(searchCtx, searchdomain.Request{
			Query:          args.Query,
			Provider:       valueOrDefault(args.Provider, viper.GetString("provider")),
			Count:          count,
			Country:        valueOrDefault(args.Country, viper.GetString("country")),
			Language:       valueOrDefault(args.Language, viper.GetString("language")),
			SafeSearch:     valueOrDefault(args.SafeSearch, viper.GetString("safe_search")),
			Freshness:      valueOrDefault(args.Freshness, viper.GetString("freshness")),
			IncludeDomains: domainsOrDefault(args.IncludeDomains, "include_domains"),
			ExcludeDomains: domainsOrDefault(args.ExcludeDomains, "exclude_domains"),
			MaxPerHost:     intOrDefault(args.MaxPerHost, viper.GetInt("max_per_host")),
		})
		if err != nil {
			return nil, searchdomain.Response{}, err
		}
		return structuredResult(), resp, nil
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
		batchCtx, cancel := withConfiguredTimeout(ctx, viper.GetDuration("batch_timeout"))
		defer cancel()
		result := runBatchSearch(batchCtx, service, args.Queries, searchdomain.Request{
			Provider:       valueOrDefault(args.Provider, viper.GetString("provider")),
			Count:          count,
			Country:        valueOrDefault(args.Country, viper.GetString("country")),
			Language:       valueOrDefault(args.Language, viper.GetString("language")),
			SafeSearch:     valueOrDefault(args.SafeSearch, viper.GetString("safe_search")),
			Freshness:      valueOrDefault(args.Freshness, viper.GetString("freshness")),
			IncludeDomains: domainsOrDefault(args.IncludeDomains, "include_domains"),
			ExcludeDomains: domainsOrDefault(args.ExcludeDomains, "exclude_domains"),
			MaxPerHost:     intOrDefault(args.MaxPerHost, viper.GetInt("max_per_host")),
		})
		return structuredResult(), result, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        toolWebRead,
		Description: "Fetch a public URL and return Markdown. Redirects are subject to the domain policy and SSRF guard. Use max_length/start_index for chunks, query for case-insensitive matches, or links to list links.",
		Annotations: readOnlyOpenWorld(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args webReadArgs) (*mcp.CallToolResult, webReadResult, error) {
		readCtx, cancel := withConfiguredTimeout(ctx, viper.GetDuration("read_timeout"))
		defer cancel()
		var query string
		if args.Query != nil {
			query = *args.Query
		}
		opts := reader.ReadOptions{
			MaxLength:       intOrDefault(args.MaxLength, 0),
			StartIndex:      intOrDefault(args.StartIndex, 0),
			Query:           query,
			ContextLines:    intOrDefault(args.Context, 0),
			ContextLinesSet: args.Context != nil,
			MaxMatches:      intOrDefault(args.MaxMatches, 0),
		}
		if err := opts.Validate(); err != nil {
			return nil, webReadResult{}, err
		}
		var content string
		var err error
		if args.Links != nil && *args.Links {
			content, err = reader.ExtractLinks(readCtx, args.URL)
			if err == nil {
				content, err = opts.Apply(content)
			}
		} else {
			content, err = reader.ReadWithOptions(readCtx, args.URL, opts)
		}
		if err != nil {
			return nil, webReadResult{}, err
		}
		return structuredResult(), webReadResult{Content: content}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        toolProviderStatus,
		Description: "Report the health of every configured search provider: circuit-breaker state, consecutive failures, cooldown remaining, and rate-limit headroom. Use it to pick a healthy provider explicitly.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, providerStatusResult, error) {
		result := collectProviderStatus(service)
		return structuredResult(), result, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        toolReadPDF,
		Description: "Read selected pages or search a PDF and return page-numbered text. Use pages for 1-based ranges such as 1-3,17, or query to find matching text. With neither, returns the PDF's metadata, page count, and outline so you can target pages. Results never include PDF bytes.",
		Annotations: readOnlyOpenWorld(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args readPDFArgs) (*mcp.CallToolResult, readPDFResult, error) {
		readCtx, cancel := withConfiguredTimeout(ctx, viper.GetDuration("read_timeout"))
		defer cancel()
		contextLines, err := clampPDFNumber(intOrDefault(args.Context, 2), maxPDFContextLines, "context")
		if err != nil {
			return nil, readPDFResult{}, err
		}
		maxResults, err := clampPDFNumber(intOrDefault(args.MaxResults, 20), maxPDFResults, "max_results")
		if err != nil {
			return nil, readPDFResult{}, err
		}
		content, err := reader.ReadPDF(readCtx, args.URL, args.Pages, args.Query, contextLines, maxResults)
		if err != nil {
			return nil, readPDFResult{}, err
		}
		return structuredResult(), readPDFResult{Content: content}, nil
	})

	return s
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
			return serveHTTP(ctx, s, addr, viper.GetString("http_token"))
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
