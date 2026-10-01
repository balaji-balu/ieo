package lo

import (
	"context"
	"go.uber.org/zap"
	"log"
	"time"
	//"errors"

	//. "github.com/balaji-balu/ieo/internal/config"
	"github.com/balaji-balu/ieo/internal/gitobserver"
	"github.com/balaji-balu/ieo/internal/natsbroker"
	"github.com/balaji-balu/ieo/internal/lo/watcher"
	"github.com/balaji-balu/ieo/internal/lo/logger"
	//"github.com/balaji-balu/ieo/internal/lo/nwadapt"
	//"github.com/balaji-balu/ieo/internal/lo"
)

// Handles PushPreferred mode
func (l *LocalOrchestrator) StartPushMode(ctx context.Context, cfg LoConfig) error {
	log.Println("🚀 Starting Push Mode (NATS subscribe to desiredstate.changed)")
	log.Println("cfg.NATS.URL", cfg.NatsUrl)
	//log.Println("cfg.Server.Site", cfgSite)

	b, err := natsbroker.New(cfg.NatsUrl)
	if err != nil {
		return err
	}

	// Subscribe to global topic
	err = b.Subscribe("git.desiredstate.changed", func(ev gitobserver.GitEvent) {
		logger.Info("Received push event", zap.Any("event", ev))

		if ev.Site != cfg.Site && ev.Site != "*" {
			logger.Error("Site mismatch",
				zap.String("expected", cfg.Site),
				zap.String("received", ev.Site))
			return
		}
		log.Printf("[LO-%s] Received push event: %v", cfg.Site, ev)
		// Trigger FSM event
		// if err := lo.FSM.Event(ctx, "git_update_received"); err != nil {
		//     lo.logger.Error("Failed to trigger FSM event", zap.Error(err))
		// }
		log.Println("✅ FSM event triggered")
		// ✅ Queue FSM event safely
		//go lo.TriggerEvent("git_update_received")
		//TBD: go l.FSM.Event(ctx, "git_update_received", nil)
	})
	if err != nil {
		logger.Error("Failed to subscribe to NATS", zap.Error(err))
		return err
	}

	// Block until canceled (Ctrl+C or mode switch)
	<-ctx.Done()
	b.Close()
	log.Println("🛑 Push Mode stopped gracefully")
	return nil
}

func (l *LocalOrchestrator) StartPullMode(ctx context.Context, cfg LoConfig) {
	logger.Info("📡 Starting Pull Mode (periodic git sync)")

	w := watcher.NewWatcher(l.Mgr, cfg.Repo, cfg.Site, 3*time.Second)
	//w.OnChange = lo.onDeployments

	//watcher := gitobserver.New(cfg.Repo, "main", 30*time.Second)
	w.OnChange = func(commit string, deployments []watcher.DeploymentChange) {
		log.Println("w.OnChange: Data received...", deployments)
		payload := GitPolledPayload{
			Commit:      commit,
			Deployments: deployments,
		}
		l.TriggerEvent(ctx, EventGitPolled, payload)
	}

	go func() {
		if err := w.Start(); err != nil {
			logger.Error("Watcher error", zap.Error(err))
		}
	}()

	<-ctx.Done()
	logger.Info("🛑 Stopping Git watcher...")
	w.Stop()
}

func (l *LocalOrchestrator) StartOfflineMode(ctx context.Context, cfg LoConfig) error {
	logger.Info("📡 Starting offline Mode")
	return nil
}

/*
// Handles AdaptivePull mode
func (lo *LocalOrchestrator) StartPullMode(ctx context.Context, cfg LoConfig) error {
	log.Println("📡 Starting Pull Mode (periodic git sync)")

    //LoConfig.Site = cfg.Site
    //LoConfig.NatsUrl = cfg.NatsUrl
    //LoConfig.Repo
	watcher := gitobserver.New(cfg.Repo, "main", 30*time.Second)
	watcher.OnChange = func(commit string, deployments []gitobserver.DeploymentChange) {
		log.Printf("💡 Git change detected: %s (%d deployments)\n", commit, len(deployments))
		lo.TriggerEvent(ctx, EventGitPolled)
	}

	go func() {
		if err := watcher.Start(cfg.Site); err != nil {
			log.Println("[PullMode] watcher error:", err)
		}
	}()

	<-ctx.Done()
	log.Println("🛑 Stopping Git watcher...")
	watcher.Stop()
	return nil
}
*/
/*

func (lo *LocalOrchestrator) StartPullMode(ctx context.Context, cfg LoConfig) error {
    log.Println("📡 Starting Pull Mode (periodic git sync)")

	watcher := gitobserver.New(repoURL, branch, 30*time.Second)
	watcher.OnChange = func(commit string) {
		lo.TriggerEvent(EventGitPolled)
	}

    go watcher.Start() // blocking loop inside goroutine

	<-ctx.Done()
	log.Info("Stopping Git watcher...")
	watcher.Stop()

    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            log.Println("🛑 Pull Mode stopped gracefully")
            return nil
        case <-ticker.C:
            log.Println("🔁 Checking for new desired state...")
            // TODO: implement git pull logic
            DesiredStateChangesForSite(cfg.Site,
                "https://github.com/edge-orchestration-platform/deployments")
            //lo.FSM.Event(ctx, "git_polled")
            // lo.TriggerEvent("git_polled")
            log.Println("✅ FSM event triggered by StartPullMode")
        }
    }

}
*/

// Handles OfflineDeterministic mode
func (lo *LocalOrchestrator) start_offline_mode(ctx context.Context) error {
	log.Println("📴 Starting Offline Mode (working from journal)")
	//TBD: lo.FSM.Event(ctx, "offline_mode_start")
	<-ctx.Done()
	log.Println("🛑 Offline Mode stopped gracefully")
	return nil
}
