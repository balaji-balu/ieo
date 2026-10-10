// cmd/root.go
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	output  string
	verbose bool
	site    string
	node    string
)

// NewRootCmd builds the CLI tree
func NewRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "edgectl",
		Short: "Unified CLI for Edge Orchestration Platform",
	}

	rootCmd.PersistentFlags().StringVarP(&output, "output", "o", "table", "Output format (table|json|yaml)")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose logging")
	rootCmd.PersistentFlags().StringVar(&site, "site", "", "Site scope override")
	rootCmd.PersistentFlags().StringVar(&node, "node", "", "Node scope override")

	rootCmd.AddCommand(
		newInitCmd(),
		newCOCmd(),
		newLOCmd(),
		newENCmd(),
		newConfigCmd(),
		newVersionCmd(),
	)

	return rootCmd
}

func Execute() {
	if err := NewRootCmd().Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
