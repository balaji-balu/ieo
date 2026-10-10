package sitenats

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// ErrNotConnected is returned, wrapped, when a message cannot be sent because the tier is not
// connected to the server now. The message is not kept for later (SPEC §11.2).
var ErrNotConnected = errors.New("not connected to the site NATS server")

// ErrNoReply is returned, wrapped, when a request got no reply: nothing is subscribed to its
// subject, or the reply did not come before the context's deadline. The two are one outcome for
// the caller (SPEC §11.2).
var ErrNoReply = errors.New("no reply to the request")

// ErrNoReplySubject is returned by Msg.Reply for a message that was not sent as a request.
var ErrNoReplySubject = errors.New("the message asks for no reply")

const (
	// defaultReconnectWait is the time between two attempts to connect (ADR 0020).
	defaultReconnectWait = 2 * time.Second
	// defaultDrainTimeout is how long Subscription.Stop lets the messages already received be
	// handled (ADR 0020).
	defaultDrainTimeout = 5 * time.Second
	// The tier asks the server for a sign of life every pingInterval, and takes the connection
	// for lost when maxPingsOut of them are unanswered: within 30 seconds of a server that went
	// away without closing the connection (ADR 0020).
	pingInterval = 10 * time.Second
	maxPingsOut  = 2
)

// Conn is a tier's connection to the site NATS server (SPEC §11.2, ADR 0020). It keeps itself
// connected: when the server cannot be reached, at the start or later, it tries again every
// Config.ReconnectWait without end and logs each failure, and it calls the function given to
// OnConnect each time it is connected. It keeps no message while it is not connected. It is safe
// for concurrent use.
type Conn struct {
	nc           *nats.Conn
	drainTimeout time.Duration

	// notify is held while the tier's function runs, so its calls do not overlap. connects counts
	// the times the tier was connected, and told is the count the function was last called for.
	notify         sync.Mutex
	onConnect      func()
	connects, told uint64

	// The goroutine of reportRefusals: Close ends it and waits for it.
	closeOnce sync.Once
	closing   chan struct{}
	reporting sync.WaitGroup
}

// Msg is one message received on a subscription.
type Msg struct {
	// Subject is the subject the message arrived on; Site.HostOf reads the host from it.
	Subject string
	// Data is the payload. It is the handler's to keep.
	Data []byte

	conn    *Conn
	replyTo string // where a reply goes; empty if the message is not a request
}

// Reply answers a request with data. It fails with ErrNoReplySubject if the message is not a
// request, and with ErrNotConnected if the connection has gone meanwhile.
func (m Msg) Reply(data []byte) error {
	if m.replyTo == "" {
		return ErrNoReplySubject
	}
	return m.conn.Publish(m.replyTo, data)
}

// Subscription is a subject a Conn receives messages on, until Stop.
type Subscription struct {
	conn   *Conn
	sub    *nats.Subscription
	closed chan struct{} // closed when the client library has ended the subscription
	once   sync.Once

	// mu is held while handle runs, so that taking it waits for a call in progress.
	mu      sync.Mutex
	stopped bool
}

