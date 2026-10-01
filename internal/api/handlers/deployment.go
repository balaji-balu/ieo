package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	//"os"
	"time"
	"github.com/google/go-github/v55/github"
	"golang.org/x/oauth2"
	"net/http"
	//"strings"

	//"github.com/joho/godotenv"
	//"path/filepath"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
	"github.com/gin-gonic/gin"

	//pb "github.com/balaji-balu/ieo/proto_generated"
	"github.com/balaji-balu/ieo/ent"
	"github.com/balaji-balu/ieo/ent/component"
	"github.com/balaji-balu/ieo/ent/deploymentprofile"
	"github.com/balaji-balu/ieo/ent/applicationdesc"
	//"github.com/balaji-balu/ieo/internal/config"
	"github.com/balaji-balu/ieo/internal/co"
	"github.com/balaji-balu/ieo/internal/streammanager"
	"github.com/balaji-balu/ieo/internal/metrics"
	"github.com/balaji-balu/ieo/pkg/application"
	"github.com/balaji-balu/ieo/pkg/deployment"
	"github.com/balaji-balu/ieo/pkg/model"
	
)

var (
//sm = streammanager.NewStreamManager()
)

// server implements CentralOrchestrator gRPC interface
// type server struct {
// 	pb.UnimplementedCentralOrchestratorServer
// 	//sm *streammanager.StreamManager
// }

type HostMapping struct {
	SiteID  string   `json:"site_id"`
	HostIDs []string `json:"host_ids"`
}

type App struct {
	AppID     string        `json:"app_id"`
	AppName   string 		`json:"app_name"`
	Version   string 		`json:"version"`
	Category 	string		`json:"category"`
	//ProfileID string        `json:"profile_id"`
	Sites     []HostMapping `json:"sites"`
	DeployType string 		`json:"deploy_type"`
}

func init() {
	// err := godotenv.Load("../.env") // relative path to project root
	// if err != nil {
	//     log.Println("xzzxzxzx No .env file found, reading from system environment")
	// }
}

// func (s *server) ReportStatus(ctx context.Context, req *pb.StatusReport) (*pb.DeployResponse, error) {
// 	status := req.Statuses[0]
// 	log.Printf("[CO] ReportStatus received for deployment %s, node=%s, status=%s",
// 		req.DeploymentId,
// 		status.NodeId,
// 		status.Status.String())
// 	return &pb.DeployResponse{
// 		DeploymentId: req.DeploymentId,
// 		Message:      "Status received by CO",
// 	}, nil
// }


func buildDeployParameters(siteID string) []deployment.Parameter {
	var params []deployment.Parameter

	//for _, site := range sites {
		param := deployment.Parameter{
			Name:  "SiteId",
			Value: siteID, // This can represent site or contextual value
			Targets: []deployment.Target{
				{
					Pointer:    fmt.Sprintf("/sites/%s", siteID),
					//Components: [], // hostIDs act as components or nodes
				},
			},
		}
		params = append(params, param)
	//}

	return params
}

func buildDeploymentComponents(ents []*ent.Component) []deployment.Component {
	comps := make([]deployment.Component, 0, len(ents))
	for _, c := range ents {
		comps = append(comps, deployment.Component{
			Name: c.Name,
			Properties: application.ComponentProperties{
				Repository:      c.Properties.Repository,
				Revision:        c.Properties.Revision,
				Wait:            c.Properties.Wait,
				Timeout:         c.Properties.Timeout,
				PackageLocation: c.Properties.PackageLocation,
				KeyLocation:     c.Properties.KeyLocation,
			},
		})
	}
	return comps
}

func buildDeploymentProfile(
	profile *ent.DeploymentProfile,
	components []*ent.Component) deployment.DeploymentProfile {
	return deployment.DeploymentProfile{
		Type:       profile.Type,
		Components: buildDeploymentComponents(components),
	}
}

func buildApplicationDeployment(
	appDesc *ent.ApplicationDesc,
	profile *ent.DeploymentProfile,
	components []*ent.Component,
	siteID string,
) deployment.ApplicationDeployment {
	// id := strings.ReplaceAll(appDesc.Name, " ", "-")
	// id = strings.ReplaceAll(id, "'", "")
	// id = strings.ToLower(id)
	// id = fmt.Sprintf("%s-%d", id, time.Now().Unix())
	id := GenerateDeploymentID()

	return deployment.ApplicationDeployment{
		APIVersion: "margo.edge/v1",
		Kind:       "Deployment",
		Metadata: deployment.Metadata{
			Name: appDesc.Name + "-deployment",
			Labels: map[string]string{
				"app": appDesc.Name,
			},
			Annotations: deployment.Annotations{
				ApplicationID: appDesc.AppID,
				ID:            id, 
				Version: appDesc.Version, 
			},
		},
		Spec: deployment.Spec{
			DeploymentProfile: buildDeploymentProfile(profile, components),
			Parameters:        buildDeployParameters(siteID),
		},
	}
}

