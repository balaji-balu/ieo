package edgectl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/operatorapi"
)

// SPEC §11.3: edgectl app import --repo <registry>/<namespace>/<app> --version <v>,
// edgectl app list | get <app-id>.
func (c cli) appCommand(ctx context.Context) *cobra.Command {
	app := &cobra.Command{Use: "app", Short: "Import and list applications", RunE: needSubcommand}

	imp := &cobra.Command{
		Use:   "import --repo <registry>/<namespace>/<app> --version <v>",
		Short: "Import an application version from an OCI registry",
		Args:  args(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			// SPEC §11.3: --version is required: the CO imports one tag (§5.3) and does not choose one.
			if err := requireFlags(cmd, "repo", "version"); err != nil {
				return err
			}
			repo, _ := cmd.Flags().GetString("repo")
			version, _ := cmd.Flags().GetString("version")
			return c.send(ctx, cmd, func(ctx context.Context, cl *client) ([]byte, error) {
				return cl.post(ctx, operatorapi.AppsPath, operatorapi.ImportRequest{Repository: repo, Version: version},
					http.StatusCreated, http.StatusOK)
			}, printApp)
		},
	}
	imp.Flags().String("repo", "", "registry repository, <registry>/<namespace>/<app> (required)")
	imp.Flags().String("version", "", "the tag to import (required)")

	list := &cobra.Command{
		Use: "list", Short: "List the imported application versions", Args: args(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return c.get(ctx, cmd, operatorapi.AppsPath, printApps)
		},
	}
	get := &cobra.Command{
		Use: "get <app-id>", Short: "List the imported versions of an application", Args: args("<app-id>"),
		RunE: func(cmd *cobra.Command, a []string) error {
			// PathEscape leaves . and .., which would name another path once the CO cleans it.
			if a[0] == "." || a[0] == ".." {
				return usageError(fmt.Sprintf("%q is not an application id", a[0]))
			}
			return c.get(ctx, cmd, operatorapi.AppPath(url.PathEscape(a[0])), printApps)
		},
	}
	app.AddCommand(imp, list, get)
	return app
}

