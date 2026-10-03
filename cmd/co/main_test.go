package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/balaji-balu/ieo/internal/co/auth"
	"github.com/balaji-balu/ieo/internal/co/deploy"
	"github.com/balaji-balu/ieo/internal/co/store/postgres"
	"github.com/balaji-balu/ieo/internal/co/store/storetest"
	"github.com/balaji-balu/ieo/internal/contract"
)

// result is one run of the co command.
type result struct {
	code           int
	stdout, stderr string
}

// runCO runs the co command with args and DATABASE_URL set to dsn.
func runCO(t *testing.T, dsn string, args ...string) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	env := map[string]string{"DATABASE_URL": dsn}
	code := run(context.Background(), args, func(k string) string { return env[k] }, &stdout, &stderr)
	return result{code, stdout.String(), stderr.String()}
}

// token returns the one line run printed, failing t unless it printed exactly one non-empty line.
func (r result) token(t *testing.T) string {
	t.Helper()
	token, ok := strings.CutSuffix(r.stdout, "\n")
	if r.code != 0 || !ok || token == "" || strings.ContainsAny(token, "\r\n \t") {
		t.Fatalf("want exit 0 and one token line; got exit %d, stdout %q, stderr %s", r.code, r.stdout, r.stderr)
	}
	return token
}

func verify(t *testing.T, s *postgres.Store, token string) (contract.SiteID, error) {
	t.Helper()
	return auth.New(s).Verify(context.Background(), token)
}

// SPEC §15.6: the CO returns a site's token once; `co site add` prints it on one line and nowhere
// else, and refuses a site that exists, so it never replaces a token an LO already holds.
func TestSiteAddPrintsTokenOnce(t *testing.T) {
	s, dsn := storetest.PostgresURL(t)
	token := runCO(t, dsn, "site", "add", "site-1").token(t)
	if site, err := verify(t, s, token); err != nil || site != "site-1" {
		t.Errorf("printed token verifies as %q, %v; want site-1", site, err)
	}
	if m, ok, err := s.Manifest(context.Background(), "site-1"); err != nil || !ok || m.Version != 1 {
		t.Errorf("new site's manifest: %+v ok=%v err=%v, want version 1", m, ok, err)
	}

	again := runCO(t, dsn, "site", "add", "site-1")
	if again.code != 1 || again.stdout != "" || !strings.Contains(again.stderr, "rotate-token") {
		t.Errorf("adding an existing site: exit %d, stdout %q, stderr %s; want exit 1, no stdout, a hint to rotate-token",
			again.code, again.stdout, again.stderr)
	}
	if _, err := verify(t, s, token); err != nil {
		t.Errorf("adding the site again replaced its token: %v", err)
	}

	for _, args := range [][]string{{"site", "add"}, {"site", "add", "any"}, {"site", "add", "a b"}, {"site", "add", "a", "b"}} {
		if r := runCO(t, dsn, args...); r.code != 2 || r.stdout != "" {
			t.Errorf("co %s: exit %d, stdout %q; want exit 2, no stdout", strings.Join(args, " "), r.code, r.stdout)
		}
	}
}

// SPEC §15.6: "Issuing a new token for a site replaces the old one." rotate-token also gives a
// token to a site added without one, as after a crash between adding the site and issuing it.
func TestRotateTokenReplacesOldToken(t *testing.T) {
	s, dsn := storetest.PostgresURL(t)
	old := runCO(t, dsn, "site", "add", "site-1").token(t)
	rotated := runCO(t, dsn, "site", "rotate-token", "site-1").token(t)
	if rotated == old {
		t.Fatal("rotate-token printed the old token")
	}
	if _, err := verify(t, s, old); !errors.Is(err, auth.ErrUnknownToken) {
		t.Errorf("old token after rotation: %v, want ErrUnknownToken", err)
	}
	if site, err := verify(t, s, rotated); err != nil || site != "site-1" {
		t.Errorf("new token verifies as %q, %v; want site-1", site, err)
	}

	ctx := context.Background()
	svc := deploy.New(s)
	if _, err := svc.AddSite(ctx, "site-2"); err != nil { // no token yet
		t.Fatal(err)
	}
	if site, err := verify(t, s, runCO(t, dsn, "site", "rotate-token", "site-2").token(t)); err != nil || site != "site-2" {
		t.Errorf("token for a site added without one verifies as %q, %v", site, err)
	}

	if r := runCO(t, dsn, "site", "rotate-token", "site-gone"); r.code != 1 || r.stdout != "" {
		t.Errorf("rotate-token of an unknown site: exit %d, stdout %q; want exit 1, no stdout", r.code, r.stdout)
	}
	if err := svc.RetireSite(ctx, "site-2"); err != nil {
		t.Fatal(err)
	}
	if r := runCO(t, dsn, "site", "rotate-token", "site-2"); r.code != 1 || r.stdout != "" || !strings.Contains(r.stderr, "retired") {
		t.Errorf("rotate-token of a retired site: exit %d, stdout %q, stderr %s; want exit 1, no stdout, \"retired\"",
			r.code, r.stdout, r.stderr)
	}
}

