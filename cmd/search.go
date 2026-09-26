package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/denysvitali/search-mcp/internal/observability"
	"github.com/denysvitali/search-mcp/internal/search"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var searchCmd = &cobra.Command{
	Use:   "search QUERY",
	Short: "Search the web",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
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
		count, err := clampCount(viper.GetInt("count"))
		if err != nil {
			return err
		}
		searchCtx, cancel := withConfiguredTimeout(ctx, viper.GetDuration("search_timeout"))
		defer cancel()
		resp, err := service.Search(searchCtx, search.Request{
			Query:          args[0],
			Provider:       viper.GetString("provider"),
			Count:          count,
			Country:        viper.GetString("country"),
			Language:       viper.GetString("language"),
			SafeSearch:     viper.GetString("safe_search"),
			Freshness:      viper.GetString("freshness"),
			IncludeDomains: viper.GetStringSlice("include_domains"),
			ExcludeDomains: viper.GetStringSlice("exclude_domains"),
			MaxPerHost:     viper.GetInt("max_per_host"),
		})
		if err != nil {
			return err
		}

		if viper.GetBool("json") {
			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetIndent("", "  ")
			return encoder.Encode(resp)
		}
		fmt.Fprint(cmd.OutOrStdout(), renderResults(resp))
		return nil
	},
}

func init() {
	searchCmd.Flags().Int("count", 10, "number of results")
	searchCmd.Flags().String("country", "", "provider country code")
	searchCmd.Flags().String("language", "", "provider language code")
	searchCmd.Flags().String("safe-search", "", "safe search mode")
	searchCmd.Flags().String("freshness", "", "freshness filter")
	searchCmd.Flags().Bool("json", false, "print JSON")
	searchCmd.Flags().StringSlice("include-domains", nil, "only return results from these domains and their subdomains")
	searchCmd.Flags().StringSlice("exclude-domains", nil, "exclude results from these domains and their subdomains")
	searchCmd.Flags().Int("max-per-host", 0, "maximum results per hostname (0 disables the limit)")

	_ = viper.BindPFlag("count", searchCmd.Flags().Lookup("count"))
	_ = viper.BindPFlag("country", searchCmd.Flags().Lookup("country"))
	_ = viper.BindPFlag("language", searchCmd.Flags().Lookup("language"))
	_ = viper.BindPFlag("safe_search", searchCmd.Flags().Lookup("safe-search"))
	_ = viper.BindPFlag("freshness", searchCmd.Flags().Lookup("freshness"))
	_ = viper.BindPFlag("json", searchCmd.Flags().Lookup("json"))
	_ = viper.BindPFlag("include_domains", searchCmd.Flags().Lookup("include-domains"))
	_ = viper.BindPFlag("exclude_domains", searchCmd.Flags().Lookup("exclude-domains"))
	_ = viper.BindPFlag("max_per_host", searchCmd.Flags().Lookup("max-per-host"))
}
