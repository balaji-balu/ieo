// Command edgectl is the operator CLI (SPEC §11.3): it imports applications, deploys them and
// shows their status through the CO's operator API. internal/edgectl holds it; run
// `edgectl --help` for the commands and the EDGECTL_* settings.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/balaji-balu/ieo/internal/edgectl"
)

func main() {
	// An interrupt ends deployment status --watch with exit code 0 (SPEC §11.3).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := edgectl.Run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
