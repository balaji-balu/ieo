package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
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
	"github.com/balaji-balu/ieo/internal/lo/store"
	"github.com/balaji-balu/ieo/internal/lo/sync/synctest"
	"github.com/balaji-balu/ieo/internal/sitenats/natstest"
)

const manifestPath = "/api/v1/deployments"

var idA = uuid.MustParse("0a0a0a0a-0000-4000-8000-00000000000a")

// logBuffer is the LO's stderr: it keeps what run writes and reports each write, so a test can
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

// runningLO is run in a goroutine.
type runningLO struct {
	t      *testing.T
	stderr *logBuffer
	cancel context.CancelFunc
	done   chan int
	code   *int // set once run has returned
}

// startLO runs the LO with env and args until the test stops it or the test ends.
func startLO(t *testing.T, env map[string]string, args ...string) *runningLO {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	l := &runningLO{t: t, stderr: newLogBuffer(), cancel: cancel, done: make(chan int, 1)}
	go func() { l.done <- run(ctx, args, func(k string) string { return env[k] }, l.stderr) }()
	t.Cleanup(func() { l.stop() })
	return l
}

// waitOutcome waits for a sync attempt with outcome, and returns its log line.
func (l *runningLO) waitOutcome(outcome string) map[string]any {
	l.t.Helper()
	return l.waitLine("a sync attempt with outcome "+outcome, func(line map[string]any) bool { return line["outcome"] == outcome })
}

// waitMsg waits for a log line with message msg, and returns it.
func (l *runningLO) waitMsg(msg string) map[string]any {
	l.t.Helper()
	return l.waitLine("a log line "+msg, func(line map[string]any) bool { return line["msg"] == msg })
}

// waitLine waits for a log line that is the one wanted, and returns it.
func (l *runningLO) waitLine(what string, wanted func(line map[string]any) bool) map[string]any {
	l.t.Helper()
	deadline := time.After(10 * time.Second) // guards against a hang only
	for {
		for _, line := range l.stderr.lines(l.t) {
			if wanted(line) {
				return line
			}
		}
		select {
		case <-l.stderr.written:
		case code := <-l.done:
			l.code = &code
			l.t.Fatalf("lo exited with %d before %s:\n%s", code, what, l.stderr)
		case <-deadline:
			l.t.Fatalf("no %s:\n%s", what, l.stderr)
		}
	}
}

// stop cancels run's context, as SIGINT or SIGTERM does, and returns its exit code.
func (l *runningLO) stop() int {
	l.t.Helper()
	l.cancel()
	if l.code != nil {
		return *l.code
	}
	select {
	case code := <-l.done:
		l.code = &code
		return code
	case <-time.After(10 * time.Second):
		l.t.Fatal("lo did not return within 10s of its context ending")
		return -1
	}
}

// runLO runs the LO with env and args, which must make it exit by itself, and returns its exit
// code and stderr.
func runLO(t *testing.T, env map[string]string, args ...string) (int, *logBuffer) {
	t.Helper()
	l := startLO(t, env, args...)
	select {
	case code := <-l.done:
		l.code = &code
		return code, l.stderr
	case <-time.After(10 * time.Second):
		t.Fatalf("lo did not exit:\n%s", l.stderr)
		return -1, nil
	}
}

// The site's NATS login in these tests. Both passwords are made up here (SPEC §15.6).
const (
	natsUser          = "site-1"
	natsPassword      = "made-up-for-the-lo-tests"
	wrongNATSPassword = "another-made-up-one"
)

// natsEnv adds to env the settings of an LO that connects to srv as the site's user. The server
// has no TLS unless the test started it with it, so lo.nats_insecure is set for a nats:// URL.
func natsEnv(env map[string]string, srv *natstest.Server) {
	env["LO_NATS_URL"], env["LO_NATS_USERNAME"], env["LO_NATS_PASSWORD"] = srv.URL(), natsUser, natsPassword
	delete(env, "LO_NATS_CA_FILE")
	delete(env, "LO_NATS_INSECURE")
	if ca := srv.CAFile(); ca != "" {
		env["LO_NATS_CA_FILE"] = ca
	} else {
		env["LO_NATS_INSECURE"] = "true"
	}
}

