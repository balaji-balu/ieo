// Package natstest runs a real NATS server inside a test, for code that talks to the site NATS
// server (ADR 0020). The server listens on a loopback port and is stopped when the test ends. Only
// tests import this package, so the server is in no shipped binary.
package natstest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
)

// loopback is the only address the server listens on.
const loopback = "127.0.0.1"

// ready is how long Start waits for the server to accept connections.
const ready = 10 * time.Second

// Options says what the server requires of a client. The zero Options is a server with no
// authentication and no TLS.
type Options struct {
	// Username and Password, if set, are the one login the server accepts (SPEC §15.6).
	Username, Password string
	// TLS makes the server require TLS, with a certificate for 127.0.0.1 signed by a certificate
	// authority made for this server alone; CAFile names it.
	TLS bool
}

// Server is a NATS server on a loopback port of its own. It keeps the port across Stop and Start,
// so a client sees the same server go away and come back.
type Server struct {
	t       testing.TB
	opts    Options
	port    int
	tls     *tls.Config
	caFile  string
	running *server.Server
}

// New returns a server that is not started yet, so that a test can connect to an address where
// nothing answers. It stops with the test.
func New(t testing.TB, o Options) *Server {
	t.Helper()
	s := &Server{t: t, opts: o, port: freePort(t)}
	if o.TLS {
		s.tls, s.caFile = serverTLS(t)
	}
	t.Cleanup(s.Stop)
	return s
}

// Start returns a started server; it is New followed by Start.
func Start(t testing.TB, o Options) *Server {
	t.Helper()
	s := New(t, o)
	s.Start()
	return s
}

// URL returns the server's URL: `tls://127.0.0.1:<port>` if it requires TLS, `nats://…` if not.
func (s *Server) URL() string {
	scheme := "nats"
	if s.opts.TLS {
		scheme = "tls"
	}
	return fmt.Sprintf("%s://%s", scheme, s.Addr())
}

// Addr returns the server's `127.0.0.1:<port>`.
func (s *Server) Addr() string { return net.JoinHostPort(loopback, fmt.Sprint(s.port)) }

// CAFile returns the PEM file of the certificate authority that signed the server's certificate,
// or "" if the server does not use TLS.
func (s *Server) CAFile() string { return s.caFile }

// Start starts the server and returns once it accepts connections. It does nothing if the server
// is running.
func (s *Server) Start() {
	s.t.Helper()
	if s.running != nil {
		return
	}
	srv, err := server.NewServer(&server.Options{
		Host: loopback, Port: s.port,
		Username: s.opts.Username, Password: s.opts.Password,
		TLSConfig: s.tls, TLSTimeout: ready.Seconds(),
		NoLog: true, NoSigs: true,
	})
	if err != nil {
		s.t.Fatalf("natstest: new server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(ready) {
		srv.Shutdown()
		s.t.Fatalf("natstest: the server on %s did not start within %v", s.Addr(), ready)
	}
	s.running = srv
}

// Stop stops the server and returns once it has: every client is disconnected. It does nothing if
// the server is not running.
func (s *Server) Stop() {
	if s.running == nil {
		return
	}
	s.running.Shutdown()
	s.running.WaitForShutdown()
	s.running = nil
}

// freePort returns a loopback port that was free a moment ago.
func freePort(t testing.TB) int {
	t.Helper()
	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "tcp", net.JoinHostPort(loopback, "0"))
	if err != nil {
		t.Fatalf("natstest: find a free port: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// serverTLS makes a certificate authority and a server certificate for 127.0.0.1, both for this
// test alone, and returns the server's TLS configuration and the authority's PEM file. No key is
// kept in the repository (SPEC §15.6).
func serverTLS(t testing.TB) (*tls.Config, string) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("natstest: %v", err)
	}
	validity := func(c *x509.Certificate) *x509.Certificate {
		c.NotBefore, c.NotAfter = time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour)
		return c
	}
	caTemplate := validity(&x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "natstest CA"},
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	})
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("natstest: %v", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("natstest: %v", err)
	}
	template := validity(&x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "natstest server"},
		IPAddresses: []net.IP{net.ParseIP(loopback)},
		KeyUsage:    x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	der, err := x509.CreateCertificate(rand.Reader, template, caTemplate, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("natstest: %v", err)
	}
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600); err != nil {
		t.Fatalf("natstest: %v", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS12,
	}, caFile
}
