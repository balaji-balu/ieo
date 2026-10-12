package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/lo/sync/synctest"
	"github.com/balaji-balu/ieo/internal/sitenats"
	"github.com/balaji-balu/ieo/internal/sitenats/natstest"
)

const msgNATSConnected = "connected to the site NATS server"

var siteLogin = natstest.Options{Username: natsUser, Password: natsPassword}

// warned reports whether l logged a warning, with site_id, that holds text.
func warned(t *testing.T, l *runningLO, text string) bool {
	t.Helper()
	for _, line := range l.stderr.lines(t) {
		if line["level"] == "WARN" && line["site_id"] == "site-1" && strings.Contains(fmt.Sprint(line), text) {
			return true
		}
	}
	return false
}

// refusedAtStartup runs an LO with env, which must exit with a configuration error that names
// key and without connecting to the site NATS server.
func refusedAtStartup(t *testing.T, env map[string]string, key, what string) {
	t.Helper()
	code, stderr := runLO(t, env)
	if code != 2 || !strings.Contains(stderr.String(), key) {
		t.Errorf("%s: exit %d, want 2 with an error naming %s:\n%s", what, code, key, stderr)
	}
	if strings.Contains(stderr.String(), msgNATSConnected) {
		t.Errorf("%s: the LO connected:\n%s", what, stderr)
	}
}

// SPEC §17.7: "Until scoped NATS credentials, the LO and the EN accept a NATS URL other than
// `tls://` only when it is a `nats://` URL and `lo.nats_insecure`/`en.nats_insecure` is set, and
// exit at startup on any other" (§15.6). This is the LO's half; the EN's, and the password that
// reaches no workload, come with the EN (roadmap D5.6).
func TestSpec_17_7_LOAcceptsANATSURLWithoutTLSOnlyWhenInsecure(t *testing.T) {
	co := publishedCO(t)
	coSrv, ca := tlsCO(t, co)
	withTLS := siteLogin
	withTLS.TLS = true
	// env is an LO that connects to srv, with the NATS settings changed by kv; an empty value
	// removes the variable.
	env := func(t *testing.T, srv *natstest.Server, kv ...string) map[string]string {
		e := loEnv(t, coSrv.URL, co.Token(), "LO_TLS_CA_FILE", ca)
		natsEnv(e, srv)
		for i := 0; i+1 < len(kv); i += 2 {
			e[kv[i]] = kv[i+1]
		}
		return e
	}

	t.Run("tls with lo.nats.ca_file", func(t *testing.T) {
		l := startLO(t, env(t, natstest.Start(t, withTLS)))
		l.waitMsg(msgNATSConnected)
		if code := l.stop(); code != 0 {
			t.Errorf("exit %d after shutdown, want 0", code)
		}
		if warned(t, l, "TLS") {
			t.Errorf("a warning about TLS with a tls:// URL:\n%s", l.stderr)
		}
	})

	t.Run("nats without lo.nats_insecure", func(t *testing.T) {
		srv := natstest.Start(t, siteLogin)
		for _, insecure := range []string{"", "false"} {
			refusedAtStartup(t, env(t, srv, "LO_NATS_INSECURE", insecure), "LO_NATS_URL", "LO_NATS_INSECURE="+insecure)
		}
		if reqs := co.TakeRequests(); len(reqs) != 0 {
			t.Errorf("the CO got %d requests from an LO that must not start", len(reqs))
		}
	})

	t.Run("nats with lo.nats_insecure", func(t *testing.T) {
		srv := natstest.Start(t, siteLogin)
		l := startLO(t, env(t, srv))
		l.waitMsg(msgNATSConnected)
		l.stop()
		if !warned(t, l, srv.URL()) {
			t.Errorf("no startup warning naming %s:\n%s", srv.URL(), l.stderr)
		}
		if strings.Contains(l.stderr.String(), natsPassword) {
			t.Error("the log contains the NATS password")
		}
	})

	t.Run("any other scheme, with or without lo.nats_insecure", func(t *testing.T) {
		srv := natstest.Start(t, siteLogin)
		for _, scheme := range []string{"ws", "wss", "http"} {
			for _, insecure := range []string{"", "true"} {
				refusedAtStartup(t, env(t, srv, "LO_NATS_URL", scheme+"://"+srv.Addr(), "LO_NATS_INSECURE", insecure),
					"LO_NATS_URL", scheme+":// with LO_NATS_INSECURE="+insecure)
			}
		}
	})

	t.Run("lo.nats.ca_file that cannot be used", func(t *testing.T) {
		srv := natstest.Start(t, withTLS)
		notPEM := filepath.Join(t.TempDir(), "ca.pem")
		if err := os.WriteFile(notPEM, []byte("not a certificate"), 0o600); err != nil {
			t.Fatal(err)
		}
		for name, file := range map[string]string{"missing": filepath.Join(t.TempDir(), "none.pem"), "not PEM": notPEM} {
			refusedAtStartup(t, env(t, srv, "LO_NATS_CA_FILE", file), "LO_NATS_CA_FILE", "CA file "+name)
		}
	})
}

