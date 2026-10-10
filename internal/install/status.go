package install

import (
	"fmt"
	"os"
	"path/filepath"
)

func RunStatus() {
	dir := defaultInstallDir()

	for _, bin := range bins {
		p := filepath.Join(dir, bin)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			fmt.Printf("❌ %s not installed\n", bin)
		} else {
			fmt.Printf("✅ %s installed at %s\n", bin, p)
		}
	}
}