// loEnv is the environment of an LO of site-1 that syncs with the CO at coURL, with a data
// directory and a site NATS server of its own. more may replace any of it.
func loEnv(t *testing.T, coURL, token string, more ...string) map[string]string {
	t.Helper()
	env := map[string]string{"LO_SITE_ID": "site-1", "LO_CO_URL": coURL, "LO_SITE_TOKEN": token, "LO_DATA_DIR": t.TempDir()}
	natsEnv(env, natstest.Start(t, natstest.Options{Username: natsUser, Password: natsPassword}))
	for i := 0; i+1 < len(more); i += 2 {
		env[more[i]] = more[i+1]
	}
	return env
}

// tlsCO serves co over HTTPS, and returns the server and a CA file that verifies it.
func tlsCO(t *testing.T, co *synctest.CO) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewTLSServer(co)
	t.Cleanup(srv.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return srv, ca
}

// plainCO serves co over plain HTTP.
func plainCO(t *testing.T, co *synctest.CO) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(co)
	t.Cleanup(srv.Close)
	return srv
}

// publishedCO is a CO whose site has one deployment at manifest version 1.
func publishedCO(t *testing.T) *synctest.CO {
	t.Helper()
	co := synctest.NewCO(t)
	co.Publish(1, map[uuid.UUID][]byte{idA: synctest.YAMLOf(idA, "1")})
	return co
}

// SPEC §17.7: "Until mutual TLS, the LO sends its site token only to an `https://` CO URL unless
// `lo.co_insecure` is set, exits at startup on an `http://` URL without it, and follows no
// redirects (§15.6)."
func TestSpec_17_7_LOSendsTokenOnlyOverHTTPSUnlessInsecure(t *testing.T) {
	t.Run("https with lo.tls.ca_file", func(t *testing.T) {
		co := publishedCO(t)
		srv, ca := tlsCO(t, co)
		l := startLO(t, loEnv(t, srv.URL, co.Token(), "LO_TLS_CA_FILE", ca))
		l.waitOutcome("Accepted")
		if code := l.stop(); code != 0 {
			t.Errorf("exit %d after shutdown, want 0", code)
		}
	})

	t.Run("https verified against the system roots without lo.tls.ca_file", func(t *testing.T) {
		co := publishedCO(t)
		srv, _ := tlsCO(t, co)
		l := startLO(t, loEnv(t, srv.URL, co.Token()))
		l.waitOutcome("Unreachable")
		if reqs := co.TakeRequests(); len(reqs) != 0 {
			t.Errorf("the CO got %d requests over a certificate the LO does not trust", len(reqs))
		}
	})

	t.Run("http without lo.co_insecure", func(t *testing.T) {
		co := publishedCO(t)
		srv := plainCO(t, co)
		code, stderr := runLO(t, loEnv(t, srv.URL, co.Token()))
		if code != 2 || !strings.Contains(stderr.String(), "LO_CO_INSECURE") {
			t.Errorf("exit %d, want 2 with an error naming LO_CO_INSECURE:\n%s", code, stderr)
		}
		if reqs := co.TakeRequests(); len(reqs) != 0 {
			t.Errorf("the CO got %d requests", len(reqs))
		}
	})

	for _, scheme := range []string{"http", "HTTP"} {
		t.Run(scheme+" with lo.co_insecure", func(t *testing.T) {
			co := publishedCO(t)
			srv := plainCO(t, co)
			coURL := scheme + strings.TrimPrefix(srv.URL, "http")
			l := startLO(t, loEnv(t, coURL, co.Token(), "LO_CO_INSECURE", "true"))
			l.waitOutcome("Accepted")
			l.stop()
			warned := false
			for _, line := range l.stderr.lines(t) {
				if line["level"] == "WARN" && strings.Contains(fmt.Sprint(line), coURL) {
					warned = true
				}
			}
			if !warned {
				t.Errorf("no startup warning naming %s:\n%s", coURL, l.stderr)
			}
			if strings.Contains(l.stderr.String(), co.Token()) {
				t.Error("the log contains the token")
			}
		})
	}

	t.Run("redirect", func(t *testing.T) {
		elsewhere := synctest.NewCO(t)
		other := plainCO(t, elsewhere)
		co := publishedCO(t)
		srv, ca := tlsCO(t, co)
		co.Override(manifestPath, synctest.Respond(http.StatusFound, nil, "Location", other.URL+manifestPath))
		l := startLO(t, loEnv(t, srv.URL, co.Token(), "LO_TLS_CA_FILE", ca))
		l.waitOutcome("Unreachable")
		l.stop()
		if reqs := elsewhere.TakeRequests(); len(reqs) != 0 {
			t.Errorf("the redirect target got %d requests", len(reqs))
		}
	})
}

