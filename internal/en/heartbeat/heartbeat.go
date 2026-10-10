package heartbeat

import (
	"fmt"
	"time"
	"math/rand"
	"go.uber.org/zap"
	"github.com/balaji-balu/ieo/pkg/model"
	"github.com/balaji-balu/ieo/internal/natsbroker"
)

func StartHeartbeat(nb *natsbroker.Broker, 
	log *zap.SugaredLogger,
	siteID, hostID string) {
 
	ticker := time.NewTicker(10 * time.Second)
	go func() {
		for range ticker.C {
			msg := model.HealthMsg{
				NodeID:     hostID,
				SiteID:     siteID,
				CPUPercent: rand.Float64() * 20,
				MemMB:      50 + rand.Float64()*20,
				Timestamp:  time.Now().Unix(),
				// Runtime:    en.Runtime,
				// //Region:     en.Region,
			}
			subj := fmt.Sprintf("health.%s.%s", siteID, hostID)
			if err := nb.Publish(subj, msg); err != nil {
				log.Errorw("Heart publish failed:", "err", err)
			} else {
				//log.Debugw("Heart msg published", "Subject:", subj,"msg:", msg)
				nb.Flush()
			}
			//log.Println("Heart msg published", id)
		}
	}()
}