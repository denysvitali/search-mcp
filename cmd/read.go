package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/denysvitali/search-mcp/internal/observability"
	"github.com/denysvitali/search-mcp/internal/reader"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var readCmd = &cobra.Command{
	Use:   "read URL",
	Short: "Fetch a URL and print its content as Markdown",
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

		readCtx, cancel := withConfiguredTimeout(ctx, viper.GetDuration("read_timeout"))
		defer cancel()

		content, err := reader.Read(readCtx, args[0])
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), content)
		return nil
	},
}