// PushDeploymentYAML converts a deployment struct to YAML and pushes to GitHub
func PushDeploymentYAML(ctx context.Context,
	token, owner, repo, path, message string, appdply interface{}) error {
	// 1. Marshal to YAML
	yamlBytes, err := yaml.Marshal(appdply)
	if err != nil {
		return fmt.Errorf("failed to marshal YAML: %w", err)
	}

	// 2. Setup GitHub client with token
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(ctx, ts)
	client := github.NewClient(tc)

	// 3. Check if file exists to get SHA (needed to update)
	fileContent, _, resp, err := client.Repositories.GetContents(ctx, owner, repo, path, nil)
	var sha *string
	if resp != nil && resp.StatusCode == 200 && fileContent != nil {
		sha = fileContent.SHA
	}

	opts := &github.RepositoryContentFileOptions{
		Message: github.String(message),
		Content: yamlBytes,
		SHA:     sha, // nil if creating new file
	}

	if sha == nil {
		_, _, err = client.Repositories.CreateFile(ctx, owner, repo, path, opts)
	} else {
		_, _, err = client.Repositories.UpdateFile(ctx, owner, repo, path, opts)
	}

	if err != nil {
		return fmt.Errorf("failed to push file to GitHub: %w", err)
	}

	log.Println("YAML file successfully pushed to GitHub:", path)
	return nil
}

func CreateDeployment(c *gin.Context,co *co.CO,  client *ent.Client, repo string) {
	//log.Println("CreateDeployment called. Site:", cfg.Server.Site)
 
	//c.StartDeplotment

	start := time.Now()

	log.Println("Repo URL:", repo)

	var app App
	if err := c.ShouldBindJSON(&app); err != nil {
		log.Printf("❌ Error binding JSON: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	log.Printf("🌀 Incoming app: %+v\n", app)
	log.Println("Targeted Sites:", app.Sites)
	ctx := context.Background()

	// --- 1️⃣ Check if the app exists by ID or name ---
	q := client.ApplicationDesc.Query()

	// Build a conditional filter
	if app.AppID != "" {
		log.Println("searching id....", app.AppID)
		uid, err := uuid.Parse(app.AppID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid uuid"})
			return
		}		
		q = q.Where(applicationdesc.IDEQ(uid))
	} else if app.AppName != "" {
		log.Println("searching name....", app.AppName)
		q = q.Where(
			applicationdesc.CategoryEQ(app.Category),
			applicationdesc.NameEQ(app.AppName),
			applicationdesc.VersionEQ(app.Version),
		)
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"error": "app_id or app name must be provided"})
		return
	}

	existingApps, err := q.All(ctx)
	if err != nil {
		log.Printf("❌ DB query failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// --- 2️⃣ Validate uniqueness ---
	if len(existingApps) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "no matching app found in registry"})
		return
	}
	if len(existingApps) > 1 {
		c.JSON(http.StatusConflict, gin.H{"error": "multiple apps matched — ambiguous identifier"})
		return
	}

	appDesc := existingApps[0]
	log.Printf("✅ Found app match: %s (id=%s)\n", appDesc.Name, appDesc.ID)


	profile, err := client.DeploymentProfile.
		Query().
		Where(
			deploymentprofile.AppIDEQ(appDesc.ID),
			deploymentprofile.TypeEQ(app.DeployType),
		).
		Only(ctx)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "deployment profile not found"})
		return
	}
	log.Println("🔹 Processing deployment profile ID:", profile.ID)

	components, err := client.Component.
		Query().
		Where(component.DeploymentProfileIDEQ(profile.ID)).
		All(ctx)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "components  not found"})
		return
	}
	log.Println("components:", components)

	//TBD: create deployment for all the selected targets
	// 
	var deployments []string
	for _, site := range app.Sites {

		appdply := buildApplicationDeployment(appDesc, profile, components, site.SiteID)
		deploymentID := appdply.Metadata.Annotations.ID
		log.Println("deploymentID:", deploymentID)

		// 1. Marshal to YAML
		yamlBytes, err := yaml.Marshal(appdply)
		if err != nil {
			fmt.Errorf("failed to marshal YAML: %w", err)
			continue
			//return fmt.Errorf("failed to marshal YAML: %w", err)
		}
		err = co.CreateDeployment(site.SiteID, deploymentID, yamlBytes)
		if err != nil {
			fmt.Errorf("Failed to create deployments repo with err:", err)
		}
		// token := os.Getenv("GITHUB_TOKEN")

		// owner := cfg.Git.Owner //"edge-orchestration-platform"
		// repo := cfg.Git.Repo   //"deployments"
		// path := filepath.Join(site.SiteID, deploymentID, "desiredstate.yaml")
		// message := "Add new deployment"

		// log.Println("token:", token)
		// log.Println("owner:", owner)
		// log.Println("repo:", repo)
		// log.Println("path:", path)
		// //log.Println("message:", message)

		// if err := PushDeploymentYAML(ctx, token, owner, 
		// 				repo, path, message, appdply); err != nil {
		// 	log.Fatal(err)
		// }

		//TBD: create a deployment record in postgres using ent
		//      can be mapped to pkg/model DeploymentStatus
		//     deploymentid, status:pending, components: [{container1}, {container2}, ]
		//     This record can be part of deploymenthistory
		//dss, err := client.DeploymentStatus
		status := &model.DeploymentStatus{
			//APIVersion:   "deployment.margo/v1",
			//Kind:         "DeploymentStatus",
			DeploymentID: deploymentID,
			Status: model.DeploymentState{
				State: string(model.StatePending),
				Error: model.StatusError{},
			},
			// Components: []model.DeploymentComponent{
			// 	{Name: "digitron-orchestrator", State: string(model.StatePending)},
			// 	{Name: "database-services", State: string(model.StatePending)},
			// },
		}
		var cs []model.DeploymentComponent
		for _, c := range components {
			cs = append(cs, model.DeploymentComponent{	
				Name: c.Name, 
				State: string(model.StatePending),
			})
		}
		status.Components = cs
		

		SaveDeploymentStatus(ctx, client, status)

		deployments = append(deployments, deploymentID)
		log.Println("before calling metrics:", site.SiteID)
		metrics.DeploymentsTotal.WithLabelValues(deploymentID).Inc()
    	metrics.DeploymentsActive.WithLabelValues(deploymentID).Inc()
		log.Printf("✅ Successfully pushed deployment YAML for profile %s", profile.ID)
	}

	log.Printf("deployments done:", deployments)

	c.JSON(http.StatusOK, gin.H{
		"deployment_ids": deployments,
		"status":        "started",
	})	

	duration := time.Since(start).Seconds()
	metrics.RequestDuration.WithLabelValues("/deploy").Observe(duration)
	return
}