// Connect checks cfg (Config.Check) and returns a connection made with it. It does not wait for
// the server: it returns at once, and the connection is made, and made again whenever it is lost,
// in the background (SPEC §11.2). The error is a *ConfigError. Close the Conn when done.
//
// With a `nats://` URL it logs a warning that names the URL (SPEC §15.6). Nothing it logs, now or
// later, holds the password.
func Connect(cfg Config) (*Conn, error) {
	roots, err := cfg.check()
	if err != nil {
		return nil, err
	}
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	log = log.With(slog.String("nats_url", cfg.URL)) // Check refused a URL that could hold a credential
	wait := cfg.ReconnectWait
	if wait <= 0 {
		wait = defaultReconnectWait
	}
	if u, err := url.Parse(cfg.URL); err == nil && strings.EqualFold(u.Scheme, schemePlain) {
		log.Warn("the site NATS URL does not require TLS: the tier's nats_insecure setting allows it") // SPEC §15.6
	}

	c := &Conn{drainTimeout: defaultDrainTimeout, closing: make(chan struct{})}
	connected := func(*nats.Conn) {
		log.Info("connected to the site NATS server")
		c.connected()
	}
	failed := func(_ *nats.Conn, err error) {
		log.Warn("cannot connect to the site NATS server", slog.String("reason", reason(err)))
	}
	nc, err := nats.Connect(cfg.URL,
		nats.UserInfo(cfg.Username, cfg.Password),
		verifyWith(roots),
		// ADR 0020: never give up, at the start or later, on any failure, a refused login included.
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.IgnoreAuthErrorAbort(),
		nats.ReconnectWait(wait),
		nats.ReconnectJitter(wait/10, wait/10),
		nats.PingInterval(pingInterval),
		nats.MaxPingsOutstanding(maxPingsOut),
		// SPEC §11.2: nothing is kept while disconnected; a publish fails instead.
		nats.ReconnectBufSize(-1),
		nats.ConnectHandler(connected),
		nats.ReconnectHandler(connected),
		nats.ReconnectErrHandler(failed),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			log.Warn("lost the connection to the site NATS server", slog.String("reason", reason(err)))
		}),
		nats.ErrorHandler(func(_ *nats.Conn, sub *nats.Subscription, err error) {
			attrs := []any{slog.String("reason", reason(err))}
			if sub != nil {
				attrs = append(attrs, slog.String("subject", sub.Subject))
			}
			log.Warn("site NATS error", attrs...)
		}),
	)
	if err != nil {
		// With RetryOnFailedConnect an unreachable server is not an error; this is a setting the
		// library refuses although Check accepted it.
		return nil, &ConfigError{Field: FieldURL, Reason: "the NATS client refused the settings: " + reason(err)}
	}
	c.nc = nc // no callback reads it
	c.reporting.Add(1)
	go func() {
		defer c.reporting.Done()
		c.reportRefusals(wait, func(err error) { failed(nc, err) })
	}()
	return c, nil
}

// reportRefusals calls report, once every wait, with the reason the server last refused the tier,
// for as long as the tier is not connected; it returns when the Conn is closed.
//
// The client library reports an attempt that reached no server (ReconnectErrHandler), but tries
// again in silence when a server answered and then refused: a wrong password, a certificate it
// cannot verify, a server without TLS. Without this, such a tier would log one failure and then
// nothing (ADR 0020: each failure is logged). The library keeps the reason of a refusal, and none
// for a server it could not reach, so the two reports do not repeat each other.
func (c *Conn) reportRefusals(wait time.Duration, report func(error)) {
	t := time.NewTicker(wait)
	defer t.Stop()
	for {
		select {
		case <-c.closing:
			return
		case <-t.C:
		}
		if c.nc.IsConnected() {
			continue
		}
		if err := c.nc.LastError(); err != nil {
			report(err)
		}
	}
}

// OnConnect sets the function the Conn calls each time the tier is connected: the first time and
// after every loss (SPEC §7.6, §11.2). Calls do not overlap. Call OnConnect once, after the
// tier's Subscribe calls: if the tier is connected by then, fn is called before OnConnect
// returns, and whatever fn publishes reaches the server after those subscriptions, so no answer
// to it is missed. fn may use the Conn, but must not call OnConnect or Close.
func (c *Conn) OnConnect(fn func()) {
	c.notify.Lock()
	defer c.notify.Unlock()
	c.onConnect = fn
	if fn != nil && c.told < c.connects && c.nc.IsConnected() {
		c.told = c.connects
		fn()
	}
}

// connected runs each time the tier is connected, and calls the tier's function if it has one
// yet; OnConnect calls it for this connect otherwise.
func (c *Conn) connected() {
	c.notify.Lock()
	defer c.notify.Unlock()
	c.connects++
	if c.onConnect != nil {
		c.told = c.connects
		c.onConnect()
	}
}

// verifyWith makes the client verify the server's certificate against roots, or the system roots
// if roots is nil, whenever the connection uses TLS. It does not make TLS required: a `tls://` URL
// does that (SPEC §15.6).
func verifyWith(roots *x509.CertPool) nats.Option {
	return func(o *nats.Options) error {
		o.TLSConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
		return nil
	}
}

