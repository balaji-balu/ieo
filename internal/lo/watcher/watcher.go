////////////////////////////////////////////////////////////
// package watcher
////////////////////////////////////////////////////////////
package watcher

import (
	//"fmt"
	"log"
	"os"
	"strings"
	"time"
	"path/filepath"

	"github.com/balaji-balu/ieo/internal/gitmanager"
	git "github.com/go-git/go-git/v5"
	//"github.com/go-git/go-git/v5/plumbing"
)

// DeploymentChange describes a changed deployment file
type DeploymentChange struct {
	DeploymentID string
	FilePath     string
	Content      string
}

// Watcher watches deployments repo for changes for a specific site
type Watcher struct {
	Mgr      *gitmanager.Manager
	RepoName string
	SiteID   string
	Interval time.Duration
	OnChange func(commit string, changes []DeploymentChange)
	stopCh   chan struct{}
}

func NewWatcher(m *gitmanager.Manager, repoName, siteID string, interval time.Duration) *Watcher {
	return &Watcher{Mgr: m, 
		RepoName: repoName, 
		SiteID: siteID, 
		Interval: interval, 
		stopCh: make(chan struct{})}
}

func (w *Watcher) Stop() { close(w.stopCh) }

func (w *Watcher) Start() error {

	log.Println("watcher:Start", w.RepoName)

	// ensure clone
	repo, err := w.Mgr.EnsureClone(w.RepoName)
	if err != nil {
		log.Println("1. err:", err)
		return err
	}
	ref, _ := repo.Head()
	last := ""
	if ref != nil {
		last = ref.Hash().String()
	}

	sitePrefix := strings.TrimSuffix(w.SiteID, "/") + "/"

	for {
		select {
		case <-time.After(w.Interval):
			if err := w.Mgr.Pull(w.RepoName); err != nil {
				// continue on pull errors
				continue
			}

			cfg, err := w.Mgr.GetRepoConfig(w.RepoName)
			if err != nil {
				continue
			}
			repo, err := git.PlainOpen(cfg.WorkingPath)
			if err != nil {
				continue
			}
			ref, _ := repo.Head()
			commit := ""
			if ref != nil {
				commit = ref.Hash().String()
			}
			if commit != last {
				files, err := gitmanager.ChangedFilesBetween(repo, last, commit)
				if err != nil {
					last = commit
					continue
				}
				var changes []DeploymentChange
				for _, f := range files {
					if strings.HasPrefix(f, sitePrefix) && strings.HasSuffix(f, "desiredstate.yaml") {
						// read blob content from working copy
						cfg, err := w.Mgr.GetRepoConfig(w.RepoName)
						if err != nil {
							continue
						}
						b, err := os.ReadFile(filepath.Join(cfg.WorkingPath, f))
						if err != nil {
							continue
						}
						dep := DeploymentChange{
							DeploymentID: extractDeploymentID(f),
							FilePath:     f,
							Content:      string(b),
						}
						changes = append(changes, dep)
					}
				}
				last = commit
				if len(changes) > 0 && w.OnChange != nil {
					w.OnChange(commit, changes)
				}
			}
		case <-w.stopCh:
			return nil
		}
	}
}

func extractDeploymentID(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2]
	}
	return ""
}
