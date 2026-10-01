package plugins

import (
	//"log"
	"github.com/balaji-balu/ieo/pkg/en/edgeruntime"
	//"github.com/balaji-balu/ieo/pkg/logx"
)

// MapRegistry is an unused placeholder for a registry type; plugins are held in a package map.
type MapRegistry struct{}

var plugins = map[string]edgeruntime.RuntimePlugin{}

//var log = logx.New("en.registry")

// Register adds a runtime plugin under its Name, replacing any plugin with the same name.
func Register(p edgeruntime.RuntimePlugin) {
	//log.Infow("registry.Register", p.Name())
	plugins[p.Name()] = p
}

// Get returns the plugin registered under name, or nil if there is none.
func Get(name string) edgeruntime.RuntimePlugin {
	//log.Infow("registry.Get")
	return plugins[name]
}

// func (r MapRegistry) Get(name string) RuntimePlugin {
//     return plugins[name]
// }
