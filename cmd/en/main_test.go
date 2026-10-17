package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/en/archive"
	"github.com/balaji-balu/ieo/internal/en/archive/archivetest"
	"github.com/balaji-balu/ieo/internal/en/compose"
	"github.com/balaji-balu/ieo/internal/en/compose/composetest"
	"github.com/balaji-balu/ieo/internal/en/override"
	"github.com/balaji-balu/ieo/internal/en/pull"
	"github.com/balaji-balu/ieo/internal/ocitest"
	"github.com/balaji-balu/ieo/internal/sitenats"
	"github.com/balaji-balu/ieo/internal/sitenats/natstest"
)

const (
	siteID = "site-1"
	hostID = "host-1"

	// The site's NATS login and a registry login in these tests, all made up here (SPEC §15.6).
	natsUser          = "site-1"
	natsPassword      = "made-up-for-the-en-tests"
	wrongNATSPassword = "another-made-up-one"
	registryUser      = "puller"
	registryPassword  = "made-up-registry-password-for-the-en-tests"

	// deadline guards against a hang only: nothing waits this long unless it is about to fail.
	deadline = 10 * time.Second
	// notYet is how long a test watches for something that must not have happened yet.
	notYet = 200 * time.Millisecond

	msgNATSConnected = "connected to the site NATS server"
	revision         = "1.0.0"
)

// registryAuth is the `auth` value of the registry login, as a Docker config.json holds it.
var registryAuth = base64.StdEncoding.EncodeToString([]byte(registryUser + ":" + registryPassword))

var (
	siteLogin    = natstest.Options{Username: natsUser, Password: natsPassword}
	deploymentID = uuid.MustParse("0a0a0a0a-0000-4000-8000-00000000000a")
	digestA      = contract.DigestOf([]byte("deployment A"))
)

// logBuffer is the EN's stderr: it keeps what run writes and reports each write, so a test can
// wait for a log line without polling.
type logBuffer struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	written chan struct{}
}

func newLogBuffer() *logBuffer { return &logBuffer{written: make(chan struct{}, 1)} }

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.buf.Write(p)
	select {
	case b.written <- struct{}{}:
	default: // a report is already pending
	}
	return n, err
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// lines decodes every line written so far, failing t on a line that is not one JSON object
// (SPEC §17.7).
func (b *logBuffer) lines(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(b.String()))
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("log line is not one JSON object: %q", sc.Text())
		}
		out = append(out, m)
	}
	return out
}

// runningEN is run in a goroutine.
type runningEN struct {
	t      *testing.T
	stderr *logBuffer
	cancel context.CancelFunc
	done   chan int
	code   *int // set once run has returned
}

// startEN runs the EN with env and args, and the Compose runner r, until the test stops it or the
// test ends.
func startEN(t *testing.T, env map[string]string, r newRunner, args ...string) *runningEN {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	e := &runningEN{t: t, stderr: newLogBuffer(), cancel: cancel, done: make(chan int, 1)}
	go func() { e.done <- run(ctx, args, func(k string) string { return env[k] }, e.stderr, r) }()
	t.Cleanup(func() { e.stop() })
	return e
}

// runEN runs the EN with env and args, which must make it exit by itself, and returns its exit
// code and stderr.
func runEN(t *testing.T, env map[string]string, r newRunner, args ...string) (int, *logBuffer) {
	t.Helper()
	e := startEN(t, env, r, args...)
	select {
	case code := <-e.done:
		e.code = &code
		return code, e.stderr
	case <-time.After(deadline):
		t.Fatalf("en did not exit:\n%s", e.stderr)
		return -1, nil
	}
}

// waitMsg waits for a log line with message msg, and returns it.
func (e *runningEN) waitMsg(msg string) map[string]any {
	e.t.Helper()
	return e.waitLine("a log line "+msg, func(line map[string]any) bool { return line["msg"] == msg })
}

