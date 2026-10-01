package install

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

const (
	githubOwner = "balaji-balu"
	githubRepo  = "ieo"
)

// GitHubRelease holds part of the GitHub API response
type GitHubRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func GetLatestRelease() (GitHubRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest",
		githubOwner, githubRepo)

	resp, err := http.Get(url)
	if err != nil {
		return GitHubRelease{}, err
	}
	defer resp.Body.Close()

	var release GitHubRelease
	err = json.NewDecoder(resp.Body).Decode(&release)
	return release, err
}

func DownloadFile(url, dest string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	tmp := dest + ".partial"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return err
	}

	return os.Rename(tmp, dest)
}

// FindAsset finds matching asset for a component
func FindAsset(release GitHubRelease, prefix string) (string, string, error) {
	for _, a := range release.Assets {
		if filepath.Base(a.Name) == prefix {
			return a.Name, a.BrowserDownloadURL, nil
		}
	}
	return "", "", fmt.Errorf("asset %s not found", prefix)
}
