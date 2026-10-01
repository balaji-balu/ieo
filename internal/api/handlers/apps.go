package handlers

import (
	"context"
	"net/http"
	"log"
	"fmt"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-yaml"
	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/ent"
	"github.com/balaji-balu/ieo/ent/applicationdesc"
	"github.com/balaji-balu/ieo/ent/deploymentprofile"
	"github.com/balaji-balu/ieo/ent/component"	
	"github.com/balaji-balu/ieo/pkg/application"
	//"github.com/balaji-balu/ieo/internal/co"
	"github.com/balaji-balu/ieo/internal/gitfetcher"
)
type AppRequest struct {
	Category string `json:"category" binding:"required"`
	AppName string `json:"app_name" binding:"required"`
	Version string `json:version" binding:"required"`
	RepoURL string `json:"repo_url" `
}

func ListApps(c *gin.Context, client *ent.Client) {
	//category := r.URL.Query().Get("category")
	//appName  := r.URL.Query().Get("app_name")
	//version  := r.URL.Query().Get("version")

	category := c.Query("category")
	appName  := c.Query("app_name")
	version  := c.Query("version")

	q := client.ApplicationDesc.Query()

	// Apply filters only if provided
	if category != "" {
		q = q.Where(applicationdesc.Category(category))
	}
	if appName != "" {
		q = q.Where(applicationdesc.Name(appName))
	}
	if version != "" {
		q = q.Where(applicationdesc.Version(version))
	}

	apps, err := q.All(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"apps": apps})
}

func GetApp(c *gin.Context, client *ent.Client) {
   	idStr := c.Param("id")

    // Convert string → uuid.UUID
    uid, err := uuid.Parse(idStr)
    if err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": "invalid uuid"})
        return
    }

	app, err := client.ApplicationDesc.Get(c, uid)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "app not found"})
		return
	}
	c.JSON(http.StatusOK, app)
}

func CreateApp(c *gin.Context, client *ent.Client, fetcher *gitfetcher.GitFetcher) {

	// verify already added, if yes, reject
	//post content : app name and app repo url
	// git private repo read margo.yaml file
	// then parse the yaml.
	// persist the application contents 
	var req AppRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	appName := req.AppName // or from query, form, etc.
	category := req.Category
	version := req.Version

	if req.RepoURL == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "repo url req"})
		return
	}

	log.Println("appName", appName, "category", category, "version", version)
	q := client.ApplicationDesc.Query()
	if category != "" {
		q = q.Where(applicationdesc.CategoryEQ(category))
	}
	if appName != "" {
		q = q.Where(applicationdesc.NameEQ(appName))
	}
	if version != "" {
		q = q.Where(applicationdesc.VersionEQ(version))
	}
	apps, err := q.Exist(c)

	// apps, err := client.ApplicationDesc.
	// 	Query().
	// 	Where(
	// 		applicationdesc.CategoryEQ(category),
    //     	applicationdesc.NameEQ(appName),
    //     	applicationdesc.VersionEQ(version),
	// 		//applicationdesc.NameContainsFold(appName), // case-insensitive partial match
	// 	).
	// 	Exist(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	log.Println("apps matched:", apps)

	if apps == true {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "duplicate apps found"})
		return
	}
/*
    TBD: Gitmanager Issue: locks. cannot use 
	path := fmt.Sprintf("%s/%s/%s", category, appName, version)
	log.Println("path:", path)
	content, err := co.ReadApp(path)
	if err != nil {
		log.Printf("❌ failed to fetch resource: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
*/			
	
	fetcher.RepoURL = req.RepoURL
	path := fmt.Sprintf("%s/%s/%s", category, appName, version)
	log.Println("path:", path)
	content, err := fetcher.FetchAppResource(path, "margo.yaml")
	if err != nil {
		log.Printf("❌ failed to fetch resource: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	log.Println("📄 margo.yaml contents:\n", string(content))

	//var app ent.ApplicationDesc
	var appDesc application.ApplicationDescription
	if err := yaml.Unmarshal([]byte(content), &appDesc); err != nil {
		log.Printf("❌ failed to unmarshall resource: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}	

	// appDesc, err := application.ParseFromFile("./tests/app3.yaml")
	// if err != nil {
	// 	c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	// 	return
	// }

	err = Persist(context.Background(), client, appName, category, &appDesc)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, appDesc)
}

func DeleteApp(c *gin.Context, client *ent.Client) {
    var req AppRequest
    if err := c.ShouldBindJSON(&req); err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
        return
    }

    category := req.Category
    appName := req.AppName
    version := req.Version

    q := client.ApplicationDesc.Query()

    if category != "" {
        q = q.Where(applicationdesc.CategoryEQ(category))
    }
    if appName != "" {
        q = q.Where(applicationdesc.NameEQ(appName))
    }
    if version != "" {
        q = q.Where(applicationdesc.VersionEQ(version))
    }

    // Get exactly one app
    app, err := q.Only(c)
    if err != nil {
        if ent.IsNotFound(err) {
            c.JSON(http.StatusNotFound, gin.H{"error": "app not found"})
            return
        }
        c.JSON(http.StatusBadRequest, gin.H{"error": "multiple apps match criteria"})
        return
    }

    // ------------------------------------------------------------------------------------
    // Delete components -> delete deployment profiles -> delete application
    // ------------------------------------------------------------------------------------

    // 1. Fetch all deployment profiles belonging to this app
    dps, err := client.DeploymentProfile.
        Query().
        Where(deploymentprofile.AppID(app.ID)).
        All(c)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to fetch deployment profiles: " + err.Error()})
        return
    }

    // 2. For each DP, delete its components
    for _, dp := range dps {
        _, err = client.Component.
            Delete().
            Where(component.DeploymentProfileID(dp.ID)).
            Exec(c)
        if err != nil {
            c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete components: " + err.Error()})
            return
        }
    }

    // 3. Delete all deployment profiles for this app
    _, err = client.DeploymentProfile.
        Delete().
        Where(deploymentprofile.AppID(app.ID)).
        Exec(c)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete deployment profiles: " + err.Error()})
        return
    }

    // 4. Delete application itself
    err = client.ApplicationDesc.DeleteOneID(app.ID).Exec(c)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
        return
    }

    c.JSON(http.StatusOK, gin.H{
        "status":    "deleted",
        "id":        app.ID,
        "name":      app.Name,
        "version":   app.Version,
        "category":  app.Category,
    })
}
