package initflow

import (
	"fmt"
	"os"
	"text/template"

	"github.com/google/uuid"
)

const coTemplateStr = `
node_id: {{ .NodeID }}
component: CO
`

const loTemplateStr = `
node_id: {{ .NodeID }}
component: LO
`

const enTemplateStr = `
node_id: {{ .NodeID }}
component: EN
`

type NodeConfig struct {
	NodeID string
}

func GenerateDirs() error {
	paths := []string{
		"/etc/ieo/co",
		"/etc/ieo/lo",
		"/etc/ieo/en",
	}

	for _, p := range paths {
		if err := os.MkdirAll(p, 0755); err != nil {
			return fmt.Errorf("failed to create %s: %w", p, err)
		}
	}
	return nil
}

func GenerateConfigFile(path, tmplStr string, data interface{}) error {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", path, err)
	}
	defer file.Close()

	tpl := template.Must(template.New("config").Parse(tmplStr))
	return tpl.Execute(file, data)
}

func GenerateAllConfigs(ctx *Context) error {
	if err := GenerateDirs(); err != nil {
		return err
	}

	// CO
	coData := NodeConfig{NodeID: uuid.New().String()}
	if err := GenerateConfigFile("/etc/ieo/co/config.yaml", coTemplateStr, coData); err != nil {
		return err
	}

	// LO
	loData := NodeConfig{NodeID: uuid.New().String()}
	if err := GenerateConfigFile("/etc/ieo/lo/config.yaml", loTemplateStr, loData); err != nil {
		return err
	}

	// EN
	enData := NodeConfig{NodeID: uuid.New().String()}
	if err := GenerateConfigFile("/etc/ieo/en/config.yaml", enTemplateStr, enData); err != nil {
		return err
	}

	return nil
}

// func main() {
// 	if err := GenerateAllConfigs(); err != nil {
// 		fmt.Printf("Error: %v\n", err)
// 	} else {
// 		fmt.Println("All configs generated successfully!")
// 	}
// }