// reason returns why the client library or the server says something failed. Neither ever
// repeats the password.
func reason(err error) string {
	if err == nil {
		return "no reason given"
	}
	return err.Error()
}

// sendError turns the client library's error for a message it could not send into this package's.
func sendError(op, subject string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, nats.ErrReconnectBufExceeded), errors.Is(err, nats.ErrDisconnected),
		errors.Is(err, nats.ErrConnectionReconnecting), errors.Is(err, nats.ErrConnectionClosed):
		return fmt.Errorf("%s on %s: %w", op, subject, ErrNotConnected)
	default:
		return fmt.Errorf("%s on %s: %w", op, subject, err)
	}
}

// Publish sends data on subject. It does not wait for the server to take it. While the tier is
// not connected it fails with ErrNotConnected and the message is dropped.
func (c *Conn) Publish(subject string, data []byte) error {
	return sendError("publish", subject, c.nc.Publish(subject, data))
}

// Request sends data on subject and returns the first reply. ctx bounds the wait: give it a
// deadline. It fails with ErrNoReply if nothing is subscribed to subject or no reply came before
// the deadline, with ErrNotConnected if the tier is not connected, and with ctx's error if ctx was
// cancelled.
func (c *Conn) Request(ctx context.Context, subject string, data []byte) ([]byte, error) {
	m, err := c.nc.RequestWithContext(ctx, subject, data)
	switch {
	case err == nil:
		return m.Data, nil
	case errors.Is(err, nats.ErrNoResponders), errors.Is(err, context.DeadlineExceeded):
		return nil, fmt.Errorf("request on %s: %w", subject, ErrNoReply) // SPEC §11.2: one outcome
	default:
		return nil, sendError("request", subject, err)
	}
}

// Subscribe calls handle with each message that arrives on subject, one at a time and in the order
// they arrive, until the Subscription is stopped. subject may hold wildcards. A subscription made
// while the tier is not connected starts when it connects, and every subscription is kept across
// reconnects; messages published while the tier was away are not delivered (SPEC §7.6).
//
// handle must not call Stop on its own Subscription.
func (c *Conn) Subscribe(subject string, handle func(Msg)) (*Subscription, error) {
	s := &Subscription{conn: c, closed: make(chan struct{})}
	sub, err := c.nc.Subscribe(subject, func(m *nats.Msg) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.stopped {
			return
		}
		handle(Msg{Subject: m.Subject, Data: m.Data, conn: c, replyTo: m.Reply})
	})
	if err != nil {
		return nil, fmt.Errorf("subscribe to %s: %w", subject, err)
	}
	sub.SetClosedHandler(func(string) { close(s.closed) })
	s.sub = sub
	return s, nil
}

// Stop ends the subscription and returns once handle will not run again: a call in progress has
// returned and no later one starts (ADR 0020). While connected, the messages the tier had already
// received are handled first, for at most 5 seconds; after that, or while not connected, they are
// dropped. Stop may be called more than once.
func (s *Subscription) Stop() {
	s.once.Do(func() {
		if !s.drain() {
			// Removes the subscription on the client; it needs no server. Its only error is that
			// the subscription or the connection has ended already, which is what Stop wants.
			_ = s.sub.Unsubscribe()
		}
		s.mu.Lock() // waits for a call of handle in progress
		s.stopped = true
		s.mu.Unlock()
	})
}

// drain asks the server to send no more, lets handle take what was already received, and reports
// whether that was done within the drain timeout. It needs the server, so it does nothing while
// the tier is not connected.
func (s *Subscription) drain() bool {
	if !s.conn.nc.IsConnected() || s.sub.Drain() != nil {
		return false
	}
	t := time.NewTimer(s.conn.drainTimeout)
	defer t.Stop()
	select {
	case <-s.closed:
		return true
	case <-t.C:
		return false
	}
}

// Close ends the connection. Stop every Subscription first; a message a handler has not been
// given yet is dropped. Close may be called more than once.
func (c *Conn) Close() {
	c.closeOnce.Do(func() {
		close(c.closing)
		c.reporting.Wait()
		c.nc.Close()
	})
}
