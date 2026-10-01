package wasm

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/balaji-balu/ieo/pkg/en/edgeruntime"
	"github.com/balaji-balu/ieo/internal/en/plugins"
)

func init() {
    plugins.Register(&WasmPlugin{})
}

// WasmPlugin implements lifecycle.RuntimePlugin
type WasmPlugin struct {
	runtime  wazero.Runtime
	modules  map[string]api.Module
}


// func NewRuntimePlugin() lifecycle.RuntimePlugin {
// 	return &WasmPlugin{
// 		runtime: wazero.NewRuntime(context.Background()),
// 		modules: map[string]api.Module{},
// 	}
// }

func (w *WasmPlugin) Name() string{
	return "wasm"
}
func (w *WasmPlugin) Capabilities() []string{
	return []string{}
}

func (w *WasmPlugin) Install(c runtime.ComponentSpec) error {
	// For WASM, “install” just checks the file exists
	fmt.Println("wasm: install")
	if _, err := os.Stat(c.Artifact); err != nil {
		return fmt.Errorf("wasm file not found: %v", err)
	}
	return nil
}

func (w *WasmPlugin) Start(c runtime.ComponentSpec) error {
	fmt.Println("wasm: starting")
	ctx := context.Background()
	fmt.Println("wasm: stage 1", c)
	wasmBytes, err := os.ReadFile(c.Artifact)
	if err != nil {
		fmt.Errorf("readfile err", err)
		return err
	}
fmt.Println("wasm: stage 1")
	mod, err := w.runtime.Instantiate(ctx, wasmBytes)
	if err != nil {
		fmt.Errorf("instantiate", err)
		return err
	}

	w.modules[c.Name] = mod

	// Call exported "run" function if exists
	if runFn := mod.ExportedFunction("run"); runFn != nil {
		go func() {
			fmt.Println("run func")
			_, err := runFn.Call(ctx)
			if err != nil {
				fmt.Println("WASM run error:", err)
			}
		}()
	}
	fmt.Println("wasm: exit")
	return nil
}

func (w *WasmPlugin) Stop(name string) error {
	if mod, ok := w.modules[name]; ok {
		mod.Close(context.Background())
		delete(w.modules, name)
	}
	return nil
}

func (w *WasmPlugin) Delete(name string) error {
	return w.Stop(name)
}

func (w *WasmPlugin) Status(name string) (runtime.ComponentStatus, error) {
	fmt.Println("wasm: status")
	_, running := w.modules[name]
	state := "Stopped"
	if running {
		state = "Running"
	}
	return runtime.ComponentStatus{
		Name:      name,
		State:     state,
		Message:   "wasm runtime",
		Timestamp: time.Now().Unix(),
	}, nil
}
