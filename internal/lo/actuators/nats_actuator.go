package actuators

import (
    "context"
    "encoding/json"
    "fmt"
    "log"
    "time"
    "io"
    "net/http"
    "bytes"

    //"github.com/nats-io/nats.go"

    "github.com/balaji-balu/ieo/internal/natsbroker"
	"github.com/balaji-balu/ieo/internal/lo/reconciler"
    "github.com/balaji-balu/ieo/internal/lo/boltstore"
	"github.com/balaji-balu/ieo/pkg/model"
)

// NatsActuator implements Actuator and talks to EN over NATS
type NatsActuator struct {
    nc      *natsbroker.Broker
    subject string // NATS subject for EN operations
    timeout time.Duration
    siteId  string
    store *boltstore.StateStore
    CoUrl string

}

// NewNatsActuator creates a new NatsActuator
func NewNatsActuator(store *boltstore.StateStore, 
    nc *natsbroker.Broker, coUrl string, siteId string, timeout time.Duration) *NatsActuator {
    //nc := natsbroker.New()

    a := NatsActuator{
        nc:      nc,
        store: store,
        //subject: subject,
        siteId: siteId,
        timeout: timeout,
        CoUrl: coUrl,
    }
    //return &

    a.ReceiveStatus()

    return &a
}

// Execute sends the operation to EN via NATS
func (a *NatsActuator) Execute(op model.DiffOp) error {
    _, cancel := context.WithTimeout(context.Background(), a.timeout)
    defer cancel()

    log.Println("NatsActuator.Execute enter")

    log.Println("NatsActuator.Execute. operation: ", op)

    subject := fmt.Sprintf("site.%s.deploy.%s", a.siteId, op.HostID)
    if err := a.nc.Publish(subject, op); err != nil {
        fmt.Errorf("NatsActuator: publish error", err)
    }

/*
    req := model.HostDeployRequest{
        HostID:       op.TargetNode,
        DeploymentID: op.DeploymentID,
        Component: model.ComponentProperties {
            Name     : op.Component.Name,   
            Repository: op.Component.Properties.Repository,
            Revision: op.Component.Properties.Revision,
            PackageLocation:op.Component.Properties.PackageURL,
            KeyLocation: op.Component.Properties.KeyURL,
        },
        Action:       string(op.Type),
    }

    subject := fmt.Sprintf("site.%s.deploy.%s", a.siteId, op.TargetNode)
    if err := a.nc.Publish(subject, req); err != nil {
        fmt.Errorf("NatsActuator: publish error", err)
    }
    // b, err := json.Marshal(req)
    // if err != nil {
    //     return fmt.Errorf("failed to marshal ENRequest: %w", err)
    // }

    // // NATS request-reply
    // msg, err := a.nc.RequestWithContext(ctx, a.subject, b)
    // if err != nil {
    //     return fmt.Errorf("nats request failed: %w", err)
    // }

    // var resp ENResponse
    // if err := json.Unmarshal(msg.Data, &resp); err != nil {
    //     return fmt.Errorf("failed to unmarshal ENResponse: %w", err)
    // }

    // if !resp.Success {
    //     return fmt.Errorf("EN operation failed: %s", resp.Message)
    // }

    log.Printf("[NatsActuator] Node: %s | Deployment: %s | Component: %s | Action: %s\n",
        op.TargetNode, op.DeploymentID, op.Component.Name, op.Type)
*/
    log.Println("NatsActuator.Execute exit")    
    return nil
}

func (a *NatsActuator) ReceiveStatus() {
	go func() {
		subStatus := fmt.Sprintf("status.%s.*", a.siteId)
		err := a.nc.Subscribe4(subStatus, func(s model.DeploymentStatus) {
			//log.Printf("[LO] status %s from %s: success=%v, msg=%s",
			//	s.DeploymentID, s.NodeID, s.Status, s.Message)
			log.Println("[LO] component state:", s, s.DeploymentID)

            // log.Println("xxxxxxxxxxxxxxxxx status:", s.Status)
            if s.Status.State == string(model.StateInstalled) {
                a.ApplySuccessOp(s.DeploymentID, s.TimeStamp)
            }

            // ds := model.DeploymentStatus{
            //    Status: {
            //         State: s.Status
            //         Error : StatusError{}
            //    } 
   
            // }
            // for {
            //     compStatus := model.DeploymentComponent{
            //         Name:
            //         State: 
            //         Error: StatusError{}
            //    }
            //    Components:= append(Components, compStatus)
            // }

                
			//if s.Status == "installed" {
				//ctx := context.Background()

				//desired, _ := l.boltstore.GetDesiredHashes(ctx,s.DeploymentID)
				//desiredHash := desired[s.ComponentName]

				// component installed successfully -> update actual hash
				// err := l.store.SetActualHash(
				// 	ctx,
				// 	s.DeploymentID,
				// 	s.HostID,     // THIS IS HOST ID
				// 	s.ComponentName,  
				// 	//desiredHash,       // hash returned from agent
				// )
				// if err != nil {
				// 	logger.Error("failed to update actual hash", "err", err)
				// }
			//}

			// Forward the status back to CO
			// l.sendStatusToCO(s)			

			//forward to CO
            coPath := fmt.Sprintf("%s/api/v1",a.CoUrl)
			forwardToCO(coPath, s)

		})
		if err != nil {
			log.Fatal("[LO] failed to subscribe to status updates:", err)
		}

	}()
}

