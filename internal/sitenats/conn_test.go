package sitenats_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/balaji-balu/ieo/internal/sitenats"
	"github.com/balaji-balu/ieo/internal/sitenats/natstest"
)

const (
	// deadline is how long a test waits for something that must happen. Nothing waits this long
	// unless it is about to fail.
	deadline = 10 * time.Second
	// notYet is how long a test watches for something that must not have happened yet.
	notYet = 100 * time.Millisecond
	// reconnectWait keeps the tests short; a tier uses 2 seconds (ADR 0020).
	reconnectWait = 20 * time.Millisecond

	// wrongPassword is made up for these tests, like testPassword.
	wrongPassword = "another-made-up-one"
)

// The log lines of a Conn that these tests wait for.
const (
	logConnected     = "connected to the site NATS server"
	logLost          = "lost the connection to the site NATS server"
	logCannotConnect = "cannot connect to the site NATS server"
)

// logs collects the log lines of a Conn, and lets a test wait for one.
type logs struct {
	mu      sync.Mutex
	lines   []string
	changed chan struct{} // closed, and replaced, on every new line
}

func newLogs() *logs { return &logs{changed: make(chan struct{})} }

// Write takes one log line: slog's handlers write each record with one call.
func (l *logs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, string(p))
	close(l.changed)
	l.changed = make(chan struct{})
	return len(p), nil
}

func (l *logs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "")
}

// wait returns the n-th line that holds text, once there is one.
func (l *logs) wait(t *testing.T, text string, n int) string {
	t.Helper()
	timeout := time.After(deadline)
	for {
		l.mu.Lock()
		seen, changed := 0, l.changed
		for _, line := range l.lines {
			if strings.Contains(line, text) {
				if seen++; seen == n {
					l.mu.Unlock()
					return line
				}
			}
		}
		l.mu.Unlock()
		select {
		case <-changed:
		case <-timeout:
			t.Fatalf("no log line %d with %q within %v; logged:\n%s", n, text, deadline, l)
		}
	}
}

// client is a Conn under test, with what it logged and a signal for each OnConnect call.
type client struct {
	*sitenats.Conn
	logs      *logs
	connected chan struct{}
}

