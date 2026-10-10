package lifecycle

import (
    //"log"

    "go.uber.org/zap"


    "github.com/balaji-balu/ieo/internal/en/plugins"
    "github.com/balaji-balu/ieo/pkg/en/edgeruntime"
    "github.com/balaji-balu/ieo/pkg/model"
)

// type EventHandler interface {
// 	OnEvent(op model.DiffOp, event string)
// }

type LifecycleController struct {
    plugin edgeruntime.RuntimePlugin
    log *zap.SugaredLogger
    statusHandler  edgeruntime.EventHandler
}

func NewLifecycleController(runtime string, cb edgeruntime.EventHandler, log *zap.SugaredLogger) *LifecycleController {
    log.Infow("Runtime", "", runtime)
    return &LifecycleController{
        plugin: plugins.Get(runtime),
        statusHandler: cb, 
        log: log,
    }
}

func (lc *LifecycleController) Apply(c edgeruntime.ComponentSpec) error {
    lc.log.Infow("LifecycleController: Apply Enter")   
    if err := lc.plugin.Install(c); err != nil {
        lc.log.Errorw("Install failed", err)
        return err
    }
    return lc.plugin.Start(c)
}

func (lc *LifecycleController) Stop(name string) error {
    return lc.plugin.Stop(name)
}

func (lc *LifecycleController) Delete(name string) error {
    return lc.plugin.Delete(name)
}

func (lc *LifecycleController) HandleAction(op model.DiffOp) (error) {
    lc.log.Debugw("HandleAction: Enter", "operation", op)

    app := op.App
    lc.log.Debugw("app details:", "app", app)
    switch op.Action {

    case model.ActionAddApp:
        lc.log.Debugw("ActionAddApp")
        //lc.statusHandler.OnEvent(op, model.StateInstalling, nil)
        if err := lc.handleAddApp(op); err != nil {
            //lc.statusHandler.OnEvent(op, model.StateFailed, err)
        }
        //lc.statusHandler.OnEvent(op, model.StateInstalled, nil)
        return nil

    case model.ActionUpdateApp:
        lc.log.Debugw("ActionUpdateApp")
        return nil //lc.handleUpdateApp(ctx, rt, app)

    case model.ActionAddComp:
        lc.log.Debugw("ActionAddComp")
        //comp := app.Component(act.CompID)
        return nil //lc.handleAddComp(ctx, rt, comp)

    case model.ActionUpdateComp:
        lc.log.Debugw("ActionUpdateComp")
        //comp := app.Component(act.CompID)
        return nil //lc.handleUpdateComp(ctx, rt, comp)

    case model.ActionRemoveComp:
        lc.log.Debugw("ActionRemoveComp")
        //comp := app.Component(act.CompID)
        return nil //lc.handleRemoveComp(ctx, rt, comp)

    case model.ActionRemoveApp:
        lc.log.Debugw("ActionRemoveApp")
        return nil //lc.handleRemoveApp(ctx, rt, app)
    }

    return nil
}

func (lc *LifecycleController) handleAddApp(op model.DiffOp) error {
    lc.log.Debugw("handleAddApp: enter")

    app := op.App

    // send deployment status
    //status := edgeruntime.ComponentStatus{}
    for _, comp := range app.Components {
        lc.log.Debugw("","", comp)
        // comp.Name
        // comp.Version
        // comp.Repository
        // comp.PackageURL
        // comp.KeyURL
        c := edgeruntime.ComponentSpec{
            Name:     comp.Name,
            Runtime:  "containerd",
            Artifact: comp.Repository,
        }
        lc.statusHandler.OnEvent(op, comp.Name, model.StateInstalling, nil)
        //if (lc.cb != nil) {lc.cb()}
        if err := lc.plugin.Install(c); err != nil {
            lc.statusHandler.OnEvent(op, comp.Name, model.StateFailed, err)
            lc.log.Errorw("plugin install","err", err)
            return err
        }
    }
    for _, comp := range app.Components {
        lc.log.Debugw("","", comp)
        c := edgeruntime.ComponentSpec{
            Name:     comp.Name,
            Runtime:  "containerd",
            Artifact: comp.Repository,
        }
        if err := lc.plugin.Start(c); err != nil {
            lc.statusHandler.OnEvent(op, comp.Name, model.StateFailed, err)
            lc.log.Errorw("plugin Start","err", err)
            return err
        }
        lc.statusHandler.OnEvent(op, comp.Name, model.StateInstalled, nil)
    }
    
    lc.log.Debugw("handleAddApp: exit")
    return nil
}

