package agent_test

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
	"github.com/balaji-balu/ieo/internal/en/agent"
	"github.com/balaji-balu/ieo/internal/en/exec"
	"github.com/balaji-balu/ieo/internal/platform"
	"github.com/balaji-balu/ieo/internal/platform/platformtest"
	"github.com/balaji-balu/ieo/internal/sitenats"
	"github.com/balaji-balu/ieo/internal/sitenats/natstest"
)

const (
	siteID = contract.SiteID("site-1")
	hostID = contract.HostID("host-1")

	interval = time.Minute
	// deadline is how long a test waits for something that must happen.
	deadline = 10 * time.Second
	// notYet is how long a test watches for something that must not happen.
	notYet = 100 * time.Millisecond
	// reconnectWait keeps the tests short; a tier uses 2 seconds (ADR 0020).
	reconnectWait = 20 * time.Millisecond
)

var (
	idA    = uuid.MustParse("0a0a0a0a-0000-4000-8000-00000000000a")
	digest = contract.DigestOf([]byte("a deployment"))
	start  = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
)

// The two sides the EN joins with an Agent (cmd/en): the executor reports through it, and it
// gives the dispatcher the commands.
var (
	_ exec.Publisher = (*agent.Agent)(nil)
	_ agent.Commands = (*exec.Dispatcher)(nil)
)

// lockedBuffer collects the log lines of the Agent and of its connection.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
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

// commands is the dispatcher's side: it keeps each message and answers as the test says.
type commands struct {
	got chan []byte

	mu    sync.Mutex
	ack   contract.CommandAck
	reply bool
}

func (c *commands) Handle(data []byte) (contract.CommandAck, bool) {
	c.got <- data
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ack, c.reply
}

func (c *commands) answer(ack contract.CommandAck, reply bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ack, c.reply = ack, reply
}

// source is the executor's side of the Inventory. Each one it builds is a second later than the
// one before. With gated set, a build reports itself on building and waits for release.
type source struct {
	building, release chan struct{}

	mu    sync.Mutex
	built int
	err   error
	gated bool
}

func (s *source) inventory(ctx context.Context) (contract.Inventory, error) {
	s.mu.Lock()
	gated, err := s.gated, s.err
	s.built++
	n := s.built
	s.mu.Unlock()
	if gated {
		s.building <- struct{}{}
		select {
		case <-s.release:
		case <-ctx.Done():
			return contract.Inventory{}, ctx.Err()
		}
	}
	if err != nil {
		return contract.Inventory{}, err
	}
	return contract.Inventory{HostID: hostID, At: start.Add(time.Duration(n) * time.Second), Deployments: []contract.InventoryDeployment{{
		DeploymentID: idA, Digest: digest,
		Components: []contract.ComponentStatus{{Name: "web", State: contract.StateInstalled}},
	}}}, nil
}

func (s *source) set(gated bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gated, s.err = gated, err
}

func (s *source) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.built
}

// notifyClock is a FakeClock that reports each timer it makes, so a test advances time only once
// the Agent is waiting.
type notifyClock struct {
	*platformtest.FakeClock
	timers chan time.Duration
}

func (c notifyClock) NewTimer(d time.Duration) platform.Timer {
	t := c.FakeClock.NewTimer(d)
	c.timers <- d
	return t
}

// fixture is an Agent on a connection of its own, and a second connection that plays the LO.
type fixture struct {
	t     *testing.T
	srv   *natstest.Server
	site  sitenats.Site
	host  sitenats.Host
	conn  *sitenats.Conn
	agent *agent.Agent
	logs  *lockedBuffer
	cmds  *commands
	src   *source
	clock notifyClock
	// inventories gets every Inventory the Agent publishes. The subscription is on the Agent's own
	// connection, made before Listen: the server sends a connection what it publishes itself,
	// after the subscriptions that connection made earlier. So none is missed across a reconnect,
	// and an Inventory received also says that the server knows the Agent's subscriptions.
	inventories chan contract.Inventory
	lo          *sitenats.Conn
}

// syncSubject is a subject the LO's connection answers itself (fixture.sync).
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

