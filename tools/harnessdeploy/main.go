// Command harnessdeploy deploys the dev harness's test application (tests/harness/hello) to one
// host of the laptop harness (deploy/dev/compose.yaml, roadmap D5.7). It is a harness tool, not an
// operator path: roadmap slice E replaces it with `edgectl deploy`.
//
// It copies a busybox image, pinned by digest, from Docker Hub into the harness registry, pushes
// the application's Compose archive and package there, imports the application into the CO's
// store (SPEC §5.3), stands in for the LO's capability reports of the site's gateway and the
// target host (SPEC §8.3, until slice H), and creates a directed deployment (SPEC §8.1). A rerun
// finds the deployment and creates no other. The site must exist: `co site add` first.
//
// Usage, from the repository root, with the harness up (docs/dev-setup.md):
//
//	DATABASE_URL=postgres://postgres:postgres@localhost:5432/orchestration?sslmode=disable \
//	  go run ./tools/harnessdeploy [-registry localhost:5000] [-target <site>/<host>]
//
// It prints DEPLOYMENT_ID=<id> and COMPOSE_PROJECT=<project of its one component> on stdout, and logs
// JSON on stderr. It never logs the database URL (SPEC §15.4). It exits 0 on success, 1 on a
// failure, and 2 on a usage error.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"

	"github.com/balaji-balu/ieo/internal/co/store/postgres"
	"github.com/balaji-balu/ieo/internal/contract"
)

const defaultTarget = "00000000-0000-4000-8000-000000000001/host-1" // LO_SITE_ID and EN_HOST_ID of en1

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	log := slog.New(slog.NewJSONHandler(stderr, nil))
	fs := flag.NewFlagSet("harnessdeploy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	registry := fs.String("registry", "localhost:5000", "address of the harness registry, reached over plain HTTP")
	targetFlag := fs.String("target", defaultTarget, "the deployment's target, <site>/<host>")
	app := fs.String("app", filepath.Join("tests", "harness", "hello"), "directory of the test application")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	target, err := contract.ParseDeviceID(*targetFlag)
	if err == nil && (target.Host == "" || target.Autonomous) {
		err = fmt.Errorf("target %s is not <site>/<host>", *targetFlag)
	}
	dsn := getenv("DATABASE_URL")
	if err == nil && dsn == "" {
		err = fmt.Errorf("DATABASE_URL is not set")
	}
	if err != nil {
		log.Error("invalid command", "error", err.Error())
		return 2
	}
	if err := deployHarness(ctx, *app, *registry, target, dsn, stdout, log); err != nil {
		log.Error("harnessdeploy failed", "error", err.Error())
		return 1
	}
	return 0
}

// deployHarness runs the harness against the registry at addr and the CO store at dsn.
func deployHarness(ctx context.Context, app, addr string, target contract.DeviceID, dsn string, stdout io.Writer, log *slog.Logger) (err error) {
	srcName, digest, _ := strings.Cut(sourceImage, "@")
	src, err := remote.NewRepository(srcName)
	if err != nil {
		return err
	}
	src.Client = auth.DefaultClient // anonymous, with the token Docker Hub asks for
	h := harness{
		appDir:        app,
		appRepository: defaultAppRepository,
		target:        target,
		open:          openThrough(addr),
		image:         image{src: src, digest: digest, dest: harnessImage},
	}
	s, err := postgres.Open(ctx, dsn)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := s.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("close co store: %w", cerr))
		}
	}()
	d, projects, created, err := h.deploy(ctx, s)
	if err != nil {
		return err
	}
	msg := "deployment created"
	if !created {
		msg = "deployment exists; none created"
	}
	log.Info(msg, "deployment_id", d.ID.String(), "digest", d.Digest.String(), "target", d.Target.String())
	if len(projects) != 1 {
		return fmt.Errorf("the test application has %d components; COMPOSE_PROJECT names one", len(projects))
	}
	_, err = fmt.Fprintf(stdout, "DEPLOYMENT_ID=%s\nCOMPOSE_PROJECT=%s\n", d.ID, projects[0])
	return err
}
