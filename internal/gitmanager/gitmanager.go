////////////////////////////////////////////////////////////
// package gitmanager
////////////////////////////////////////////////////////////
package gitmanager

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
)

type GitMode string

const (
	GitLocal  GitMode = "local"
	GitRemote GitMode = "remote"
)

// Config describes one repo
type RepoConfig struct {
	Name        string
	Mode        GitMode
	LocalPath   string // path to origin in local mode (bare or non-bare ok)
	RemoteURL   string
	Token       string
	Branch      string
	WorkingPath string // where to clone/pull
}

func (c RepoConfig) URL() string {
	if c.Mode == GitLocal {
		return "file://" + c.LocalPath
	}
	return c.RemoteURL
}

func (c RepoConfig) Auth() transport.AuthMethod {
	if c.Mode == GitLocal {
		return nil
	}
	if c.Token != "" {
		return &http.BasicAuth{Username: "git", Password: c.Token}
	}
	return nil
}

// manager
type Manager struct {
	mu    sync.Mutex
	repos map[string]*RepoConfig
	locks map[string]*sync.Mutex
}

func NewManager() *Manager {
	return &Manager{
		repos: make(map[string]*RepoConfig),
		locks: make(map[string]*sync.Mutex),
	}
}

// InitRepo ensures WorkingPath contains a valid git clone.
// Call this once at service startup for every registered repo.
func (m *Manager) InitRepo(name string) error {
    cfg, err := m.GetConfig(name)
    if err != nil {
        return err
    }

    // Lock per repo
    return m.withLock(name, func() error {
        // Ensure directory exists
        if _, err := os.Stat(cfg.WorkingPath); os.IsNotExist(err) {
            if err := os.MkdirAll(cfg.WorkingPath, 0755); err != nil {
                return fmt.Errorf("mkdir working: %w", err)
            }
        }

        gitDir := filepath.Join(cfg.WorkingPath, ".git")

        // Case 1: Already initialized
        if _, err := os.Stat(gitDir); err == nil {
            // Verify repo integrity
            if _, err := git.PlainOpen(cfg.WorkingPath); err == nil {
                return nil // fully initialized
            }
            // Repo is corrupt → wipe and reclone
            os.RemoveAll(cfg.WorkingPath)
            if err := os.MkdirAll(cfg.WorkingPath, 0755); err != nil {
                return fmt.Errorf("mkdir (recreate): %w", err)
            }
        }

        // Case 2: Clone fresh
		switch cfg.Mode {
		case GitLocal:
			// Just open the local repo (no clone)
			_, err = git.PlainOpen(cfg.LocalPath)
			if err != nil {
				return fmt.Errorf("open local repo failed: %w", err)
			}
			// For Local mode, WorkingPath should point to LocalPath
			cfg.WorkingPath = cfg.LocalPath
			return nil
		case GitRemote:
			_, err = git.PlainClone(cfg.WorkingPath, false, &git.CloneOptions{
				URL:           cfg.URL(),
				Auth:          cfg.Auth(),
				Depth:         1,
				SingleBranch:  true,
				ReferenceName: plumbing.NewBranchReferenceName(cfg.Branch),
			})
			if err != nil {
				return fmt.Errorf("clone failed: %w", err)
			}
			return nil
		}
		return nil
    })
}

func (m *Manager) Register(cfg RepoConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cfg.WorkingPath == "" {
		return errors.New("WorkingPath required")
	}
	m.repos[cfg.Name] = &cfg
	m.locks[cfg.Name] = &sync.Mutex{}
	return nil
}

func (m *Manager) GetConfig(name string) (*RepoConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.repos[name]
	if !ok {
		return nil, fmt.Errorf("repo not registered: %s", name)
	}
	return c, nil
}

func (m *Manager) withLock(name string, fn func() error) error {
	m.mu.Lock()
	lk, ok := m.locks[name]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("no lock for repo %s", name)
	}
	lk.Lock()
	defer lk.Unlock()
	return fn()
}

// EnsureClone clones repo to working path if needed and returns *git.Repository
func (m *Manager) EnsureClone(name string) (*git.Repository, error) {
	cfg, err := m.GetConfig(name)
	if err != nil {
		return nil, err
	}

	return m.ensureCloneLocked(cfg)
}

func (m *Manager) ensureCloneLocked(cfg *RepoConfig) (*git.Repository, error) {
	// lock per-repo
	m.mu.Lock()
	lk := m.locks[cfg.Name]
	m.mu.Unlock()

	lk.Lock()
	defer lk.Unlock()

	dot := filepath.Join(cfg.WorkingPath, ".git")
	if _, err := os.Stat(dot); os.IsNotExist(err) {
		// remove stale working path
		os.RemoveAll(cfg.WorkingPath)
		repo, err := git.PlainClone(cfg.WorkingPath, false, &git.CloneOptions{
			URL:           cfg.URL(),
			Auth:          cfg.Auth(),
			Depth:         1,
			SingleBranch:  true,
			ReferenceName: plumbing.NewBranchReferenceName(cfg.Branch),
		})
		if err != nil {
			return nil, fmt.Errorf("clone failed: %w", err)
		}
		return repo, nil
	}

	repo, err := git.PlainOpen(cfg.WorkingPath)
	if err != nil {
		// try reclone
		os.RemoveAll(cfg.WorkingPath)
		repo, err = git.PlainClone(cfg.WorkingPath, false, &git.CloneOptions{
			URL:           cfg.URL(),
			Auth:          cfg.Auth(),
			Depth:         1,
			SingleBranch:  true,
			ReferenceName: plumbing.NewBranchReferenceName(cfg.Branch),
		})
		if err != nil {
			return nil, fmt.Errorf("reclone failed: %w", err)
		}
	}
	return repo, nil
}

