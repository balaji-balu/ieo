package api

import (
	"log"
	"os"
	"github.com/gin-gonic/gin"

	"github.com/balaji-balu/ieo/ent"
	"github.com/balaji-balu/ieo/internal/api/handlers"
	"github.com/balaji-balu/ieo/internal/api/middleware"
	//"github.com/balaji-balu/ieo/internal/config"
	"github.com/balaji-balu/ieo/internal/streammanager"
	"github.com/balaji-balu/ieo/internal/gitfetcher"
	"github.com/balaji-balu/ieo/internal/co"
	"github.com/balaji-balu/ieo/pkg/co/model"
)

func NewRouter(client *ent.Client, co *co.CO, cfg model.COConfig) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	//.SetMode(gin.ReleaseMode)
	r.Use(gin.Logger())
	r.Use(gin.Recovery())
	r.Use(middleware.CORSMiddleware())

	sm := streammanager.NewStreamManager()

	log.Println("App registry details:",
		cfg.Appregistry.Repo,
		cfg.Appregistry.Branch,
	)
	fetcher := gitfetcher.GitFetcher{
		RepoURL:  cfg.Appregistry.Repo, //"https://github.com/edge-orchestration-platform/app-registry",
		Branch:   cfg.Appregistry.Branch,
		LocalDir: "./cache/app-registry",
		Token: os.Getenv("GITHUB_TOKEN"),
	}

	api := r.Group("/api/v1")
	{
		api.GET("/apps", func(c *gin.Context) { handlers.ListApps(c, client) })
		api.POST("/apps", func(c *gin.Context) { 
			handlers.CreateApp(c, client, &fetcher) })
		api.GET("/apps/:id", func(c *gin.Context) { handlers.GetApp(c, client) })
		api.DELETE("/apps", func(c *gin.Context) { handlers.DeleteApp(c, client)})	

		api.POST("/deployments/:id/status", func(c *gin.Context) { 
			handlers.DeploymentStatusHandler(c, client, sm) })
		api.GET("/deployments", func(c *gin.Context){
			handlers.ListDeploymentsStatus(c, client) })
		api.GET("/deployments/:id/status", func(c *gin.Context) { 
			handlers.GetDeploymentStatus(c, client) })
		api.GET("/deployments/:id/stream", func(c *gin.Context) { 
			handlers.HandleStreamDeployment(c, sm) })			
		api.POST("/deployments", func(c *gin.Context) { 
			handlers.CreateDeployment(c,co, client, cfg.Git.Repo) })

		api.GET("/sites", func(c *gin.Context){ handlers.ListSites(c,client)})	
		api.GET("/sites/:siteId", func(c *gin.Context){handlers.GetSiteInfo(c,client)})	
		api.POST("/register", func(c *gin.Context) {handlers.RegisterSite(c,client)})
		api.POST("/register/:hostId", func(c *gin.Context) {handlers.RegisterHost(c,client)})

		api.GET("/healthz", handlers.HealthzHandler)
	}

	return r
}
