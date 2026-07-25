package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/denysvitali/search-mcp/internal/provider"
	_ "github.com/denysvitali/search-mcp/internal/provider/brave"
	_ "github.com/denysvitali/search-mcp/internal/provider/duckduckgo"
	_ "github.com/denysvitali/search-mcp/internal/provider/exa"
	_ "github.com/denysvitali/search-mcp/internal/provider/kagi"
	_ "github.com/denysvitali/search-mcp/internal/provider/marginalia"
	_ "github.com/denysvitali/search-mcp/internal/provider/mojeek"
	_ "github.com/denysvitali/search-mcp/internal/provider/searxng"
	_ "github.com/denysvitali/search-mcp/internal/provider/tavily"
	_ "github.com/denysvitali/search-mcp/internal/provider/yahoo"
	"github.com/denysvitali/search-mcp/internal/reader"
	"github.com/denysvitali/search-mcp/internal/resilience"
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
		// Errors are printed once by Execute; without these cobra prints them a
		// second time and appends the full usage block to every failed search.
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if showVersion, _ := cmd.Flags().GetBool("version"); showVersion {
				fmt.Fprintln(cmd.OutOrStdout(), version)
				os.Exit(0)
			}
			reader.SetPageCacheTTL(viper.GetDuration("web_cache_ttl"))
			reader.SetPageCacheDir(viper.GetString("web_cache_dir"))
			reader.SetDomainPolicy(viper.GetStringSlice("allow_domains"), viper.GetStringSlice("block_domains"))
			return nil
		},
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
	rootCmd.PersistentFlags().String("provider", "", "search provider for a single query (\"all\" fans out; empty fans out too)")
	rootCmd.PersistentFlags().StringSlice("providers", defaultKeylessProviders, "keyless providers to enable (keyed providers are enabled by their API key)")
	rootCmd.PersistentFlags().String("brave-api-key", "", "Brave Search API key")
	rootCmd.PersistentFlags().String("brave-endpoint", "", "Brave Search API endpoint")
	rootCmd.PersistentFlags().String("kagi-api-key", "", "Kagi Search API key")
	rootCmd.PersistentFlags().String("kagi-endpoint", "", "Kagi Search API endpoint")
	rootCmd.PersistentFlags().String("exa-api-key", "", "Exa Search API key")
	rootCmd.PersistentFlags().String("exa-endpoint", "", "Exa Search API endpoint")
	rootCmd.PersistentFlags().String("tavily-api-key", "", "Tavily Search API key")
	rootCmd.PersistentFlags().String("tavily-endpoint", "", "Tavily Search API endpoint")
	rootCmd.PersistentFlags().String("duckduckgo-endpoint", "", "DuckDuckGo HTML search endpoint")
	rootCmd.PersistentFlags().String("marginalia-endpoint", "", "Marginalia public search API endpoint")
	rootCmd.PersistentFlags().String("mojeek-endpoint", "", "Mojeek search HTML endpoint")
	rootCmd.PersistentFlags().String("yahoo-endpoint", "", "Yahoo search HTML endpoint")
	rootCmd.PersistentFlags().String("searxng-url", "", "SearXNG instance URL (enables the searxng provider)")
	rootCmd.PersistentFlags().Float64("rate-rps", 1, "requests per second per provider")
	rootCmd.PersistentFlags().Int("rate-burst", 2, "rate limit burst per provider")
	rootCmd.PersistentFlags().Int("retry-max-attempts", 3, "max attempts per provider on transient failures")
	rootCmd.PersistentFlags().Duration("retry-base-delay", 200*time.Millisecond, "base delay for retry exponential backoff")
	rootCmd.PersistentFlags().Int("breaker-threshold", 5, "consecutive failures before a provider's circuit opens")
	rootCmd.PersistentFlags().Duration("breaker-cooldown", 30*time.Second, "open-circuit cooldown before a half-open trial")
	rootCmd.PersistentFlags().Duration("cache-ttl", 5*time.Minute, "in-memory result cache TTL (0 disables caching)")
	rootCmd.PersistentFlags().Duration("web-cache-ttl", 15*time.Minute, "in-memory web_read page cache TTL (0 disables caching)")
	rootCmd.PersistentFlags().String("web-cache-dir", "", "directory for the persistent web_read page cache (empty disables persistence)")
	rootCmd.PersistentFlags().StringSlice("allow-domains", nil, "if set, web_read may only fetch these domains (and their subdomains)")
	rootCmd.PersistentFlags().StringSlice("block-domains", nil, "domains (and their subdomains) web_read must never fetch")
	rootCmd.PersistentFlags().Bool("otel", false, "enable stdout OpenTelemetry traces and metrics")
	rootCmd.PersistentFlags().String("otel-exporter", "stdout", "OpenTelemetry exporter: stdout or otlp")
	rootCmd.PersistentFlags().String("otel-endpoint", "", "OTLP exporter endpoint (overrides OTEL_EXPORTER_OTLP_ENDPOINT)")
	rootCmd.PersistentFlags().String("log-level", "info", "log level")
	rootCmd.PersistentFlags().Bool("version", false, "print version and exit")

	_ = viper.BindPFlag("config", rootCmd.PersistentFlags().Lookup("config"))
	_ = viper.BindPFlag("provider", rootCmd.PersistentFlags().Lookup("provider"))
	_ = viper.BindPFlag("providers", rootCmd.PersistentFlags().Lookup("providers"))
	_ = viper.BindPFlag("brave_api_key", rootCmd.PersistentFlags().Lookup("brave-api-key"))
	_ = viper.BindPFlag("brave_endpoint", rootCmd.PersistentFlags().Lookup("brave-endpoint"))
	_ = viper.BindPFlag("kagi_api_key", rootCmd.PersistentFlags().Lookup("kagi-api-key"))
	_ = viper.BindPFlag("kagi_endpoint", rootCmd.PersistentFlags().Lookup("kagi-endpoint"))
	_ = viper.BindPFlag("exa_api_key", rootCmd.PersistentFlags().Lookup("exa-api-key"))
	_ = viper.BindPFlag("exa_endpoint", rootCmd.PersistentFlags().Lookup("exa-endpoint"))
	_ = viper.BindPFlag("tavily_api_key", rootCmd.PersistentFlags().Lookup("tavily-api-key"))
	_ = viper.BindPFlag("tavily_endpoint", rootCmd.PersistentFlags().Lookup("tavily-endpoint"))
	_ = viper.BindPFlag("duckduckgo_endpoint", rootCmd.PersistentFlags().Lookup("duckduckgo-endpoint"))
	_ = viper.BindPFlag("marginalia_endpoint", rootCmd.PersistentFlags().Lookup("marginalia-endpoint"))
	_ = viper.BindPFlag("mojeek_endpoint", rootCmd.PersistentFlags().Lookup("mojeek-endpoint"))
	_ = viper.BindPFlag("yahoo_endpoint", rootCmd.PersistentFlags().Lookup("yahoo-endpoint"))
	_ = viper.BindPFlag("searxng_url", rootCmd.PersistentFlags().Lookup("searxng-url"))
	_ = viper.BindPFlag("rate_rps", rootCmd.PersistentFlags().Lookup("rate-rps"))
	_ = viper.BindPFlag("rate_burst", rootCmd.PersistentFlags().Lookup("rate-burst"))
	_ = viper.BindPFlag("retry_max_attempts", rootCmd.PersistentFlags().Lookup("retry-max-attempts"))
	_ = viper.BindPFlag("retry_base_delay", rootCmd.PersistentFlags().Lookup("retry-base-delay"))
	_ = viper.BindPFlag("breaker_threshold", rootCmd.PersistentFlags().Lookup("breaker-threshold"))
	_ = viper.BindPFlag("breaker_cooldown", rootCmd.PersistentFlags().Lookup("breaker-cooldown"))
	_ = viper.BindPFlag("cache_ttl", rootCmd.PersistentFlags().Lookup("cache-ttl"))
	_ = viper.BindPFlag("web_cache_ttl", rootCmd.PersistentFlags().Lookup("web-cache-ttl"))
	_ = viper.BindPFlag("web_cache_dir", rootCmd.PersistentFlags().Lookup("web-cache-dir"))
	_ = viper.BindPFlag("allow_domains", rootCmd.PersistentFlags().Lookup("allow-domains"))
	_ = viper.BindPFlag("block_domains", rootCmd.PersistentFlags().Lookup("block-domains"))
	_ = viper.BindPFlag("otel", rootCmd.PersistentFlags().Lookup("otel"))
	_ = viper.BindPFlag("otel_exporter", rootCmd.PersistentFlags().Lookup("otel-exporter"))
	_ = viper.BindPFlag("otel_endpoint", rootCmd.PersistentFlags().Lookup("otel-endpoint"))
	_ = viper.BindPFlag("log_level", rootCmd.PersistentFlags().Lookup("log-level"))

	viper.SetEnvPrefix("SEARCH_MCP")
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	viper.AutomaticEnv()

	rootCmd.AddCommand(searchCmd)
	rootCmd.AddCommand(readCmd)
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
		// Only look in the dedicated search-mcp config dir. We
		// intentionally skip "." so running from inside another project
		// (e.g. one with its own search-mcp.yaml) does not silently shadow
		// the global config. Use --config to point at a project-local file.
		viper.AddConfigPath("$HOME/.config/search-mcp")
	}
	if err := viper.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) {
			// Surface real problems (e.g. malformed YAML, unreadable file).
			fmt.Fprintf(os.Stderr, "error: reading config file: %v\n", err)
		}
	}
}