// startServe runs serve on a free local port until the test ends, and returns its base URL, a
// function that stops it and waits for serve to return, and the log.
func startServe(t *testing.T, dsn string) (string, func() error, *syncBuffer) {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	logs := &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- serve(ctx, config{DatabaseURL: dsn}, ln, slog.New(slog.NewJSONHandler(logs, nil))) }()
	stopped := false
	stop := func() error {
		cancel()
		select {
		case err := <-done:
			stopped = true
			return err
		case <-time.After(15 * time.Second):
			t.Fatal("serve did not return within 15s of its context ending")
			return nil
		}
	}
	t.Cleanup(func() {
		if !stopped {
			_ = stop() // the test already failed or stopped it
		}
	})
	return "http://" + ln.Addr().String(), stop, logs
}

func get(t *testing.T, url, token string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req) // the listener is bound before serve starts
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return resp.StatusCode, b
}

// SPEC §11.1, §15.6, §13: `co serve` serves the Margo API to a site's LO with the token
// `co site add` printed, refuses a request without one, answers /healthz without one, and stops
// cleanly when its context ends.
func TestServeAnswersManifestWithIssuedToken(t *testing.T) {
	_, dsn := storetest.PostgresURL(t)
	token := runCO(t, dsn, "site", "add", "site-1").token(t)
	base, stop, _ := startServe(t, dsn)

	code, body := get(t, base+"/api/v1/deployments", token)
	var m contract.StateManifest
	if code != http.StatusOK || json.Unmarshal(body, &m) != nil || m.ManifestVersion != 1 {
		t.Errorf("manifest with the site's token: %d %s; want 200 with manifestVersion 1", code, body)
	}
	if code, body := get(t, base+"/api/v1/deployments", ""); code != http.StatusUnauthorized {
		t.Errorf("manifest without a token: %d %s; want 401", code, body)
	}
	if code, body := get(t, base+"/healthz", ""); code != http.StatusOK {
		t.Errorf("/healthz: %d %s; want 200", code, body)
	}

	if err := stop(); err != nil {
		t.Errorf("serve returned %v after its context ended, want nil", err)
	}
	if c, err := (&net.Dialer{Timeout: time.Second}).DialContext(context.Background(), "tcp", strings.TrimPrefix(base, "http://")); err == nil {
		_ = c.Close()
		t.Error("the listener still accepts connections after serve returned")
	}
}

type pinger func(context.Context) error

func (p pinger) Ping(ctx context.Context) error { return p(ctx) }

// SPEC §13 [IEO]: /healthz is 200 while the database answers and 503 when it does not.
func TestHealthzReportsDatabase(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	down := errors.New("connection refused")
	for _, tc := range []struct {
		name   string
		method string
		ping   error
		want   int
	}{
		{"database up", http.MethodGet, nil, http.StatusOK},
		{"database up, HEAD", http.MethodHead, nil, http.StatusOK},
		{"database down", http.MethodGet, down, http.StatusServiceUnavailable},
		{"POST", http.MethodPost, nil, http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := handler(pinger(func(context.Context) error { return tc.ping }), http.NotFoundHandler(), log)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tc.method, "/healthz", nil))
			if rec.Code != tc.want {
				t.Errorf("%s /healthz = %d %s, want %d", tc.method, rec.Code, rec.Body, tc.want)
			}
		})
	}
	if !strings.Contains(logs.String(), "connection refused") {
		t.Errorf("a failed health check is not logged:\n%s", logs.String())
	}
}

// A database outage is logged once when it starts and once when it ends, not on every probe.
func TestHealthzLogsOnlyChanges(t *testing.T) {
	var logs bytes.Buffer
	var ping error
	h := handler(pinger(func(context.Context) error { return ping }), http.NotFoundHandler(), slog.New(slog.NewJSONHandler(&logs, nil)))
	probe := func() {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	}
	probe()
	ping = errors.New("connection refused")
	probe()
	probe()
	probe()
	ping = nil
	probe()
	probe()
	if n := strings.Count(logs.String(), "database unavailable"); n != 1 {
		t.Errorf("outage logged %d times, want once:\n%s", n, logs.String())
	}
	if n := strings.Count(logs.String(), "database available"); n != 1 {
		t.Errorf("recovery logged %d times, want once:\n%s", n, logs.String())
	}
}