// newFixture returns an Agent that is not listening yet, on a server that runs only if up is set.
func newFixture(t *testing.T, up bool) *fixture {
	t.Helper()
	site, err := sitenats.ForSite(siteID)
	if err != nil {
		t.Fatalf("ForSite: %v", err)
	}
	host, err := site.Host(hostID)
	if err != nil {
		t.Fatalf("Host: %v", err)
	}
	f := &fixture{
		t: t, srv: natstest.New(t, natstest.Options{}), site: site, host: host, logs: &lockedBuffer{},
		cmds:        &commands{got: make(chan []byte, 16)},
		src:         &source{building: make(chan struct{}, 16), release: make(chan struct{})},
		clock:       notifyClock{platformtest.NewFakeClock(start), make(chan time.Duration, 64)},
		inventories: make(chan contract.Inventory, 16),
	}
	if up {
		f.srv.Start()
	}
	log := slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})).
		With(slog.String("site_id", string(siteID)), slog.String("host_id", string(hostID)))
	f.conn = dial(t, f.srv, log)
	echo, err := f.conn.Subscribe(host.Inventory(), func(m sitenats.Msg) {
		inv, err := contract.DecodeSiteMessage[contract.Inventory](m.Data)
		if err != nil {
			t.Errorf("the inventory is not one the LO would accept: %v", err)
		}
		f.inventories <- inv
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	t.Cleanup(echo.Stop)
	f.agent = agent.New(f.conn, site, host, log)
	t.Cleanup(f.agent.Stop)
	return f
}

// listen starts the Agent with ctx.
func (f *fixture) listen(ctx context.Context) {
	f.t.Helper()
	if err := f.agent.Listen(ctx, f.cmds, f.src.inventory, agent.Config{Interval: interval, Clock: f.clock}); err != nil {
		f.t.Fatalf("Listen: %v", err)
	}
}

// listening returns an Agent that listens on a running server, once it has published the
// Inventory of its first connect, and with a connection for the LO.
func listening(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t, true)
	f.listen(context.Background())
	await(t, f.inventories, "the inventory of the first connect")
	f.lo = dial(t, f.srv, slog.New(slog.DiscardHandler))
	answer, err := f.lo.Subscribe(syncSubject, func(m sitenats.Msg) {
		if err := m.Reply(nil); err != nil {
			t.Errorf("Reply: %v", err)
		}
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	t.Cleanup(answer.Stop)
	connected := make(chan struct{}, 16)
	f.lo.OnConnect(func() { connected <- struct{}{} })
	await(t, connected, "the LO's connection")
	return f
}

// sync returns once the server knows every subscription the LO's connection has made: a
// connection's messages reach the server in order, so the answer to a request it sends itself
// comes after them.
func (f *fixture) sync() {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	if _, err := f.lo.Request(ctx, syncSubject, nil); err != nil {
		f.t.Fatalf("sync the LO's connection: %v", err)
	}
}

// request sends data on the host's command subject as the LO does, and returns the reply.
func (f *fixture) request(data []byte, wait time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	return f.lo.Request(ctx, f.host.Cmd(), data)
}

// logged waits for a log line with message msg and returns the first.
func (f *fixture) logged(msg string) map[string]any {
	f.t.Helper()
	for end := time.Now().Add(deadline); ; time.Sleep(time.Millisecond) {
		if lines := f.logs.lines(f.t, msg); len(lines) > 0 {
			return lines[0]
		}
		if time.Now().After(end) {
			f.t.Fatalf("no log line %q within %v", msg, deadline)
		}
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

// SPEC §17.6: "Inventory is published on start and on every NATS reconnect."
//
// "On start" is when the EN is first connected: an EN that starts before the server keeps trying
// (SPEC §11.2), and what it publishes meanwhile is not kept.
func TestSpec_17_6_InventoryIsPublishedOnStartAndOnEveryNATSReconnect(t *testing.T) {
	f := newFixture(t, false)
	f.listen(context.Background())
	never(t, f.inventories, "an inventory before the server is up")

	f.srv.Start()
	first := await(t, f.inventories, "the inventory of the first connect")
	if first.HostID != hostID || len(first.Deployments) != 1 || first.Deployments[0].DeploymentID != idA {
		t.Errorf("the inventory of the first connect = %+v, want what the executor returned", first)
	}
	never(t, f.inventories, "a second inventory while connected")

	for _, again := range []string{"the first reconnect", "the second reconnect"} {
		f.srv.Stop()
		f.srv.Start()
		inv := await(t, f.inventories, "the inventory of "+again)
		if !inv.At.After(first.At) {
			t.Errorf("the inventory of %s has at %v, want one built after the connect, later than %v", again, inv.At, first.At)
		}
		never(t, f.inventories, "a second inventory after "+again)
	}

	// An EN that starts with the server up publishes at once.
	listening(t)
}

// SPEC §8.9, §11.2: the ack of a command goes to the reply subject of its message, and a message
// the dispatcher answers with nothing gets no reply.
func TestCommandIsAckedOnItsReplySubject(t *testing.T) {
	f := listening(t)
	accepted := contract.CommandAck{CommandID: uuid.MustParse("0c0c0c0c-0000-4000-8000-00000000000c"), Accepted: true}
	rejected := contract.CommandAck{CommandID: accepted.CommandID, Error: &contract.AckError{Code: contract.CodeInvalidCommand, Message: "no"}}

	for _, want := range []contract.CommandAck{accepted, rejected} {
		f.cmds.answer(want, true)
		sent := []byte(`{"a":"message of the command subject"}`)
		reply, err := f.request(sent, deadline)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		if got := await(t, f.cmds.got, "the message at the dispatcher"); string(got) != string(sent) {
			t.Errorf("the dispatcher got %q, want %q", got, sent)
		}
		ack, err := contract.DecodeSiteMessage[contract.CommandAck](reply)
		if err != nil || !reflect.DeepEqual(ack, want) {
			t.Errorf("reply = %+v, %v; want %+v", ack, err, want)
		}
	}

	f.cmds.answer(contract.CommandAck{}, false)
	if reply, err := f.request([]byte("not a command"), notYet); !errors.Is(err, sitenats.ErrNoReply) {
		t.Errorf("request the dispatcher answers with nothing = %q, %v; want ErrNoReply", reply, err)
	}
	await(t, f.cmds.got, "the message at the dispatcher")
}

// An ack that cannot be sent is logged with its command, never with the message. Here the message
// was published, not requested, so there is nowhere to reply to; the dispatcher has taken the
// command all the same, as it does when the connection is lost before the reply.
func TestAckThatCannotBeSentIsLogged(t *testing.T) {
	f := listening(t)
	ack := contract.CommandAck{CommandID: uuid.MustParse("0c0c0c0c-0000-4000-8000-00000000000c"), Accepted: true}
	f.cmds.answer(ack, true)
	if err := f.lo.Publish(f.host.Cmd(), []byte(`{"a":"message with no reply subject"}`)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	await(t, f.cmds.got, "the message at the dispatcher")
	l := f.logged("command ack not sent")
	if l["level"] != "WARN" || l["site_id"] != "site-1" || l["host_id"] != "host-1" || l["command_id"] != ack.CommandID.String() || l["reason"] == nil {
		t.Errorf("log line for the ack that was not sent: %v", l)
	}
	if strings.Contains(f.logs.String(), "message with no reply subject") {
		t.Error("a log line holds the message")
	}
}

// SPEC §11.2: every EN answers `site.<s>.inventory.request` by publishing its Inventory.
func TestInventoryRequestPublishesAnInventory(t *testing.T) {
	f := listening(t)
	if err := f.lo.Publish(f.site.InventoryRequest(), encode(t, contract.InventoryRequest{})); err != nil {
		t.Fatalf("publish the request: %v", err)
	}
	await(t, f.inventories, "the inventory the LO asked for")
	never(t, f.inventories, "a second inventory for one request")
}

// Inventories are published one at a time, and one answers every reason that came up while the one
// before it was built: a burst of requests does not get an Inventory each.
func TestReasonsWhileAnInventoryIsBuiltGetOneInventory(t *testing.T) {
	f := listening(t)
	f.src.set(true, nil)
	f.agent.InventoryDue()
	await(t, f.src.building, "the first inventory to be built")
	for range 5 {
		f.agent.InventoryDue()
	}
	never(t, f.src.building, "a second inventory built while the first is")

	f.src.release <- struct{}{}
	first := await(t, f.inventories, "the first inventory")
	await(t, f.src.building, "the inventory for the reasons that came meanwhile")
	f.src.release <- struct{}{}
	second := await(t, f.inventories, "the second inventory")
	if !second.At.After(first.At) {
		t.Errorf("inventories published with at %v and then %v, want them in the order of their at", first.At, second.At)
	}
	never(t, f.src.building, "a third inventory")
}

// SPEC §11.2, §16.7: the Inventory is published every `en.inventory.interval`.
func TestInventoryEveryInterval(t *testing.T) {
	f := listening(t)
	if d := await(t, f.clock.timers, "the interval's timer"); d != interval {
		t.Fatalf("the timer waits %v, want %v", d, interval)
	}
	f.clock.Advance(interval - time.Nanosecond)
	never(t, f.inventories, "an inventory before the interval has passed")
	f.clock.Advance(time.Nanosecond)
	await(t, f.inventories, "the inventory of the first interval")

	if d := await(t, f.clock.timers, "the next interval's timer"); d != interval {
		t.Errorf("the next timer waits %v, want %v", d, interval)
	}
	f.clock.Advance(interval)
	await(t, f.inventories, "the inventory of the second interval")
}

// SPEC §11.2: a status event goes to the host's status subject, as a message the LO accepts.
func TestStatusEventGoesToTheHostsStatusSubject(t *testing.T) {
	f := listening(t)
	statuses := make(chan []byte, 4)
	sub, err := f.lo.Subscribe(f.host.Status(), func(m sitenats.Msg) { statuses <- m.Data })
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Stop()
	f.sync()

	ev := contract.ComponentStatusEvent{DeploymentID: idA, Digest: digest, Component: "web", State: contract.StateFailed,
		Error: &contract.StatusError{Code: contract.CodePullFailed, Source: "web", Message: "registry unreachable"}, At: start}
	if err := f.agent.Publish(context.Background(), ev); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	got, err := contract.DecodeSiteMessage[contract.ComponentStatusEvent](await(t, statuses, "the status event"))
	if err != nil || !reflect.DeepEqual(got, ev) {
		t.Errorf("the LO received %+v, %v; want %+v", got, err, ev)
	}

	// An event the LO would drop is not sent.
	if err := f.agent.Publish(context.Background(), contract.ComponentStatusEvent{DeploymentID: idA}); !errors.Is(err, contract.ErrInvalidMessage) {
		t.Errorf("Publish of an event that is not valid = %v, want ErrInvalidMessage", err)
	}
	never(t, statuses, "an event that is not valid")
}

// ADR 0020: a message the EN cannot send while it is not connected is not kept for later. It is
// logged, and the Inventory of the next connect carries the state.
func TestNothingIsKeptWhileDisconnected(t *testing.T) {
	f := newFixture(t, false)
	f.listen(context.Background())

	ev := contract.ComponentStatusEvent{DeploymentID: idA, Digest: digest, Component: "web", State: contract.StateInstalled, At: start}
	if err := f.agent.Publish(context.Background(), ev); !errors.Is(err, sitenats.ErrNotConnected) {
		t.Errorf("Publish while not connected = %v, want ErrNotConnected", err)
	}
	f.agent.InventoryDue()
	if l := f.logged("inventory not published"); l["level"] != "WARN" || l["site_id"] != "site-1" || l["host_id"] != "host-1" || l["reason"] == nil {
		t.Errorf("log line for the inventory that was not sent: %v", l)
	}

	f.srv.Start()
	await(t, f.inventories, "the inventory of the first connect")
	never(t, f.inventories, "the inventory that could not be sent, sent later")
}

// An Inventory the executor cannot build is logged; the next reason publishes one.
func TestInventoryThatCannotBeBuiltIsLogged(t *testing.T) {
	f := listening(t)
	f.src.set(false, errors.New("store is closed"))
	f.agent.InventoryDue()
	if l := f.logged("inventory not published"); l["level"] != "ERROR" || l["reason"] != "store is closed" {
		t.Errorf("log line for the inventory that was not built: %v", l)
	}
	never(t, f.inventories, "an inventory that was not built")

	f.src.set(false, nil)
	f.agent.InventoryDue()
	await(t, f.inventories, "the next inventory")
}

// SPEC §16.7: once Stop has returned, the dispatcher gets no more commands and no Inventory is
// built; a status event of a command still in flight can still be sent.
func TestStopEndsTheCalls(t *testing.T) {
	f := listening(t)
	f.cmds.answer(contract.CommandAck{CommandID: uuid.New(), Accepted: true}, true)
	f.agent.Stop()
	f.agent.Stop() // more than once is fine
	built := f.src.count()

	if reply, err := f.request([]byte("{}"), notYet); !errors.Is(err, sitenats.ErrNoReply) {
		t.Errorf("request after Stop = %q, %v; want ErrNoReply", reply, err)
	}
	never(t, f.cmds.got, "a command after Stop")
	f.agent.InventoryDue()
	f.clock.Advance(interval)
	never(t, f.inventories, "an inventory after Stop")
	if n := f.src.count(); n != built {
		t.Errorf("%d inventories were built after Stop", n-built)
	}

	ev := contract.ComponentStatusEvent{DeploymentID: idA, Digest: digest, State: contract.StateRemoved, At: start}
	if err := f.agent.Publish(context.Background(), ev); err != nil {
		t.Errorf("Publish after Stop: %v", err)
	}
}

// The Agent publishes no Inventory once the context given to Listen has ended.
func TestNoInventoryAfterTheContextEnds(t *testing.T) {
	f := newFixture(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	f.listen(ctx)
	await(t, f.inventories, "the inventory of the first connect")

	cancel()
	f.agent.InventoryDue()
	never(t, f.inventories, "an inventory after the context ended")
	f.agent.Stop() // returns: the Agent's goroutine has ended
}

// Stop before Listen does nothing, Listen twice is refused, as the connection has one OnConnect,
// and so is an interval that is not positive.
func TestListenOnce(t *testing.T) {
	f := newFixture(t, true)
	f.agent.Stop()
	if err := f.agent.Listen(context.Background(), f.cmds, f.src.inventory, agent.Config{Clock: f.clock}); err == nil {
		t.Fatal("Listen without an interval succeeded")
	}
	f.listen(context.Background())
	await(t, f.inventories, "the inventory of the first connect")
	if err := f.agent.Listen(context.Background(), f.cmds, f.src.inventory, agent.Config{Interval: interval, Clock: f.clock}); err == nil {
		t.Error("a second Listen succeeded")
	}
	never(t, f.inventories, "an inventory from a second Listen")
}
