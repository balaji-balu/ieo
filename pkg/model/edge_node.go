package model

import (
	//"time"
)
type HostDeployRequest struct {
	DeploymentID	string				`json:"deployment_id"`
	Labels   		map[string]string	`json:"labels,omitempty"`
	Action			string				`json:"action"`
	HostID		string				`json:"target_node"`
	Component		ComponentProperties `json:"component"`

	// Alive    bool              `json:"alive"`
	// NodeID   string            `json:"node_id"`
	// SiteID   string            `json:"site_id"`
	// Runtime  string            `json:"runtime"`
	// LastSeen time.Time         `json:"last_seen"`
	// Region   string            `json:"region"`
	// CPUFree  float64           `json:"cpu_free"`

}

type ComponentProperties struct {
	Name string `json:"name"`
	Repository      string `json:"repository,omitempty"`
	Revision        string `json:"revision,omitempty"`
	Wait            bool   `json:"wait,omitempty"`
	Timeout         string `json:"timeout,omitempty"`
	PackageLocation string `json:"packageLocation,omitempty"`
	KeyLocation     string `json:"keyLocation,omitempty"`
}