// SPEC §17.7: "No credential, key or token appears in logs or status messages." and "Every tier
// writes each log line as one JSON object.", for the LO: a configuration error, an unreachable CO,
// a refused token, a redirect, an accepted manifest and a refused status; and for the site NATS password (SPEC
// §15.6): a NATS URL that holds it, a server that refuses the login, and a connection that is
// made.
func TestSpec_17_7_NoCredentialOrTokenInLOLogs(t *testing.T) {
	co := publishedCO(t)
	srv, ca := tlsCO(t, co)
	token := co.Token()
	var logs []*logBuffer

	env := loEnv(t, srv.URL, token, "LO_TLS_CA_FILE", ca)
	delete(env, "LO_DATA_DIR")
	if code, stderr := runLO(t, env); code != 2 {
		t.Errorf("without LO_DATA_DIR: exit %d, want 2", code)
	} else {
		logs = append(logs, stderr)
	}

	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	unreachable := startLO(t, loEnv(t, closed.URL, token, "LO_CO_INSECURE", "true"))
	unreachable.waitOutcome("Unreachable")

	refused := startLO(t, loEnv(t, srv.URL, "not-"+token, "LO_TLS_CA_FILE", ca))
	refused.waitOutcome("Unreachable")

	accepted := startLO(t, loEnv(t, srv.URL, token, "LO_TLS_CA_FILE", ca))
	accepted.waitOutcome("Accepted")
	// The fake CO answers no status POST: the deployment's status is sent and refused (SPEC §8.8).
	accepted.waitMsg("status not accepted by the CO")

	co.Override(manifestPath, synctest.Respond(http.StatusTemporaryRedirect, nil, "Location", srv.URL+"/elsewhere?token="+token))
	redirected := startLO(t, loEnv(t, srv.URL, token, "LO_TLS_CA_FILE", ca))
	redirected.waitOutcome("Unreachable")

	accepted.waitMsg("connected to the site NATS server")

	// A NATS URL that holds the password is refused, and not repeated (SPEC §15.4).
	env = loEnv(t, srv.URL, token, "LO_TLS_CA_FILE", ca)
	env["LO_NATS_URL"] = strings.Replace(env["LO_NATS_URL"], "://", "://"+natsUser+":"+natsPassword+"@", 1)
	if code, stderr := runLO(t, env); code != 2 {
		t.Errorf("with a password in LO_NATS_URL: exit %d, want 2", code)
	} else {
		logs = append(logs, stderr)
	}

	loginRefused := startLO(t, loEnv(t, srv.URL, token, "LO_TLS_CA_FILE", ca, "LO_NATS_PASSWORD", wrongNATSPassword))
	loginRefused.waitMsg("cannot connect to the site NATS server")

	for _, l := range []*runningLO{unreachable, refused, accepted, redirected, loginRefused} {
		l.stop()
		logs = append(logs, l.stderr)
	}
	for _, l := range logs {
		for name, secret := range map[string]string{"token": token, "NATS password": natsPassword, "wrong NATS password": wrongNATSPassword} {
			if strings.Contains(l.String(), secret) {
				t.Errorf("the log contains the %s:\n%s", name, l)
			}
		}
		l.lines(t) // fails on a line that is not one JSON object
	}
}

