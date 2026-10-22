// Command harnessdeploy readies the laptop dev harness (deploy/dev/compose.yaml) for the golden
// path test (tests/harness, SPEC §17.8). It is a harness tool, not an operator path, and roadmap
// slice H deletes it: the LO then reports its devices itself.
//
// It has two jobs. It copies a busybox image, pinned by digest, from Docker Hub into the harness
// registry and pushes the test application's (tests/harness/hello) Compose archive and package
// there. And it stands in for the LO's capability reports of the site's gateway and one host (SPEC
// §8.3), so that the CO accepts a directed deployment to that host (SPEC §8.1.1). It imports
// nothing and deploys nothing: the golden path does both through edgectl. A rerun pushes the same
// bytes and replaces the reports. The site must exist: `co site add` first.
//
// Usage, from the repository root, with the harness up (docs/dev-setup.md):
//
//	DATABASE_URL=postgres://postgres:postgres@localhost:5432/orchestration?sslmode=disable \
//	  go run ./tools/harnessdeploy [-registry localhost:5000] [-target <site>/<host>]
//
// It logs JSON on stderr and never logs the database URL (SPEC §15.4). It exits 0 on success, 1
// on a failure, and 2 on a usage error.
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
	code := run(ctx, os.Args[1:], os.Getenv, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer) int {
	log := slog.New(slog.NewJSONHandler(stderr, nil))
	fs := flag.NewFlagSet("harnessdeploy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	registry := fs.String("registry", "localhost:5000", "address of the harness registry, reached over plain HTTP")
	targetFlag := fs.String("target", defaultTarget, "the host whose capabilities are reported, <site>/<host>")
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
	if err := prepareHarness(ctx, *app, *registry, target, dsn, log); err != nil {
		log.Error("harnessdeploy failed", "error", err.Error())
		return 1
	}
	return 0
}

// prepareHarness runs the harness against the registry at addr and the CO store at dsn.
func prepareHarness(ctx context.Context, app, addr string, target contract.DeviceID, dsn string, log *slog.Logger) (err error) {
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
	if err := h.prepare(ctx, s); err != nil {
		return err
	}
	log.Info("harness ready", "repository", h.appRepository, "host", target.String())
	return nil
}
