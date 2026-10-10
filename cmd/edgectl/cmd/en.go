package cmd

import (
	"fmt"
	"github.com/spf13/cobra"

	"github.com/balaji-balu/ieo/cmd/edgectl/internal/lo"
	"github.com/balaji-balu/ieo/cmd/edgectl/internal/util"
)

func newENCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "en",
		Short: "Edge Node operations",
	}

	cmd.AddCommand(
		&cobra.Command{
			Use:   "status",
			Short: "Show node health, workloads, metrics",
			RunE: func(cmd *cobra.Command, args []string) error {

				cfg, err := util.Load()
				if err != nil {
					return fmt.Errorf("failed to load config: %v", err)
				}
		
				client := lo.NewClient(cfg.EdgeNode.URL)
				//co := client.New(url)
				h, err := client.Health()
				if err != nil {
					return fmt.Errorf("❌ %v", err)
				}
				fmt.Printf("✅ EN service healthy: %s\n", h.Status)
				return nil
			},
		},
		&cobra.Command{
			Use:   "metrics",
			Short: "Display node metrics",
			Run: func(cmd *cobra.Command, args []string) {
				fmt.Println("CPU: 12%, Mem: 230MB")
			},
		},
	)
	return cmd
}


