package reconciler

import (
	"log"
	"fmt"
	"time"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/balaji-balu/ieo/pkg/model"
	"github.com/balaji-balu/ieo/internal/lo/boltstore"

)

/*
desired 
	<deploymentid>
		apps 
			<app1> : json {id: app1, version:v1} 
actual 
	<hostAA> 
		<app1>: json {id: app1, version:v0.9} 
		<app2>: json {id: app2, version v0.5}
*/
// -------------------- Utilities --------------------
func ComputeHash(content string) string {
    h := sha256.Sum256([]byte(content))
    return hex.EncodeToString(h[:])
}

func ComputeAppHash(app model.App) string {
    b, _ := json.Marshal(app)   // components + content + version + id
    h := sha256.Sum256(b)
    return hex.EncodeToString(h[:])
}

func pretty(v interface{}) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

// this is used for remove_app, remove_comp only
func copyApp(actualApp model.ActualApp) (model.App) {
	copied := make(map[string]model.Component, len(actualApp.Components))
	for k, v := range actualApp.Components {
		copied[k] = model.Component{
			Name:    v.Name,
			Version: v.Version,
			//Config:  v.Config,
			// add the rest of the fields if needed
    	}
	}

	return model.App{
		ID: actualApp.ID,
		Version: actualApp.Version,
		Components: copied, 
	}
}


// func PathForDesired(depID string) []string {
// 	return []string{"desired", depID}
// }

// func pathForActual(hostID string) []string {
// 	return []string{"actual", hostID}
// }

// func pathForOperation() []string {
// 	return []string{"operations"} 
// }

func computeDiff(desired model.App, 
	actual model.ActualState, hosts map[string]model.Host) []model.DiffOp {
	var ops []model.DiffOp

	desiredAppHash := ComputeAppHash(desired)

	//fmt.Println("Hosts:", len(hosts))
	for _, host := range hosts {

		hostID := host.ID
		appID := desired.ID
		desiredApp := desired //.DesiredApp
		// ensure maps exist to avoid nil panics
		if actual.AppsByHost == nil {
			actual.AppsByHost = map[string]map[string]model.ActualApp{}
		}
		if actual.AppsByHost[hostID] == nil {
			actual.AppsByHost[hostID] = map[string]model.ActualApp{}
		}

		//fmt.Println("hostid:", hostID, "appID:",  appID)
		// if appID == "" {
		// 	log.Println("desired AppId is null")
		// 	continue
		// }

		if appID != "" {
			actualApp, appExists := actual.AppsByHost[hostID][appID]
			if !appExists {
				ops = append(ops, model.DiffOp{
					Action: model.ActionAddApp, 
					SiteID: "", 
					HostID: hostID, 
					App: desiredApp,
				})
				continue
			}

			// NO-OP: if app-level hash matches exactly, skip
			// (actualApp.Hash may be empty if you haven't set it previously)
			if actualApp.Hash != "" && actualApp.Hash == desiredAppHash {
				// nothing changed for this host/app
				log.Println("No-Op")
				continue
			}

			// if app version differs -> full app update
			//log.Println("versions:", actualApp.Version, desiredApp.Version)
			if desiredApp.Version != "" && actualApp.Version != desiredApp.Version {
				log.Println("UpdateApp", actualApp.Version, desiredApp.Version)
				ops = append(ops, model.DiffOp{
					Action: model.ActionUpdateApp, 
					SiteID: "", 
					HostID: hostID, 
					App: desiredApp,
				})
				continue
			}

			// same app version; compare 
			//fmt.Println("components length:", len(desiredApp.Components))
			for _, desiredComp := range desiredApp.Components {
				compName := desiredComp.Name
				//fmt.Println("compname", compName)
				actualComp, compExists := actualApp.Components[compName]
				if !compExists {
					ops = append(ops, model.DiffOp{
						Action: model.ActionAddComp, 
						SiteID: "", 
						HostID: hostID, 
						App: desiredApp, 
						CompName: compName, 
					})
					continue
				}

				if actualComp.Version != desiredComp.Version {
					ops = append(ops, model.DiffOp{
						Action:   model.ActionUpdateComp,
						SiteID:   "",
						HostID:   hostID,
						App:      desiredApp,
						CompName: compName,
					})
					continue
				}

				if actualComp.Hash != "" && desiredComp.Content != "" {
					desiredCompHash := ComputeHash(desiredComp.Content)
					if actualComp.Hash != desiredCompHash {
						ops = append(ops, model.DiffOp{
							Action: model.ActionUpdateComp, 
							SiteID: "", 
							HostID: hostID, 
							App: desiredApp, 
							CompName: compName,
						})
					}
				}	
			}

			// components in actual but not in desired -> remove
			for _, actualComp := range actualApp.Components {
				compName := actualComp.Name
				fmt.Println("xxxxxxxxxxxxxxx", desired.Components)
				if _, exists := desiredApp.Components[compName]; !exists {
					log.Println("update component...")
					ops = append(ops, model.DiffOp{
						Action: model.ActionRemoveComp, 
						SiteID: "", 
						HostID: hostID, 
						App: copyApp(actualApp), 
						CompName: compName,
					})
				}
			}
		}

		// removeapp
		if desiredApp.ID == "" {
			log.Println("removeapp desird is null", actual)
			// desired has no app at all → remove everything
			for _, actualApp := range actual.AppsByHost[hostID] {
				ops = append(ops, model.DiffOp{
					Action: model.ActionRemoveApp,
					HostID: hostID,
					App:    copyApp(actualApp),
				})
			}
		} else {
			log.Println("removeapp desird is not null", actual.AppsByHost[hostID])

			// desired has exactly one app → remove all others
			for _, actualApp := range actual.AppsByHost[hostID] {
				if actualApp.ID != desiredApp.ID {
					ops = append(ops, model.DiffOp{
						Action: model.ActionRemoveApp,
						HostID: hostID,
						App:    copyApp(actualApp),
					})
				}
			}
		}

	}
	return ops	
}