func forwardToCO(baseurl string, report model.DeploymentStatus) {
	url := fmt.Sprintf("%s/deployments/%s/status", baseurl, report.DeploymentID)
	payload, err := json.Marshal(report)
	if err != nil {
		log.Println("[LO] failed to marshal report:", err)
		return
	}

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(payload))
	if err != nil {
		log.Println("[LO] failed to send report to CO:", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[LO] CO returned status %d: %s", resp.StatusCode, string(body))
		return
	}

	log.Printf("[LO] ✅ Report forwarded to CO successfully (deployment_id=%s)", 
					report.DeploymentID)
}


func (a *NatsActuator) ApplySuccessOp(
    //boltstore *store.StateStore,
	depId string,
    timeStamp int64,
    //siteID string,
    //op model.DiffOp,
    //desired model.DesiredState,
) error {

    op, _ := a.store.GetOperation(depId, timeStamp)
	desired,_ := a.store.GetDesired(depId)

	actual, _ := a.store.GetActual()

	log.Println("desired:", desired, "actual:", actual)

    // actual, err := boltstore.LoadActualState(siteID)
    // if err != nil {
    //     return fmt.Errorf("load actual: %w", err)
    // }
    if actual.AppsByHost == nil {
        actual.AppsByHost = map[string]map[string]model.ActualApp{}
    }
    if actual.AppsByHost[op.HostID] == nil {
        actual.AppsByHost[op.HostID] = map[string]model.ActualApp{}
    }

	//log.Println("applySuccessOp:", op.HostID, op.App.ID, op)

    switch op.Action {

    case model.ActionAddApp, model.ActionUpdateApp: //"add_app", "update_app":
        dApp := desired //desired.Apps[op.App.ID]
        // if dApp == nil {
        //     return nil // desired removed, nothing to do
        // }

        compMap := map[string]model.ActualComponent{}
        for name, dc := range dApp.Components {
			//log.Println("X X X X X X ", name, dc)
            compMap[name] = model.ActualComponent{
				Name: 		 name,
                Status:      "success",
                Version:     dc.Version,
                LastUpdated: time.Now().Unix(),
                //Hash:        ComputeHash(dc.Content),
            }
        }

        actual.AppsByHost[op.HostID][op.App.ID] = model.ActualApp{
			ID : op.App.ID,
            Version:    dApp.Version,
            Components: compMap,
        }
		//log.Println("ZZZZ ZZZ ZZZZ ", actual.AppsByHost[op.HostID][op.App.ID])
		//VerifyDeploymentHashes(store, siteID, desired, actual)

    case model.ActionAddComp, model.ActionUpdateComp: //"add_comp", "update_comp":
        aApp := actual.AppsByHost[op.HostID][op.App.ID]
        if aApp.Components == nil {
            aApp.Components = map[string]model.ActualComponent{}
        }
        dComp := desired.Components[op.CompName]
        aApp.Components[op.CompName] = model.ActualComponent{
			Name: op.CompName,
            Status:      "success",
            Version:     dComp.Version,
            LastUpdated: time.Now().Unix(),
            //Hash:        ComputeHash(dComp.Content),
        }
        actual.AppsByHost[op.HostID][op.App.ID] = aApp

    case model.ActionRemoveComp: //"remove_comp":
        aApp := actual.AppsByHost[op.HostID][op.App.ID]
        delete(aApp.Components, op.CompName)
        actual.AppsByHost[op.HostID][op.App.ID] = aApp

    case model.ActionRemoveApp: //"remove_app":
        delete(actual.AppsByHost[op.HostID], op.App.ID)

    }

    // 2. Save updated state
    log.Println("ApplySuccessOp: ", op.HostID, "appid:", op.App.ID)
    updatedApp := actual.AppsByHost[op.HostID][op.App.ID]
    updatedApp.Hash = reconciler.ComputeAppHash(desired)
	if err := a.store.SetActual(op.HostID, updatedApp); err != nil {
        return fmt.Errorf("save actual app for host %s app %s: %w", op.HostID, op.App.ID, err)
	}
    // if err := r.store.SaveState(, op.App.ID, &updatedApp); err != nil {
    //     return fmt.Errorf("save actual app for host %s app %s: %w", op.HostID, op.App.ID, err)
    // }	
    return nil 
}
