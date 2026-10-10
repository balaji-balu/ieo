package runtimemgr

import (
    "fmt"
    "go.uber.org/zap"

    "github.com/balaji-balu/ieo/pkg/model"
    "github.com/balaji-balu/ieo/pkg/en/edgeruntime"
    "github.com/balaji-balu/ieo/internal/natsbroker"
    "github.com/balaji-balu/ieo/internal/en/lifecycle"
    "github.com/balaji-balu/ieo/internal/en/reporter"
)

type Component struct {
	Name    string
	Version string
	Status  model.DeploymentStage
	Err     error
}

type App struct {
	ID         string
	Version    string
	Status     model.DeploymentStage
	Err        error
	Components map[string]*Component // keyed by component name
}

type RuntimeManager struct {
    lifecycle   *lifecycle.LifecycleController
    reporter    *reporter.StatusReporter
    log         *zap.SugaredLogger
    nb          *natsbroker.Broker
    SiteID      string
    HostID      string
    queue chan model.DiffOp
	apps map[string]*App
	
}

func NewRuntimeManager(nb *natsbroker.Broker, 
    siteID, hostID string,
    runtime string, 
    log *zap.SugaredLogger) *RuntimeManager {  
    //handler := EventHandler{}   
    rm := &RuntimeManager{
        SiteID: siteID,
        HostID: hostID,   
        log: log,
        nb: nb,
        queue:  make(chan model.DiffOp, 100),
		apps: make(map[string]*App),
    }   
    rm.lifecycle = lifecycle.NewLifecycleController(runtime, rm, log)
    rm.reporter = reporter.NewStatusReporter(runtime, log)

    rm.startWorker()
    return rm
}

func (rm *RuntimeManager) Deploy(c edgeruntime.ComponentSpec) error {
    rm.log.Infow("RuntimeManager: Deploy")
    return rm.lifecycle.Apply(c)
}

func (rm *RuntimeManager) GetStatus(name string) edgeruntime.ComponentStatus {
    return rm.reporter.Status(name)
}

func (rm *RuntimeManager) Stop(name string) error {
    return rm.lifecycle.Stop(name)
}

func (rm *RuntimeManager) Delete(name string) error {
    return rm.lifecycle.Delete(name)
}

func (rm *RuntimeManager) LoActionDispatcher(){
    go func() {
        subj := fmt.Sprintf("site.%s.deploy.%s", rm.SiteID, rm.HostID)
        rm.nb.Subscribe3(subj, func(req model.DiffOp) {
            rm.log.Infow("req received:", "req", req)

            rm.log.Infow("Received", "Deployment type", req.App.DepType)
			select {
			case rm.queue <- req:
				rm.log.Debugw("operation queued")
			default:
				rm.log.Warnw("queue full, dropping operation")
			}            
            //TBD: runtime must be "containerd". rest "not implemented"
            //rm.lifecycle.HandleAction(req)
        })
    }()
}

func (rm *RuntimeManager) startWorker() {
	go func() {
		for op := range rm.queue {
			rm.log.Infow("Processing queued operation", "op", op)

			if err := rm.lifecycle.HandleAction(op); err != nil {
				rm.log.Errorw("HandleAction failed", "err", err)
			}
		}
	}()
}

func (rm *RuntimeManager) OnEvent(
	op model.DiffOp,
	compName string,
	event model.DeploymentStage,
	err error,
) {
	rm.log.Debugw("OnEvent", "app", op.App.ID, "component", compName, "event", event)

	// ----- Get or create App -----
	app, ok := rm.apps[op.App.ID]
	if !ok {
		app = &App{
			ID:         op.App.ID,
			Version:    op.App.Version,
			Status:     model.StatePending,
			Components: make(map[string]*Component),
		}
		rm.apps[op.App.ID] = app
	}

	// ----- Get or create Component -----
	comp, ok := app.Components[compName]
	if !ok {
		comp = &Component{
			Name:    compName,
			Version: op.App.Version,
		}
		app.Components[compName] = comp
	}

	// ----- Update component state -----
	comp.Status = event
	comp.Err = err

	// ----- Build DeploymentStatus -----
	ds := model.DeploymentStatus{
		DeploymentID: op.DeploymentID,
		TimeStamp:    op.TimeStamp,
	}

	for _, c := range app.Components {
		var serr model.StatusError
		if c.Status == model.StateFailed && c.Err != nil {
			serr = model.StatusError{
				Code:    "DEPLOYMENT_FAILED",
				Message: c.Err.Error(),
			}
		}

		ds.Components = append(ds.Components, model.DeploymentComponent{
			Name:  c.Name,
			State: string(c.Status),
			Error: serr,
		})
	}

	// ----- Resolve overall app state -----
	overall := inheritState(ds.Components)
	app.Status = overall

	var overallErr model.StatusError
	if overall == model.StateFailed {
		for _, c := range ds.Components {
			if c.State == string(model.StateFailed) {
				overallErr = c.Error
				break
			}
		}
	}

	ds.Status = model.DeploymentState{
		State: string(overall),
		Error: overallErr,
	}

	// ----- Publish status -----
	rm.log.Debugw("OnEvent", "apps", rm.apps)
	rm.log.Debugw("OnEvent", "ds", ds)
	subj := fmt.Sprintf("status.%s.%s", rm.SiteID, rm.HostID)
	if err := rm.nb.Publish(subj, ds); err != nil {
		rm.log.Errorw("Status publish failed", "err", err)
	}
}

func inheritState(comps []model.DeploymentComponent) model.DeploymentStage {
	if len(comps) == 0 {
		return model.StatePending
	}

	overall := model.StateInstalled

	for _, c := range comps {
		switch c.State {
		case "failed":
			// Highest priority
			return model.StateFailed

		case "installing":
			// Only upgrade if overall is not Failed (we return Failed earlier)
			if overall != model.StateInstalling {
				overall = model.StateInstalling
			}

		case "pending":
			// Only set if nothing more important seen yet
			if overall == model.StateInstalled {
				overall = model.StatePending
			}

		case "installed":
			// Do nothing (lowest priority)
		}
	}

	return overall
}