func newLogger() logrus.FieldLogger {
	logger := logrus.New()
	logger.SetOutput(os.Stderr)
	logLevel := viper.GetString("log_level")
	level, err := logrus.ParseLevel(logLevel)
	if err != nil {
		level = logrus.InfoLevel
		logger.SetLevel(level)
		logger.WithError(err).Warnf("invalid log_level %q, defaulting to %q", logLevel, level)
		return logger
	}
	logger.SetLevel(level)
	return logger
}

// defaultKeylessProviders is the no-API-key provider set enabled out of the box.
//
// Mojeek is deliberately absent: it answers a datacenter IP with an HTTP 200
// captcha page regardless of User-Agent, so leaving it on costs a wasted round
// trip on every search while contributing nothing. It stays available via
// --providers for anyone searching from an IP it does serve.
var defaultKeylessProviders = []string{"duckduckgo", "marginalia", "yahoo"}

// keylessProviderSet resolves the --providers list into a lookup set.
//
// Entries are split on commas as well as on slice boundaries: viper hands a
// SEARCH_MCP_PROVIDERS env var through as a single string, so "a,b" would
// otherwise arrive as one unmatchable name.
func keylessProviderSet() map[string]bool {
	names := viper.GetStringSlice("providers")
	if len(names) == 0 {
		names = defaultKeylessProviders
	}
	set := make(map[string]bool, len(names))
	for _, entry := range names {
		for name := range strings.SplitSeq(entry, ",") {
			if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
				set[name] = true
			}
		}
	}
	return set
}

