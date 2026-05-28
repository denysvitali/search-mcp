package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/denysvitali/search-mcp/internal/observability"
	"github.com/denysvitali/search-mcp/internal/reader"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// readTimeout bounds a single web_read fetch so a slow or hanging server
// cannot block the command forever.
const readTimeout = 30 * time.Second

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

		readCtx, cancel := context.WithTimeout(ctx, readTimeout)
		defer cancel()

		content, err := reader.Read(readCtx, args[0])
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), content)
		return nil
	},
}
