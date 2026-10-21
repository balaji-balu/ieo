// Package edgectl is the operator CLI (SPEC §11.3): a client of the CO's operator API that imports
// applications, deploys them and shows their status. cmd/edgectl runs it; tests run it in-process
// through Run.
//
// It is configured from the environment and flags, which win (SPEC §6.3); the token comes only
// from the environment, and edgectl never prints it (SPEC §15.4, §15.6). It exits 0 on success, 1
// on a failed request or an unreachable CO, and 2 on a usage or configuration error (SPEC §11.3).
package edgectl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// watchInterval is how often deployment status --watch polls (SPEC §11.3: implementation-defined).
const watchInterval = 2 * time.Second

// Output formats (SPEC §11.3).
const (
	outputText = "text"
	outputJSON = "json"
)

// usageError is a command line or configuration edgectl cannot run: exit code 2 (SPEC §11.3).
type usageError string

func (e usageError) Error() string { return string(e) }

// cli is one run of edgectl.
type cli struct {
	getenv         func(string) string
	stdout, stderr io.Writer
	// after is the clock of deployment status --watch: time.After but in tests.
	after func(time.Duration) <-chan time.Time
}

// Run runs edgectl with the command line args, reading its configuration from getenv, and
// returns its exit code: 0 on success, 1 on a failed request or an unreachable CO, 2 on a usage
// or configuration error (SPEC §11.3). Results go to stdout, errors to stderr.
func Run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	return cli{getenv: getenv, stdout: stdout, stderr: stderr, after: time.After}.run(ctx, args)
}

func (c cli) run(ctx context.Context, args []string) int {
	root := c.commands(ctx)
	root.SetArgs(args)
	root.SetOut(c.stdout)
	root.SetErr(c.stderr)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	// No error holds the token: bearer keeps it out of the transport errors, and client out of
	// what the CO sends back (SPEC §15.4, §15.6).
	_, _ = fmt.Fprintf(c.stderr, "edgectl: %v\n", err) // nothing to do about a failed write to stderr
	if isFailure(err) {
		return 1
	}
	// Anything else is a usage or configuration error, including Cobra's own flag and argument
	// errors.
	_, _ = fmt.Fprintln(c.stderr, "Run 'edgectl --help' for usage.")
	return 2
}

// commands returns the command tree of SPEC §11.3's minimum CLI commands specified so far. Their
// requests end with ctx.
func (c cli) commands(ctx context.Context) *cobra.Command {
	root := &cobra.Command{
		Use:   "edgectl",
		Short: "Operate the edge orchestrator through the CO's operator API",
		Long: `edgectl talks to the CO's operator API (SPEC §11.3).

Configuration (flags win over the environment):
  EDGECTL_CO_URL       --co-url        the operator API's base URL, https:// (required)
  EDGECTL_CO_INSECURE  --co-insecure   true: allow an http:// URL, sending the token without TLS
  EDGECTL_TLS_CA_FILE  --tls-ca-file   PEM file of the CA that verifies the CO (default: system roots)
  EDGECTL_TOKEN                        the operator token (required; environment only)

Exit codes: 0 success, 1 failed request or unreachable CO, 2 usage or configuration error.`,
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.NoArgs,
		RunE:          needSubcommand,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if o := output(cmd); o != outputText && o != outputJSON {
				return usageError(fmt.Sprintf("--output %q: want text or json", o))
			}
			return nil
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError(err.Error()) })
	pf := root.PersistentFlags()
	pf.StringP("output", "o", outputText, "output format: text or json (the CO's response as it sent it)")
	addConfigFlags(pf)
	root.AddCommand(c.appCommand(ctx), c.deployCommand(ctx), c.deploymentCommand(ctx), c.deviceCommand(ctx))
	return root
}

// needSubcommand is the RunE of a command that only groups others.
func needSubcommand(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return usageError(fmt.Sprintf("unknown command %q for %q", args[0], cmd.CommandPath()))
	}
	return usageError(fmt.Sprintf("%s needs a command", cmd.CommandPath()))
}

// output returns the --output format of cmd.
func output(cmd *cobra.Command) string {
	o, _ := cmd.Flags().GetString("output") // a persistent flag of root: always defined
	return o
}

// connect reads the configuration and returns the client of the CO it names.
func (c cli) connect(cmd *cobra.Command) (*client, error) {
	cfg, err := loadConfig(cmd.Flags(), c.getenv)
	if err != nil {
		return nil, err
	}
	return newClient(cfg, c.stderr)
}

// requireFlags returns a usageError naming the first of the string flags names that is not set
// or empty.
func requireFlags(cmd *cobra.Command, names ...string) error {
	for _, n := range names {
		if v, _ := cmd.Flags().GetString(n); v == "" {
			return usageError(fmt.Sprintf("--%s is required", n))
		}
	}
	return nil
}

// args returns a cobra.PositionalArgs that accepts exactly the named arguments, and reports any
// other count as a usageError.
func args(names ...string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != len(names) {
			if len(names) == 0 {
				return usageError(fmt.Sprintf("%s takes no arguments", cmd.CommandPath()))
			}
			return usageError(fmt.Sprintf("%s takes %s", cmd.CommandPath(), strings.Join(names, " ")))
		}
		return nil
	}
}

// interrupted reports whether err comes from ctx ending.
func interrupted(ctx context.Context, err error) bool {
	return ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
}
