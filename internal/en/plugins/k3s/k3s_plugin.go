//go:build k3s

package plugins

import "github.com/balaji-balu/ieo/pkg/model"

type Plugin struct {}

func NewPlugin() *Plugin {
    return &Plugin{}
}

// k3s / k8s logic here...
