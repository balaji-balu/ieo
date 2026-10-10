package gitfetcher

import (
	"errors"
	"fmt"
	//"io/ioutil"
	"log"
	"os"
	"path/filepath"

	"github.com/go-git/go-git/v5/plumbing/transport/http"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

type GitFetcher struct {
	RepoURL  string
	Branch   string
	LocalDir string
	Token 	 string
}

// Clone performs a fresh clone of the repo to LocalDir
func (g *GitFetcher) Clone() error {
	if _, err := os.Stat(filepath.Join(g.LocalDir, ".git")); !os.IsNotExist(err) {
		return fmt.Errorf("repo already exists at %s", g.LocalDir)
	}

	log.Printf("🌀 Cloning %s (branch=%s)...", g.RepoURL, g.Branch)
	_, err := git.PlainClone(g.LocalDir, false, &git.CloneOptions{
		URL:           g.RepoURL,
		Auth: &http.BasicAuth{
			Username: "git", // can be anything but not empty
			Password: g.Token,
		},		
		ReferenceName: plumbing.NewBranchReferenceName(g.Branch),
		SingleBranch:  true,
		Depth:         1,
	})
	if err != nil {
		return fmt.Errorf("clone failed: %w", err)
	}
	log.Println("✅ Clone complete")
	return nil
}

// Pull updates the local repo with latest changes
func (g *GitFetcher) Pull() error {
	repo, err := git.PlainOpen(g.LocalDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("repo not found; please clone first")
		}
		return fmt.Errorf("open repo failed: %w", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("worktree failed: %w", err)
	}

	log.Println("⬇️  Pulling latest changes...")
	err = wt.Pull(&git.PullOptions{
		RemoteName: "origin",
	})
	if err == git.NoErrAlreadyUpToDate {
		log.Println("✅ Repo already up-to-date")
		return nil
	}
	return err
}

func (g *GitFetcher) CloneOrPull() error {
    repoPath := filepath.Join(g.LocalDir, ".git")
    token := os.Getenv("GITHUB_TOKEN")

    auth := &http.BasicAuth{
        Username: "git", // arbitrary but required
        Password: token,
    }

    if _, err := os.Stat(repoPath); os.IsNotExist(err) {
        log.Printf("🌀 Cloning %s (branch=%s)...", g.RepoURL, g.Branch)
        _, err := git.PlainClone(g.LocalDir, false, &git.CloneOptions{
            URL:           g.RepoURL,
            ReferenceName: plumbing.NewBranchReferenceName(g.Branch),
            SingleBranch:  true,
            Depth:         1,
            Auth:          auth,
        })
        return err
    }

    repo, err := git.PlainOpen(g.LocalDir)
    if err != nil {
        log.Printf("⚠️ Repo open failed, cleaning cache...")
        os.RemoveAll(g.LocalDir)
        return g.CloneOrPull()
    }

    wt, _ := repo.Worktree()
    log.Println("⬇️ Pulling latest changes...")
    err = wt.Pull(&git.PullOptions{
        RemoteName: "origin",
        Auth:       auth,
    })
    if err == git.NoErrAlreadyUpToDate {
        log.Println("✅ Repo already up-to-date")
        return nil
    }
    if err != nil {
        log.Printf("⚠️ Pull failed: %v, retrying clone", err)
        os.RemoveAll(g.LocalDir)
        return g.CloneOrPull()
    }

    return nil
}


// FetchAppResource pulls the repo (if possible) and returns file contents
func (g *GitFetcher) FetchAppResource(appName, relativePath string) ([]byte, error) {
    if err := g.CloneOrPull(); err != nil {
        return nil, fmt.Errorf("git fetch failed: %w", err)
    }

    target := filepath.Join(g.LocalDir, appName, relativePath)
    data, err := os.ReadFile(target)
    if err != nil {
        return nil, fmt.Errorf("read file failed (%s): %w", target, err)
    }

    return data, nil

}
