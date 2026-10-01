package cmd

import (
	"context"
	"fmt"
	"encoding/json"
	"sync"
	"github.com/spf13/cobra"
	"github.com/balaji-balu/ieo/cmd/edgectl/internal/co"
	"github.com/balaji-balu/ieo/cmd/edgectl/internal/util"
)

func newCOCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "co",
		Short: "Control Orchestrator operations",
	}

	cmd.AddCommand(
		newCOListCmd(),
		newCODeployCmd(),
		newCORolloutCmd(),
		newCOStatusCmd(),
		newCOAddAppCmd(),
		newCODeleteAppCmd(),
		newCODeploymentsCmd(),
	)

	return cmd
}

func newCODeleteAppCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "app delete",
		Short: "Delete application in the Coordinator",
		RunE: func(cmd *cobra.Command, args []string) (error) {
			fmt.Println("Deleting application ...")
           	name, _ := cmd.Flags().GetString("name")
            //vendor, _ := cmd.Flags().GetString("vendor")
            artifact, _ := cmd.Flags().GetString("artifact")

            if name == "" {
                return fmt.Errorf("⚠️  Please provide --name ")
            }

			sel, err := util.ParseAppSelector(name)
			if err != nil {
				return err
			}			

			// only enforce version when adding
			if sel.Version == "" || sel.App == "" || sel.Category == "" {
				return fmt.Errorf("Deleting an app requires category/app/version")
			}

            cfg, err := util.Load()
            if err != nil {
                return fmt.Errorf("⚠️  Failed to load config:", err)
            }

            client := co.NewClient(cfg.Coordinator.URL)
            if err := client.DeleteApp(
				sel.Category, sel.App, sel.Version, artifact,
			); err != nil {
                return fmt.Errorf("❌ Failed to delete app:", err)
            }

            fmt.Println("✅ Application deleted successfully!")
			return nil
		},
	}

	// Add flags for the command
	cmd.PersistentFlags().String("name", "", "Application name")

	return cmd
}
func newCOAddAppCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add app",
		Short: "Register a new application in the Coordinator",
		RunE: func(cmd *cobra.Command, args []string) (error) {
			fmt.Println("Adding new application ...")
           	name, _ := cmd.Flags().GetString("name")
            //vendor, _ := cmd.Flags().GetString("vendor")
            artifact, _ := cmd.Flags().GetString("artifact")

            if name == "" || artifact == "" {
                return fmt.Errorf("⚠️  Please provide --name and --artifact")
            }

			sel, err := util.ParseAppSelector(name)
			if err != nil {
				return err
			}			

			// only enforce version when adding
			if sel.Version == "" {
				return fmt.Errorf("adding an app requires category/app/version")
			}

            cfg, err := util.Load()
            if err != nil {
                return fmt.Errorf("⚠️  Failed to load config:", err)
            }

            client := co.NewClient(cfg.Coordinator.URL)
            if err := client.AddApp(
				sel.Category, sel.App, sel.Version, artifact,
			); err != nil {
                return fmt.Errorf("❌ Failed to add app:", err)
            }

            fmt.Println("✅ Application added successfully!")
			return nil
		},
	}

	// Add flags for the command
	cmd.PersistentFlags().String("name", "", "Application name")
	//cmd.PersistentFlags().String("vendor", "", "Vendor name")
	cmd.PersistentFlags().String("artifact", "", "Artifact URL (OCI or Docker)")	

	return cmd
}

func newCOListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List entities (apps, sites, nodes, deployments)",
	}

	cmd.AddCommand(
		&cobra.Command{
			Use:   "apps",
			Short: "List available applications (--name category/App/Version)",
			Run: func(cmd *cobra.Command, args []string) {
				fmt.Println("Listing apps ...")
           		name, _ := cmd.Flags().GetString("name")
		
				cfg, err := util.Load()
				if err != nil {
					return //fmt.Errorf("failed to load config: %v", err)
				}
				// Trigger 
				client := co.NewClient(cfg.Coordinator.URL) // or from config

				sel, err := util.ParseAppSelector(name)

				apps, err := client.ListApps(sel.Category, sel.App, sel.Version)
				if err != nil {
					fmt.Println(err)
					return //fmt.Errorf("failed to trigger: %w", err)
				}

				// Display results
				if len(apps) == 0 {
					fmt.Println("No applications found.")
					return
				}

				fmt.Println("✅ Available applications:")
				for i, app := range apps {
					fmt.Printf("%2d. %-20s %-10s %-15s %s\n",
						i+1, app.Name, app.Version, app.Category, app.ID)
				}			
			},
		},
		&cobra.Command{
			Use:   "sites",
			Short: "List registered sites",
			Run: func(cmd *cobra.Command, args []string) {
				fmt.Println("Listing sites ...")
			},
		},
	)
		// Add flags for the command
	cmd.PersistentFlags().String("name", "", "category/app/version")

	return cmd
}

