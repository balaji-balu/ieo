// cmd/config.go
package cmd

import (
	"fmt"
	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage CLI configuration",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "view",
			Short: "View current configuration",
			Run: func(cmd *cobra.Command, args []string) {
				fmt.Println("Showing config...")
			},
		},
	)
	return cmd
}
