package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/balaji-balu/ieo/cmd/edgectl/internal/initflow"
)

func newInitCmd() *cobra.Command{
	return initCmd
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize edge orchestration environment",
	Long: `Initializes the local edge orchestration environment.

This command installs and starts all required components:
- Central Orchestrator (CO)
- Local Orchestrator (LO)
- Edge Node agent (EN)

No configuration is required.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := initflow.NewContext()

		fmt.Println("▶ Initializing edge orchestration")

		if err := initflow.Run(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "✖ init failed: %v\n", err)
			os.Exit(1)
		}

		fmt.Println("✔ System is ready")
		return nil
	},
}

// func init() {
	
// 	rootCmd.AddCommand(initCmd)
// }