func newCODeployCmd() *cobra.Command {
	var site, app, deploytype string
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Trigger a new deployment",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("Deploying app %s to site %s\n", app, site)

			cfg, err := util.Load()
			if err != nil {
				return fmt.Errorf("failed to load config: %v", err)
			}

			sel, err := util.ParseAppSelector(app)

			// Trigger 
			client := co.NewClient(cfg.Coordinator.URL) // or from config
			resp, err := client.Deploy(site, sel.Category,sel.App, sel.Version, deploytype)
			if err != nil {
				return fmt.Errorf("failed to trigger deployment: %w", err)
			}

			fmt.Printf("✅ Deployment started for %d targets\n", len(resp.DeploymentIDs))

			var wg sync.WaitGroup
			done := make(chan struct{})

			ctx, cancel := context.WithCancel(context.Background())
			for _, depID := range resp.DeploymentIDs {
				wg.Add(1)
				go func(depID string) {
					defer wg.Done()

					err := client.StreamStatus(ctx, depID, func(ev co.DeployEvent) {
						fmt.Printf("→ [%s] %d%% %s\n", depID, ev.Progress, ev.Message)

						switch ev.Status {
						case "in-progress":
							fmt.Printf("⏳ [%s] %s\n", depID, ev.Message)
						case "completed":
							fmt.Printf("✅ [%s] Completed\n", depID)
							// End stream for this deployment
							client.StopStream(cancel)
						case "failed":
							fmt.Printf("❌ [%s] Failed: %s\n", depID, ev.Message)
							client.StopStream(cancel)
						default:
							fmt.Printf("📡 [%s] %s\n", depID, ev.Message)
						}
					})
					if err != nil {
						fmt.Printf("⚠️  stream error for %s: %v\n", depID, err)
					}
				}(depID)
			}

			// Wait for all streams to complete
			go func() {
				wg.Wait()
				close(done)
			}()

			<-done // block until all completed or failed
			fmt.Println("🏁 All deployments finished.")
			return nil
		},
	}
	cmd.Flags().StringVar(&site, "site", "", "Target site")
	cmd.Flags().StringVar(&app, "app", "", "Application name")
	cmd.Flags().StringVar(&deploytype, "deploytype", "", "Deployment type")
	_ = cmd.MarkFlagRequired("site")
	_ = cmd.MarkFlagRequired("app")
	_ = cmd.MarkFlagRequired("deploytype")
	return cmd
}

func newCORolloutCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rollout",
		Short: "Manage deployment rollouts",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "status [deployment-id]",
		Args:  cobra.ExactArgs(1),
		Short: "Show rollout status",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("Checking rollout status for %s\n", args[0])
		},
	})

	return cmd
}

func newCOStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show CO service health",
		RunE: func(cmd *cobra.Command, args []string) error {

			cfg, err := util.Load()
			if err != nil {
				return fmt.Errorf("failed to load config: %v", err)
			}			
			client := co.NewClient(cfg.Coordinator.URL)
			//co := client.New(url)
			h, err := client.Health()
			if err != nil {
				return fmt.Errorf("❌ %v", err)
			}
			fmt.Printf("✅ CO service healthy: %s\n", h.Status)
			return nil
		},	
	}
}

func newCODeploymentsCmd() *cobra.Command {
    cmd := &cobra.Command{
        Use:   "deployments",
        Short: "Manage deployments",
    }

    cmd.AddCommand(newCODeploymentsListCmd())
    cmd.AddCommand(newCODeploymentStatusCmd())

    return cmd
}

func newCODeploymentsListCmd() *cobra.Command {
    return &cobra.Command{
        Use:   "list",
        Short: "Show all deployment statuses",
        RunE: func(cmd *cobra.Command, args []string) error {
            cfg, err := util.Load()
            if err != nil {
                return fmt.Errorf("failed to load config: %v", err)
            }

			fmt.Println(cfg)
            // client := co.NewClient(cfg.Coordinator.URL)
            // deployments, err := client.Deployments()
            // if err != nil {
            //     return fmt.Errorf("❌ failed to fetch deployments: %v", err)
            // }

            // fmt.Println("📦 Deployments:")
            // for _, d := range deployments {
            //     fmt.Printf("- %s (%s)\n", d.ID, d.State)
            // }

            return nil
        },
    }
}

func newCODeploymentStatusCmd() *cobra.Command {
    var depID string

    cmd := &cobra.Command{
        Use:   "status",
        Short: "Show the deployment status for a specific deployment",
        RunE: func(cmd *cobra.Command, args []string) error {

            cfg, err := util.Load()
            if err != nil {
                return fmt.Errorf("failed to load config: %v", err)
            }

            client := co.NewClient(cfg.Coordinator.URL)
            resp, err := client.DeploymentStatus(depID)
            if err != nil {
                return fmt.Errorf("❌ %v", err)
            }
			fmt.Println(pretty(resp))

            // fmt.Printf("📦 Deployment: %s\n", depID)
            // fmt.Printf("State: %s\n", resp.State)
            // fmt.Printf("Error: %s\n", resp.ErrorMessage)

            // fmt.Println("🧩 Components:")
            // for _, c := range resp.Components {
            //     fmt.Printf("- %s : %s (%s)\n", c.Name, c.State, c.ErrorMessage)
            // }

            return nil
        },
    }

    cmd.Flags().StringVar(&depID, "depid", "", "Deployment ID")
    cmd.MarkFlagRequired("depid")

    return cmd
}

func pretty(v interface{}) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