// waitLine waits for a log line that is the one wanted, and returns it.
func (e *runningEN) waitLine(what string, wanted func(line map[string]any) bool) map[string]any {
	e.t.Helper()
	timeout := time.After(deadline)
	for {
		for _, line := range e.stderr.lines(e.t) {
			if wanted(line) {
				return line
			}
		}
		select {
		case <-e.stderr.written:
		case code := <-e.done:
			e.code = &code
			e.t.Fatalf("en exited with %d before %s:\n%s", code, what, e.stderr)
		case <-timeout:
			e.t.Fatalf("no %s:\n%s", what, e.stderr)
		}
	}
}

// stop cancels run's context, as SIGINT or SIGTERM does, and returns its exit code.
func (e *runningEN) stop() int {
	e.t.Helper()
	e.cancel()
	return e.wait()
}

// wait returns the exit code once run has returned.
func (e *runningEN) wait() int {
	e.t.Helper()
	if e.code != nil {
		return *e.code
	}
	select {
	case code := <-e.done:
		e.code = &code
		return code
	case <-time.After(deadline):
		e.t.Fatalf("en did not return within %v of its context ending", deadline)
		return -1
	}
}

// fake returns a newRunner that gives r.
func fake(r compose.Runner) newRunner {
	return func(context.Context, compose.CLIConfig) (compose.Runner, error) { return r, nil }
}

// natsEnv adds to env the settings of an EN that connects to srv as the site's user. The server
// has no TLS unless the test started it with it, so en.nats_insecure is set for a nats:// URL.
func natsEnv(env map[string]string, srv *natstest.Server) {
	env["EN_NATS_URL"], env["EN_NATS_USERNAME"], env["EN_NATS_PASSWORD"] = srv.URL(), natsUser, natsPassword
	delete(env, "EN_NATS_CA_FILE")
	delete(env, "EN_NATS_INSECURE")
	if ca := srv.CAFile(); ca != "" {
		env["EN_NATS_CA_FILE"] = ca
	} else {
		env["EN_NATS_INSECURE"] = "true"
	}
}

// enEnv is the environment of the EN of host-1 of site-1, with a data directory of its own, that
// connects to srv and reaches registries over plain HTTP. more may replace any of it; an empty
// value removes the variable.
func enEnv(t *testing.T, srv *natstest.Server, more ...string) map[string]string {
	t.Helper()
	env := map[string]string{
		"EN_SITE_ID": siteID, "EN_HOST_ID": hostID, "EN_DATA_DIR": t.TempDir(),
		"EN_OTEL_HTTP_ENDPOINT": "http://otel.test:4318", "EN_REGISTRY_INSECURE": "true",
	}
	natsEnv(env, srv)
	for i := 0; i+1 < len(more); i += 2 {
		if more[i+1] == "" {
			delete(env, more[i])
		} else {
			env[more[i]] = more[i+1]
		}
	}
	return env
}

// lo plays the site's LO: it sends commands to host-1 and receives what the EN reports.
type lo struct {
	t           *testing.T
	conn        *sitenats.Conn
	site        sitenats.Site
	host        sitenats.Host
	statuses    chan contract.ComponentStatusEvent
	inventories chan contract.Inventory
}

