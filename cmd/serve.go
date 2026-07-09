package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/denysvitali/search-mcp/internal/observability"
	"github.com/denysvitali/search-mcp/internal/reader"
	searchdomain "github.com/denysvitali/search-mcp/internal/search"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	// toolSearch is the MCP tool name for web search.
	toolSearch = "search"
	// toolWebRead is the MCP tool name for fetching a URL as Markdown.
	toolWebRead = "web_read"
	// toolReadPDF is the MCP tool name for targeted PDF extraction.
	toolReadPDF = "read_pdf"

	// maxResultCount caps the requested result count to a sane upper bound.
	maxResultCount = 100
	// maxPDFContextLines bounds surrounding lines returned for each PDF match.
	maxPDFContextLines = 10
	// maxPDFResults bounds pages or matches returned by read_pdf.
	maxPDFResults = 50
)

// clampCount validates and clamps an MCP-supplied result count, rejecting
// negative values and capping absurdly large ones before casting to int.
func clampCount(v float64) (int, error) {
	if v < 0 {
		return 0, fmt.Errorf("count must not be negative, got %v", v)
	}
	if v > maxResultCount {
		return maxResultCount, nil
	}
	return int(v), nil
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the MCP stdio server",
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

		s := server.NewMCPServer(
			"search-mcp",
			version,
			server.WithToolCapabilities(false),
			server.WithRecovery(),
		)

		tool := mcp.NewTool(toolSearch,
			mcp.WithDescription("Search the web using a configured provider."),
			mcp.WithString("query", mcp.Required(), mcp.Description("Search query")),
			mcp.WithString("provider", mcp.Description("Provider name: duckduckgo, mojeek, or brave")),
			mcp.WithNumber("count", mcp.Description("Maximum number of results")),
			mcp.WithString("country", mcp.Description("Provider country code")),
			mcp.WithString("language", mcp.Description("Provider language code")),
			mcp.WithString("safe_search", mcp.Description("Provider safe search mode")),
			mcp.WithString("freshness", mcp.Description("Provider freshness filter")),
		)

		s.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			query, err := request.RequireString("query")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			count, err := clampCount(request.GetFloat("count", float64(viper.GetInt("count"))))
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			resp, err := service.Search(ctx, searchdomain.Request{
				Query:      query,
				Provider:   request.GetString("provider", viper.GetString("provider")),
				Count:      count,
				Country:    request.GetString("country", viper.GetString("country")),
				Language:   request.GetString("language", viper.GetString("language")),
				SafeSearch: request.GetString("safe_search", viper.GetString("safe_search")),
				Freshness:  request.GetString("freshness", viper.GetString("freshness")),
			})
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			data, err := json.MarshalIndent(resp, "", "  ")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		})

		readTool := mcp.NewTool(toolWebRead,
			mcp.WithDescription("Fetch a URL and return its content as Markdown. GitHub repo / issue / pull-request URLs and Reddit comment threads are pulled from their JSON APIs; everything else is fetched as HTML and converted."),
			mcp.WithString("url", mcp.Required(), mcp.Description("The URL to fetch")),
		)
		s.AddTool(readTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			urlStr, err := request.RequireString("url")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			content, err := reader.Read(ctx, urlStr)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(content), nil
		})

		readPDFTool := mcp.NewTool(toolReadPDF,
			mcp.WithDescription("Read selected pages or search a PDF and return page-numbered text. Use pages for 1-based ranges such as 1-3,17, or query to find matching text. Results never include PDF bytes."),
			mcp.WithString("url", mcp.Required(), mcp.Description("The PDF URL to fetch")),
			mcp.WithString("pages", mcp.Description("Optional 1-based page ranges, for example 1-3,17")),
			mcp.WithString("query", mcp.Description("Optional case-insensitive text to search for")),
			mcp.WithNumber("context", mcp.Description("Lines of context around each match, default 2, maximum 10")),
			mcp.WithNumber("max_results", mcp.Description("Maximum pages or matches to return, default 20, maximum 50")),
		)
		s.AddTool(readPDFTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			urlStr, err := request.RequireString("url")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			contextLines, err := clampPDFNumber(request.GetFloat("context", 2), maxPDFContextLines, "context")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			maxResults, err := clampPDFNumber(request.GetFloat("max_results", 20), maxPDFResults, "max_results")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			content, err := reader.ReadPDF(ctx, urlStr, request.GetString("pages", ""), request.GetString("query", ""), contextLines, maxResults)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(content), nil
		})

		// Drive the stdio server with the signal-aware context so SIGINT/SIGTERM
		// trigger a clean shutdown.
		stdio := server.NewStdioServer(s)
		if err := stdio.Listen(ctx, os.Stdin, os.Stdout); err != nil && ctx.Err() == nil {
			return err
		}
		return nil
	},
}

func clampPDFNumber(value float64, maximum int, name string) (int, error) {
	if value < 0 {
		return 0, fmt.Errorf("%s must not be negative, got %v", name, value)
	}
	if value > float64(maximum) {
		return maximum, nil
	}
	return int(value), nil
}
