package lo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/balaji-balu/ieo/internal/lo/actuators"
	"github.com/balaji-balu/ieo/internal/lo/boltstore"
	"github.com/balaji-balu/ieo/internal/lo/heartbeat"
	"github.com/balaji-balu/ieo/internal/metrics"
	"github.com/balaji-balu/ieo/internal/natsbroker"
	"github.com/balaji-balu/ieo/pkg/model"
)

type LoConfig struct {
	NatsUrl string
	Site    string
}

type LocalOrchestrator struct {
	Config  LoConfig
	httpClient *http.Client
	Hosts []string
	CoUrl		string
	rb 			*ResultBus
	RootCtx 	context.Context
	nc      	*natsbroker.Broker
	store 	*boltstore.StateStore
	monitor 	*heartbeat.Monitor	
	log      *zap.SugaredLogger
}

func NewLO(
	ctx context.Context,
	siteID string, 
	boltDb string,
	natsURL,
	coURL string,
	//boltz *bolt.DB,
	//db *ent.Client,
	nc *natsbroker.Broker,
	metrics_port string,
	log *zap.SugaredLogger,
) *LocalOrchestrator {

	
	rb := NewResultBus()

	log.Debugw("LocalOrchestrator.new enter ")

	store, err:= boltstore.NewStateStore(boltDb)
	if err != nil {
		log.Errorf("store create error", "err", err)
		return nil
	}
	monitor := heartbeat.NewMonitor(10*time.Second, 3, store) // EN heartbeat every ~10 sec, max 3 misses
	monitor.Start()

	metrics.Init("lo")
	metrics.StartServer(metrics_port)

	// The actuator records the status reports ENs send over NATS in the store. Only the Git path
	// sent work to ENs through it; the Margo path reaches ENs in roadmap slice D.
	actuators.NewNatsActuator(store, nc, coURL, siteID, 30)

	log.Debugw("LocalOrchestrator.new exiting  ")
	return &LocalOrchestrator{
		Config: LoConfig{
			NatsUrl: natsURL, //cfg.NATS.URL,
			Site:    siteID,  //cfg.Server.Site,
		},
		log: log,
		rb:     rb,
		RootCtx: ctx,
		//db:      db,
		nc:      nc,
		//Store: store,
		monitor: monitor,
		store: store,
		CoUrl: coURL,
		httpClient: &http.Client{
            Timeout: 10 * time.Second,
        },

	}

}

func (l *LocalOrchestrator) Start(coURL string) {
	if err := l.RegisterSite(); err != nil {
		l.log.Errorw("err", err)
	}

	l.MonitorHealthandStatusFromEN(l.monitor, coURL)
}

func (l *LocalOrchestrator) HandlerGetActual(c *gin.Context) {
	actual, err := l.store.GetActual()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, actual)	
}

func (l *LocalOrchestrator) HandlerGetHosts(c *gin.Context) {
	hosts, err := l.store.LoadAllHosts()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, hosts)
}

type RegisterSiteRequest struct {
    ID   string `json:"id" binding:"required"`
    Name string `json:"name" binding:"required"`
}

func (l *LocalOrchestrator) RegisterSite() error {
    reqBody := RegisterSiteRequest{
        ID:   l.Config.Site,
        Name: "site-id-1", //l.SiteName,
    }

    body, err := json.Marshal(reqBody)
    if err != nil {
        return fmt.Errorf("marshal error: %w", err)
    }

    url := fmt.Sprintf("%s/api/v1/register", l.CoUrl)

    req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
    if err != nil {
        return fmt.Errorf("create request error: %w", err)
    }

    req.Header.Set("Content-Type", "application/json")

    resp, err := l.httpClient.Do(req)
    if err != nil {
        return fmt.Errorf("post error: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusCreated {
        b, _ := io.ReadAll(resp.Body)
        return fmt.Errorf("CO rejected: %s", string(b))
    }

    return nil
}
/*
func (l *LocalOrchestrator) RegisterEN(c *gin.Context) {
    var req struct {
        HostID string `json:"host_id"`
    }

    if err := c.BindJSON(&req); err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
        return
    }     

    if req.HostID == "" {
        c.JSON(http.StatusBadRequest, gin.H{"error": "hostID missing"})
        return
    }

	// store host id 
	if err := l.store.AddOrUpdateHost(model.Host{ID: req.HostID}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	} 

	// return site id
	c.JSON(http.StatusOK, l.Config.Site)
}
*/
func (l *LocalOrchestrator) RegisterEN(c *gin.Context) {
    var req struct {
        HostID   string            `json:"host_id"`
        // additional fields you might want to pass, e.g.
        // Hostname string `json:"hostname"`
        // Info     map[string]interface{} `json:"info"`
    }

    if err := c.BindJSON(&req); err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json", "details": err.Error()})
        return
    }

    if req.HostID == "" {
        c.JSON(http.StatusBadRequest, gin.H{"error": "host_id missing"})
        return
    }

    // 1. Store host locally
    if err := l.store.AddOrUpdateHost(model.Host{ID: req.HostID}); err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to store host", "details": err.Error()})
        return
    }

    // 2. Send host registration to CO
    // Build request payload for CO
    postBody := struct {
        SiteID  string `json:"siteId"`
        HostID  string `json:"hostId"`
		Hostname string `json:"hostname"`
        // include more fields if needed (hostname, metadata, etc.)
    }{
        SiteID: l.Config.Site,   // assuming Site is string ID of your site
        HostID: req.HostID,
		Hostname: "host1",
    }

    bodyBytes, err := json.Marshal(postBody)
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to marshal CO request", "details": err.Error()})
        return
    }

    // Example CO URL: http://co-server:8080/register/host  (adapt as needed)
    coURL := fmt.Sprintf("%s/api/v1/register/%s", l.CoUrl, req.HostID)

    httpReq, err := http.NewRequest("POST", coURL, bytes.NewBuffer(bodyBytes))
    if err != nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create CO request", "details": err.Error()})
        return
    }
    httpReq.Header.Set("Content-Type", "application/json")

    resp, err := l.httpClient.Do(httpReq)
    if err != nil {
        c.JSON(http.StatusBadGateway, gin.H{"error": "failed to reach CO", "details": err.Error()})
        return
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
        respBytes, _ := io.ReadAll(resp.Body)
        c.JSON(http.StatusBadGateway, gin.H{
            "error":   "CO rejected host registration",
            "details": string(respBytes),
        })
        return
    }

    // 3. Return success + site id or CO response
    c.JSON(http.StatusOK, gin.H{
        "siteId": l.Config.Site,
        "hostId": req.HostID,
    })
}
