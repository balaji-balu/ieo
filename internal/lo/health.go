package lo

import (
	//"context"
	
	//"encoding/json"
	"fmt"
	//"io"
	"log"
	

	"github.com/balaji-balu/ieo/internal/lo/heartbeat"
	//"github.com/balaji-balu/ieo/internal/lo/reconciler"
	//"github.com/balaji-balu/ieo/internal/lo/logger"
	"github.com/balaji-balu/ieo/pkg/model"

)

func (l *LocalOrchestrator) MonitorHealthandStatusFromEN(
	monitor *heartbeat.Monitor, coUrl string) {

	// Subscribe to health
	go func() {
		log.Println("lo with siteid:", fmt.Sprintf("health.%s.*", l.Config.Site))
		subHealth := fmt.Sprintf("health.%s.*", l.Config.Site)
		err := l.nc.Subscribe2(subHealth, func(h model.HealthMsg) {
			//log.Printf("[LO] health from %s runtime=%s", h.NodeID, h.Runtime)

			host := model.Host{
				ID: h.NodeID,
				Alive: true,
				// ID: h.NodeID,
				// Labels: map[string]string{
				// 	"region": "us-east",
				// 	"role":   "worker",
				// },
				// Status: "alive",
			}
			l.store.AddOrUpdateHost(host)
			monitor.Update(h.NodeID)	

			// err := lo.CreateEdgeNode(lo.RootCtx, h)
			// if err != nil {
			// 	log.Printf("[LO] error saving the Node:", err)
			// }
			//nodeCount.Set(float64(len(orchestrator.GetAllNodes(db))))
			//if fsm.GetState() == shared.Discovering  {
			//	fsm.Transition(shared.Running)
			//}
		})
		if err != nil {
			log.Println("subscribe error:", err)
		} else {
			log.Println("subscribed to", subHealth)
		}
		l.nc.Flush()
		log.Println("subscription ready for", subHealth)

	}()
}

