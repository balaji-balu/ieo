package edgeruntime
import (
	//"github.com/balaji-balu/ieo/pkg/en"
    "github.com/balaji-balu/ieo/pkg/model"
)

type ComponentSpec struct {
    Name      string
    Version   string
    Runtime     string
    Image     string
    WasmFile  string
    Artifact  string
    Args      []string
}

type ComponentStatus struct {
    Name       string `json:"name"`
    Version    string `json:"version"`
    State      string `json:"state"`
    Message    string `json:"message,omitempty"`
    Timestamp  int64  `json:"timestamp"`
}

type RuntimePlugin interface {
    Name() string
    Capabilities() []string

    Install(ComponentSpec) error
    Start(ComponentSpec) error
    Stop(string) error
    Delete(string) error
    Status(string) (ComponentStatus, error)
}

type EventHandler interface {
	OnEvent(op model.DiffOp, compName string,event model.DeploymentStage, err error)
}

// type RuntimePlugin interface {
//     Install(c en.ComponentSpec) error
//     Start(c en.ComponentSpec) error
//     Stop(name string) error
//     Remove(name string) error
//     Status(name string) en.ComponentStatus
// }