package cmd

import (
	"context"
	"encoding/json"
	"os"

	"github.com/denysvitali/search-mcp/internal/observability"
	searchdomain "github.com/denysvitali/search-mcp/internal/search"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the MCP stdio server",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		shutdown, err := observability.Setup(ctx, observability.Config{
			Enabled:     viper.GetBool("otel"),
			ServiceName: "search-mcp",
			Exporter:    viper.GetString("otel_exporter"),
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

		tool := mcp.NewTool("search",
			mcp.WithDescription("Search the web using a configured provider."),
			mcp.WithString("query", mcp.Required(), mcp.Description("Search query")),
			mcp.WithString("provider", mcp.Description("Provider name: duckduckgo or brave")),
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
			resp, err := service.Search(ctx, searchdomain.Request{
				Query:      query,
				Provider:   request.GetString("provider", viper.GetString("provider")),
				Count:      int(request.GetFloat("count", float64(viper.GetInt("count")))),
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

		return server.ServeStdio(s)
	},
}
