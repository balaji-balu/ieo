package main

// The old, Git-based LO, run when LO_PORT is set until roadmap slices C3b–E replace it and delete
// this file (ADR 0002). It is the old wiring unchanged, except that it uses lo.site_id instead of
// a random site ID kept in a file, keeps its bolt file in lo.data_dir, registers with the CO at
// LO_LEGACY_CO_URL, and stops with the LO.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/balaji-balu/ieo/internal/gitmanager"
	"github.com/balaji-balu/ieo/internal/lo"
	"github.com/balaji-balu/ieo/internal/natsbroker"
	"github.com/balaji-balu/ieo/pkg/logx"
)

// legacy runs the old LO for cfg's site until ctx ends. It reads its own settings from getenv:
// LO_NATS_URL, LO_METRICS_PORT, LO_LEGACY_CO_URL, and BOLTDB_PATH (default
// <lo.data_dir>/db/bolt.db).
func legacy(ctx context.Context, cfg config, getenv func(string) string) error {
	if err := logx.Init(logx.Options{Env: getenv("APP_ENV")}); err != nil {
		return fmt.Errorf("legacy: init logger: %w", err)
	}
	log := logx.New("lo")

	natsURL := getenv("LO_NATS_URL")
	coURL := getenv("LO_LEGACY_CO_URL")
	boltPath := getenv("BOLTDB_PATH")
	if boltPath == "" {
		dir := filepath.Join(cfg.DataDir, "db")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("legacy: %w", err)
		}
		boltPath = filepath.Join(dir, "bolt.db")
	}
	nc, err := natsbroker.New(natsURL)
	if err != nil {
		return fmt.Errorf("legacy: connect to NATS: %w", err)
	}

	gitmgr := gitmanager.NewManager()
	if err := gitmgr.Register(gitmanager.RepoConfig{
		Name:        "deployments",
		Mode:        gitmanager.GitRemote,
		RemoteURL:   "https://github.com/edge-orchestration-platform/deployments.git",
		Branch:      "main",
		Token:       getenv("GITHUB_TOKEN"),
		WorkingPath: "/tmp/deployments-lo",
	}); err != nil {
		log.Errorw("Git register failed", "err", err)
	}

	localorch := lo.NewLO(ctx, string(cfg.SiteID), boltPath, natsURL, coURL, "deployments", nc, gitmgr,
		getenv("LO_METRICS_PORT"), log)
	if localorch == nil {
		return fmt.Errorf("legacy: start the LO with store %s", boltPath)
	}

	r := gin.Default()
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	r.GET("/hosts", localorch.HandlerGetHosts)
	r.GET("/actual", localorch.HandlerGetActual)
	r.POST("/register", localorch.RegisterEN)
	srv := &http.Server{Addr: ":" + cfg.LegacyPort, Handler: r, ReadHeaderTimeout: 10 * time.Second}

	localorch.Start(coURL)

	errs := make(chan error, 1)
	go func() { errs <- srv.ListenAndServe() }()
	log.Infow("HTTP server started on :", "port", cfg.LegacyPort)
	select {
	case err := <-errs:
		return fmt.Errorf("legacy: serve :%s: %w", cfg.LegacyPort, err)
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return fmt.Errorf("legacy: shut down :%s: %w", cfg.LegacyPort, err)
	}
	return nil
}
