package deployment

import (
	//"gopkg.in/yaml.v3"
	"github.com/balaji-balu/ieo/pkg/application"
	"github.com/goccy/go-yaml"
	"os"
)

type ApplicationDeployment struct {
	APIVersion string   `yaml:"apiVersion"`
	Kind       string   `yaml:"kind"`
	Metadata   Metadata `yaml:"metadata"`
	Spec       Spec     `yaml:"spec"`
}

type Metadata struct {
	Name        string            `yaml:"name"`
	Namespace   string            `yaml:"namespace"`
	Labels      map[string]string `yaml:"labels,omitempty"`
	Annotations Annotations       `yaml:"annotations"`
}

type Annotations struct {
	ApplicationID string `yaml:"applicationId"`
	ID            string `yaml:"id"`
	Version			string `yaml:"version"`
}

type Spec struct {
	DeploymentProfile DeploymentProfile `yaml:"deploymentProfile"`
	Parameters        []Parameter       `yaml:"parameters"`
}

type DeploymentProfile struct {
	Type       string      `yaml:"type"`
	Components []Component `yaml:"components"`
}

type Component struct {
	Name string `yaml:"name"`
	//Properties map[string]string `yaml:"properties"`
	Properties application.ComponentProperties `yaml:"properties"`
}

type Parameter struct {
	Name    string   `yaml:"name"`
	Value   string   `yaml:"value"`
	Targets []Target `yaml:"targets"`
}

type Target struct {
	Pointer    string   `yaml:"pointer"`
	Components []string `yaml:"components"`
}

func ParseDesiredStateYAML(path string) (*ApplicationDeployment, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var obj ApplicationDeployment
	if err := yaml.Unmarshal(data, &obj); err != nil {
		return nil, err
	}
	return &obj, nil
}