type Actuator interface {
	Execute(op model.DiffOp) error
}

type Reconciler struct {
	actuator Actuator
	store *boltstore.StateStore
}

func NewReconciler(s *boltstore.StateStore, a Actuator) *Reconciler {
	return &Reconciler{store: s, actuator: a}
}

func (r *Reconciler) ReconcileMulti( 
	depId string) (error) {
	//maxRetries int, debug bool

	log.Println("dep id", depId)

	// var desired model.App
	// path := PathForDesired(depId)
	// key := "app" // could also be "deploy-" + appID or version
	// if err := r.store.LoadState(path, key, &desired); err != nil {
	// 	log.Fatalf("failed to save desired state for %s/%s: %v", path, key, err)
	// }
	desired, _ := r.store.GetDesired(depId)	
	log.Printf("Desired App:%v", desired)

	hosts, _ := r.store.LoadAllHosts()
	log.Println("hosts", hosts)

	actual, _ := r.store.GetActual()
	log.Printf("Actual App:%v", actual)	
	// actual := model.ActualState{
	// 	AppsByHost: map[string]map[string]model.ActualApp{},
	// }
	// for hostid, _ := range hosts {
	// 	a, err := r.store.LoadActualForHost(hostid)
	// 	if err != nil {
    //         // log and continue if host state not found; or return - choose one
    //         //log.Printf("load actual for host %s: %v (continuing)", hostid, err)
    //         a = map[string]model.ActualApp{}
    //     }
	// 	actual.AppsByHost[hostid] = a
	// }

	//if debug {
		fmt.Println("======= Desired State =======")
		fmt.Println(pretty(desired))
		fmt.Println("======= Actual State (before) =======")
		fmt.Println(pretty(actual))
		fmt.Println("======= Hosts =======")
		fmt.Println(pretty(hosts))
	//}

	// 2️⃣ Filter alive hosts only
	aliveHosts := map[string]model.Host{}
	for id, host := range hosts {
		if host.Alive {
			aliveHosts[id] = host
		} else  {
			fmt.Printf("[SKIP] Host offline: %s\n", id)
		}
	}

	// 3️⃣ Compute diff only for alive hosts
	ops := computeDiff(desired, actual, aliveHosts)
	//if debug {
		fmt.Println("=== Diff Ops ===")
		for range aliveHosts {
			for _, op := range ops {
				op.DeploymentID = depId
				op.TimeStamp = time.Now().UnixNano()
				fmt.Printf("%+v\n", op)
				//key := fmt.Sprintf("%s-%d", depId, time.Now().UnixNano())
				//r.store.SaveState(pathForOperation(), key, op)
				r.store.SetOperation(depId, op)
				if err := r.actuator.Execute(op); err != nil {
					log.Println("Actuator Error:", err)
				}			
			}
		}
	//}

    // // 5. execute operations per node
	// for hostid, _ := range aliveHosts {
	// 	log.Println("hostid:", hostid)
	// 	for _, op := range ops {
	// 		log.Println("op:", op)
	// 		op.DeploymentID = depId 
	// 		if err := r.actuator.Execute(op); err != nil {
	// 			log.Println("Actuator Error:", err)
	// 		}
	// 	}
	// }

    // for nodeID, ops := range ops {
    //     for _, op := range ops {
    //         op.TargetNode = nodeID
    //         logger.Info("Executing operation", "deploymentID", op.DeploymentID, "nodeID", nodeID, "component", op.Component.Name, "opType", op.Type)

    //         if err := r.actuator.Execute(op); err != nil {
    //             logger.Error("Operation execution failed", "deploymentID", op.DeploymentID, "nodeID", nodeID, "component", op.Component.Name, "opType", op.Type, "err", err)
    //         }
    //     }
    // }

	return nil
}