// SPEC §11.2 (ADR 0020): "A tier that cannot reach the site NATS server keeps trying, at startup
// and after a connection is lost, and logs each failure. It does not exit: the LO goes on syncing
// with the CO".
func TestLOSyncsWhileTheSiteNATSServerIsAway(t *testing.T) {
	co := publishedCO(t)
	coSrv, ca := tlsCO(t, co)
	srv := natstest.New(t, siteLogin) // not started
	env := loEnv(t, coSrv.URL, co.Token(), "LO_TLS_CA_FILE", ca)
	natsEnv(env, srv)
	l := startLO(t, env)
	l.waitOutcome("Accepted")
	if line := l.waitMsg("cannot connect to the site NATS server"); line["level"] != "WARN" || line["site_id"] != "site-1" {
		t.Errorf("the failure is logged as %v, want a WARN line with site_id", line)
	}
	srv.Start()
	l.waitMsg(msgNATSConnected)
	if code := l.stop(); code != 0 {
		t.Errorf("exit %d after shutdown, want 0:\n%s", code, l.stderr)
	}
}

const helloDescription = `apiVersion: margo.org/v1-alpha1
id: com-example-hello
deploymentProfiles:
  - type: compose
    id: hello-compose
    components:
      - name: web
        properties:
          repository: oci://registry.test/example/hello-web
          revision: 1.2.3
`

// SPEC §16.2, §16.4: the LO asks the hosts for their inventories when it connects, takes the
// manifest from the CO, and sends each host what it lacks. A test connection plays the EN of
// host-1: it answers the inventory request with an empty Inventory and accepts every command.
func TestLOSendsAnApplyToTheHostOfADirectedDeployment(t *testing.T) {
	target, err := contract.ParseDeviceID("site-1/host-1")
	if err != nil {
		t.Fatal(err)
	}
	yaml, err := contract.BuildApplicationDeployment([]byte(helloDescription), contract.DeploymentChoices{
		ID: idA, Name: "hello", Namespace: "apps", DeviceID: target, Profile: "hello-compose",
	})
	if err != nil {
		t.Fatalf("build deployment: %v", err)
	}
	co := synctest.NewCO(t)
	co.Publish(1, map[uuid.UUID][]byte{idA: yaml})
	coSrv, ca := tlsCO(t, co)

	srv := natstest.Start(t, siteLogin)
	en, err := sitenats.Connect(sitenats.Config{
		URL: srv.URL(), Username: natsUser, Password: natsPassword, Insecure: true, Log: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer en.Close()
	site, err := sitenats.ForSite("site-1")
	if err != nil {
		t.Fatal(err)
	}
	host, err := site.Host("host-1")
	if err != nil {
		t.Fatal(err)
	}
	commands := make(chan contract.Command, 4)
	subscribe := func(subject string, handle func(sitenats.Msg)) {
		t.Helper()
		sub, err := en.Subscribe(subject, handle)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(sub.Stop)
	}
	subscribe(host.Cmd(), func(m sitenats.Msg) {
		cmd, err := contract.DecodeCommand(m.Data)
		if err != nil {
			t.Errorf("the EN cannot decode the command: %v", err)
			return
		}
		ack, err := contract.EncodeSiteMessage(contract.CommandAck{CommandID: cmd.CommandID, Accepted: true})
		if err == nil {
			err = m.Reply(ack)
		}
		if err != nil {
			t.Errorf("ack: %v", err)
		}
		commands <- cmd
	})
	subscribe(site.InventoryRequest(), func(sitenats.Msg) {
		inv, err := contract.EncodeSiteMessage(contract.Inventory{HostID: "host-1", At: time.Now().UTC()})
		if err == nil {
			err = en.Publish(host.Inventory(), inv)
		}
		if err != nil {
			t.Errorf("publish the inventory: %v", err)
		}
	})
	// The EN answers itself once the server knows the subscriptions above: a connection's
	// messages reach the server in order.
	const syncSubject = "test.sync"
	subscribe(syncSubject, func(m sitenats.Msg) { _ = m.Reply(nil) })
	connected := make(chan struct{}, 1)
	en.OnConnect(func() {
		select {
		case connected <- struct{}{}:
		default:
		}
	})
	select {
	case <-connected:
	case <-time.After(10 * time.Second):
		t.Fatal("the EN's connection was not made")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := en.Request(ctx, syncSubject, nil); err != nil {
		t.Fatalf("sync the EN's connection: %v", err)
	}

	env := loEnv(t, coSrv.URL, co.Token(), "LO_TLS_CA_FILE", ca)
	natsEnv(env, srv)
	l := startLO(t, env)
	select {
	case cmd := <-commands:
		if cmd.Action != contract.ActionApply || cmd.DeploymentID != idA || cmd.Digest != contract.DigestOf(yaml) ||
			cmd.Deployment == nil || cmd.Deployment.Metadata.DeviceID != target {
			t.Errorf("the EN received %+v, want an Apply of %s at %s with the deployment for %s", cmd, idA, contract.DigestOf(yaml), target)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("no command reached the host:\n%s", l.stderr)
	}
	if code := l.stop(); code != 0 {
		t.Errorf("exit %d after shutdown, want 0:\n%s", code, l.stderr)
	}
	// SPEC §17.7: every LO log line carries site_id.
	for _, line := range l.stderr.lines(t) {
		if line["site_id"] != "site-1" {
			t.Errorf("a log line without site_id: %v", line)
		}
	}
}