// SPEC §11.3: edgectl deploy --app <app-id> --version <v> [--profile <profile-id>]
// --target <site-id>/<host-id>|<site-id>/* [--param k=v]...
func (c cli) deployCommand(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deploy --app <app-id> --version <v> --target <site-id>/<host-id>|<site-id>/* [--profile <profile-id>] [--param k=v]...",
		Short: "Deploy an imported application version; prints the new deployment's id first",
		Args:  args(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireFlags(cmd, "app", "version", "target"); err != nil {
				return err
			}
			f := cmd.Flags()
			req := operatorapi.CreateRequest{}
			req.AppID, _ = f.GetString("app")
			req.Version, _ = f.GetString("version")
			req.Profile, _ = f.GetString("profile")
			req.Target, _ = f.GetString("target")
			raw, _ := f.GetStringArray("param")
			params, err := parseParams(raw)
			if err != nil {
				return err
			}
			if len(params) > 0 {
				req.Parameters = params
			}
			return c.send(ctx, cmd, func(ctx context.Context, cl *client) ([]byte, error) {
				return cl.post(ctx, operatorapi.DeploymentsPath, req, http.StatusCreated)
			}, printCreated)
		},
	}
	f := cmd.Flags()
	f.String("app", "", "application id (required)")
	f.String("version", "", "application version (required)")
	f.String("profile", "", "compose profile id; required only if the application has several")
	f.String("target", "", "<site-id>/<host-id>, or <site-id>/* for the LO to choose (required)")
	f.StringArray("param", nil, "parameter value k=v, sent as a string; repeatable, once per name")
	return cmd
}

// parseParams returns the --param values by name (SPEC §11.3): each splits at its first `=`, and
// its value is sent as a JSON string. A value without `=`, an empty name or a repeated name is a
// usageError.
func parseParams(raw []string) (map[string]any, error) {
	params := map[string]any{}
	for _, p := range raw {
		name, value, ok := strings.Cut(p, "=")
		switch {
		case !ok || name == "":
			// Its position, not its text: the value may be a credential (SPEC §15.4).
			return nil, usageError(fmt.Sprintf("--param %d (of %d) is not name=value", len(params)+1, len(raw)))
		case params[name] != nil:
			return nil, usageError(fmt.Sprintf("--param %s is given more than once", name))
		}
		params[name] = value
	}
	return params, nil
}

// SPEC §11.3: edgectl deployment list | get <id>, edgectl deployment status <id> [--watch].
func (c cli) deploymentCommand(ctx context.Context) *cobra.Command {
	dep := &cobra.Command{Use: "deployment", Short: "List deployments and show their status", RunE: needSubcommand}

	list := &cobra.Command{
		Use: "list [--site <site-id>]", Short: "List deployments", Args: args(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return c.get(ctx, cmd, withSite(cmd, operatorapi.DeploymentsPath), printDeployments)
		},
	}
	list.Flags().String("site", "", "only the deployments of this site")

	get := &cobra.Command{
		Use: "get <id>", Short: "Show a deployment", Args: args("<id>"),
		RunE: func(cmd *cobra.Command, a []string) error {
			id, err := parseID(a[0])
			if err != nil {
				return err
			}
			return c.get(ctx, cmd, operatorapi.DeploymentPath(id), printDeployment)
		},
	}

	status := &cobra.Command{
		Use: "status <id> [--watch]", Short: "Show a deployment's current status", Args: args("<id>"),
		RunE: func(cmd *cobra.Command, a []string) error {
			id, err := parseID(a[0])
			if err != nil {
				return err
			}
			if watch, _ := cmd.Flags().GetBool("watch"); watch {
				return c.watch(ctx, cmd, id)
			}
			return c.get(ctx, cmd, operatorapi.StatusPath(id), printStatus)
		},
	}
	status.Flags().Bool("watch", false, "poll the status until interrupted, printing each new one")

	dep.AddCommand(list, get, status)
	return dep
}

// SPEC §11.3: edgectl device list [--site <site-id>].
func (c cli) deviceCommand(ctx context.Context) *cobra.Command {
	dev := &cobra.Command{Use: "device", Short: "List devices", RunE: needSubcommand}
	list := &cobra.Command{
		Use: "list [--site <site-id>]", Short: "List devices with their last capability report", Args: args(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return c.get(ctx, cmd, withSite(cmd, operatorapi.DevicesPath), printDevices)
		},
	}
	list.Flags().String("site", "", "only the devices of this site")
	dev.AddCommand(list)
	return dev
}

// parseID returns the deployment id s, or a usageError.
func parseID(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, usageError(fmt.Sprintf("deployment id %q is not a UUID", s))
	}
	return id, nil
}

// withSite returns p with the ?site= query of cmd's --site flag, if it is set.
func withSite(cmd *cobra.Command, p string) string {
	if site, _ := cmd.Flags().GetString("site"); site != "" {
		return p + "?" + url.Values{"site": {site}}.Encode()
	}
	return p
}

// printer writes the text output of a response body.
type printer func(w io.Writer, body []byte) error

// get sends GET p and prints the response.
func (c cli) get(ctx context.Context, cmd *cobra.Command, p string, print printer) error {
	return c.send(ctx, cmd, func(ctx context.Context, cl *client) ([]byte, error) { return cl.get(ctx, p) }, print)
}

// send connects to the CO, sends the request of do and prints its response body: as the CO sent
// it with --output json, through print with --output text (SPEC §11.3).
func (c cli) send(ctx context.Context, cmd *cobra.Command, do func(context.Context, *client) ([]byte, error),
	print printer) error {
	cl, err := c.connect(cmd)
	if err != nil {
		return err
	}
	body, err := do(ctx, cl)
	if err != nil {
		return err
	}
	return c.print(cmd, body, print)
}

func (c cli) print(cmd *cobra.Command, body []byte, print printer) error {
	if output(cmd) == outputJSON {
		_, err := c.stdout.Write(body)
		return writeFailure(err)
	}
	return print(c.stdout, body)
}

// watch polls deployment id's status until ctx ends, printing the response each time its current
// status changes (SPEC §11.3: the CO serves no /stream yet). It returns nil once ctx ends. A
// failed first poll is the command's failure; a later one is written to stderr, and the next poll
// tries again, so that a CO restart does not end the watch.
func (c cli) watch(ctx context.Context, cmd *cobra.Command, id uuid.UUID) error {
	cl, err := c.connect(cmd)
	if err != nil {
		return err
	}
	var last json.RawMessage // the last current status printed; nil before the first one
	for {
		err := c.poll(ctx, cmd, cl, id, &last)
		switch {
		case interrupted(ctx, err):
			return nil
		case err != nil && last == nil:
			return err
		case err != nil:
			_, _ = fmt.Fprintf(c.stderr, "edgectl: %v\n", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-c.after(watchInterval):
		}
	}
}

// poll gets deployment id's status once and prints it if its current status differs from *last,
// which it then updates.
func (c cli) poll(ctx context.Context, cmd *cobra.Command, cl *client, id uuid.UUID, last *json.RawMessage) error {
	body, err := cl.get(ctx, operatorapi.StatusPath(id))
	if err != nil {
		return err
	}
	var st struct{ Current json.RawMessage }
	if err := json.Unmarshal(body, &st); err != nil || st.Current == nil {
		return &failure{msg: fmt.Sprintf("GET %s: the response is not a status", operatorapi.StatusPath(id))}
	}
	if *last != nil && bytes.Equal(st.Current, *last) {
		return nil
	}
	if *last != nil && output(cmd) == outputText {
		_, _ = fmt.Fprintln(c.stdout) // a blank line between statuses
	}
	if err := c.print(cmd, body, printStatus); err != nil {
		return err
	}
	*last = st.Current
	return nil
}

// writeFailure returns a failure for a failed write to stdout, nil if err is nil.
func writeFailure(err error) error {
	if err == nil {
		return nil
	}
	return &failure{msg: "write output: " + err.Error(), err: err}
}

// decode decodes a response body into v, or returns a failure.
func decode(body []byte, v any) error {
	if err := json.Unmarshal(body, v); err != nil {
		return &failure{msg: fmt.Sprintf("the CO's response is not what edgectl expects: %v", err)}
	}
	return nil
}

// table writes rows, the first one the header, as aligned columns.
func table(w io.Writer, rows [][]string) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, r := range rows {
		_, _ = fmt.Fprintln(tw, strings.Join(r, "\t")) // the error is Flush's
	}
	return writeFailure(tw.Flush())
}

func printApp(w io.Writer, body []byte) error {
	var a operatorapi.App
	if err := decode(body, &a); err != nil {
		return err
	}
	return appTable(w, []operatorapi.App{a})
}

func printApps(w io.Writer, body []byte) error {
	var apps []operatorapi.App
	if err := decode(body, &apps); err != nil {
		return err
	}
	return appTable(w, apps)
}

func appTable(w io.Writer, apps []operatorapi.App) error {
	rows := [][]string{{"APP", "VERSION", "REPOSITORY", "DIGEST"}}
	for _, a := range apps {
		rows = append(rows, []string{a.ID, a.Version, a.Repository, string(a.Digest)})
	}
	return table(w, rows)
}

// printCreated prints the new deployment's id on the first line (SPEC §11.3).
func printCreated(w io.Writer, body []byte) error {
	var d operatorapi.Deployment
	if err := decode(body, &d); err != nil {
		return err
	}
	_, err := fmt.Fprintln(w, d.ID)
	return writeFailure(err)
}

func printDeployments(w io.Writer, body []byte) error {
	var ds []operatorapi.Deployment
	if err := decode(body, &ds); err != nil {
		return err
	}
	rows := [][]string{{"ID", "APP", "VERSION", "PROFILE", "TARGET", "DELETED"}}
	for _, d := range ds {
		rows = append(rows, []string{d.ID.String(), d.AppID, d.Version, d.Profile, d.Target.String(), fmt.Sprint(d.Deleted)})
	}
	return table(w, rows)
}

func printDeployment(w io.Writer, body []byte) error {
	var d operatorapi.Deployment
	if err := decode(body, &d); err != nil {
		return err
	}
	rows := [][]string{
		{"ID", d.ID.String()}, {"APP", d.AppID}, {"VERSION", d.Version}, {"PROFILE", d.Profile},
		{"TARGET", d.Target.String()}, {"NAME", d.Name}, {"NAMESPACE", d.Namespace},
		{"DIGEST", string(d.Digest)}, {"DELETED", fmt.Sprint(d.Deleted)},
	}
	for _, name := range slices.Sorted(maps.Keys(d.Parameters)) {
		v, _ := json.Marshal(d.Parameters[name]) // a value decoded from JSON encodes
		rows = append(rows, []string{"PARAM", name + "=" + string(v)})
	}
	return table(w, rows)
}

func printStatus(w io.Writer, body []byte) error {
	var st operatorapi.Status
	if err := decode(body, &st); err != nil {
		return err
	}
	cur := st.Current
	if cur == nil {
		_, err := fmt.Fprintln(w, "no status reported yet")
		return writeFailure(err)
	}
	rows := [][]string{
		{"STATE", string(cur.Status.State) + statusError(cur.Status.Error)},
		{"DEVICE", cur.DeviceID.String()},
		{"MANIFEST VERSION", fmt.Sprint(cur.AdoptedManifestVersion)},
		{"COMPONENT", "STATE"},
	}
	for _, comp := range cur.Components {
		rows = append(rows, []string{comp.Name, string(comp.State) + statusError(comp.Error)})
	}
	return table(w, rows)
}

// statusError returns e as a suffix of a state, "" for nil.
func statusError(e *contract.StatusError) string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf(" (%s: %s)", e.Code, e.Message)
}

func printDevices(w io.Writer, body []byte) error {
	var devs []operatorapi.Device
	if err := decode(body, &devs); err != nil {
		return err
	}
	rows := [][]string{{"DEVICE", "MEMORY", "LABELS"}}
	for _, d := range devs {
		var labels []string
		for _, k := range slices.Sorted(maps.Keys(d.Capabilities.Labels)) {
			labels = append(labels, k+"="+d.Capabilities.Labels[k])
		}
		rows = append(rows, []string{d.DeviceID.String(), d.Capabilities.Properties.Memory, strings.Join(labels, ",")})
	}
	return table(w, rows)
}
