package main

import (
    //"context"
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	_ "github.com/lib/pq" // enables the 'postgres' driver
	"github.com/joho/godotenv"
	"google.golang.org/grpc"
	"go.uber.org/zap"
	
	"github.com/balaji-balu/ieo/pkg/logx"
	"github.com/balaji-balu/ieo/pkg/co/model"
	"github.com/balaji-balu/ieo/ent"
	"github.com/balaji-balu/ieo/internal/api"
	//"github.com/balaji-balu/ieo/internal/config"
	"github.com/balaji-balu/ieo/internal/gitmanager"
	"github.com/balaji-balu/ieo/internal/metrics"
	"github.com/balaji-balu/ieo/internal/co"
)


func init() {
	err := godotenv.Load("./.env") // relative path to project root
	if err != nil {
		//log.Println("No .env file found, reading from system environment")
	}
	
}

func main() {
	//ctx := context.Background()
    logx.Init(logx.Options{
        Env:     os.Getenv("APP_ENV"),     // dev / prod
        //Version: "0.1.0",
    })    
    log := logx.New("co")	
    port := os.Getenv("CO_PORT")
	metrics_port := os.Getenv("CO_METRICS_PORT")

    // options := config.Options{
    //     AppName: "ieo",
    //     Unit: "co",
    //     Env: os.Getenv("APP_ENV"),
    // }
    // rootDir, err := config.RootDir(options) 
    // if err != nil {
    //     log.Fatalw("rootdir detection error","error", err)
    // }

    // log.Debugw("root directory", "rootDir", rootDir)
    // //cfgPath := filepath.Join("./configs", options.Unit, "dev.yaml")
    // //options.RootDir = rootDir
    // var cfg model.COConfig
    // if err := config.Load(options, &cfg); err != nil {
    //     log.Fatalw("Configution load error","error", err)
    // }    
    cfg := model.COConfig{}
cfg.Git.Repo = "deployments"
cfg.Git.Branch = "main"

cfg.Appregistry.Repo = "https://github.com/edge-orchestration-platform/app-registry"
cfg.Appregistry.Branch = "main"

    // log.Debugw("loaded config", "cfg:", cfg)

    log.Infow("CO starting", "pid", os.Getpid())
	log.Infow("conf done")

	grpcPort := flag.String("grpc", ":50051", "CO gRPC listen address")
	// versionFlag := flag.Bool("version", false, "Print the version and exit")
	// flag.Parse()
    // if *versionFlag {
    //     fmt.Println("CO version:", cfg.Server.Version)
    //     os.Exit(0)
    // }	

	dsn := os.Getenv("DATABASE_URL")
	log.Infow("connecting to postgres at", zap.String("dsn:", dsn))

	var drv *sql.Driver
	var err1 error
	for i := 1; i <= 10; i++ {
		drv, err1 = sql.Open(dialect.Postgres, dsn)
		if err1 == nil {
			if err1 = drv.DB().Ping(); err1 == nil {
				log.Infow("✅ Connected to Postgres")
				break
			}
		}
		log.Infow("⏳ Waiting for Postgres \n", zap.Int("attempt...", i))
		time.Sleep(3 * time.Second)
	}
	if err1 != nil {
		log.Errorw("❌ Failed to connect to Postgres after retries.", err1)
		return
	}

	client := ent.NewClient(ent.Driver(drv))
	defer client.Close()

// if err := client.Schema.Create(ctx); err != nil {
//     log.Fatalf("failed creating schema resources: %v", err)
// }	
	metrics.Init("co")
	metrics.StartServer(metrics_port)

	gitm := gitmanager.NewManager()

	gitm.Register(gitmanager.RepoConfig{
		Name: "app-registry",
		Mode: gitmanager.GitRemote, // or GitLocal
		RemoteURL: "https://github.com/edge-orchestration-platform/app-registry.git",
		Branch: "main",
		WorkingPath: "/tmp/app-registry",
	})

	gitm.Register(gitmanager.RepoConfig{
		Name: "deployments",
		Mode: gitmanager.GitRemote, //GitLocal, //,
		RemoteURL: "https://github.com/edge-orchestration-platform/deployments.git",
		//LocalPath: "/home/balaji/local-deployments",
		Branch: "main",
		Token: os.Getenv("GITHUB_TOKEN"),
		WorkingPath: "/tmp/deployments-co",
	})
	if err := gitm.InitRepo("deployments"); err != nil {
    	log.Errorw("Git initrepo failed", "err", err)
	}
	cfg1, err := gitm.GetConfig("deployments")
	if err != nil {
		log.Errorw("config", "err", err)
	}
	log.Infow("CONFIG: \n", "config", cfg1)
	//fmt.Printf("CONFIG: %+v\n", gitm.GetConfig("deployments"))	
	c := co.NewCO(gitm, "app-registry", "deployments")

	router := api.NewRouter(client, c, cfg)
	log.Infow("CO API running on :", "", port)
	if err := router.Run(fmt.Sprintf(":%s", port)); err != nil {
		log.Errorw("Router init failed", "err", err)
		return
	}

	// Start gRPC server for callbacks from LO
	go func() {
		lis, err := net.Listen("tcp", *grpcPort)
		if err != nil {
			log.Errorw("[CO] failed to listen:","", err)
		}
		s := grpc.NewServer()
		//pb.RegisterCentralOrchestratorServer(s, &server{})
		log.Infow("[CO] gRPC listening on", "", *grpcPort)
		if err := s.Serve(lis); err != nil {
			log.Errorw("[CO] serve: ", "err", err)
			return
		}
	}()
}


