package install

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

var bins = []string{"ieo-co", "ieo-lo", "ieo-en", "edgectl"}

func RunInstall(version string) {
	targetDir := defaultInstallDir()

	fmt.Println("Installing into:", targetDir)

	var release GitHubRelease
	var err error

	if version == "" {
		fmt.Println("Fetching latest release info...")
		release, err = GetLatestRelease()
		if err != nil {
			fmt.Println("Error fetching latest release:", err)
			return
		}
		version = release.TagName
	} else {
		fmt.Println("Installing specified version:", version)
		// Optional: implement GetReleaseByTag(version)
		release, err = GetLatestRelease()
	}

	fmt.Println("Using version:", version)

	for _, bin := range bins {
		prefix := assetName(bin)

		_, url, err := FindAsset(release, prefix)
		if err != nil {
			fmt.Printf("❌ Asset not found for %s: %v\n", bin, err)
			continue
		}

		dest := filepath.Join(targetDir, bin)

		fmt.Printf("⬇ Downloading %s...\n", bin)
		if err := DownloadFile(url, dest); err != nil {
			fmt.Printf("❌ Failed to download %s: %v\n", bin, err)
			continue
		}

		os.Chmod(dest, 0755)
		fmt.Printf("✅ Installed %s\n", bin)
	}
}

func assetName(bin string) string {
	goos := runtime.GOOS
	arch := runtime.GOARCH
	return fmt.Sprintf("%s-%s-%s", bin, goos, arch)
}

func defaultInstallDir() string {
	if runtime.GOOS == "windows" {
		return `C:\Program Files\edge-runtime`
	}
	return "/usr/local/bin"
}

func ParseVersionFlag(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "--version" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