func newSearchService(logger logrus.FieldLogger) (*search.Service, error) {
	resilienceCfg := resilience.Config{
		RetryMaxAttempts: viper.GetInt("retry_max_attempts"),
		RetryBaseDelay:   viper.GetDuration("retry_base_delay"),
		BreakerThreshold: viper.GetInt("breaker_threshold"),
		BreakerCooldown:  viper.GetDuration("breaker_cooldown"),
		CacheTTL:         viper.GetDuration("cache_ttl"),
	}

	enabledKeyless := keylessProviderSet()
	configured := []struct {
		name, key, endpoint string
		enabled             bool
	}{
		{name: "duckduckgo", endpoint: viper.GetString("duckduckgo_endpoint"), enabled: enabledKeyless["duckduckgo"]},
		{name: "marginalia", endpoint: viper.GetString("marginalia_endpoint"), enabled: enabledKeyless["marginalia"]},
		{name: "mojeek", endpoint: viper.GetString("mojeek_endpoint"), enabled: enabledKeyless["mojeek"]},
		{name: "yahoo", endpoint: viper.GetString("yahoo_endpoint"), enabled: enabledKeyless["yahoo"]},
		{name: "brave", key: viper.GetString("brave_api_key"), endpoint: viper.GetString("brave_endpoint"), enabled: viper.GetString("brave_api_key") != ""},
		{name: "searxng", endpoint: viper.GetString("searxng_url"), enabled: viper.GetString("searxng_url") != ""},
		{name: "kagi", key: viper.GetString("kagi_api_key"), endpoint: viper.GetString("kagi_endpoint"), enabled: viper.GetString("kagi_api_key") != ""},
		{name: "exa", key: viper.GetString("exa_api_key"), endpoint: viper.GetString("exa_endpoint"), enabled: viper.GetString("exa_api_key") != ""},
		{name: "tavily", key: viper.GetString("tavily_api_key"), endpoint: viper.GetString("tavily_endpoint"), enabled: viper.GetString("tavily_api_key") != ""},
	}
	providers := make([]search.Provider, 0, len(configured))
	for _, cfg := range configured {
		if !cfg.enabled {
			continue
		}
		p, err := provider.New(cfg.name, cfg.key, cfg.endpoint)
		if err != nil {
			return nil, fmt.Errorf("%s provider: %w", cfg.name, err)
		}
		providers = append(providers, resilience.Wrap(p, resilienceCfg))
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
