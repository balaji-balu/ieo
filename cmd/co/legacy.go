package main

// The old, Git-based CO API, served on CO_PORT until roadmap slice E replaces it and deletes this
// file (ADR 0002). It is the old wiring unchanged, except that it no longer logs the database URL
// or the deployments repository's GitHub token (SPEC §15.4), and it shuts down with co serve.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	_ "github.com/lib/pq" // enables the 'postgres' driver

	"github.com/balaji-balu/ieo/ent"
	"github.com/balaji-balu/ieo/internal/api"
	"github.com/balaji-balu/ieo/internal/co"
	"github.com/balaji-balu/ieo/internal/gitmanager"
	"github.com/balaji-balu/ieo/internal/metrics"
	"github.com/balaji-balu/ieo/pkg/co/model"
	"github.com/balaji-balu/ieo/pkg/logx"
)

// legacy serves the old API on port until ctx ends.
func legacy(ctx context.Context, port, dsn string) error {
	if err := logx.Init(logx.Options{Env: os.Getenv("APP_ENV")}); err != nil {
		return fmt.Errorf("legacy: init logger: %w", err)
	}
	log := logx.New("co")

	cfg := model.COConfig{}
	cfg.Git.Repo = "deployments"
	cfg.Git.Branch = "main"
	cfg.Appregistry.Repo = "https://github.com/edge-orchestration-platform/app-registry"
	cfg.Appregistry.Branch = "main"

	drv, err := sql.Open(dialect.Postgres, dsn)
	if err != nil {
		return errors.New("legacy: open the database") // err may repeat dsn (SPEC §15.4)
	}
	client := ent.NewClient(ent.Driver(drv))
	defer func() { _ = client.Close() }() // nothing to do about a failed close at exit

	metrics.Init("co")
	metrics.StartServer(os.Getenv("CO_METRICS_PORT"))

	gitm := gitmanager.NewManager()
	if err := gitm.Register(gitmanager.RepoConfig{
		Name:        "app-registry",
		Mode:        gitmanager.GitRemote,
		RemoteURL:   "https://github.com/edge-orchestration-platform/app-registry.git",
		Branch:      "main",
		WorkingPath: "/tmp/app-registry",
	}); err != nil {
		log.Errorw("Git register failed", "err", err)
	}
	if err := gitm.Register(gitmanager.RepoConfig{
		Name:        "deployments",
		Mode:        gitmanager.GitRemote,
		RemoteURL:   "https://github.com/edge-orchestration-platform/deployments.git",
		Branch:      "main",
		Token:       os.Getenv("GITHUB_TOKEN"),
		WorkingPath: "/tmp/deployments-co",
	}); err != nil {
		log.Errorw("Git register failed", "err", err)
	}
	if err := gitm.InitRepo("deployments"); err != nil {
		log.Errorw("Git initrepo failed", "err", err)
	}
	c := co.NewCO(gitm, "app-registry", "deployments")

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", ":"+port)
	if err != nil {
		return fmt.Errorf("legacy: listen: %w", err)
	}
	log.Infow("CO API running on :", "", port)
	return serveHTTP(ctx, &http.Server{Handler: api.NewRouter(client, c, cfg), ReadHeaderTimeout: readHeaderTimeout}, ln)
}
