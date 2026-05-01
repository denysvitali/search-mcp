package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/denysvitali/search-mcp/internal/provider"
	"github.com/denysvitali/search-mcp/internal/search"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	version = "dev"
	rootCmd = &cobra.Command{
		Use:   "search-mcp",
		Short: "Search the web from CLI or MCP",
	}
)

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().String("config", "", "config file")
	rootCmd.PersistentFlags().String("provider", "", "search provider")
	rootCmd.PersistentFlags().String("brave-api-key", "", "Brave Search API key")
	rootCmd.PersistentFlags().String("brave-endpoint", "", "Brave Search API endpoint")
	rootCmd.PersistentFlags().String("duckduckgo-endpoint", "", "DuckDuckGo Instant Answer API endpoint")
	rootCmd.PersistentFlags().Float64("rate-rps", 1, "requests per second per provider")
	rootCmd.PersistentFlags().Int("rate-burst", 2, "rate limit burst per provider")
	rootCmd.PersistentFlags().Bool("otel", false, "enable stdout OpenTelemetry traces and metrics")
	rootCmd.PersistentFlags().String("otel-exporter", "stdout", "OpenTelemetry exporter: stdout or otlp")
	rootCmd.PersistentFlags().String("log-level", "info", "log level")

	_ = viper.BindPFlag("config", rootCmd.PersistentFlags().Lookup("config"))
	_ = viper.BindPFlag("provider", rootCmd.PersistentFlags().Lookup("provider"))
	_ = viper.BindPFlag("brave_api_key", rootCmd.PersistentFlags().Lookup("brave-api-key"))
	_ = viper.BindPFlag("brave_endpoint", rootCmd.PersistentFlags().Lookup("brave-endpoint"))
	_ = viper.BindPFlag("duckduckgo_endpoint", rootCmd.PersistentFlags().Lookup("duckduckgo-endpoint"))
	_ = viper.BindPFlag("rate_rps", rootCmd.PersistentFlags().Lookup("rate-rps"))
	_ = viper.BindPFlag("rate_burst", rootCmd.PersistentFlags().Lookup("rate-burst"))
	_ = viper.BindPFlag("otel", rootCmd.PersistentFlags().Lookup("otel"))
	_ = viper.BindPFlag("otel_exporter", rootCmd.PersistentFlags().Lookup("otel-exporter"))
	_ = viper.BindPFlag("log_level", rootCmd.PersistentFlags().Lookup("log-level"))

	viper.SetEnvPrefix("SEARCH_MCP")
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	viper.AutomaticEnv()

	rootCmd.AddCommand(searchCmd)
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintln(cmd.OutOrStdout(), version)
		},
	})
}

func initConfig() {
	if cfg := viper.GetString("config"); cfg != "" {
		viper.SetConfigFile(cfg)
	} else {
		viper.SetConfigName("search-mcp")
		viper.SetConfigType("yaml")
		viper.AddConfigPath(".")
		viper.AddConfigPath("$HOME/.config/search-mcp")
	}
	_ = viper.ReadInConfig()
}

func newLogger() logrus.FieldLogger {
	logger := logrus.New()
	level, err := logrus.ParseLevel(viper.GetString("log_level"))
	if err != nil {
		level = logrus.InfoLevel
	}
	logger.SetLevel(level)
	logger.SetOutput(os.Stderr)
	return logger
}

func newSearchService(logger logrus.FieldLogger) (*search.Service, error) {
	providers := []search.Provider{provider.NewDuckDuckGo(viper.GetString("duckduckgo_endpoint"))}
	if viper.GetString("brave_api_key") != "" {
		providers = append(providers, provider.NewBrave(viper.GetString("brave_api_key"), viper.GetString("brave_endpoint")))
	}
	return search.NewService(providers, viper.GetFloat64("rate_rps"), viper.GetInt("rate_burst"), logger)
}

func renderResults(resp search.Response) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	urlStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("36"))
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n\n", titleStyle.Render(resp.Provider), mutedStyle.Render(resp.Query))
	for i, result := range resp.Results {
		fmt.Fprintf(&b, "%d. %s\n", i+1, titleStyle.Render(result.Title))
		if result.URL != "" {
			fmt.Fprintf(&b, "   %s\n", urlStyle.Render(result.URL))
		}
		if result.Description != "" {
			fmt.Fprintf(&b, "   %s\n", result.Description)
		}
	}
	if len(resp.Results) == 0 {
		fmt.Fprintf(&b, "%s\n", mutedStyle.Render("No results."))
	}
	return b.String()
}
