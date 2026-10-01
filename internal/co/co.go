package co

import (
	//"context"
	"fmt"
	"os"
	"path/filepath"
	//"github.com/balaji-balu/ieo/internal/gitobserver"
	//"github.com/balaji-balu/ieo/internal/natsbroker"
	"github.com/balaji-balu/ieo/internal/gitmanager"
)

// CO uses gitmanager to read app-registry and write deployments
type CO struct {
	Mgr *gitmanager.Manager
	AppRepo string
	DepRepo string
}

func NewCO(m *gitmanager.Manager, appRepo, depRepo string) *CO {
	return &CO{Mgr: m, AppRepo: appRepo, DepRepo: depRepo}
}

// CreateDeployment writes a desiredstate.yaml under deployments/<site>/<deploymentID>/desiredstate.yaml
func (c *CO) CreateDeployment(siteID, deploymentID string, yaml []byte) error {
	fmt.Println("CreateDeployment: enter")
	rel := filepath.Join(siteID, deploymentID, "desiredstate.yaml")
	fmt.Println("xxxxxxxxxxxxxxxx rel", rel)

	// write file into working path
	cfg, _ := c.Mgr.GetConfig(c.DepRepo)
	full := filepath.Join(cfg.WorkingPath, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		fmt.Println("err................")
		return err
	}
	if err := os.WriteFile(full, yaml, 0644); err != nil {
		return err
	}
	// commit and push
	msg := fmt.Sprintf("CO: create deployment %s for site %s", deploymentID, siteID)
	fmt.Println("CreateDeployment: exit", msg)
	return c.Mgr.CommitAndPush(c.DepRepo, rel, msg)
}

// ReadApp reads a file from app-registry
func (c *CO) ReadApp(path string) ([]byte, error) {
	
	return c.Mgr.ReadFile(c.AppRepo, path)
}

