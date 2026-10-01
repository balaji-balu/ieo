package lo

import (
	"context"
	"log"
	"net"
	"time"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"		

	//"github.com/balaji-balu/ieo/internal/lo/reconciler"
	"github.com/balaji-balu/ieo/pkg/model"
	"github.com/balaji-balu/ieo/internal/lo/logger"
)

type NetworkChangePayload struct {
	OldMode string
	NewMode string
}

type NetworkAdapt struct {
	
}

func networkStable() bool {
	conn, err := net.DialTimeout("tcp", "github.com:443", 2*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func (l *LocalOrchestrator) DetectMode() string {
	// if networkStable() {
	//     return "pushpreferred"
	// }
	// if time.Since(l.Journal.LastSuccess).Hours() > 2 {
	//     return "offline"
	// }
	return "adaptive"
}

func (l *LocalOrchestrator) StartNetworkMonitor(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	currentMode := l.currentMode

	for {
		select {
		case <-ticker.C:
			newMode := l.DetectMode()
			if newMode != currentMode {
				payload := NetworkChangePayload{OldMode: currentMode, NewMode: newMode}
				l.TriggerEvent(ctx, EventNetworkChange, payload)
				currentMode = newMode
				l.currentMode = newMode
			}

		case <-ctx.Done():
			return
		}
	}
}

func (l *LocalOrchestrator) handleNetworkChange(ctx context.Context, cfg LoConfig, data NetworkChangePayload) {
	logger.Info("Network mode change detected",
		zap.String("old_mode", data.OldMode),
		zap.String("new_mode", data.NewMode),
	)

	// Cancel any existing process (pull/push/offline)
	if l.cancelFunc != nil {
		l.cancelFunc()
		logger.Info("Stopped previous mode process", zap.String("mode", data.OldMode))
	}

	// Start the new mode
	ctxNew, cancel := context.WithCancel(ctx)
	l.cancelFunc = cancel

	switch data.NewMode {
	case "adaptive":
		go l.StartPullMode(ctxNew, cfg)
	case "pushpreferred":
		go l.StartPushMode(ctxNew, cfg)
	case "offline":
		go l.StartOfflineMode(ctxNew, cfg)
	default:
		logger.Warn("Unknown mode", zap.String("mode", data.NewMode))
	}
}

func (l *LocalOrchestrator) StartEventDispatcher(ctx context.Context) {
	logger.Info("Starting FSM event dispatcher...")
	for {
		select {
		case <-ctx.Done():
			logger.Info("Event dispatcher stopped")
			return
		case ev := <-l.eventCh:
			logger.Info("Processing FSM event", zap.Any("event", ev))

			switch ev.Name {
			case EventGitPolled:
				if data, ok := ev.Data.(GitPolledPayload); ok {
					l.handleGitPolled(data)
				}
			case EventNetworkChange:
				if data, ok := ev.Data.(NetworkChangePayload); ok {
					l.handleNetworkChange(ctx, l.Config, data)
				}
			}

			// if err := l.FSM.Event(ctx, ev.Name); err != nil {
			// 	logger.Error("FSM event failed",
			// 		zap.Any("event", ev),
			// 		zap.String("state", l.FSM.Current()),
			// 		zap.Error(err))
			// }
		}
	}
}

func (l *LocalOrchestrator) TriggerEvent(ctx context.Context, name string, data interface{}) {
	ev := Event{Name: name, Data: data, Time: time.Now()}

	select {
	case l.eventCh <- ev:
		logger.Info("Queued FSM event", zap.String("event", name))
	default:
		logger.Warn("Event queue full, dropping event", zap.String("event", name))
	}
}

func (l *LocalOrchestrator) handleGitPolled(data GitPolledPayload) {
	logger.Info("Handling GitPolled",
		zap.String("commit", data.Commit),
		zap.Int("deployments", len(data.Deployments)),
	)

	//ctx := context.Background()
	for _, d := range data.Deployments {
		logger.Info("Deploying", zap.String("deployment_id", d.DeploymentID))
		var dep model.ApplicationDeployment
		if err := yaml.Unmarshal([]byte(d.Content), &dep); err != nil {
			logger.Error("Failed to unmarshal deployment YAML", zap.Error(err))
			continue
		}

		app := model.App{
			DepType: dep.Spec.DeploymentProfile.Type,
			ID: dep.Metadata.Annotations.ApplicationID,
			Version: dep.Metadata.Annotations.Version,
			Components: make(map[string]model.Component),
		}
		for _, c := range dep.Spec.DeploymentProfile.Components {
			log.Println("comp", c)
			comp := model.Component{
				Name: c.Name,
				Version: c.Properties.Revision,
				Repository: c.Properties.Repository,
				PackageURL: c.Properties.PackageURL,
				KeyURL: c.Properties.KeyURL, 
			}
			app.Components[c.Name] = comp
		}
		depId := dep.Metadata.Annotations.ID
		l.store.SetDesired(depId, app)


		// Call reconciler here
		if err := l.reconcile.ReconcileMulti(depId); err != nil {
			logger.Info("Reconcilemulti failed:", zap.Error(err))
    		log.Fatal(err)
		}
		//l.DeployToEdges(d.DeploymentID, dep)
	}
}