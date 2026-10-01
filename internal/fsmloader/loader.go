package fsmloader

import (
	"context"

	"github.com/balaji-balu/ieo/internal/lo/logger"
	"github.com/looplab/fsm"
)

type Loader struct {
	FSM *fsm.FSM
	ctx context.Context
	//logger *zap.Logger
	//l     *lo.LocalOrchestrator
	//
}

func NewLoader(ctx context.Context) *Loader {
	loader := &Loader{
		ctx: ctx,
	}

	logger.Debug("creating fsm events ")
	loader.FSM = fsm.NewFSM(
		"idle",
		fsm.Events{
			//{Name: "start", Src: []string{"idle"}, Dst: "ready"},
			{Name: "deploy", Src: []string{"idle", "ready"}, Dst: "deploying"},
			{Name: "success", Src: []string{"deploying"}, Dst: "ready"},
			{Name: "fail", Src: []string{"deploying"}, Dst: "error"},
			{Name: "enable_push", Src: []string{"idle"}, Dst: "ready"},
			{Name: "enable_pull", Src: []string{"idle"}, Dst: "ready"},
			{Name: "enable_offline", Src: []string{"ready"}, Dst: "ready"},
			//{Name: "git_polled", Src: []string{"ready"}, Dst: "ready"},
			{Name: "git_update_received", Src: []string{"ready"}, Dst: "ready"},
			{Name: "EventNetworkChange", Src: []string{"idle", "ready"}, Dst: "ready"},
			{Name: "EventGitPolled", Src: []string{"ready"}, Dst: "ready"},
		},
		fsm.Callbacks{},
		// "before_EventNetworkChange": func(ctx context.Context, evt *fsm.Event) {
		// 	//l.logger.Info("FSM: EventNetworkChange")
		// 	//data := evt.Data.(orchestrator.NetworkChangePayload)
		// 	//l.lo.handleNetworkChange(ctx, l.lo.Config, data)
		// },
		// "before_git_update_received": func(ctx context.Context, evt *fsm.Event) {
		// 	//l.logger.Info("FSM: git_update_received")
		// 	//l.lo.DeployToEdges(ctx)
		// 	//l.onDeploy(evt)
		// },
		// "before_start": func(ctx context.Context, evt *fsm.Event) {
		// 	//l.logger.Info("FSM: before_start")
		// 	//l.lo.StartModeLoop(ctx)
		// },
		// "enter_start": func(ctx context.Context, evt *fsm.Event) {
		// 	//l.logger.Info("FSM: start")
		// },
		// "before_enable_push": func(ctx context.Context, evt *fsm.Event) {
		// 	//l.logger.Info("FSM: before enable_push")
		// 	// go func() {
		// 	// 	if err := l.lo.StartPushMode(l.lo.RootCtx, l.lo.Config); err != nil {
		// 	// 		l.logger.Error("Push mode error", zap.Error(err))
		// 	// 	}
		// 	// }()
		// 	//go l.lo.StartPushMode(ctx, l.lo.Config)
		// 	//go l.lo.start_push_mode(ctx, l.lo)
		// },
		// "enter_enable_push": func(ctx context.Context, evt *fsm.Event) {
		// 	//l.logger.Info("FSM: enable_push")
		// },
		// "before_enable_pull": func(ctx context.Context, evt *fsm.Event) {
		// 	//l.logger.Info("FSM: efore enable_pull")
		// 	// go func() {
		// 	//     if err := l.lo.StartPullMode(l.lo.RootCtx, l.lo.Config); err != nil {
		// 	//         l.logger.Error("Pull mode error", zap.Error(err))
		// 	//     }
		// 	// }()

		// },
		// "enter_enable_pull": func(ctx context.Context, evt *fsm.Event) {
		// 	//l.logger.Info("FSM: enable_pull")
		// },
		// "before_enable_offline": func(ctx context.Context, evt *fsm.Event) {
		// 	//l.logger.Info("FSM: efore enable_offline")
		// },
		// "enter_enable_offline": func(ctx context.Context, evt *fsm.Event) {
		// 	//l.logger.Info("FSM: enable_offline")
		// },
		// "enter_ready": func(ctx context.Context, e *fsm.Event) {
		// 	//l.logger.Info("FSM: ready")
		// },
		// "enter_deploying": func(ctx context.Context, e *fsm.Event) {  },
		//},
	)

	//mkk := lo.Machine()
	//log.Println(mkk)

	//mkk = l.FSM

	// // Watcher: listens for results asynchronously
	// go loader.resultListener()

	// // Start dispatcher once
	// loader.once.Do(func() {
	// 	go loader.l.StartEventDispatcher(ctx)
	// })
	logger.Debug("creating fsm events ", "loader:", loader)
	return loader
}

// func (l *Loader) onDeploy(e *fsm.Event) {
// 	l.logger.Info("FSM: deploy started")
// 	//go l.lo.DeployToEdges()
// }

// func (l *Loader) resultListener() {
// 	//lo := l.l
// 	for {
// 		select {
// 		case <-l.ctx.Done():
// 			//l.logger.Warn("Result watcher stopped")
// 			return
// 		case res := <-lo.ResultBus().Results:
// 			switch res.Status {
// 			case "success":
// 				//l.logger.Info("Node success", zap.String("node", res.Node))
// 			case "fail":
// 				//l.logger.Error("Node failed", zap.String("node", res.Node), zap.Error(res.Error))
// 				_ = l.FSM.Event(l.ctx, "fail")
// 				continue
// 			case "done":
// 				//l.logger.Info("All nodes processed, marking success")
// 				_ = l.FSM.Event(l.ctx, "success")
// 			}
// 		}
// 	}
// }