// func GetDeploymentStatus(c *gin.Context, client *ent.Client) {
// 	//id := c.Param("id")

// 	// ctx := context.Background()
// 	// deployment, err := client.DeploymentProfile.Query().Where(deploymentprofile.ID(id)).Only(ctx)
// 	// if err != nil {
// 	// 	c.JSON(http.StatusNotFound, gin.H{"error": "deployment.Get(ctx, id)) not found"})
// 	// 	return
// 	// }

// 	// c.JSON(http.StatusOK, deployment)

// 	// if err != nil {
// 	// 	c.JSON(http.StatusNotFound, gin.H{"error": "deployment not found"})
// 	// 	return
// 	// }

// 	// c.JSON(http.StatusOK, deployment)
// }

func HandleStreamDeployment(c *gin.Context, sm *streammanager.StreamManager) {
	id := c.Param("id")

	log.Println("HandleStreamDeployment called with id", id)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		http.Error(c.Writer, "Streaming unsupported!", http.StatusInternalServerError)
		return
	}

	ch := sm.Register(id)
	defer sm.Unregister(id, ch)

	c.Status(http.StatusOK)
	flusher.Flush()

	for ev := range ch {
		data, _ := json.Marshal(ev)
		fmt.Fprintf(c.Writer, "data:%s\n\n", data)
		flusher.Flush()

		if ev.Status == "completed" || ev.Status == "failed" {
			break
		}
	}

	// When done, ensure connection closes cleanly
	//c.Writer.Flush()
}

func GenerateDeploymentID() string {
	return uuid.New().String()
}


func SaveDeploymentStatus(ctx context.Context, client *ent.Client, ds *model.DeploymentStatus) error {
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

	deploy, err := tx.DeploymentStatus.
		Create().
		SetID(id).
		//SetAPIVersion(ds.APIVersion).
		//SetKind(ds.Kind).
		SetState(ds.Status.State).
		SetErrorCode(ds.Status.Error.Code).
		SetErrorMessage(ds.Status.Error.Message).
		Save(ctx)
	if err != nil {
		return tx.Rollback()
	}

	for _, c := range ds.Components {
		_, err = tx.DeploymentComponentStatus.
			Create().
			SetName(c.Name).
			SetState(c.State).
			SetErrorCode(c.Error.Code).
			SetErrorMessage(c.Error.Message).
			SetDeployment(deploy).
			Save(ctx)
		if err != nil {
			return tx.Rollback()
		}
	}

	return tx.Commit()
}


