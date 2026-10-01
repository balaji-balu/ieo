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

// NodeConfig is the template data for an EN config file.
type NodeConfig struct {
	NodeID string
}

// GenerateDirs creates the /etc/ieo config directories for each component.
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

// GenerateConfigFile renders tmplStr with data into the file at path.
func GenerateConfigFile(path, tmplStr string, data interface{}) (err error) {
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", path, err)
	}
	defer func() {
		if cerr := file.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("failed to close file %s: %w", path, cerr)
		}
	}()

	tpl := template.Must(template.New("config").Parse(tmplStr))
	return tpl.Execute(file, data)
}

// GenerateAllConfigs writes the CO, LO and EN config files.
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