// SPEC §12: `co sites raise-versions --by N` raises every site's manifestVersion by N ≥ 1.
func TestRaiseVersionsCommand(t *testing.T) {
	s, dsn := storetest.PostgresURL(t)
	runCO(t, dsn, "site", "add", "site-1").token(t)
	for _, args := range [][]string{
		{"sites", "raise-versions"},
		{"sites", "raise-versions", "--by", "0"},
		{"sites", "raise-versions", "--by", "-1"},
		{"sites", "raise-versions", "--by", "x"},
		{"sites", "raise-versions", "--by", "5", "extra"},
	} {
		if r := runCO(t, dsn, args...); r.code != 2 {
			t.Errorf("co %s: exit %d, want 2\n%s", strings.Join(args, " "), r.code, r.stderr)
		}
	}
	if m, _, _ := s.Manifest(context.Background(), "site-1"); m.Version != 1 {
		t.Fatalf("a refused raise changed the version to %d", m.Version)
	}

	r := runCO(t, dsn, "sites", "raise-versions", "--by", "5")
	if r.code != 0 {
		t.Fatalf("raise-versions --by 5: exit %d\n%s", r.code, r.stderr)
	}
	if m, _, _ := s.Manifest(context.Background(), "site-1"); m.Version != 6 {
		t.Errorf("version %d after raising 1 by 5, want 6", m.Version)
	}
	if !strings.Contains(r.stderr, `"site_id":"site-1"`) || !strings.Contains(r.stderr, `"manifest_version":6`) {
		t.Errorf("the raise of site-1 is not logged with site_id and manifest_version:\n%s", r.stderr)
	}
}

func TestUsageErrors(t *testing.T) {
	_, dsn := storetest.PostgresURL(t)
	for _, args := range [][]string{{"nope"}, {"site"}, {"site", "nope", "x"}, {"sites"}, {"sites", "nope"}} {
		if r := runCO(t, dsn, args...); r.code != 2 {
			t.Errorf("co %s: exit %d, want 2", strings.Join(args, " "), r.code)
		}
	}
	if r := runCO(t, "", "site", "add", "site-1"); r.code != 2 || !strings.Contains(r.stderr, "DATABASE_URL") {
		t.Errorf("without DATABASE_URL: exit %d, stderr %s; want exit 2 naming DATABASE_URL", r.code, r.stderr)
	}
}

// SPEC §17.7: "No credential, key or token appears in logs or status messages." and "Every tier
// writes each log line as one JSON object.", for the CO. This covers every co command, and a database
// that cannot be reached, whose URL carries a password.
func TestSpec_17_7_NoCredentialOrTokenInCOLogs(t *testing.T) {
	_, dsn := storetest.PostgresURL(t)
	var logs strings.Builder
	add := runCO(t, dsn, "site", "add", "site-1")
	token := add.token(t)
	rotate := runCO(t, dsn, "site", "rotate-token", "site-1")
	rotated := rotate.token(t)
	raise := runCO(t, dsn, "sites", "raise-versions", "--by", "1")
	jsonLogs := []string{add.stderr, rotate.stderr, raise.stderr}

	base, stop, serveLogs := startServe(t, dsn)
	get(t, base+"/api/v1/deployments", rotated)
	get(t, base+"/api/v1/deployments", token) // the replaced token, refused
	get(t, base+"/healthz", "")
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	jsonLogs = append(jsonLogs, serveLogs.String())

	const password = "pw-must-not-be-logged"
	unreachable := "postgres://postgres:" + password + "@127.0.0.1:1/postgres?sslmode=disable&connect_timeout=2"
	failed := runCO(t, unreachable, "site", "add", "site-1")
	if failed.code != 1 {
		t.Errorf("site add with an unreachable database: exit %d, want 1", failed.code)
	}
	jsonLogs = append(jsonLogs, failed.stderr)
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var serveFailed bytes.Buffer
	if err := serve(context.Background(), config{DatabaseURL: unreachable}, ln, slog.New(slog.NewJSONHandler(&serveFailed, nil))); err == nil {
		t.Error("serve with an unreachable database returned nil")
	} else {
		logs.WriteString(err.Error() + "\n") // run logs it
	}
	jsonLogs = append(jsonLogs, serveFailed.String())
	for _, l := range jsonLogs {
		logs.WriteString(l)
	}

	secrets := map[string]string{"token": token, "rotated token": rotated, "database URL": dsn, "unreachable database URL": unreachable,
		"password": password}
	if u, err := url.Parse(dsn); err == nil {
		if p, ok := u.User.Password(); ok && p != "" {
			secrets["test database password"] = p
		}
	}
	out := logs.String()
	if out == "" {
		t.Fatal("no logs")
	}
	for name, secret := range secrets {
		if strings.Contains(out, secret) {
			t.Errorf("the logs contain the %s", name)
		}
	}
	sc := bufio.NewScanner(strings.NewReader(strings.Join(jsonLogs, "")))
	for sc.Scan() {
		var line map[string]any
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Errorf("log line is not one JSON object: %q", sc.Text())
		}
	}
}

// syncBuffer is a bytes.Buffer that serve's goroutines may write while a test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
