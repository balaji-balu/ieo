package model

type DeploymentStage string

const (
    StatePending    DeploymentStage = "pending"
    StateInstalling DeploymentStage = "installing"
    StateInstalled  DeploymentStage = "installed"
    StateFailed     DeploymentStage = "failed"
)

type DeploymentStatus struct {
    APIVersion   string                 `json:"apiVersion"`
    Kind         string                 `json:"kind"`
    DeploymentID string                 `json:"deploymentId"`
    Status       DeploymentState        `json:"status"`  
    Components   []DeploymentComponent  `json:"components"`
    SiteID       string                 `json:"site_id"`
    TimeStamp int64 `json:"time_stamp"`
}

type DeploymentState struct {
    State string        `json:"state"`
    Error StatusError   `json:"error"`
}

type DeploymentComponent struct {
    Name        string        `json:"name"`
    State       string        `json:"state"`
    Error       StatusError   `json:"error"`
    SpecHash    string        `json:"spec_hash"`
    HostID      string        `json:"host_id"`
    DeploymentID string       `json:"deployment_id"`
}

type StatusError struct {
    Code    string `json:"code"`
    Message string `json:"message"`
}