/*
func (lc *LifecycleController) HandleAction(ctx context.Context, act types.Action) error {
    app, err := lc.Store.GetApp(act.AppID)
    if err != nil {
        return err
    }

    rt := lc.RuntimeManager.For(app.Runtime) // "containerd", "wasm", etc.
    if rt == nil {
        return fmt.Errorf("no runtime plugin for %s", app.Runtime)
    }

    switch act.Type {

    case types.ActionAddApp:
        return lc.handleAddApp(ctx, rt, app)

    case types.ActionUpdateApp:
        return lc.handleUpdateApp(ctx, rt, app)

    case types.ActionAddComp:
        comp := app.Component(act.CompID)
        return lc.handleAddComp(ctx, rt, comp)

    case types.ActionUpdateComp:
        comp := app.Component(act.CompID)
        return lc.handleUpdateComp(ctx, rt, comp)

    case types.ActionRemoveComp:
        comp := app.Component(act.CompID)
        return lc.handleRemoveComp(ctx, rt, comp)

    case types.ActionRemoveApp:
        return lc.handleRemoveApp(ctx, rt, app)
    }

    return nil
}

func (c *Controller) handleAddApp(ctx context.Context, rt edgeruntime.Plugin, app *types.App) error {
    for _, comp := range app.Components {
        if err := rt.Install(ctx, comp); err != nil {
            return err
        }
    }
    for _, comp := range app.Components {
        if err := rt.Start(ctx, comp.Name); err != nil {
            return err
        }
    }
    return nil
}

func (c *Controller) handleUpdateApp(ctx context.Context, rt edgeruntime.Plugin, app *types.App) error {
    for _, comp := range app.Components {
        rt.Stop(ctx, comp.Name)
        rt.Delete(ctx, comp.Name)
        if err := rt.Install(ctx, comp); err != nil {
            return err
        }
        if err := rt.Start(ctx, comp.Name); err != nil {
            return err
        }
    }
    return nil
}

func (c *Controller) handleAddComp(ctx context.Context, rt edgeruntime.Plugin, comp *types.Component) error {
    comp := app.GetComponent(act.CompID)
    lc.Reporter.App(comp, "Installing", "")
    if err := rt.Install(ctx, comp); err != nil {
        lc.Reporter.App(comp, "InstallFailed", err.Error())
        return err
    }
    lc.Reporter.App(comp, "Installed", "")

    lc.Reporter.App(comp, "Starting", "")
    if err := rt.Start(ctx, comp.Name); err != nil {
        lc.Reporter.App(comp, "Error", err.Error())
        return err
    }
    //lc.Reporter.App(comp, "Running", "")

    lc.Reporter.App(app) // always recompute app status

    if err := rt.Install(ctx, comp); err != nil {
        return err
    }
    return rt.Start(ctx, comp.Name)
}

func (c *Controller) handleUpdateComp(ctx context.Context, rt edgeruntime.Plugin, comp *types.Component) error {
    rt.Stop(ctx, comp.Name)
    rt.Delete(ctx, comp.Name)
    if err := rt.Install(ctx, comp); err != nil {
        return err
    }
    return rt.Start(ctx, comp.Name)
}

func (c *Controller) handleRemoveComp(ctx context.Context, rt edgeruntime.Plugin, comp *types.Component) error {
    rt.Stop(ctx, comp.Name)
    return rt.Delete(ctx, comp.Name)
}

func (c *Controller) handleRemoveApp(ctx context.Context, rt edgeruntime.Plugin, app *types.App) error {
    for _, comp := range app.Components {
        rt.Stop(ctx, comp.Name)
        if err := rt.Delete(ctx, comp.Name); err != nil {
            return err
        }
    }
    return nil
}
*/