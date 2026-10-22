package harness_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/edgectl"
	"github.com/balaji-balu/ieo/internal/operatorapi"
)

const (
	// coURLEnv names the harness CO's operator API, e.g. http://localhost:8443. The test skips
	// without it, as the CO store tests do without IEO_TEST_DATABASE_URL.
	coURLEnv = "IEO_HARNESS_CO_URL"
	// envFileEnv, when set, names a file the test appends DEPLOYMENT_ID=<id> and
	// COMPOSE_PROJECT=<project> to, for the checks CI runs on the hosts afterwards ($GITHUB_ENV).
	envFileEnv = "IEO_HARNESS_ENV_FILE"

	// appRepository is the test application's package, as the CO reaches the harness registry
	// (tools/harnessdeploy pushes it there).
	appRepository = "registry:5000/ieo-harness/hello"
	// target is host1's EN: LO_SITE_ID and EN_HOST_ID of en1 in deploy/dev/compose.yaml.
	target = "00000000-0000-4000-8000-000000000001/host-1"

	// installDeadline bounds the wait for the installed status (G-F4): the LO's poll, the EN's
	// pulls and the Compose project's start on host1.
	installDeadline = 4 * time.Minute
	pollInterval    = 2 * time.Second
)

// appDir is the harness's test application, from this package's directory.
const appDir = "hello"

// SPEC §17.8: "Golden path: import app → deploy directed → `installed` visible via `edgectl`"
//
// Part 1, up to installed (roadmap E6). Roadmap slices F and G add the update and the delete.
// tools/harnessdeploy must have pushed the test application and reported the devices first
// (docs/dev-setup.md).
func TestSpec_17_8_GoldenPath(t *testing.T) {
	coURL := os.Getenv(coURLEnv)
	if coURL == "" {
		t.Skipf("%s is not set; see docs/dev-setup.md", coURLEnv)
	}
	ctx := t.Context()
	run := edgectlRunner(coURL)

	desc := description(t)

	// SPEC §11.3, §15.6: the operator API answers no request without the operator token.
	if code := codeWithoutToken(ctx, t, coURL); code != http.StatusUnauthorized {
		t.Fatalf("GET %s without the operator token: %d, want %d", operatorapi.AppsPath, code, http.StatusUnauthorized)
	}

	// SPEC §5.3, §11.3: edgectl app import.
	out := run.ok(ctx, t, "app", "import", "--repo", appRepository, "--version", desc.Metadata.Version, "--output", "json")
	var app operatorapi.App
	decode(t, "app import", out, &app)
	if app.ID != desc.ID || app.Version != desc.Metadata.Version || app.Repository != appRepository {
		t.Fatalf("imported %+v, want %s %s from %s", app, desc.ID, desc.Metadata.Version, appRepository)
	}

	// SPEC §8.1, §11.3: edgectl deploy, directed to host-1.
	out = run.ok(ctx, t, "deploy", "--app", app.ID, "--version", app.Version, "--target", target, "--output", "json")
	var d operatorapi.Deployment
	decode(t, "deploy", out, &d)
	if d.ID == uuid.Nil || d.Target.String() != target {
		t.Fatalf("deployed %+v, want a deployment to %s", d, target)
	}
	t.Logf("deployment %s", d.ID)
	writeEnvFile(t, d, desc)

	// SPEC §8.7, §8.8, §11.3: the status the LO reports reaches edgectl deployment status.
	cur := waitInstalled(ctx, t, run, d.ID)
	if cur.DeviceID.String() != target {
		t.Errorf("installed on %s, want %s", cur.DeviceID, target)
	}
	if len(cur.Components) != 1 || cur.Components[0].State != contract.StateInstalled {
		t.Errorf("components %+v, want one, installed", cur.Components)
	}
}

// waitInstalled polls edgectl deployment status until the deployment's current status is
// installed, and returns it. It fails the test at installDeadline, with the last answer.
func waitInstalled(ctx context.Context, t *testing.T, run runner, id uuid.UUID) *contract.DeploymentStatus {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, installDeadline)
	defer cancel()
	var last string // the last answer, for the failure message
	for {
		code, stdout, stderr := run.run(ctx, "deployment", "status", id.String(), "--output", "json")
		switch {
		case code != 0: // the CO may be restarting: poll again
			last = fmt.Sprintf("exit %d: %s", code, strings.TrimSpace(stderr))
		default:
			var st operatorapi.Status
			if err := json.Unmarshal([]byte(stdout), &st); err != nil {
				t.Fatalf("deployment status: %v in %s", err, stdout)
			}
			if st.Current != nil && st.Current.Status.State == contract.StateInstalled {
				return st.Current
			}
			last = strings.TrimSpace(stdout)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("deployment %s not installed within %s; last status: %s", id, installDeadline, last)
		case <-time.After(pollInterval):
		}
	}
}

// runner runs edgectl in-process against the harness CO.
type runner struct{ getenv func(string) string }

// edgectlRunner returns a runner whose EDGECTL_CO_URL is coURL. The other settings, the token
// among them, are the process's EDGECTL_* variables (docs/dev-setup.md).
func edgectlRunner(coURL string) runner {
	return runner{getenv: func(k string) string {
		if k == "EDGECTL_CO_URL" {
			return coURL
		}
		return os.Getenv(k)
	}}
}

// run runs edgectl with args and returns its exit code, stdout and stderr.
func (r runner) run(ctx context.Context, args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := edgectl.Run(ctx, args, r.getenv, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// ok runs edgectl with args and returns its stdout; it fails the test unless edgectl exits 0.
func (r runner) ok(ctx context.Context, t *testing.T, args ...string) string {
	t.Helper()
	code, stdout, stderr := r.run(ctx, args...)
	if code != 0 {
		t.Fatalf("edgectl %s: exit %d\n%s", strings.Join(args, " "), code, stderr)
	}
	return stdout
}

// description returns the test application's Application Description.
func description(t *testing.T) contract.ApplicationDescription {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(appDir, "margo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	desc, err := contract.ParseApplicationDescription(b)
	if err != nil {
		t.Fatal(err)
	}
	return desc
}

// codeWithoutToken returns the status code of GET /api/v1/apps sent with no Authorization header.
func codeWithoutToken(ctx context.Context, t *testing.T, coURL string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(coURL, "/")+operatorapi.AppsPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", operatorapi.AppsPath, err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func decode(t *testing.T, what, out string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(out), v); err != nil {
		t.Fatalf("%s: %v in %s", what, err, out)
	}
}

// writeEnvFile appends the deployment's id and the Compose project of its one component to the
// file IEO_HARNESS_ENV_FILE names, if it is set (SPEC §4.2: the project name).
func writeEnvFile(t *testing.T, d operatorapi.Deployment, desc contract.ApplicationDescription) {
	t.Helper()
	name := os.Getenv(envFileEnv)
	if name == "" {
		return
	}
	var components []string
	for _, p := range desc.DeploymentProfiles {
		if p.ID == d.Profile {
			for _, c := range p.Components {
				components = append(components, c.Name)
			}
		}
	}
	if len(components) != 1 {
		t.Fatalf("profile %q has components %q; COMPOSE_PROJECT names one", d.Profile, components)
	}
	f, err := os.OpenFile(name, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(f, "DEPLOYMENT_ID=%s\nCOMPOSE_PROJECT=%s\n", d.ID, contract.ComposeProjectName(d.ID, components[0]))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatalf("%s: %v", envFileEnv, err)
	}
}
