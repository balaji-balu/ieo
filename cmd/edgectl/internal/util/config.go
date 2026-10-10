package util

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
	"github.com/joho/godotenv"
)

// Config defines CLI configuration
type Config struct {
	Coordinator struct {
		URL string `yaml:"url"`
	} `yaml:"coordinator"`

	LocalOrchestrator struct {
		URL string `yaml:"url"`
	} `yaml:"local_orchestrator"`

	EdgeNode struct {
		URL string `yaml:"url"`
	} `yaml:"edge_node"`
}

func init() {
	err := godotenv.Load()
    if err != nil {
        fmt.Println("⚠️  No .env file found or failed to load, using system environment variables")
    }
}
// Default path for config file
func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".edgectl", "config.yaml"), nil
}

// Load loads config from ~/.edgectl/config.yaml or env vars
func Load() (*Config, error) {
	cfg := &Config{}

	// 1. Try to load from file
	path, _ := configPath()
	// fmt.Println("config path:", path)
	if data, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("invalid config file: %w", err)
		}
	}

	// 2. Override with env vars if present
	if val := os.Getenv("EDGECTL_CO_URL"); val != "" {
		cfg.Coordinator.URL = val
	}
	if val := os.Getenv("EDGECTL_LO_URL"); val != "" {
		cfg.LocalOrchestrator.URL = val
	}
	if val := os.Getenv("EDGECTL_EN_URL"); val != "" {
		cfg.EdgeNode.URL = val
	}

	// 3. Set defaults if still empty
	if cfg.Coordinator.URL == "" {
		cfg.Coordinator.URL = "http://localhost:8080"
	}
	if cfg.LocalOrchestrator.URL == "" {
		cfg.LocalOrchestrator.URL = "http://localhost:8081"
	}
	if cfg.EdgeNode.URL == "" {
		cfg.EdgeNode.URL = "http://localhost:8082"
	}

	return cfg, nil
}

// Save writes config back to ~/.edgectl/config.yaml
func Save(cfg *Config) error {
	path, err := configPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}
