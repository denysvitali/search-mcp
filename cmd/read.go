package cmd

import (
	"fmt"

	"github.com/denysvitali/search-mcp/internal/reader"
	"github.com/spf13/cobra"
)

var readCmd = &cobra.Command{
	Use:   "read URL",
	Short: "Fetch a URL and print its content as Markdown",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		content, err := reader.Read(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), content)
		return nil
	},
}