// playLO connects to srv as the LO, and subscribes to host-1's reports.
func playLO(t *testing.T, srv *natstest.Server) *lo {
	t.Helper()
	conn, err := sitenats.Connect(sitenats.Config{
		URL: srv.URL(), Username: natsUser, Password: natsPassword, Insecure: true,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(conn.Close)
	site, err := sitenats.ForSite(siteID)
	if err != nil {
		t.Fatal(err)
	}
	host, err := site.Host(hostID)
	if err != nil {
		t.Fatal(err)
	}
	l := &lo{t: t, conn: conn, site: site, host: host,
		statuses: make(chan contract.ComponentStatusEvent, 64), inventories: make(chan contract.Inventory, 64)}
	subscribe := func(subject string, got func([]byte)) {
		sub, err := conn.Subscribe(subject, func(m sitenats.Msg) { got(m.Data) })
		if err != nil {
			t.Fatalf("Subscribe %s: %v", subject, err)
		}
		t.Cleanup(sub.Stop)
	}
	subscribe(host.Status(), func(data []byte) {
		e, err := contract.DecodeSiteMessage[contract.ComponentStatusEvent](data)
		if err != nil {
			t.Errorf("a status event the LO would drop: %v", err)
		}
		l.statuses <- e
	})
	subscribe(host.Inventory(), func(data []byte) {
		inv, err := contract.DecodeSiteMessage[contract.Inventory](data)
		if err != nil {
			t.Errorf("an Inventory the LO would drop: %v", err)
		}
		l.inventories <- inv
	})
	// The subscriptions are known to the server once a request on this connection is answered.
	if _, err := conn.Request(context.Background(), "test.none", nil); err == nil {
		t.Fatal("a request to no subscriber was answered")
	}
	return l
}

// send sends cmd to host-1 and returns the ack.
func (l *lo) send(cmd contract.Command) (contract.CommandAck, error) {
	l.t.Helper()
	data, err := contract.EncodeSiteMessage(cmd)
	if err != nil {
		l.t.Fatalf("encode command: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	reply, err := l.conn.Request(ctx, l.host.Cmd(), data)
	if err != nil {
		return contract.CommandAck{}, err
	}
	ack, err := contract.DecodeSiteMessage[contract.CommandAck](reply)
	if err != nil {
		l.t.Fatalf("an ack the LO would drop: %v", err)
	}
	return ack, nil
}

// inventory waits for an Inventory for which wanted holds.
func (l *lo) inventory(what string, wanted func(contract.Inventory) bool) contract.Inventory {
	l.t.Helper()
	timeout := time.After(deadline)
	for {
		select {
		case inv := <-l.inventories:
			if wanted(inv) {
				return inv
			}
		case <-timeout:
			l.t.Fatalf("no Inventory %s", what)
		}
	}
}

// anyInventory is the first Inventory: the EN has subscribed to its commands once it is sent.
func (l *lo) anyInventory() contract.Inventory {
	l.t.Helper()
	return l.inventory("at all", func(contract.Inventory) bool { return true })
}

// states waits for the status events of the deployment until one has state last, and returns the
// states seen, in order.
func (l *lo) states(last contract.ComponentState) []contract.ComponentState {
	l.t.Helper()
	var seen []contract.ComponentState
	timeout := time.After(deadline)
	for {
		select {
		case e := <-l.statuses:
			seen = append(seen, e.State)
			if e.State == last {
				return seen
			}
		case <-timeout:
			l.t.Fatalf("no status event %s; seen %v", last, seen)
		}
	}
}

// startRegistry serves reg on 127.0.0.1 over plain HTTP, through check, and returns its host.
func startRegistry(t *testing.T, reg *ocitest.Registry, check func(http.Handler) http.Handler) string {
	t.Helper()
	h := reg.Handler()
	if check != nil {
		h = check(h)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// withLogin serves next only to the registry login.
func withLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); ok && u == registryUser && p == registryPassword {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
}

// pushWeb pushes the Compose archive of the component web, with one service, to host.
func pushWeb(t *testing.T, reg *ocitest.Registry, host string) {
	t.Helper()
	yaml := "services:\n  web:\n    image: example\n"
	reg.PushComposeArchive(t, host+"/acme/web", revision, archivetest.Build(t, archivetest.Valid("app", yaml)...))
}

// applyWeb is an Apply of a deployment of the component web, pulled from host.
func applyWeb(host string) contract.Command {
	d := contract.ApplicationDeployment{ID: deploymentID}
	d.Metadata = contract.DeploymentMetadata{
		Name: "hello", Namespace: "default", Annotations: map[string]string{},
		DeviceID: contract.DeviceID{Site: siteID, Host: hostID},
	}
	d.Spec.ApplicationID = "hello-world"
	d.Spec.DeploymentProfile.Type = "compose"
	d.Spec.DeploymentProfile.Components = []contract.Component{{
		Name:       "web",
		Properties: contract.ComponentProperties{Repository: "oci://" + host + "/acme/web", Revision: revision},
	}}
	return contract.Command{CommandID: uuid.New(), Action: contract.ActionApply, DeploymentID: deploymentID, Digest: digestA, Deployment: &d}
}

// endToEnd runs an EN with env (from enEnv, on srv) that applies a deployment pulled from the
// registry at host, as the LO asks, and checks each step.
func endToEnd(t *testing.T, srv *natstest.Server, env map[string]string, reg *ocitest.Registry, host string) *runningEN {
	t.Helper()
	l := playLO(t, srv)
	runner := composetest.New()
	en := startEN(t, env, fake(runner))
	if inv := l.anyInventory(); inv.HostID != hostID || len(inv.Deployments) != 0 {
		t.Fatalf("first Inventory = %+v; want host-1 with no deployment", inv)
	}

	ack, err := l.send(applyWeb(host))
	if err != nil || !ack.Accepted {
		t.Fatalf("Apply: ack %+v, %v; want it accepted", ack, err)
	}
	if got := l.states(contract.StateInstalled); len(got) < 2 || got[0] != contract.StateInstalling {
		t.Errorf("status events %v; want installing, then installed", got)
	}
	if want := contract.ComposeProjectName(deploymentID, "web"); len(runner.Projects()) != 1 || runner.Projects()[0] != want {
		t.Errorf("projects up %v; want %s", runner.Projects(), want)
	}

	request, err := contract.EncodeSiteMessage(contract.InventoryRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.conn.Publish(l.site.InventoryRequest(), request); err != nil {
		t.Fatalf("publish inventory.request: %v", err)
	}
	l.inventory("with the deployment", func(inv contract.Inventory) bool {
		return len(inv.Deployments) == 1 && inv.Deployments[0].DeploymentID == deploymentID && inv.Deployments[0].Digest == digestA
	})
	return en
}

// The EN takes an Apply from the LO, pulls the component from the registry over HTTP, runs it,
// and reports it (SPEC §8.9, §11.2, §16.7).
func TestENAppliesADeploymentEndToEnd(t *testing.T) {
	srv := natstest.Start(t, siteLogin)
	reg := ocitest.NewRegistry()
	host := startRegistry(t, reg, nil)
	pushWeb(t, reg, host)
	en := endToEnd(t, srv, enEnv(t, srv), reg, host)
	if code := en.stop(); code != 0 {
		t.Errorf("exit %d after the context ended, want 0:\n%s", code, en.stderr)
	}
}

// SPEC §17.7: "Every LO and EN log line carries `site_id`; every EN log line and every host-scoped
// LO log line carries `host_id`" (§13.1). Here: the EN's, from the moment it knows its host ID.
func TestSpec_17_7_EveryENLogLineCarriesSiteAndHostID(t *testing.T) {
	srv := natstest.Start(t, siteLogin)
	reg := ocitest.NewRegistry()
	host := startRegistry(t, reg, nil)
	pushWeb(t, reg, host)
	en := endToEnd(t, srv, enEnv(t, srv), reg, host)
	en.stop()
	lines := en.stderr.lines(t)
	if len(lines) < 3 {
		t.Fatalf("%d log lines; want those of a whole run:\n%s", len(lines), en.stderr)
	}
	for _, line := range lines {
		if line["site_id"] != siteID || line["host_id"] != hostID {
			t.Errorf("a line without site_id %s and host_id %s: %v", siteID, hostID, line)
		}
	}
}

// refusedAtStartup runs an EN with env, which must exit with a configuration error that names
// key, without connecting to the site NATS server.
func refusedAtStartup(t *testing.T, env map[string]string, key, what string, args ...string) *logBuffer {
	t.Helper()
	code, stderr := runEN(t, env, fake(composetest.New()), args...)
	// Decoded, so a Windows path is not escaped as it is in JSON.
	if code != 2 || !strings.Contains(fmt.Sprint(stderr.lines(t)), key) {
		t.Errorf("%s: exit %d, want 2 with an error naming %s:\n%s", what, code, key, stderr)
	}
	if strings.Contains(stderr.String(), msgNATSConnected) {
		t.Errorf("%s: the EN connected:\n%s", what, stderr)
	}
	return stderr
}

// warned reports whether e logged a warning, with site_id, that holds text.
func warned(t *testing.T, e *runningEN, text string) bool {
	t.Helper()
	for _, line := range e.stderr.lines(t) {
		if line["level"] == "WARN" && line["site_id"] == siteID && strings.Contains(fmt.Sprint(line), text) {
			return true
		}
	}
	return false
}

// SPEC §17.7: "Until scoped NATS credentials, the LO and the EN accept a NATS URL other than
// `tls://` only when it is a `nats://` URL and `lo.nats_insecure`/`en.nats_insecure` is set, and
// exit at startup on any other" (§15.6). This is the EN's half; the LO's is in cmd/lo.
func TestSpec_17_7_ENAcceptsANATSURLWithoutTLSOnlyWhenInsecure(t *testing.T) {
	withTLS := siteLogin
	withTLS.TLS = true

	t.Run("tls with en.nats.ca_file", func(t *testing.T) {
		en := startEN(t, enEnv(t, natstest.Start(t, withTLS)), fake(composetest.New()))
		en.waitMsg(msgNATSConnected)
		if code := en.stop(); code != 0 {
			t.Errorf("exit %d after shutdown, want 0", code)
		}
		if warned(t, en, "TLS") {
			t.Errorf("a warning about TLS with a tls:// URL:\n%s", en.stderr)
		}
	})

	t.Run("nats without en.nats_insecure", func(t *testing.T) {
		srv := natstest.Start(t, siteLogin)
		for _, insecure := range []string{"", "false"} {
			env := enEnv(t, srv)
			if insecure == "" {
				delete(env, "EN_NATS_INSECURE")
			} else {
				env["EN_NATS_INSECURE"] = insecure
			}
			refusedAtStartup(t, env, "EN_NATS_URL", "EN_NATS_INSECURE="+insecure)
		}
	})

	t.Run("nats with en.nats_insecure", func(t *testing.T) {
		srv := natstest.Start(t, siteLogin)
		en := startEN(t, enEnv(t, srv), fake(composetest.New()))
		en.waitMsg(msgNATSConnected)
		en.stop()
		if !warned(t, en, srv.URL()) {
			t.Errorf("no startup warning naming %s:\n%s", srv.URL(), en.stderr)
		}
	})

	t.Run("any other scheme, with or without en.nats_insecure", func(t *testing.T) {
		srv := natstest.Start(t, siteLogin)
		for _, scheme := range []string{"ws", "wss", "http"} {
			for _, insecure := range []string{"false", "true"} {
				refusedAtStartup(t, enEnv(t, srv, "EN_NATS_URL", scheme+"://"+srv.Addr(), "EN_NATS_INSECURE", insecure),
					"EN_NATS_URL", scheme+":// with EN_NATS_INSECURE="+insecure)
			}
		}
	})

	t.Run("en.nats.ca_file that cannot be used", func(t *testing.T) {
		srv := natstest.Start(t, withTLS)
		notPEM := filepath.Join(t.TempDir(), "ca.pem")
		if err := os.WriteFile(notPEM, []byte("not a certificate"), 0o600); err != nil {
			t.Fatal(err)
		}
		for name, file := range map[string]string{"missing": filepath.Join(t.TempDir(), "none.pem"), "not PEM": notPEM} {
			refusedAtStartup(t, enEnv(t, srv, "EN_NATS_CA_FILE", file), "EN_NATS_CA_FILE", "CA file "+name)
		}
	})
}

// authFile writes a Docker config.json with an entry for host, and returns its path.
func authFile(t *testing.T, host, auth string) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{"auths": map[string]any{host: map[string]string{"auth": auth}}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// SPEC §17.7: "No credential, key or token appears in logs or status messages." Here: the NATS
// password and the registry credential, in a whole run and on each configuration error about
// them (§15.4).
func TestSpec_17_7_NoCredentialOrTokenInENLogs(t *testing.T) {
	var logs []*logBuffer
	srv := natstest.Start(t, siteLogin)
	reg := ocitest.NewRegistry()
	host := startRegistry(t, reg, withLogin)
	pushWeb(t, reg, host)
	en := endToEnd(t, srv, enEnv(t, srv, "EN_REGISTRY_AUTH_FILE", authFile(t, host, registryAuth)), reg, host)
	en.stop()
	logs = append(logs, en.stderr)

	notBase64 := authFile(t, host, "%"+registryPassword)
	logs = append(logs, refusedAtStartup(t, enEnv(t, srv, "EN_REGISTRY_AUTH_FILE", notBase64), notBase64, "an auth file that cannot be decoded"))
	inURL := strings.Replace(srv.URL(), "://", "://"+natsUser+":"+natsPassword+"@", 1)
	logs = append(logs, refusedAtStartup(t, enEnv(t, srv, "EN_NATS_URL", inURL), "EN_NATS_URL", "a password in the NATS URL"))
	logs = append(logs, refusedAtStartup(t, enEnv(t, srv), "nats-password", "a password as a flag", "--nats-password", natsPassword))

	refused := startEN(t, enEnv(t, srv, "EN_NATS_PASSWORD", wrongNATSPassword), fake(composetest.New()))
	refused.waitMsg("cannot connect to the site NATS server")
	refused.stop()
	logs = append(logs, refused.stderr)

	for _, l := range logs {
		for _, secret := range []string{natsPassword, wrongNATSPassword, registryPassword, registryAuth} {
			if strings.Contains(l.String(), secret) {
				t.Errorf("a log holds %q:\n%s", secret, l)
			}
		}
	}
}

// Each key of SPEC §6.3 that the EN reads: a value that is missing or not valid is a configuration
// error that names the key, and the EN exits 2 before it connects (SPEC §6.1).
func TestConfigErrorExits2(t *testing.T) {
	srv := natstest.Start(t, siteLogin)
	cases := []struct {
		key, value string
		want       string // in the error; key when empty
	}{
		{key: "EN_SITE_ID"},
		{key: "EN_SITE_ID", value: "site.1"},
		{key: "EN_HOST_ID", value: "host.1"},
		{key: "EN_HOST_ID", value: "host 1"},
		{key: "EN_DATA_DIR"},
		{key: "EN_NATS_URL"},
		{key: "EN_NATS_USERNAME"},
		{key: "EN_NATS_PASSWORD"},
		{key: "EN_NATS_INSECURE", value: "maybe"},
		{key: "EN_RUNTIME", value: "lxc"},
		{key: "EN_INVENTORY_INTERVAL", value: "0s"},
		{key: "EN_INVENTORY_INTERVAL", value: "often"},
		{key: "EN_OTEL_HTTP_ENDPOINT"},
		{key: "EN_OTEL_HTTP_ENDPOINT", value: "otel.test:4318"},
		{key: "EN_OTEL_GRPC_ENDPOINT", value: "ftp://otel.test"},
		{key: "EN_REGISTRY_INSECURE", value: "maybe"},
		{key: "EN_REGISTRY_AUTH_FILE", value: filepath.Join(t.TempDir(), "none.json")},
		{key: "EN_PULL_MAX_BYTES", value: "0"},
		{key: "EN_PULL_MAX_BYTES", value: "256MiB"},
		{key: "EN_PULL_TIMEOUT", value: "-1s"},
		{key: "EN_ARCHIVE_MAX_EXTRACTED_BYTES", value: "-5"},
		{key: "EN_ARCHIVE_MAX_ENTRIES", value: "0"},
		{key: "EN_START_TIMEOUT", value: "0s"},
	}
	for _, tc := range cases {
		env := enEnv(t, srv)
		if tc.value == "" {
			delete(env, tc.key)
		} else {
			env[tc.key] = tc.value
		}
		refusedAtStartup(t, env, tc.key, fmt.Sprintf("%s=%q", tc.key, tc.value))
	}
	refusedAtStartup(t, enEnv(t, srv), "argument", "an argument that is not a flag", "now")
}

// A data directory that cannot be created is a failure of the host, not of the configuration
// (SPEC §6.1), and the error names en.data_dir.
func TestDataDirThatCannotBeCreatedExits1(t *testing.T) {
	srv := natstest.Start(t, siteLogin)
	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	code, stderr := runEN(t, enEnv(t, srv, "EN_DATA_DIR", filepath.Join(file, "en")), fake(composetest.New()))
	if code != 1 || !strings.Contains(stderr.String(), "EN_DATA_DIR") {
		t.Errorf("a data directory under a file: exit %d, want 1 with an error naming EN_DATA_DIR:\n%s", code, stderr)
	}
}

// ADR 0016: a host.id that differs from en.host_id is a configuration error.
func TestHostIDThatDiffersExits2(t *testing.T) {
	srv := natstest.Start(t, siteLogin)
	env := enEnv(t, srv)
	if err := os.WriteFile(filepath.Join(env["EN_DATA_DIR"], "host.id"), []byte("host-9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	refusedAtStartup(t, env, "EN_HOST_ID", "host.id holds another ID")
}

// Each value is read from its environment variable, and its flag wins (SPEC §6.1); each key not
// set has the default of SPEC §6.3.
func TestConfig(t *testing.T) {
	env := map[string]string{
		"EN_SITE_ID": siteID, "EN_DATA_DIR": "/var/lib/ieo/en", "EN_NATS_URL": "tls://nats.test:4222",
		"EN_NATS_USERNAME": natsUser, "EN_NATS_PASSWORD": natsPassword, "EN_OTEL_HTTP_ENDPOINT": "http://otel.test:4318",
	}
	cfg, err := loadConfig(nil, func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	want := config{
		SiteID: siteID, DataDir: "/var/lib/ieo/en", Runtime: "docker",
		NATSURL: "tls://nats.test:4222", NATSUsername: natsUser, NATSPassword: natsPassword,
		InventoryInterval: time.Minute, OTel: override.OTel{HTTPEndpoint: "http://otel.test:4318"},
		Pull:         pull.Limits{MaxBytes: 256 << 20, Timeout: 5 * time.Minute},
		Archive:      archive.Limits{MaxEntries: 10000, MaxExtractedBytes: 1 << 30},
		StartTimeout: 5 * time.Minute,
	}
	if cfg != want {
		t.Errorf("defaults:\n got %+v\nwant %+v", cfg, want)
	}

	cfg, err = loadConfig([]string{
		"--site-id", "site-2", "--host-id", "host-7", "--runtime", "podman", "--inventory-interval", "30s",
		"--otel-grpc-endpoint", "http://otel.test:4317", "--registry-insecure", "--registry-auth-file", "/etc/ieo/config.json",
		"--pull-max-bytes", "1024", "--pull-timeout", "1m", "--archive-max-extracted-bytes", "2048",
		"--archive-max-entries", "10", "--start-timeout", "2m", "--nats-insecure", "--nats-url", "nats://nats.test:4222",
	}, func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("loadConfig with flags: %v", err)
	}
	want = config{
		SiteID: "site-2", HostID: "host-7", DataDir: "/var/lib/ieo/en", Runtime: "podman",
		NATSURL: "nats://nats.test:4222", NATSInsecure: true, NATSUsername: natsUser, NATSPassword: natsPassword,
		InventoryInterval: 30 * time.Second,
		OTel:              override.OTel{HTTPEndpoint: "http://otel.test:4318", GRPCEndpoint: "http://otel.test:4317"},
		RegistryAuthFile:  "/etc/ieo/config.json", RegistryInsecure: true,
		Pull:         pull.Limits{MaxBytes: 1024, Timeout: time.Minute},
		Archive:      archive.Limits{MaxEntries: 10, MaxExtractedBytes: 2048},
		StartTimeout: 2 * time.Minute,
	}
	if cfg != want {
		t.Errorf("flags:\n got %+v\nwant %+v", cfg, want)
	}
}

// SPEC §16.7: "Before it subscribes, the EN removes what an earlier run left unfinished: every file
// in `<en.data_dir>/pull/`, and every unfinished extraction under `<en.data_dir>/deployments/`".
func TestStartupRemovesUnfinishedWork(t *testing.T) {
	srv := natstest.Start(t, siteLogin)
	env := enEnv(t, srv)
	dataDir := env["EN_DATA_DIR"]
	layer := filepath.Join(dataDir, "pull", "layer-123")
	staging := filepath.Join(dataDir, "deployments", deploymentID.String(), "sha256-0123", "web"+archive.StagingInfix+"456")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(layer), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layer, []byte("part of a layer"), 0o600); err != nil {
		t.Fatal(err)
	}

	l := playLO(t, srv)
	startEN(t, env, fake(composetest.New()))
	l.anyInventory() // sent once the EN has subscribed
	for _, left := range []string{layer, staging} {
		if _, err := os.Lstat(left); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is still there once the EN has subscribed (%v)", left, err)
		}
	}
}

// blockingRunner is a Compose runner whose Up waits for release, whatever its context says.
type blockingRunner struct {
	*composetest.Fake
	up      chan struct{} // gets a value when Up is called
	release chan struct{}
}

func (b *blockingRunner) Up(ctx context.Context, p compose.Project, o compose.UpOptions) error {
	b.up <- struct{}{}
	<-b.release
	return b.Fake.Up(context.WithoutCancel(ctx), p, o)
}

// SPEC §16.7: "When it stops, it stops receiving commands first, and then waits for the commands
// in flight to end (§8.9)."
func TestStopWaitsForTheCommandInFlight(t *testing.T) {
	srv := natstest.Start(t, siteLogin)
	reg := ocitest.NewRegistry()
	host := startRegistry(t, reg, nil)
	pushWeb(t, reg, host)
	l := playLO(t, srv)
	runner := &blockingRunner{Fake: composetest.New(), up: make(chan struct{}, 1), release: make(chan struct{})}
	en := startEN(t, enEnv(t, srv), fake(runner))
	l.anyInventory()
	if ack, err := l.send(applyWeb(host)); err != nil || !ack.Accepted {
		t.Fatalf("Apply: ack %+v, %v; want it accepted", ack, err)
	}
	select {
	case <-runner.up:
	case <-time.After(deadline):
		t.Fatal("the Apply did not reach Up")
	}

	en.cancel() // as SIGTERM does
	select {
	case code := <-en.done:
		en.code = &code
		t.Fatalf("en returned %d with a command in flight", code)
	case <-time.After(notYet):
	}
	if ack, err := l.send(applyWeb(host)); err == nil {
		t.Errorf("a command sent while the EN stops was acked: %+v", ack)
	}
	close(runner.release)
	if code := en.wait(); code != 0 {
		t.Errorf("exit %d after the command in flight ended, want 0:\n%s", code, en.stderr)
	}
}

// SPEC §14.2, ADR 0016: a store another EN holds is a failure at startup, not a configuration
// error, and the file is left as it is.
func TestStoreHeldExitsAtStartup(t *testing.T) {
	srv := natstest.Start(t, siteLogin)
	env := enEnv(t, srv)
	first := startEN(t, env, fake(composetest.New()))
	first.waitMsg(msgNATSConnected)
	if code, stderr := runEN(t, env, fake(composetest.New())); code != 1 {
		t.Errorf("a second EN on the same data directory: exit %d, want 1:\n%s", code, stderr)
	}
}

// ADR 0003: a host without the Compose CLI fails at startup; it is not a configuration error.
func TestNoComposeCLIExits1(t *testing.T) {
	srv := natstest.Start(t, siteLogin)
	missing := func(context.Context, compose.CLIConfig) (compose.Runner, error) {
		return nil, errors.New(`"docker compose version" did not run`)
	}
	if code, stderr := runEN(t, enEnv(t, srv), missing); code != 1 {
		t.Errorf("no Compose CLI: exit %d, want 1:\n%s", code, stderr)
	}
}