// dial connects to srv as the site's user, with whatever edit changes; the Conn is closed when t
// ends.
func dial(t *testing.T, srv *natstest.Server, edit func(*sitenats.Config)) *client {
	t.Helper()
	c := &client{logs: newLogs(), connected: make(chan struct{}, 64)}
	cfg := sitenats.Config{
		URL: srv.URL(), Username: "site-1", Password: testPassword, CAFile: srv.CAFile(),
		Insecure:      true, // a loopback server without TLS, unless the test says otherwise
		Log:           slog.New(slog.NewJSONHandler(c.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		ReconnectWait: reconnectWait,
	}
	if edit != nil {
		edit(&cfg)
	}
	conn, err := sitenats.Connect(cfg)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(conn.Close)
	c.Conn = conn
	conn.OnConnect(func() { c.connected <- struct{}{} })
	return c
}

// await returns the next value of ch, which must come.
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

// none fails if ch holds a value now.
func none[T any](t *testing.T, ch <-chan T, what string) {
	t.Helper()
	select {
	case v := <-ch:
		t.Fatalf("%s: got %v, want nothing", what, v)
	default:
	}
}

// subscribe returns the payloads c receives on subject, and the subscription.
func subscribe(t *testing.T, c *client, subject string) (<-chan string, *sitenats.Subscription) {
	t.Helper()
	got := make(chan string, 64)
	sub, err := c.Subscribe(subject, func(m sitenats.Msg) { got <- string(m.Data) })
	if err != nil {
		t.Fatalf("Subscribe(%s): %v", subject, err)
	}
	return got, sub
}

func publish(t *testing.T, c *client, subject, payload string) {
	t.Helper()
	if err := c.Publish(subject, []byte(payload)); err != nil {
		t.Fatalf("Publish(%s): %v", subject, err)
	}
}

// login is a server that accepts the site's user.
var login = natstest.Options{Username: "site-1", Password: testPassword}

func TestConnectChecksTheConfig(t *testing.T) {
	c, err := sitenats.Connect(sitenats.Config{URL: "nats://127.0.0.1:4222", Username: "site-1", Password: testPassword})
	if !errors.Is(err, sitenats.ErrConfig) || c != nil {
		t.Fatalf("Connect with a nats:// URL and no Insecure = %v, %v; want nil and an ErrConfig", c, err)
	}
}

// SPEC §11.2: "A tier that cannot reach the site NATS server keeps trying, at startup and after a
// connection is lost, and logs each failure. It does not exit".
func TestConnectDoesNotWaitForTheServer(t *testing.T) {
	srv := natstest.New(t, login) // not started
	c := dial(t, srv, nil)
	if err := c.Publish("test.subject", []byte("too early")); !errors.Is(err, sitenats.ErrNotConnected) {
		t.Errorf("Publish before the server is up: error = %v, want ErrNotConnected", err)
	}
	got, _ := subscribe(t, c, "test.subject") // a subscription made now starts with the connection
	c.logs.wait(t, logCannotConnect, 2)       // it tries again
	none(t, c.connected, "OnConnect before the server is up")

	srv.Start()
	await(t, c.connected, "OnConnect once the server is up")
	c.logs.wait(t, logConnected, 1)
	publish(t, c, "test.subject", "hello")
	if m := await(t, got, "the message"); m != "hello" {
		t.Errorf("received %q, want hello: nothing published before the connection may arrive", m)
	}
}

// SPEC §7.6: "Missed messages are recovered by state, never by replay": the tier is told of every
// (re)connect, so the EN can publish its inventory and the LO can ask for it (SPEC §11.2).
func TestOnConnectRunsOnEveryConnect(t *testing.T) {
	srv := natstest.Start(t, login)
	c := dial(t, srv, nil)
	await(t, c.connected, "OnConnect after the first connect")
	for i := 1; i <= 2; i++ {
		srv.Stop()
		c.logs.wait(t, logLost, i)
		none(t, c.connected, "OnConnect while the server is down")
		srv.Start()
		await(t, c.connected, "OnConnect after a reconnect")
	}
	none(t, c.connected, "a further OnConnect")
}

// A tier subscribes first and gives its function after: the function then runs at once if the
// tier is already connected, exactly once for that connect, and an answer to what it publishes
// finds the subscription there. The LO asks for inventory this way (SPEC §16.2).
func TestOnConnectAfterSubscribe(t *testing.T) {
	srv := natstest.Start(t, login)
	logged := newLogs()
	conn, err := sitenats.Connect(sitenats.Config{
		URL: srv.URL(), Username: "site-1", Password: testPassword, Insecure: true, ReconnectWait: reconnectWait,
		Log: slog.New(slog.NewJSONHandler(logged, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Close)
	logged.wait(t, logConnected, 1) // connected before the tier has given its function

	answers := make(chan string, 8)
	if _, err := conn.Subscribe("test.answer", func(m sitenats.Msg) { answers <- string(m.Data) }); err != nil {
		t.Fatal(err)
	}
	calls := make(chan struct{}, 8) // the function runs before OnConnect returns:
	conn.OnConnect(func() {
		calls <- struct{}{}
		if err := conn.Publish("test.answer", []byte("answer")); err != nil {
			t.Errorf("Publish from the function: %v", err)
		}
	})
	if len(calls) != 1 {
		t.Fatalf("the function ran %d times before OnConnect returned, want once", len(calls))
	}
	await(t, answers, "the answer to what the function published")

	srv.Stop()
	logged.wait(t, logLost, 1)
	srv.Start()
	await(t, answers, "the answer after the reconnect")
	if len(calls) != 2 {
		t.Errorf("the function ran %d times over two connects, want twice", len(calls))
	}
}

// SPEC §11.2: "A message a tier cannot send while it is disconnected is not kept for later."
func TestNothingIsKeptWhileDisconnected(t *testing.T) {
	srv := natstest.Start(t, login)
	c := dial(t, srv, nil)
	await(t, c.connected, "OnConnect")
	got, _ := subscribe(t, c, "test.subject")

	srv.Stop()
	c.logs.wait(t, logLost, 1)
	if err := c.Publish("test.subject", []byte("during")); !errors.Is(err, sitenats.ErrNotConnected) {
		t.Errorf("Publish while disconnected: error = %v, want ErrNotConnected", err)
	}
	srv.Start()
	await(t, c.connected, "OnConnect after the reconnect")
	publish(t, c, "test.subject", "after")
	if m := await(t, got, "the message"); m != "after" {
		t.Errorf("first message after the reconnect = %q, want \"after\"", m)
	}
	none(t, got, "another message")
}

// ADR 0020: "A refused login is treated the same way: it is logged and tried again".
func TestRefusedLoginIsRetried(t *testing.T) {
	srv := natstest.Start(t, login)
	c := dial(t, srv, func(cfg *sitenats.Config) { cfg.Password = wrongPassword })
	line := c.logs.wait(t, logCannotConnect, 3)
	if !strings.Contains(strings.ToLower(line), "authorization") {
		t.Errorf("the log line does not give the server's reason: %s", line)
	}
	none(t, c.connected, "OnConnect with a wrong password")
}

// SPEC §17.7: "No credential, key or token appears in logs or status messages." Here: the site's
// NATS password, right or wrong, in what a Conn logs when it connects, is refused, loses the
// server and finds it again (SPEC §15.6).
func TestSpec_17_7_NoNATSPasswordInLogs(t *testing.T) {
	srv := natstest.Start(t, login)
	refused := dial(t, srv, func(cfg *sitenats.Config) { cfg.Password = wrongPassword })
	refused.logs.wait(t, logCannotConnect, 2)

	c := dial(t, srv, nil)
	await(t, c.connected, "OnConnect")
	srv.Stop()
	c.logs.wait(t, logLost, 1)
	c.logs.wait(t, logCannotConnect, 1)
	srv.Start()
	await(t, c.connected, "OnConnect after the reconnect")

	for name, l := range map[string]*logs{"refused": refused.logs, "accepted": c.logs} {
		text := l.String()
		for _, secret := range []string{testPassword, wrongPassword} {
			if strings.Contains(text, secret) {
				t.Errorf("%s connection: the log holds a password:\n%s", name, text)
			}
		}
	}
	// SPEC §15.6: a URL that does not require TLS is named in a warning at startup.
	warning := c.logs.wait(t, "does not require TLS", 1)
	if !strings.Contains(warning, srv.URL()) || !strings.Contains(warning, `"level":"WARN"`) {
		t.Errorf("the warning about a nats:// URL = %s; want a WARN line that names %s", warning, srv.URL())
	}
}

// SPEC §11.2: a Command is a request and a CommandAck its reply; "a command the server cannot
// deliver to any subscriber is handled like one that is not acknowledged" in time.
func TestRequest(t *testing.T) {
	srv := natstest.Start(t, login)
	c := dial(t, srv, nil)
	await(t, c.connected, "OnConnect")
	ctx, cancel := context.WithTimeout(t.Context(), deadline)
	defer cancel()

	_, err := c.Subscribe("test.echo", func(m sitenats.Msg) {
		if err := m.Reply(append([]byte("re: "), m.Data...)); err != nil {
			t.Errorf("Reply: %v", err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply, err := c.Request(ctx, "test.echo", []byte("ping")); err != nil || string(reply) != "re: ping" {
		t.Errorf("Request = %q, %v; want \"re: ping\"", reply, err)
	}

	if _, err := c.Request(ctx, "test.nobody", nil); !errors.Is(err, sitenats.ErrNoReply) {
		t.Errorf("Request with no subscriber: error = %v, want ErrNoReply", err)
	}
	if ctx.Err() != nil {
		t.Fatal("a request with no subscriber waited for the whole deadline")
	}

	if _, err := c.Subscribe("test.silent", func(sitenats.Msg) {}); err != nil {
		t.Fatal(err)
	}
	short, cancelShort := context.WithTimeout(ctx, notYet)
	defer cancelShort()
	if _, err := c.Request(short, "test.silent", nil); !errors.Is(err, sitenats.ErrNoReply) {
		t.Errorf("Request that is not answered in time: error = %v, want ErrNoReply", err)
	}

	cancelled, cancelNow := context.WithCancel(ctx)
	cancelNow()
	if _, err := c.Request(cancelled, "test.echo", nil); !errors.Is(err, context.Canceled) {
		t.Errorf("Request with a cancelled context: error = %v, want context.Canceled", err)
	}

	srv.Stop()
	c.logs.wait(t, logLost, 1)
	if _, err := c.Request(ctx, "test.echo", nil); !errors.Is(err, sitenats.ErrNotConnected) {
		t.Errorf("Request while disconnected: error = %v, want ErrNotConnected", err)
	}
}

func TestReplyToAMessageThatIsNoRequest(t *testing.T) {
	srv := natstest.Start(t, login)
	c := dial(t, srv, nil)
	await(t, c.connected, "OnConnect")
	errs := make(chan error, 1)
	if _, err := c.Subscribe("test.subject", func(m sitenats.Msg) { errs <- m.Reply([]byte("re")) }); err != nil {
		t.Fatal(err)
	}
	publish(t, c, "test.subject", "not a request")
	if err := await(t, errs, "Reply's error"); !errors.Is(err, sitenats.ErrNoReplySubject) {
		t.Errorf("Reply = %v, want ErrNoReplySubject", err)
	}
}

// SPEC §15.6: "they use a `tls://` NATS URL and verify the server's certificate against
// `lo.nats.ca_file`/`en.nats.ca_file` when set".
func TestTLSServerIsVerified(t *testing.T) {
	secure := natstest.Options{Username: "site-1", Password: testPassword, TLS: true}
	srv := natstest.Start(t, secure)
	otherCA := natstest.New(t, secure).CAFile() // a certificate authority that did not sign srv's certificate

	t.Run("its own CA", func(t *testing.T) {
		c := dial(t, srv, func(cfg *sitenats.Config) { cfg.Insecure = false })
		await(t, c.connected, "OnConnect")
	})
	t.Run("another CA", func(t *testing.T) {
		c := dial(t, srv, func(cfg *sitenats.Config) { cfg.Insecure, cfg.CAFile = false, otherCA })
		if line := c.logs.wait(t, logCannotConnect, 2); !strings.Contains(line, "certificate") {
			t.Errorf("the log line does not say the certificate was refused: %s", line)
		}
		none(t, c.connected, "OnConnect to a server whose certificate another CA signed")
	})
	t.Run("the system roots", func(t *testing.T) {
		c := dial(t, srv, func(cfg *sitenats.Config) { cfg.Insecure, cfg.CAFile = false, "" })
		c.logs.wait(t, logCannotConnect, 2)
		none(t, c.connected, "OnConnect to a server whose certificate no system root signed")
	})
	// Insecure allows a URL without TLS; it never turns verification off (ADR 0020).
	t.Run("a nats:// URL, its own CA", func(t *testing.T) {
		c := dial(t, srv, func(cfg *sitenats.Config) { cfg.URL = "nats://" + srv.Addr() })
		await(t, c.connected, "OnConnect")
	})
	t.Run("a nats:// URL, another CA", func(t *testing.T) {
		c := dial(t, srv, func(cfg *sitenats.Config) { cfg.URL, cfg.CAFile = "nats://"+srv.Addr(), otherCA })
		c.logs.wait(t, logCannotConnect, 2)
		none(t, c.connected, "OnConnect with Insecure to a server whose certificate another CA signed")
	})
}

// SPEC §15.6: "The LO and the EN require TLS": a `tls://` URL never connects without it.
func TestTLSURLRefusesAServerWithoutTLS(t *testing.T) {
	srv := natstest.Start(t, login)
	c := dial(t, srv, func(cfg *sitenats.Config) { cfg.URL, cfg.Insecure = "tls://"+srv.Addr(), false })
	c.logs.wait(t, logCannotConnect, 2)
	none(t, c.connected, "OnConnect over a connection without TLS")
	if err := c.Publish("test.subject", nil); !errors.Is(err, sitenats.ErrNotConnected) {
		t.Errorf("Publish = %v, want ErrNotConnected", err)
	}
}

// ADR 0020: after Stop "no handler of that subscription will run again".
func TestStopEndsDelivery(t *testing.T) {
	srv := natstest.Start(t, login)
	c := dial(t, srv, nil)
	await(t, c.connected, "OnConnect")
	got, sub := subscribe(t, c, "test.subject")
	other, _ := subscribe(t, c, "test.subject") // shows when a message has been delivered

	publish(t, c, "test.subject", "1")
	await(t, got, "message 1")
	await(t, other, "message 1 on the other subscription")
	sub.Stop()
	sub.Stop() // more than once is fine
	publish(t, c, "test.subject", "2")
	await(t, other, "message 2 on the other subscription")
	none(t, got, "a message after Stop")
}

// ADR 0020: "While disconnected … the subscription is removed on the client alone, which needs no
// server".
func TestStopWhileDisconnected(t *testing.T) {
	srv := natstest.Start(t, login)
	c := dial(t, srv, nil)
	await(t, c.connected, "OnConnect")
	got, sub := subscribe(t, c, "test.subject")
	other, _ := subscribe(t, c, "test.subject")

	srv.Stop()
	c.logs.wait(t, logLost, 1)
	stopped := make(chan struct{})
	go func() { sub.Stop(); close(stopped) }()
	await(t, stopped, "Stop while disconnected")

	srv.Start()
	await(t, c.connected, "OnConnect after the reconnect")
	publish(t, c, "test.subject", "after")
	await(t, other, "the message on the other subscription")
	none(t, got, "a message on the stopped subscription")
}

// ADR 0020: Stop returns only once the handler in progress has returned, whether the messages
// already received are handled first (the default) or the 5 seconds for that have run out.
func TestStopWaitsForTheHandler(t *testing.T) {
	for name, drain := range map[string]time.Duration{"within the drain time": 0, "after the drain time": time.Millisecond} {
		t.Run(name, func(t *testing.T) {
			srv := natstest.Start(t, login)
			c := dial(t, srv, nil)
			if drain != 0 {
				c.SetDrainTimeout(drain)
			}
			await(t, c.connected, "OnConnect")
			started, release := make(chan struct{}, 1), make(chan struct{})
			sub, err := c.Subscribe("test.subject", func(sitenats.Msg) {
				started <- struct{}{}
				<-release
			})
			if err != nil {
				t.Fatal(err)
			}
			publish(t, c, "test.subject", "1")
			await(t, started, "the handler")

			stopped := make(chan struct{})
			go func() { sub.Stop(); close(stopped) }()
			select {
			case <-stopped:
				t.Fatal("Stop returned while the handler was running")
			case <-time.After(notYet):
			}
			close(release)
			await(t, stopped, "Stop once the handler has returned")
		})
	}
}

// ADR 0020: "While connected, a subscription is drained: … the messages already received are
// handled."
func TestStopHandlesWhatWasReceived(t *testing.T) {
	srv := natstest.Start(t, login)
	c := dial(t, srv, nil)
	await(t, c.connected, "OnConnect")
	started, release := make(chan struct{}, 1), make(chan struct{})
	handled := make(chan string, 8)
	sub, err := c.Subscribe("test.subject", func(m sitenats.Msg) {
		if string(m.Data) == "1" {
			started <- struct{}{}
			<-release
		}
		handled <- string(m.Data)
	})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := subscribe(t, c, "test.subject")
	for _, payload := range []string{"1", "2", "3"} {
		publish(t, c, "test.subject", payload)
	}
	await(t, started, "the handler")
	for range 3 { // the connection has read all three: 2 and 3 wait behind the handler
		await(t, other, "a message on the other subscription")
	}

	stopped := make(chan struct{})
	go func() { sub.Stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("Stop returned while the handler was running")
	case <-time.After(notYet):
	}
	close(release)
	await(t, stopped, "Stop")
	for _, want := range []string{"1", "2", "3"} {
		select {
		case got := <-handled:
			if got != want {
				t.Fatalf("handled %q, want %q", got, want)
			}
		default:
			t.Fatalf("message %q was received before Stop and not handled", want)
		}
	}
}