// SPEC §8.7, §8.8: the LO sends the status of each desired deployment to the CO, here the gateway
// status of a deployment it cannot resolve (SPEC §8.4).
func TestLOSendsDeploymentStatusToTheCO(t *testing.T) {
	co := publishedCO(t)
	srv, ca := tlsCO(t, co)
	got := make(chan *http.Request, 1)
	bodies := make(chan []byte, 1)
	co.Override("/api/v1/deployments/"+idA.String()+"/status", func(w http.ResponseWriter, r *http.Request) {
		var b bytes.Buffer
		_, _ = b.ReadFrom(r.Body)
		select {
		case got <- r:
			bodies <- b.Bytes()
		default:
		}
		w.WriteHeader(http.StatusCreated)
	})
	l := startLO(t, loEnv(t, srv.URL, co.Token(), "LO_TLS_CA_FILE", ca))
	defer l.stop()
	var r *http.Request
	select {
	case r = <-got:
	case <-time.After(10 * time.Second):
		t.Fatalf("no status was sent:\n%s", l.stderr)
	}
	if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+co.Token() {
		t.Fatalf("status request %s with the token %v, want POST with the site token", r.Method, r.Header.Get("Authorization") != "")
	}
	var s contract.DeploymentStatus
	if err := json.Unmarshal(<-bodies, &s); err != nil {
		t.Fatalf("status body: %v", err)
	}
	if s.DeploymentID != idA || s.Status.State != contract.StateFailed || s.Status.Error == nil ||
		s.Status.Error.Code != contract.CodeInvalidDeployment || s.DeviceID.String() != "site-1" || s.AdoptedManifestVersion != 1 {
		t.Fatalf("status = %+v, want %s failed with %s on site-1 at adopted version 1", s, idA, contract.CodeInvalidDeployment)
	}
}

