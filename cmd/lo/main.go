// file: cmd/lo/main.go
package main

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"os"
	"os/signal"
	"syscall"
	"time"
	"runtime"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/pkg/logx"
	"github.com/balaji-balu/ieo/internal/lo"
	//"github.com/balaji-balu/ieo/internal/config"
	"github.com/balaji-balu/ieo/internal/natsbroker"
	"github.com/balaji-balu/ieo/internal/gitmanager"

)

type LOStorage struct {
    BaseDir   string
    SiteID    string
    BoltPath  string
}

func init() {
	if err := godotenv.Load("./.env"); err != nil {
		//log.Println("No .env file found, reading from system environment")
	}
}

type LoConfig struct{
	Port 		string
	MetricsPort string
    NATS struct {
        URL      string `koanf:"url"`
	}
	CO struct {
		URL		string `koanf:"url"`
	}
}

func main() {

	// ------------------------------------------------------------
	// 1️⃣ Context + Logger setup
	// ------------------------------------------------------------
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

    logx.Init(logx.Options{
        Env:     os.Getenv("APP_ENV"),     // dev / prod
        //Version: "0.1.0",
    })    
    log := logx.New("lo")
    log.Infow("LO starting", "pid", os.Getpid())

	loStorage, err := InitLOStorage() //(getBaseDir("LO"), "site_id")
	if err != nil {
		log.Errorw("config load err", "err", err)
		return
	}
	log.Infow("lostorage", "", loStorage)

	// loader := config.New()
    // var cfg LoConfig
    // if err := loader.Load(&cfg); err != nil {
    //     log.Errorw("config load err", "err", err)
    // } 	
	// log.Infow("Loaded LO config:", "config", cfg)
    cfg := LoConfig{}
	cfg.Port = os.Getenv("LO_PORT")
	cfg.NATS.URL = os.Getenv("LO_NATS_URL")
	cfg.MetricsPort = os.Getenv("LO_METRICS_PORT")
	cfg.CO.URL = os.Getenv("LO_CO_URL")
	boltdbpath := os.Getenv("BOLTDB_PATH")
	if boltdbpath == "" {
		boltdbpath = loStorage.BoltPath
	}
	log.Infow("Connecting to", "nats(url)", cfg.NATS.URL)
	nc, err := natsbroker.New(cfg.NATS.URL)
	if err != nil {
		log.Errorw("nats connect:", "err", err)
		return
	}
	log.Infow("connected to", "nats url", cfg.NATS.URL)

	gitmgr := gitmanager.NewManager()
	gitmgr.Register(gitmanager.RepoConfig{
		Name: "deployments",
		Mode: gitmanager.GitRemote, //, GitLocal
		RemoteURL: "https://github.com/edge-orchestration-platform/deployments.git",
		//LocalPath: "/home/balaji/local-deployments",
		Branch: "main",
		Token: os.Getenv("GITHUB_TOKEN"),
		WorkingPath: "/tmp/deployments-lo",
	})

	// ------------------------------------------------------------
	// 2️⃣ Setup orchestrator + FSM loader
	// ------------------------------------------------------------

	localorch := lo.NewLO(ctx, 
		loStorage.SiteID,
		boltdbpath, //loStorage.BoltPath, 
		cfg.NATS.URL, 
		cfg.CO.URL, 
		"deployments", nc, gitmgr, cfg.MetricsPort, log)
	if localorch == nil {
		log.Errorw("localorch is nil")
		return
	}

	log.Infow("🚀 Starting adaptive mode manager...")

	// ------------------------------------------------------------
	// 3️⃣ Setup Gin router
	// ------------------------------------------------------------
	r := gin.Default()
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	r.GET("/hosts", localorch.HandlerGetHosts)
	r.GET("/actual", localorch.HandlerGetActual)

	r.POST("/register", localorch.RegisterEN)
	//r.POST("/deployment_status", lo.DeployStatus)

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%s", cfg.Port),
		Handler: r,
	}

	// ------------------------------------------------------------
	// 4️⃣ Start orchestrator and HTTP server
	// ------------------------------------------------------------
	//go lo.StartModeLoop(ctx)
	localorch.Start(cfg.CO.URL) // NetworkMonitor(ctx)

	go func() {
		log.Infow("🌐 HTTP server started on :", "port", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Errorw("HTTP server crashed", err)
			return
		}
	}()

	// ------------------------------------------------------------
	// 5️⃣ Handle shutdown signals
	// ------------------------------------------------------------
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	<-stop
	log.Infow("🛑 Shutdown signal received...")
	cancel() // broadcast cancel to all goroutines

	// ------------------------------------------------------------
	// 6️⃣ Gracefully stop HTTP server
	// ------------------------------------------------------------
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalw("HTTP server forced to shutdown", "err", err)
	} else {
		log.Infow("HTTP server shutdown gracefully")
	}

	// ------------------------------------------------------------
	// 7️⃣ Final cleanup
	// ------------------------------------------------------------
	time.Sleep(500 * time.Millisecond)
	log.Infow("🧹 All systems stopped. Goodbye!")
}

func InitLOStorage() (*LOStorage, error) {
    baseDir := LOBaseDir("lo") // cross-platform base dir (linux/macos/windows)

    // 1. Ensure base directory exists
    if err := os.MkdirAll(baseDir, 0755); err != nil {
        return nil, err
    }

    // 2. Load or create site_id
    siteID, err := loadOrCreateID(baseDir, "site_id")
    if err != nil {
        return nil, err
    }

    // 3. Setup BoltDB directory
    dbDir := filepath.Join(baseDir, "db")
    if err := os.MkdirAll(dbDir, 0755); err != nil {
        return nil, err
    }

    boltPath := filepath.Join(dbDir, "bolt.db")

    return &LOStorage{
        BaseDir:  baseDir,
        SiteID:   siteID,
        BoltPath: boltPath,
    }, nil
}

func loadOrCreateID(baseDir, name string) (string, error) {
    idPath := filepath.Join(baseDir, name)

    if data, err := os.ReadFile(idPath); err == nil {
        id := strings.TrimSpace(string(data))
        if id != "" {
            return id, nil
        }
    }

    id := uuid.New().String()

    os.MkdirAll(baseDir, 0755)
    os.WriteFile(idPath, []byte(id), 0644)

    return id, nil
}

func LOBaseDir(app string) string {
	if os.Getenv("APP_ENV") == "development" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".lo")
	}

    switch runtime.GOOS {
    case "windows":
        return filepath.Join(os.Getenv("ProgramData"), app)
    case "darwin":
        return filepath.Join("/Library/Application Support", app)
    default: // linux, unix, others
        return filepath.Join("/var/lib", strings.ToLower(app))
    }
}
