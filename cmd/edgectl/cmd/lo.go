package cmd

import (
	"fmt"
	"github.com/spf13/cobra"

	"github.com/balaji-balu/ieo/cmd/edgectl/internal/lo"
	"github.com/balaji-balu/ieo/cmd/edgectl/internal/util"
)

func newLOCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lo",
		Short: "Local Orchestrator operations",
	}

	cmd.AddCommand(
		&cobra.Command{
			Use:   "status",
			Short: "Show LO health, current commit, connected nodes",
			RunE: func(cmd *cobra.Command, args []string) error {

				cfg, err := util.Load()
				if err != nil {
					return fmt.Errorf("failed to load config: %v", err)
				}			
				client := lo.NewClient(cfg.LocalOrchestrator.URL)
				//co := client.New(url)
				h, err := client.Health()
				if err != nil {
					return fmt.Errorf("❌ %v", err)
				}
				fmt.Printf("✅ LO service healthy: %s\n", h.Status)
				return nil
			},	
		},
		&cobra.Command{
			Use:   "hosts",
			Short: "Show LO Hosts",
			RunE: func(cmd *cobra.Command, args []string) error {
				cfg, err := util.Load()
				if err != nil {
					return fmt.Errorf("failed to load config: %v", err)
				}				
				client := lo.NewClient(cfg.LocalOrchestrator.URL)
				if e := client.Hosts(); e != nil {
					fmt.Errorf("", e)
				}
				
				return nil
			},
		},
		&cobra.Command{
			Use:   "actual",
			Short: "Show LO Actual",
			RunE: func(cmd *cobra.Command, args []string) error {
				cfg, err := util.Load()
				if err != nil {
					return fmt.Errorf("failed to load config: %v", err)
				}				
				client := lo.NewClient(cfg.LocalOrchestrator.URL)
				if e := client.Actual(); e != nil {
					fmt.Errorf("", e)
				}
				
				return nil
			},
		},		
		&cobra.Command{
			Use:   "sync",
			Short: "Force sync with Git and CO",
			Run: func(cmd *cobra.Command, args []string) {
				fmt.Println("Syncing with Git and CO...")
			},
		},
	)
	return cmd
}