// SPEC §12, §16.2: the LO keeps its sync state in <lo.data_dir>/lo.db (ADR 0014), so a restarted
// LO sends the stored ETag.
func TestLOSyncsAndPersistsToDataDir(t *testing.T) {
	co := publishedCO(t)
	srv, ca := tlsCO(t, co)
	env := loEnv(t, srv.URL, co.Token(), "LO_TLS_CA_FILE", ca)
	first := startLO(t, env)
	first.waitOutcome("Accepted")
	if code := first.stop(); code != 0 {
		t.Fatalf("exit %d, want 0:\n%s", code, first.stderr)
	}

	s, err := store.OpenBolt(filepath.Join(env["LO_DATA_DIR"], "lo.db"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.Load(context.Background())
	if cerr := s.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	if err != nil {
		t.Fatal(err)
	}
	etag := contract.ETag(co.ManifestBody())
	if st.Version != 1 || st.ETag != etag || len(st.Desired) != 1 {
		t.Fatalf("stored state = %+v, want version 1, ETag %s and one deployment", st, etag)
	}

	co.TakeRequests()
	second := startLO(t, env)
	second.waitOutcome("NotModified")
	second.stop()
	reqs := co.TakeRequests()
	if len(reqs) == 0 || reqs[0].Header.Get("If-None-Match") != etag {
		t.Errorf("the restarted LO did not send If-None-Match %s", etag)
	}
}

// SPEC §14.2: "LO store file cannot be opened: another LO process holds it … Exit non-zero at
// startup with a readable error naming the file. Never delete or overwrite it."
func TestStoreFileHeldExitsAtStartup(t *testing.T) {
	co := publishedCO(t)
	srv, ca := tlsCO(t, co)
	env := loEnv(t, srv.URL, co.Token(), "LO_TLS_CA_FILE", ca)
	path := filepath.Join(env["LO_DATA_DIR"], "lo.db")
	s, err := store.OpenBolt(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitVersion(context.Background(), 7, `"etag"`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	held, err := store.OpenBolt(path)
	if err != nil {
		t.Fatal(err)
	}

	code, stderr := runLO(t, env)

	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	if code != 1 || !strings.Contains(stderr.String(), strings.ReplaceAll(path, `\`, `\\`)) {
		t.Errorf("exit %d, want 1 with an error naming %s:\n%s", code, path, stderr)
	}
	// Opening the file commits a transaction, so its bytes change; what it stores must not.
	s, err = store.OpenBolt(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.Load(context.Background())
	if cerr := s.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	if err != nil || st.Version != 7 || st.ETag != `"etag"` {
		t.Errorf("stored state = %+v, %v; want version 7 and its ETag, as before", st, err)
	}
	if reqs := co.TakeRequests(); len(reqs) != 0 {
		t.Errorf("the CO got %d requests from an LO without a store", len(reqs))
	}
}

// configEnv is a valid LO environment, changed by kv: name, value pairs, where an empty value
// removes the variable.
func configEnv(kv ...string) map[string]string {
	env := map[string]string{
		"LO_SITE_ID": "site-1", "LO_CO_URL": "https://co.example", "LO_SITE_TOKEN": "t", "LO_DATA_DIR": "data",
		"LO_NATS_URL": "tls://nats.example:4222", "LO_NATS_USERNAME": "site-1", "LO_NATS_PASSWORD": "p",
	}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == "" {
			delete(env, kv[i])
		} else {
			env[kv[i]] = kv[i+1]
		}
	}
	return env
}

// SPEC §6.1, §6.3: every key comes from the environment, and every key but the token also from a
// flag, which wins.
func TestConfigSources(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		args []string
		want config
	}{
		{"defaults", configEnv(), nil,
			config{SiteID: "site-1", COURL: "https://co.example", SiteToken: "t", DataDir: "data", PollInterval: 60 * time.Second,
				NATSURL: "tls://nats.example:4222", NATSUsername: "site-1", NATSPassword: "p",
				AckTimeout: 5 * time.Second, ReconcileInterval: 5 * time.Minute}},
		{"every key from the environment", configEnv("LO_CO_URL", "http://co.example:9002", "LO_CO_INSECURE", "true",
			"LO_TLS_CA_FILE", "ca.pem", "LO_POLL_INTERVAL", "5s",
			"LO_NATS_URL", "nats://nats:4222", "LO_NATS_INSECURE", "true", "LO_NATS_USERNAME", "lo", "LO_NATS_PASSWORD", "pw",
			"LO_NATS_CA_FILE", "nats-ca.pem", "LO_COMMAND_ACK_TIMEOUT", "2s", "LO_RECONCILE_INTERVAL", "1m"), nil,
			config{SiteID: "site-1", COURL: "http://co.example:9002", COInsecure: true, CAFile: "ca.pem", SiteToken: "t",
				DataDir: "data", PollInterval: 5 * time.Second,
				NATSURL: "nats://nats:4222", NATSInsecure: true, NATSUsername: "lo", NATSPassword: "pw", NATSCAFile: "nats-ca.pem",
				AckTimeout: 2 * time.Second, ReconcileInterval: time.Minute}},
		{"flags win over the environment",
			configEnv("LO_SITE_ID", "a b", "LO_CO_URL", "ftp://x", "LO_CO_INSECURE", "maybe", "LO_POLL_INTERVAL", "0s",
				"LO_NATS_URL", "ws://x", "LO_NATS_INSECURE", "maybe", "LO_NATS_USERNAME", "", "LO_NATS_CA_FILE", "env.pem",
				"LO_COMMAND_ACK_TIMEOUT", "0s", "LO_RECONCILE_INTERVAL", "never"),
			[]string{"--site-id", "site-2", "--co-url", "http://co.example", "--co-insecure", "--tls-ca-file", "ca.pem",
				"--data-dir", "other", "--poll-interval", "90s",
				"--nats-url", "nats://nats.example", "--nats-insecure", "--nats-username", "lo", "--nats-ca-file", "flag.pem",
				"--command-ack-timeout", "3s", "--reconcile-interval", "10m"},
			config{SiteID: "site-2", COURL: "http://co.example", COInsecure: true, CAFile: "ca.pem", SiteToken: "t",
				DataDir: "other", PollInterval: 90 * time.Second,
				NATSURL: "nats://nats.example", NATSInsecure: true, NATSUsername: "lo", NATSPassword: "p", NATSCAFile: "flag.pem",
				AckTimeout: 3 * time.Second, ReconcileInterval: 10 * time.Minute}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadConfig(tc.args, func(k string) string { return tc.env[k] })
			if err != nil || cfg != tc.want {
				t.Errorf("config = %+v, %v\nwant %+v", cfg, err, tc.want)
			}
		})
	}
}

// SPEC §6.1: a missing or invalid key is a configuration error that names the key.
func TestConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		args []string
		want string // in the error
	}{
		{"no site ID", configEnv("LO_SITE_ID", ""), nil, "LO_SITE_ID"},
		{"no CO URL", configEnv("LO_CO_URL", ""), nil, "LO_CO_URL"},
		{"no token", configEnv("LO_SITE_TOKEN", ""), nil, "LO_SITE_TOKEN"},
		{"no data directory", configEnv("LO_DATA_DIR", ""), nil, "LO_DATA_DIR"},
		{"invalid site ID", configEnv("LO_SITE_ID", "a/b"), nil, "LO_SITE_ID"},
		{"reserved site ID", configEnv("LO_SITE_ID", "any"), nil, "LO_SITE_ID"},
		{"CO URL without a scheme", configEnv("LO_CO_URL", "co.example"), nil, "LO_CO_URL"},
		{"CO URL with another scheme", configEnv("LO_CO_URL", "ftp://co.example"), nil, "LO_CO_URL"},
		{"CO URL without a host", configEnv("LO_CO_URL", "https://"), nil, "LO_CO_URL"},
		{"CO URL with user info", configEnv("LO_CO_URL", "https://site:pw@co.example"), nil, "LO_CO_URL"},
		{"CO URL with a query", configEnv("LO_CO_URL", "https://co.example/?a=b"), nil, "LO_CO_URL"},
		{"http CO URL", configEnv("LO_CO_URL", "http://co.example"), nil, "LO_CO_INSECURE"},
		{"http CO URL, insecure false", configEnv("LO_CO_URL", "http://co.example", "LO_CO_INSECURE", "false"), nil, "LO_CO_INSECURE"},
		{"invalid insecure", configEnv("LO_CO_INSECURE", "maybe"), nil, "LO_CO_INSECURE"},
		{"zero interval", configEnv("LO_POLL_INTERVAL", "0s"), nil, "LO_POLL_INTERVAL"},
		{"negative interval", configEnv("LO_POLL_INTERVAL", "-1s"), nil, "LO_POLL_INTERVAL"},
		{"interval without a unit", configEnv("LO_POLL_INTERVAL", "60"), nil, "LO_POLL_INTERVAL"},
		{"interval without a unit, the example", configEnv("LO_POLL_INTERVAL", "60"), nil, "such as 60s"},
		{"reconcile interval without a unit, the example", configEnv("LO_RECONCILE_INTERVAL", "5"), nil, "such as 5m"},
		{"invalid interval flag", configEnv(), []string{"--poll-interval", "soon"}, "poll-interval"},
		{"no token flag", configEnv(), []string{"--site-token", "t"}, "site-token"},
		// ADR 0020: the site ID goes into every subject, where `.` separates the parts.
		{"site ID with a dot", configEnv("LO_SITE_ID", "plant.1"), nil, "LO_SITE_ID"},
		{"no NATS URL", configEnv("LO_NATS_URL", ""), nil, "LO_NATS_URL"},
		{"no NATS username", configEnv("LO_NATS_USERNAME", ""), nil, "LO_NATS_USERNAME"},
		{"no NATS password", configEnv("LO_NATS_PASSWORD", ""), nil, "LO_NATS_PASSWORD"},
		{"no NATS password flag", configEnv(), []string{"--nats-password", "p"}, "nats-password"},
		{"nats:// URL", configEnv("LO_NATS_URL", "nats://nats.example"), nil, "LO_NATS_URL"},
		// The error also names the key that would allow the URL.
		{"nats:// URL, the insecure key", configEnv("LO_NATS_URL", "nats://nats.example"), nil, "LO_NATS_INSECURE"},
		{"NATS URL with another scheme, the insecure key", configEnv("LO_NATS_URL", "ws://nats.example"), nil, "LO_NATS_INSECURE"},
		{"nats:// URL, insecure false", configEnv("LO_NATS_URL", "nats://nats.example", "LO_NATS_INSECURE", "false"), nil, "LO_NATS_URL"},
		{"NATS URL with another scheme", configEnv("LO_NATS_URL", "wss://nats.example", "LO_NATS_INSECURE", "true"), nil, "LO_NATS_URL"},
		{"NATS URL with user info", configEnv("LO_NATS_URL", "tls://site:pw@nats.example"), nil, "LO_NATS_URL"},
		{"NATS URL with a path", configEnv("LO_NATS_URL", "tls://nats.example/x"), nil, "LO_NATS_URL"},
		{"invalid NATS insecure", configEnv("LO_NATS_INSECURE", "maybe"), nil, "LO_NATS_INSECURE"},
		{"zero ack timeout", configEnv("LO_COMMAND_ACK_TIMEOUT", "0s"), nil, "LO_COMMAND_ACK_TIMEOUT"},
		{"ack timeout without a unit", configEnv("LO_COMMAND_ACK_TIMEOUT", "5"), nil, "LO_COMMAND_ACK_TIMEOUT"},
		{"negative reconcile interval", configEnv("LO_RECONCILE_INTERVAL", "-5m"), nil, "LO_RECONCILE_INTERVAL"},
		{"invalid reconcile interval flag", configEnv(), []string{"--reconcile-interval", "often"}, "reconcile-interval"},
		{"an argument", configEnv(), []string{"serve"}, "serve"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadConfig(tc.args, func(k string) string { return tc.env[k] })
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want one naming %s", err, tc.want)
			}
		})
	}
}

// SPEC §6.1: "A tier MUST validate its configuration at startup and exit non-zero with an
// operator-readable error if validation fails."
func TestConfigErrorExits2(t *testing.T) {
	code, stderr := runLO(t, configEnv("LO_SITE_ID", ""))
	lines := stderr.lines(t)
	if code != 2 || len(lines) != 1 || lines[0]["level"] != "ERROR" || !strings.Contains(stderr.String(), "LO_SITE_ID") {
		t.Errorf("without LO_SITE_ID: exit %d, want 2 with one error line naming it:\n%s", code, stderr)
	}

	for name, content := range map[string]string{"missing": "", "not PEM": "not a certificate"} {
		ca := filepath.Join(t.TempDir(), "ca.pem")
		if content != "" {
			if err := os.WriteFile(ca, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		code, stderr := runLO(t, configEnv("LO_TLS_CA_FILE", ca, "LO_DATA_DIR", t.TempDir()))
		if code != 2 || !strings.Contains(stderr.String(), "LO_TLS_CA_FILE") {
			t.Errorf("CA file %s: exit %d, want 2 with an error naming LO_TLS_CA_FILE:\n%s", name, code, stderr)
		}
	}
}

// SPEC §15.6: the LO sends Margo requests directly to lo.co_url, through no proxy, and verifies the
// CO against lo.tls.ca_file when it is set.
func TestTransportUsesNoProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://proxy.example:3128")
	t.Setenv("HTTP_PROXY", "http://proxy.example:3128")

	tr, err := newTransport("")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Proxy != nil {
		t.Error("Proxy is set without lo.tls.ca_file")
	}
	if tr.TLSClientConfig != nil && tr.TLSClientConfig.RootCAs != nil {
		t.Error("RootCAs is set without lo.tls.ca_file; want the system roots")
	}

	_, ca := tlsCO(t, synctest.NewCO(t))
	tr, err = newTransport(ca)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Proxy != nil {
		t.Error("Proxy is set with lo.tls.ca_file")
	}
	if tr.TLSClientConfig == nil || tr.TLSClientConfig.RootCAs == nil {
		t.Error("lo.tls.ca_file does not set RootCAs")
	}
}