// Pull updates working copy
func (m *Manager) Pull(name string) error {
	cfg, err := m.GetConfig(name)
	if err != nil {
		return err
	}
	return m.withLock(name, func() error {
		repo, err := git.PlainOpen(cfg.WorkingPath)
		if err != nil {
			// try ensure
			repo, err = m.ensureCloneLocked(cfg)
			if err != nil {
				return err
			}
		}
		wt, err := repo.Worktree()
		if err != nil {
			return err
		}
		err = wt.Pull(&git.PullOptions{RemoteName: "origin", Auth: cfg.Auth(), Force: true})
		if err == git.NoErrAlreadyUpToDate {
			return nil
		}

		return err
	})
}

// CommitAndPush commits a file under WorkingPath but performs git ops at ClonePath
func (m *Manager) CommitAndPush(name, relPath, msg string) error {


	cfg, err := m.GetConfig(name)
    if err != nil {
        return err
    }

fmt.Println("DEBUG: WorkingPath =", cfg.WorkingPath)
fmt.Println("DEBUG: Folder exists? = ", isDir(cfg.WorkingPath))
fmt.Println("DEBUG: .git exists? =", isDir(filepath.Join(cfg.WorkingPath, ".git")))

    return m.withLock(name, func() error {
        // Always open the REAL repo root
        repo, err := git.PlainOpen(cfg.WorkingPath)
        if err != nil {
			fmt.Println("CommitAndPush: open repo: %w", err)
            return fmt.Errorf("open repo: %w", err)
        }
		fmt.Println("CommitAndPush: stage 1")

        wt, err := repo.Worktree()
        if err != nil {
			fmt.Println("CommitAndPush: worktree: %w", err)
            return fmt.Errorf("worktree: %w", err)
        }
		fmt.Println("CommitAndPush: stage 2")

        // Add file relative to the repo root
        if _, err := wt.Add(relPath); err != nil {
			fmt.Println("CommitAndPush: add file: %w", err)
            return fmt.Errorf("add %s: %w", relPath, err)
        }
		fmt.Println("CommitAndPush: stage 3")

        // Commit
        if _, err := wt.Commit(msg, &git.CommitOptions{
			Author: &object.Signature{
				Name:  "balaji balu",
				Email: "balaji.balu@gmail.com",
				When:  time.Now(),
			},
		}); err != nil {
			fmt.Println("CommitAndPush: commit: %w", err)
            return fmt.Errorf("commit: %w", err)
        }
		fmt.Println("CommitAndPush: stage 4")

        // Push
        if err := repo.Push(&git.PushOptions{Auth: cfg.Auth()}); err != nil {
            if err == git.NoErrAlreadyUpToDate {
                return nil
            }
            return fmt.Errorf("push: %w", err)
        }
		fmt.Println("dsssssssssssssssssssssssfdf push complete")
        return nil
    })
}

func isDir(path string) bool {
    info, err := os.Stat(path)
    return err == nil && info.IsDir()
}

// ReadFile from working copy (ensure clone/pull first)
func (m *Manager) ReadFile(name string, relPath string) ([]byte, error) {
	fmt.Println("ReadFile: Enter", name, relPath)
	cfg, err := m.GetConfig(name)
	if err != nil {
	    fmt.Println("get config failed:")
		return nil, err
	}
    fmt.Println("stage 2", cfg) 
	if err := m.Pull(name); err != nil {
		fmt.Println("pull failed")

		// Pull may fail for local-only scenarios; ignore NoErrAlreadyUpToDate
	}

	fmt.Println("stage 3")
	full := filepath.Join(cfg.WorkingPath, relPath)
	fmt.Println("stage 4 full=", full)
	b, err := os.ReadFile(full)
	if err != nil {
		fmt.Println("ReadFile: failed", err)
		return nil, err
	}
    fmt.Println("ReadFile: Exit")

	return b, nil
}

// Helper to list files changed between commits
func ChangedFilesBetween(repo *git.Repository, oldHash, newHash string) ([]string, error) {
	if oldHash == "" {
		return []string{}, nil
	}
	oldC, err := repo.CommitObject(plumbing.NewHash(oldHash))
	if err != nil {
		return nil, err
	}
	newC, err := repo.CommitObject(plumbing.NewHash(newHash))
	if err != nil {
		return nil, err
	}
	patch, err := oldC.Patch(newC)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, s := range patch.Stats() {
		files = append(files, s.Name)
	}
	return files, nil
}

// GetHeadCommit returns HEAD commit hash for working repo
func (m *Manager) GetHeadCommit(name string) (string, error) {
	cfg, err := m.GetConfig(name)
	if err != nil {
		return "", err
	}
	repo, err := git.PlainOpen(cfg.WorkingPath)
	if err != nil {
		return "", err
	}
	ref, err := repo.Head()
	if err != nil {
		return "", err
	}
	return ref.Hash().String(), nil
}

// ListRepoNames returns all registered repo names.
func (m *Manager) ListRepoNames() []string {
    m.mu.Lock()
    defer m.mu.Unlock()

    names := make([]string, 0, len(m.repos))
    for name := range m.repos {
        names = append(names, name)
    }
    return names
}

// GetRepoConfig returns a *copy* of the repo config (safe).
func (m *Manager) GetRepoConfig(name string) (RepoConfig, error) {
    m.mu.Lock()
    defer m.mu.Unlock()

    cfg, ok := m.repos[name]
    if !ok {
        return RepoConfig{}, fmt.Errorf("repo not registered: %s", name)
    }

    return *cfg, nil // return copy
}
