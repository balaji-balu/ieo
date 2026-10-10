package model

import (
	"time"
	"github.com/google/uuid"
)

type Host struct {
	ID    string `json:"id"`
	Alive bool   `json:"alive"`
}

//
//---------------- Desired state ----------------
//
type ComponentSpec struct {
	Name       string `yaml:"name"`
	Properties struct {
		PackageURL string `yaml:"packageLocation,omitempty"`
		KeyURL     string `yaml:"keyLocation,omitempty"`
		Repository string `yaml:"repository,omitempty"`
		Revision   string `yaml:"revision,omitempty"`
		Wait       *bool  `yaml:"wait,omitempty"`
		Timeout    string `yaml:"timeout,omitempty"`
		NodeSelector map[string]string `yaml:"nodeSelector,omitempty"`
	} `yaml:"properties"`
}

//
// TBD: repetition of pkg/deployment
//
type ApplicationDeployment struct {
	Metadata struct {
		Annotations struct {
			ID string `yaml:"id"`
			ApplicationID string `yaml:"applicationId"`
			Version		  string `yaml:"version"` //not there in spec. exception.
		} `yaml:"annotations"`
	} `yaml:"metadata"`
	Spec struct {
		DeploymentProfile struct {
			Type       string          `yaml:"type"`
			Components []ComponentSpec `yaml:"components"`
		} `yaml:"deploymentProfile"`
	} `yaml:"spec"`
}

type Component struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Content string `json:"content,omitempty"` // optional for hash
	Hash    string `json:"hash,omitempty"`
	Repository string `json:"repository"`
	PackageURL string `json:"package_url"`
	KeyURL string `json:"key_url"`
}

type App struct {
	ID         string               `json:"id"`
	Version    string               `json:"version"`
	DepType	   string 				`json:"dep_type"`	
	Components map[string]Component `json:"components"`
}

//
//---------------- Actual state ----------------
//
type ActualComponent struct {
	Name string `json:"name"`
	Status      string `json:"status"`      // success/failed/pending
	Version     string `json:"version"`     // deployed version
	LastUpdated int64  `json:"last_updated"`
	Hash        string `json:"hash"`
}

type ActualApp struct {
	ID         string               `json:"id"`
	Version    string                      `json:"version"`
	Components map[string]ActualComponent `json:"components"`
	Hash        string `json:"hash"`

}

type ActualState struct {
	AppsByHost map[string]map[string]ActualApp `json:"apps_by_host"` // hostID -> appID -> ActualApp
}


//
//-------------- Operation ----------------
//
type Action string
const (
    ActionAddApp    Action = "add_app"
    ActionUpdateApp Action = "update_app"
	ActionRemoveApp Action = "remove_app"
	
    ActionAddComp   Action = "add_comp"
	ActionUpdateComp Action = "update_comp"
	ActionRemoveComp Action = "remove_comp"
)

// DiffOp represents a deployment operation to be applied on a host
type DiffOp struct {
	Action  		Action `json:"action"`    // add_app, update_app, remove_app, add_comp, update_comp, remove_comp
	SiteID  		string `json:"site_id"`
	HostID  		string `json:"host_id"`
	App				App `json:"app"`
	CompName		string `json:"comp_name,omitempty"` // empty for app-level ops
	DeploymentID	string `json:"deployment_id"`
	Status 			string `json:"status"`
	TimeStamp		int64	`json:"time_stamp"`	
}


//
//---------------- Deployment status report ----------------
//
type DeploymentComponentStatus struct {
	ID            uuid.UUID
	DeploymentID  string
	HostID        string
	ComponentName string
	DesiredHash   string
	ActualHash    string
	Status        string
	Message       string
	LastUpdate    time.Time
}