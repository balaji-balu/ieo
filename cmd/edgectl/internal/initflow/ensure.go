package initflow

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

var requiredBinaries = []string{
	"ieo-co",
	"ieo-lo",
	"ieo-en",
}

// EnsureBinaries checks that the component binaries are on PATH and their versions are compatible.
func EnsureBinaries(ctx *Context) error {
	fmt.Println("✔ Ensuring required components")

	versions := make(map[string]string)

	for _, bin := range requiredBinaries {
		path, err := exec.LookPath(bin)
		if err != nil {
			return fmt.Errorf(
				"%s not found in PATH. Please reinstall using official installer",
				bin,
			)
		}

		v, err := getVersion(bin)
		if err != nil {
			return fmt.Errorf("failed to read %s version: %w", bin, err)
		}

		fmt.Printf("  ✓ %s (%s)\n", path, v)
		versions[bin] = v
	}

	if err := ensureCompatibleVersions(versions); err != nil {
		return err
	}

	return nil
}

func getVersion(bin string) (string, error) {
	out, err := exec.CommandContext(context.Background(), bin, "--version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func ensureCompatibleVersions(v map[string]string) error {
	ref := ""

	for bin, ver := range v {
		if ref == "" {
			ref = majorMinor(ver)
			continue
		}
		if majorMinor(ver) != ref {
			return fmt.Errorf(
				"version mismatch: all components must share major.minor (%s != %s)",
				bin, ver,
			)
		}
	}
	return nil
}

func majorMinor(v string) string {
	// v0.1.15 → v0.1
	p := strings.Split(v, ".")
	if len(p) < 2 {
		return v
	}
	return p[0] + "." + p[1]
}
