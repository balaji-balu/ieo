package handlers

import (
	"context"
	"time"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/ent"
	"github.com/balaji-balu/ieo/ent/deploymentstatus"
	"github.com/balaji-balu/ieo/ent/deploymentcomponentstatus"
	"github.com/balaji-balu/ieo/internal/streammanager"
	"github.com/balaji-balu/ieo/internal/metrics"
	"github.com/balaji-balu/ieo/pkg/model"
	
)

/*
TBD: add to deployment history esp installed, failed 
*/
func DeploymentStatusHandler(c *gin.Context, client *ent.Client, sm *streammanager.StreamManager) {

	ctx := c.Request.Context()
	
	log.Println("DeploymentStatusHandler called")

	var ds model.DeploymentStatus
	if err := c.ShouldBindJSON(&ds); err != nil {
		log.Println("Error binding JSON:", err)
		return
	}

	log.Println("Received deployment status:", ds)

	log.Println("Deployment id:", ds.DeploymentID, sm)
	
	UpdateDeploymentStatus(ctx, client, &ds)

	if ds.Status.State == "failed" || ds.Status.State == "installed" {
		metrics.DeploymentsActive.WithLabelValues(ds.DeploymentID).Dec()
	} 

	// brodcast the status to streaming(SSR) clients
	sm.Broadcast(ds.DeploymentID, streammanager.DeployEvent{
		DeploymentId: ds.DeploymentID,
		Timestamp:    time.Now().Format(time.RFC3339),
		SiteID:       "",
		Message:      "", //dr.Message,
		Status:       string(ds.Status.State),
	})

}

func UpdateDeploymentStatus(ctx context.Context, 
		client *ent.Client, ds *model.DeploymentStatus) error {
	id, err := uuid.Parse(ds.DeploymentID)
	if err != nil {
		return err
	}

	tx, err := client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	// Update main deployment state
	_, err = tx.DeploymentStatus.
		Update().
		Where(deploymentstatus.IDEQ(id)).
		SetState(ds.Status.State).
		SetErrorCode(ds.Status.Error.Code).
		SetErrorMessage(ds.Status.Error.Message).
		SetUpdatedAt(time.Now()).
		Save(ctx)
	if err != nil {
		return tx.Rollback()
	}

	// Update each component state
	for _, c := range ds.Components {
		_, err = tx.DeploymentComponentStatus.
			Update().
			Where(deploymentcomponentstatus.NameEQ(c.Name)).
			SetState(c.State).
			SetErrorCode(c.Error.Code).
			SetErrorMessage(c.Error.Message).
			Save(ctx)
		if err != nil {
			return tx.Rollback()
		}
	}

	return tx.Commit()
}

// GetDeploymentsStatus returns all deployment statuses with components
/*func GetDeploymentsStatus(c *gin.Context, client *ent.Client) {
    ctx := c.Request.Context()

    // Query all DeploymentStatuses and eager-load components
    deployments, err := client.DeploymentStatus.
        Query().
        WithComponents().
        All(ctx)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{
            "error":   "failed to fetch deployment statuses",
            "details": err.Error(),
        })
        return
    }

    // Transform results
    var result []gin.H
    for _, d := range deployments {
        components := make([]gin.H, 0, len(d.Edges.Components))
        for _, c := range d.Edges.Components {
            components = append(components, gin.H{
                "name":         c.Name,
                "state":        c.State,
                "errorCode":    c.ErrorCode,
                "errorMessage": c.ErrorMessage,
            })
        }

        result = append(result, gin.H{
            "id":          d.ID,
            "deploymentID": d.ID.String(),
            "state":       d.State,
            "errorCode":   d.ErrorCode,
            "errorMessage": d.ErrorMessage,
            "components":  components,
        })
    }

    c.JSON(http.StatusOK, gin.H{
        "deployments": result,
    })
}
*/
func fetchDeployment(ctx context.Context, client *ent.Client, id string) (*ent.DeploymentStatus, error) {
    return client.DeploymentStatus.
        Query().
        Where(deploymentstatus.IDEQ(uuid.MustParse(id))).
        WithComponents().
        Only(ctx)
}


func ListDeploymentsStatus(c *gin.Context, client *ent.Client) {
    ctx := c.Request.Context()

    deployments, err := client.DeploymentStatus.
        Query().
        WithComponents().
        All(ctx)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{
            "error":   "failed to fetch deployment statuses",
            "details": err.Error(),
        })
        return
    }

    result := make([]gin.H, 0, len(deployments))

    for _, d := range deployments {
        components := make([]gin.H, 0, len(d.Edges.Components))
        for _, comp := range d.Edges.Components {
            components = append(components, gin.H{
                "name":         comp.Name,
                "state":        comp.State,
                "errorCode":    comp.ErrorCode,
                "errorMessage": comp.ErrorMessage,
            })
        }

        result = append(result, gin.H{
            "id":           d.ID,
            "deploymentID": d.ID.String(),
            "state":        d.State,
            "errorCode":    d.ErrorCode,
            "errorMessage": d.ErrorMessage,
            "components":   components,
        })
    }

    c.JSON(http.StatusOK, gin.H{"deployments": result})
}


func GetDeploymentStatus(c *gin.Context, client *ent.Client) {
    ctx := c.Request.Context()
    id := c.Param("id")

    log.Println("GetDeploymentStatus: enter", id)

    deployment, err := fetchDeployment(ctx, client, id)
    if ent.IsNotFound(err) {
        c.JSON(http.StatusNotFound, gin.H{"error": "deployment not found"})
        return
    }
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{
            "error":   "failed to fetch deployment",
            "details": err.Error(),
        })
        return
    }

    components := make([]gin.H, 0, len(deployment.Edges.Components))
    for _, comp := range deployment.Edges.Components {
        components = append(components, gin.H{
            "name":         comp.Name,
            "state":        comp.State,
            "errorCode":    comp.ErrorCode,
            "errorMessage": comp.ErrorMessage,
        })
    }

    c.JSON(http.StatusOK, gin.H{
        "id":           deployment.ID,
        "deploymentID": deployment.ID.String(),
        "state":        deployment.State,
        "errorCode":    deployment.ErrorCode,
        "errorMessage": deployment.ErrorMessage,
        "components":   components,
    })
}
