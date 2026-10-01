package config

import (
	//"os"
	"fmt"
	"log"
	"path/filepath"
	"runtime"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// Options selects which component config to load and for which environment.
type Options struct {
	//RootDir	string
	Env     string // dev | prod | staging
	AppName string // ieo
	Unit    string // co | lo | en
}

// RootDir returns the root directory for a component: "./" in dev, the OS default otherwise.
func RootDir(opts Options) (string, error) {
	// dev mode → ~/.<app>/<unit>
	if opts.Env == "" || opts.Env == "dev" {
		// home, err := os.UserHomeDir()
		// if err != nil {
		// 	return "", err
		// }
		// return filepath.Join(
		// 	home,
		// 	"."+opts.AppName,
		// 	opts.Unit,
		// ), nil
		return "./", nil
	}

	// flags

	// prod / staging → OS defaults
	return osDefaultRoot(opts)
}

// func LoadPath(options Options) {
// 	path := osDefault(options)
// 	return path
// }

// Load reads the component's YAML config into out: configs/<unit>.yaml in dev, otherwise
// /etc/<app>/<unit>/config.yaml.
func Load(o Options, out interface{}) error {
	path := ""
	if o.Env == "" || o.Env == "dev" {
		path = filepath.Join("./", "configs", o.Unit+".yaml")
	} else {
		path = filepath.Join("/etc", o.AppName, o.Unit, "config.yaml")
	}

	log.Println("cfg loader", "path", path)

	k := koanf.New(".")

	if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
		return err
	}

	return k.Unmarshal("", out)
}

func osDefaultRoot(o Options) (string, error) {
	switch runtime.GOOS {
	case "linux":
		return filepath.Join(
			"/var/lib",
			o.AppName,
			o.Unit,
		), nil
	default:
		return "", fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

// LoadOrCreateID returns a persistent UUID stored at path
func LoadOrCreateID(path string) (string, error) {
	log.Println("LoadOrCreateId: enter")
	// if b, err := os.ReadFile(path); err == nil {
	// 	log.Println("readfile err", err)
	// 	return string(b), nil
	// }

	// // generate new
	// id := uuid.New().String()

	// // ensure directory exists
	// if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
	// 	log.Println("mkdir err", err)
	// 	return "", err
	// }

	// if err := os.WriteFile(path, []byte(id), 0o644); err != nil {
	// 	log.Println("writefile err", err)
	// 	return "", err
	// }

	//return id, nil
	return "", nil
}
