package main

import (
    //"fmt"
    "io"
    "encoding/json"
    "os"
    "strings"
    "net/http"
    "path/filepath"
    "bytes"
    "errors"
    "runtime"
    
    "go.uber.org/zap"
    "github.com/google/uuid"
    "github.com/joho/godotenv"

    "github.com/balaji-balu/ieo/internal/en/runtimemgr"
    "github.com/balaji-balu/ieo/pkg/logx"
    //"github.com/balaji-balu/ieo/internal/config"
    "github.com/balaji-balu/ieo/internal/natsbroker"
    "github.com/balaji-balu/ieo/internal/en/heartbeat"
    //_ "github.com/balaji-balu/ieo/internal/en/plugins/containerd"
    _ "github.com/balaji-balu/ieo/internal/en/plugins/mock_containerd"
)
// sudo ctr -n ieo-en containers ls
// sudo ctr -n ieo-en tasks ls

// sudo ctr -n ieo-en tasks kill --signal KILL edge-ai-sample
// sudo ctr -n ieo-en tasks delete edge-ai-sample
// sudo ctr -n ieo-en containers delete edge-ai-sample
// func main() {
//     plugin := plugins.NewRuntimePlugin()

//     rm := runtimemanager.NewRuntimeManager(plugin)

//     comp := en.ComponentSpec{
//         Name:     "hello",
//         Runtime:  "wasm",
//         Artifact: "hello.wasm",
//     }

//     fmt.Println("Deploying component...")
//     rm.Deploy(comp)

//     fmt.Println("Status:", rm.GetStatus("hello"))
// }

type ENStorage struct {
    BaseDir string
    HostID  string
}

type ENConfig struct {
    Log struct {
        Level  string `koanf:"level"`
        Format string `koanf:"format"`
    } `koanf:"log"`

    NATS struct {
        URL      string `koanf:"url"`
        Username string `koanf:"username"`
        Password string `koanf:"password"`
    } `koanf:"nats"`

    LO struct {
        URL     string `koanf:"url"`
    } `koanf:"lo"`
    Host struct {
        HostID  string `koanf:"host_id"`
    } `koanf:"host"`    
}

var log *zap.SugaredLogger

func init() {
    if err := godotenv.Load("./.env"); err != nil {
		//log.Println("No .env file found, reading from system environment")
	}
    //flag.StringVar(&cfgFlag, "config", "", "config root directory")
}

func main() {
    logx.Init(logx.Options{
        Env:     os.Getenv("APP_ENV"),     // dev / prod
        //Version: "0.1.0",
    })    
    log = logx.New("en")
    log.Infow("EN starting", "pid", os.Getpid())
/*
    options := config.Options{
        AppName: "ieo",
        Unit: "en",
        Env: os.Getenv("APP_ENV"),
    }
    rootDir, err := config.RootDir(options) 
    if err != nil {
        log.Fatalw("rootdir detection error","error", err)
    }

    log.Debugw("root directory", "rootDir", rootDir)
    //cfgPath := filepath.Join("./configs", options.Unit, "dev.yaml")
    //options.RootDir = rootDir
    var cfg ENConfig
    if err := config.Load(options, &cfg); err != nil {
        log.Fatalw("Configution load error","error", err)
    }    
    
    log.Debugw("loaded config", "cfg:", cfg)

    // if err := loader.Load(&cfg); err != nil {
    //     log.Errorw("config load err", err)
    // }    
    //log.Infow("Loaded EN config:", "config", cfg)    
*/

    natsUrl := os.Getenv("IEO_EN_NATS_URL") 
    log.Infow("📡 Connecting to ","NATS at", natsUrl)
	nb, err := natsbroker.New(natsUrl)
	if err != nil {
		log.Errorf("❌ Failed to connect to NATS.","err:", err)
        return
	}
    ls, err := InitENStorage() //uuid.New().String()
    log.Infow("localstorage", "ls", ls)
    hostID := ls.HostID
    loUrl := os.Getenv("IEO_EN_LO_URL")
    siteID, err := register(loUrl, hostID)
    if err != nil {
        log.Errorf("❌ Unable to Register with LO","err:", err)
        return
    }
    log.Infow("LO", "siteid", siteID)
    
    heartbeat.StartHeartbeat(nb, log, siteID, hostID)

    rm := runtimemgr.NewRuntimeManager( nb, siteID, hostID, "mock-containerd", log)
    rm.LoActionDispatcher()
   
    select{}

}

func register(loURL, hostID string) (string, error) {
    // Prepare payload
    payload := map[string]string{
        "host_id": hostID,
    }

    b, err := json.Marshal(payload)
    if err != nil {
        log.Errorw("failed to serialize payload:", "err", err)
        return "", err
    }

    // Create request
    req, err := http.NewRequest("POST", loURL+"/register", bytes.NewBuffer(b))
    if err != nil {
        log.Errorw("failed to create request:","err", err)
        return "", err
    }
    req.Header.Set("Content-Type", "application/json")

    // Make request
    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        log.Errorw("register request failed: ","err", err)
        return "", err
    }
    defer resp.Body.Close()

    // Check for non-OK status
    if resp.StatusCode != http.StatusOK {
        body, _ := io.ReadAll(resp.Body)
        log.Errorw("LO returned", "statuscode", resp.StatusCode, "body", string(body))
        return "", errors.New("returned non ok status") 
    }

    // Parse LO's response (siteID)
    type Response struct {
        SiteID string `json:"siteId"`
        HostID string `json:"hostId"`
    }
    var response Response
    if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
        log.Errorw("failed to decode LO response: ","err", err)
        return "", err
    }

    log.Debugw("register response", "reponse", response)
    return response.SiteID, nil
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

func InitENStorage() (*ENStorage, error) {
    baseDir := ENBaseDir() // cross-platform version same as LOBaseDir

    if err := os.MkdirAll(baseDir, 0755); err != nil {
        return nil, err
    }

    hostID, err := loadOrCreateID(baseDir, "host_id")
    if err != nil {
        return nil, err
    }

    return &ENStorage{
        BaseDir: baseDir,
        HostID:  hostID,
    }, nil
}

func ENBaseDir() string {
    if os.Getenv("APP_ENV") == "development" {
        home, _ := os.UserHomeDir()
        return filepath.Join(home, ".ieo", "en")
    }

    switch runtime.GOOS {
    case "windows":
        return filepath.Join(os.Getenv("ProgramData"), "ieo", "en")

    case "darwin":
        return filepath.Join("/Library/Application Support", "ieo", "en")

    default:
        return "/var/lib/ieo/en"
    }
}
