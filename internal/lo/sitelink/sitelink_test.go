package sitelink_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/lo/sitelink"
	"github.com/balaji-balu/ieo/internal/sitenats"
	"github.com/balaji-balu/ieo/internal/sitenats/natstest"
)

const (
	siteID = contract.SiteID("site-1")
	host1  = contract.HostID("host-1")
	host2  = contract.HostID("host-2")

	// deadline is how long a test waits for something that must happen.
	deadline = 10 * time.Second
	// notYet is how long a test watches for something that must not happen.
	notYet = 100 * time.Millisecond
	// reconnectWait keeps the tests short; a tier uses 2 seconds (ADR 0020).
	reconnectWait = 20 * time.Millisecond

	// maxReason is the longest `reason` a dropped message may put in a log line.
	maxReason = 1024
)

var (
	idA    = uuid.MustParse("0a0a0a0a-0000-4000-8000-00000000000a")
	digest = contract.DigestOf([]byte("a deployment"))
	at     = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
)

// lockedBuffer collects the log lines of the Link and of its connection.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

// lines returns the log lines with message msg.
func (l *lockedBuffer) lines(t *testing.T, msg string) []map[string]any {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(l.b.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		if m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

type gotInventory struct {
	host contract.HostID
	inv  contract.Inventory
}

type gotStatus struct {
	host contract.HostID
	ev   contract.ComponentStatusEvent
}

// handler records what the Link gives it.
type handler struct {
	inventories chan gotInventory
	statuses    chan gotStatus
}

func (h *handler) OnInventory(_ context.Context, host contract.HostID, inv contract.Inventory) {
	h.inventories <- gotInventory{host, inv}
}

func (h *handler) OnStatus(_ context.Context, host contract.HostID, ev contract.ComponentStatusEvent) {
	h.statuses <- gotStatus{host, ev}
}

// fixture is a Link on a connection of its own, and a second connection that plays the ENs.
type fixture struct {
	t    *testing.T
	srv  *natstest.Server
	site sitenats.Site
	link *sitelink.Link
	logs *lockedBuffer
	h    *handler
	// requests gets a value for each `inventory.request` the Link publishes. The subscription is
	// on the Link's own connection, made before Listen: the server sends a connection what it
	// publishes itself, after the subscriptions that connection made earlier. A request received
	// therefore also says that the server knows the Link's subscriptions.
	requests chan struct{}
	en       *sitenats.Conn
	// enConnected gets a value each time the EN's connection is made.
	enConnected chan struct{}
}

// syncSubject is a subject the EN's connection answers itself (fixture.sync).
const syncSubject = "test.sync"

func dial(t *testing.T, srv *natstest.Server, log *slog.Logger) *sitenats.Conn {
	t.Helper()
	conn, err := sitenats.Connect(sitenats.Config{
		URL: srv.URL(), Username: "site-1", Password: "made-up-for-this-test",
		Insecure: true, Log: log, ReconnectWait: reconnectWait,
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(conn.Close)
	return conn
}

// newFixture returns a Link that listens, once the server knows its subscriptions.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	site, err := sitenats.ForSite(siteID)
	if err != nil {
		t.Fatalf("ForSite: %v", err)
	}
	f := &fixture{
		t: t, srv: natstest.Start(t, natstest.Options{}), site: site, logs: &lockedBuffer{},
		h:        &handler{inventories: make(chan gotInventory, 16), statuses: make(chan gotStatus, 16)},
		requests: make(chan struct{}, 16), enConnected: make(chan struct{}, 16),
	}
	log := slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})).With(slog.String("site_id", string(siteID)))
	conn := dial(t, f.srv, log)
	echo, err := conn.Subscribe(site.InventoryRequest(), func(m sitenats.Msg) {
		if _, err := contract.DecodeSiteMessage[contract.InventoryRequest](m.Data); err != nil {
			t.Errorf("the inventory request is not valid: %v", err)
		}
		f.requests <- struct{}{}
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	t.Cleanup(echo.Stop)

	f.link = sitelink.New(conn, site, log)
	if err := f.link.Listen(context.Background(), f.h); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(f.link.Stop)
	await(t, f.requests, "the inventory request of the first connect")

	f.en = dial(t, f.srv, slog.New(slog.DiscardHandler))
	answer, err := f.en.Subscribe(syncSubject, func(m sitenats.Msg) {
		if err := m.Reply(nil); err != nil {
			t.Errorf("Reply: %v", err)
		}
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	t.Cleanup(answer.Stop)
	f.en.OnConnect(func() { f.enConnected <- struct{}{} })
	await(t, f.enConnected, "the EN's connection")
	return f
}

// sync returns once the server knows every subscription the EN's connection has made: a
// connection's messages reach the server in order, so the answer to a request it sends itself
// comes after them.
func (f *fixture) sync() {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	if _, err := f.en.Request(ctx, syncSubject, nil); err != nil {
		f.t.Fatalf("sync the EN's connection: %v", err)
	}
}

// host returns the subjects of host id.
func (f *fixture) host(id contract.HostID) sitenats.Host {
	f.t.Helper()
	h, err := f.site.Host(id)
	if err != nil {
		f.t.Fatalf("Host(%q): %v", id, err)
	}
	return h
}

// publish sends data on subject as an EN would.
func (f *fixture) publish(subject string, data []byte) {
	f.t.Helper()
	if err := f.en.Publish(subject, data); err != nil {
		f.t.Fatalf("publish on %s: %v", subject, err)
	}
}

func encode[T contract.SiteMessage](t *testing.T, msg T) []byte {
	t.Helper()
	data, err := contract.EncodeSiteMessage(msg)
	if err != nil {
		t.Fatalf("encode %T: %v", msg, err)
	}
	return data
}

func await[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(deadline):
		t.Fatalf("%s: nothing within %v", what, deadline)
		panic("unreachable")
	}
}

// never fails if ch gets a value within notYet.
func never[T any](t *testing.T, ch <-chan T, what string) {
	t.Helper()
	select {
	case v := <-ch:
		t.Fatalf("%s: got %+v, want nothing", what, v)
	case <-time.After(notYet):
	}
}

func inventory(host contract.HostID) contract.Inventory {
	return contract.Inventory{HostID: host, At: at, Deployments: []contract.InventoryDeployment{{
		DeploymentID: idA, Digest: digest,
		Components: []contract.ComponentStatus{{Name: "web", State: contract.StateInstalled}},
	}}}
}

// SPEC §11.2: the LO keys host state by the `<h>` of the subject a message arrived on. The Link
// gives the handler that host with the decoded message; comparing it with the payload's `hostId`
// is the authority's rule.
func TestInventoryAndStatusReachTheHandlerWithTheSubjectsHost(t *testing.T) {
	f := newFixture(t)

	inv := inventory(host1)
	f.publish(f.host(host1).Inventory(), encode(t, inv))
	if got := await(t, f.h.inventories, "the inventory"); got.host != host1 || !reflect.DeepEqual(got.inv, inv) {
		t.Errorf("OnInventory(%q, %+v), want host-1 and %+v", got.host, got.inv, inv)
	}

	named := inventory("host-9")
	f.publish(f.host(host2).Inventory(), encode(t, named))
	if got := await(t, f.h.inventories, "the inventory that names another host"); got.host != host2 || got.inv.HostID != "host-9" {
		t.Errorf("OnInventory(%q, hostId %q), want the subject's host-2 and the payload's host-9", got.host, got.inv.HostID)
	}

	ev := contract.ComponentStatusEvent{DeploymentID: idA, Digest: digest, Component: "web", State: contract.StateInstalling, At: at}
	f.publish(f.host(host2).Status(), encode(t, ev))
	if got := await(t, f.h.statuses, "the status event"); got.host != host2 || !reflect.DeepEqual(got.ev, ev) {
		t.Errorf("OnStatus(%q, %+v), want host-2 and %+v", got.host, got.ev, ev)
	}
}

// SPEC §11.2: a message that fails schema validation is logged and dropped. One subscription
// gives its messages in order, so the valid message that follows shows the others were handled.
func TestInvalidMessagesAreLoggedAndDropped(t *testing.T) {
	f := newFixture(t)

	long := strings.Repeat("x", 10*maxReason)
	f.publish(f.host(host1).Inventory(), []byte(`{"hostId": "host-1", "at": "`+long+`", "deployments": []}`))
	f.publish(f.host(host1).Inventory(), []byte(`not JSON`))
	// `!` is not a character of a host ID (SPEC §4.2), so the subject names no host.
	f.publish("site.site-1.host.a!b.inventory", encode(t, inventory(host1)))
	f.publish(f.host(host1).Inventory(), encode(t, inventory(host1)))
	await(t, f.h.inventories, "the valid inventory")

	f.publish(f.host(host2).Status(), []byte(`{"deploymentId": "`+idA.String()+`", "state": "installed"}`))
	f.publish(f.host(host2).Status(), encode(t, contract.ComponentStatusEvent{DeploymentID: idA, Digest: digest, State: contract.StateRemoved, At: at}))
	await(t, f.h.statuses, "the valid status event")

	never(t, f.h.inventories, "a second inventory")
	never(t, f.h.statuses, "a second status event")

	check := func(msg string, want int, host string) {
		t.Helper()
		lines := f.logs.lines(t, msg)
		if len(lines) != want {
			t.Fatalf("%d log lines %q, want %d: %v", len(lines), msg, want, lines)
		}
		for _, l := range lines {
			reason, _ := l["reason"].(string)
			if l["level"] != "WARN" || l["site_id"] != "site-1" || l["host_id"] != host || reason == "" || len(reason) > maxReason {
				t.Errorf("log line %q: level %v, site_id %v, host_id %v, a reason of %d bytes; want WARN, site-1, %s and a reason of 1 to %d bytes",
					msg, l["level"], l["site_id"], l["host_id"], len(reason), host, maxReason)
			}
		}
	}
	check("inventory dropped", 2, "host-1")
	check("status event dropped", 1, "host-2")
	lines := f.logs.lines(t, "site message dropped")
	if len(lines) != 1 || lines[0]["level"] != "WARN" || lines[0]["subject"] != "site.site-1.host.a!b.inventory" {
		t.Errorf("log lines for the subject without a host: %v", lines)
	}
}

// SPEC §11.2, §16.4: a Command goes to its host's `cmd` subject as a request, and the reply is
// the ack.
func TestSendReturnsTheHostsAck(t *testing.T) {
	f := newFixture(t)
	cmd := contract.Command{CommandID: uuid.New(), Action: contract.ActionRemove, DeploymentID: idA, Digest: digest}
	rejected := contract.CommandAck{CommandID: cmd.CommandID, Error: &contract.AckError{Code: "IEO-TEST", Message: "no"}}

	received := make(chan contract.Command, 1)
	sub, err := f.en.Subscribe(f.host(host1).Cmd(), func(m sitenats.Msg) {
		got, err := contract.DecodeCommand(m.Data)
		if err != nil {
			t.Errorf("the EN cannot decode the command: %v", err)
		}
		received <- got
		if err := m.Reply(encode(t, rejected)); err != nil {
			t.Errorf("Reply: %v", err)
		}
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Stop()
	f.sync()

	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	ack, err := f.link.Send(ctx, host1, cmd)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !reflect.DeepEqual(ack, rejected) {
		t.Errorf("Send = %+v, want %+v", ack, rejected)
	}
	if got := await(t, received, "the command"); !reflect.DeepEqual(got, cmd) {
		t.Errorf("the EN received %+v, want %+v", got, cmd)
	}
}

// SPEC §11.2: every way a command gets no valid ack is an error, which the authority handles as
// "not acknowledged".
func TestSendFailsWithoutAValidAck(t *testing.T) {
	f := newFixture(t)
	cmd := contract.Command{CommandID: uuid.New(), Action: contract.ActionRemove, DeploymentID: idA, Digest: digest}
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	t.Run("no EN is subscribed", func(t *testing.T) {
		if ack, err := f.link.Send(ctx, host2, cmd); !errors.Is(err, sitenats.ErrNoReply) {
			t.Errorf("Send = %+v, %v; want sitenats.ErrNoReply", ack, err)
		}
	})
	t.Run("the host ID cannot go into a subject", func(t *testing.T) {
		if ack, err := f.link.Send(ctx, "host.3", cmd); !errors.Is(err, sitenats.ErrDotInID) {
			t.Errorf("Send = %+v, %v; want sitenats.ErrDotInID", ack, err)
		}
	})
	t.Run("the command is not valid", func(t *testing.T) {
		bad := cmd
		bad.Action = "restart"
		if ack, err := f.link.Send(ctx, host2, bad); !errors.Is(err, contract.ErrInvalidMessage) {
			t.Errorf("Send = %+v, %v; want contract.ErrInvalidMessage", ack, err)
		}
	})
	t.Run("the reply is not an ack", func(t *testing.T) {
		sub, err := f.en.Subscribe(f.host(host1).Cmd(), func(m sitenats.Msg) {
			if err := m.Reply([]byte(`{"accepted": "yes"}`)); err != nil {
				t.Errorf("Reply: %v", err)
			}
		})
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
		defer sub.Stop()
		f.sync()
		ack, err := f.link.Send(ctx, host1, cmd)
		if !errors.Is(err, contract.ErrInvalidMessage) || !reflect.DeepEqual(ack, contract.CommandAck{}) {
			t.Errorf("Send = %+v, %v; want no ack and contract.ErrInvalidMessage", ack, err)
		}
	})
}

// SPEC §11.2, §16.2: the LO publishes `site.<s>.inventory.request` on every (re)connect.
func TestInventoryRequestOnEveryConnect(t *testing.T) {
	f := newFixture(t) // has waited for the request of the first connect
	never(t, f.requests, "a second request while connected")

	f.srv.Stop()
	f.srv.Start()
	await(t, f.requests, "the inventory request after the server came back")
	await(t, f.enConnected, "the EN's connection after the server came back")

	// The subscriptions are kept across the reconnect.
	f.publish(f.host(host1).Inventory(), encode(t, inventory(host1)))
	await(t, f.h.inventories, "an inventory after the reconnect")
}

// ADR 0020: once Stop has returned, the handler is not called again.
func TestStopEndsTheHandlersCalls(t *testing.T) {
	f := newFixture(t)
	f.publish(f.host(host1).Inventory(), encode(t, inventory(host1)))
	await(t, f.h.inventories, "the inventory before Stop")

	f.link.Stop()
	f.link.Stop() // more than once is fine
	f.publish(f.host(host1).Inventory(), encode(t, inventory(host1)))
	f.publish(f.host(host1).Status(), encode(t, contract.ComponentStatusEvent{DeploymentID: idA, Digest: digest, State: contract.StateRemoved, At: at}))
	never(t, f.h.inventories, "an inventory after Stop")
	never(t, f.h.statuses, "a status event after Stop")
}

// Stop before Listen does nothing, and Listen twice is refused: the connection has one OnConnect.
func TestListenOnce(t *testing.T) {
	site, err := sitenats.ForSite(siteID)
	if err != nil {
		t.Fatalf("ForSite: %v", err)
	}
	srv := natstest.Start(t, natstest.Options{})
	link := sitelink.New(dial(t, srv, slog.New(slog.DiscardHandler)), site, nil)
	link.Stop()
	h := &handler{inventories: make(chan gotInventory, 1), statuses: make(chan gotStatus, 1)}
	if err := link.Listen(context.Background(), h); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer link.Stop()
	if err := link.Listen(context.Background(), h); err == nil {
		t.Error("a second Listen succeeded, want an error")
	}
}
