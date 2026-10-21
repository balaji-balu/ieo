package outbox_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/lo/coapi"
	"github.com/balaji-balu/ieo/internal/lo/outbox"
	"github.com/balaji-balu/ieo/internal/platform/platformtest"
)

const (
	coURL    = "https://co.test"
	interval = time.Minute
	// deadline bounds every wait for something the test expects to happen.
	deadline = 5 * time.Second
)

var (
	idA   = uuid.MustParse("0a0a0a0a-0000-4000-8000-00000000000a")
	idB   = uuid.MustParse("0b0b0b0b-0000-4000-8000-00000000000b")
	start = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
)

// call is one request the outbox sent, waiting for the test's answer.
type call struct {
	path   string
	auth   string
	status contract.DeploymentStatus
	reply  chan int
}

// fakeCO is an http.RoundTripper that hands every request to the test, which answers it with a
// status code, so the test decides what the CO says and when.
type fakeCO struct {
	calls chan call
}

func (f fakeCO) RoundTrip(req *http.Request) (*http.Response, error) {
	b, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	c := call{path: req.URL.Path, auth: req.Header.Get("Authorization"), reply: make(chan int)}
	if err := json.Unmarshal(b, &c.status); err != nil {
		return nil, err
	}
	select {
	case f.calls <- c:
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
	select {
	case code := <-c.reply:
		return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
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

type fixture struct {
	t     *testing.T
	ctx   context.Context
	co    fakeCO
	clock *platformtest.FakeClock
	token string
	logs  *lockedBuffer
	o     *outbox.Outbox
}

// newFixture returns an outbox whose Run runs until the test ends.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	tok := make([]byte, 32)
	_, _ = rand.Read(tok) // never fails (crypto/rand)
	f := &fixture{
		t: t, co: fakeCO{calls: make(chan call)}, clock: platformtest.NewFakeClock(start),
		token: hex.EncodeToString(tok), logs: &lockedBuffer{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.ctx = ctx
	f.o = outbox.New(outbox.Config{
		Client: coapi.New(coURL, f.token, f.co), RetryInterval: interval, Clock: f.clock,
		Log: slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	done := make(chan error, 1)
	go func() { done <- f.o.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(deadline):
			t.Error("Run did not return after its context ended")
		}
	})
	return f
}

func status(id uuid.UUID, state contract.ComponentState) contract.DeploymentStatus {
	return contract.DeploymentStatus{
		DeploymentID: id, DeviceID: contract.DeviceID{Site: "site-1", Host: "host-1"}, AdoptedManifestVersion: 1,
		Status:     contract.DeploymentState{State: state},
		Components: []contract.ComponentStatus{{Name: "web", State: state}},
	}
}

func (f *fixture) put(s contract.DeploymentStatus) {
	f.o.Put(f.ctx, s, contract.DigestOf([]byte(s.DeploymentID.String())))
}

// next returns the next request the outbox sends.
func (f *fixture) next() call {
	f.t.Helper()
	select {
	case c := <-f.co.calls:
		return c
	case <-time.After(deadline):
		f.t.Fatal("timed out waiting for the outbox to send a status")
		panic("unreachable")
	}
}

// expect waits for the next request, checks that it sends want, and answers it with code.
func (f *fixture) expect(want contract.DeploymentStatus, code int) {
	f.t.Helper()
	c := f.next()
	wantPath := "/api/v1/deployments/" + want.DeploymentID.String() + "/status"
	if c.path != wantPath || c.status.DeploymentID != want.DeploymentID || c.status.Status.State != want.Status.State {
		f.t.Errorf("sent %s %s %s, want %s %s", c.path, c.status.DeploymentID, c.status.Status.State, wantPath, want.Status.State)
	}
	if c.auth != "Bearer "+f.token {
		f.t.Error("the request does not carry the site token") // never print the token
	}
	select {
	case c.reply <- code:
	case <-time.After(deadline):
		f.t.Fatal("the outbox stopped waiting for the answer")
	}
}

// A status is POSTed with the site token, and an entry answered 2xx is removed: a later status of
// another deployment is sent alone (SPEC §8.8).
func TestStatusSentAndRemovedAfter2xx(t *testing.T) {
	f := newFixture(t)
	f.put(status(idA, contract.StateInstalling))
	f.expect(status(idA, contract.StateInstalling), http.StatusCreated)
	f.put(status(idB, contract.StateInstalled))
	f.expect(status(idB, contract.StateInstalled), http.StatusOK)
	f.clock.Advance(interval)
	f.put(status(idB, contract.StateFailed))
	f.expect(status(idB, contract.StateFailed), http.StatusOK)
}

// A failed send stays, and is tried again once the retry interval has passed; a new status of
// another deployment does not wait for it (SPEC §8.8).
func TestFailedSendStaysAndIsTriedAgainAfterTheInterval(t *testing.T) {
	f := newFixture(t)
	f.put(status(idA, contract.StateInstalled))
	f.expect(status(idA, contract.StateInstalled), http.StatusServiceUnavailable)
	f.put(status(idB, contract.StateInstalled))
	f.expect(status(idB, contract.StateInstalled), http.StatusCreated)
	f.clock.Advance(interval)
	f.expect(status(idA, contract.StateInstalled), http.StatusCreated)
	// Removed now: the next status goes alone.
	f.put(status(idB, contract.StatePending))
	f.expect(status(idB, contract.StatePending), http.StatusOK)
}

// A newer status replaces an older one; an entry replaced while it is being sent is not removed
// by the 2xx of the older one, and the newer one is sent (SPEC §8.8).
func TestEntryReplacedDuringSendIsNotRemoved(t *testing.T) {
	f := newFixture(t)
	f.put(status(idA, contract.StateInstalling))
	c := f.next()
	f.put(status(idA, contract.StateInstalled)) // while the first is in flight
	c.reply <- http.StatusCreated
	f.expect(status(idA, contract.StateInstalled), http.StatusCreated)
	f.put(status(idB, contract.StateInstalled))
	f.expect(status(idB, contract.StateInstalled), http.StatusCreated)
}

// A failed send is logged at warning with the deployment, its digest and the answer, and never
// with the token (SPEC §13.1, §15.4).
func TestFailedSendIsLogged(t *testing.T) {
	f := newFixture(t)
	f.put(status(idA, contract.StateInstalled))
	f.expect(status(idA, contract.StateInstalled), http.StatusUnprocessableEntity)
	f.put(status(idB, contract.StateInstalled)) // the log line of A is written before B is sent
	f.expect(status(idB, contract.StateInstalled), http.StatusCreated)

	logs := f.logs.String()
	if strings.Contains(logs, f.token) {
		t.Fatal("the token is in the log")
	}
	var found bool
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		if m["msg"] != "status not sent" {
			continue
		}
		found = true
		if m["level"] != "WARN" || m["deployment_id"] != idA.String() || m["digest"] == "" || m["status"] != float64(422) {
			t.Errorf("log line %v", m)
		}
	}
	if !found {
		t.Errorf("no line %q in the log:\n%s", "status not sent", logs)
	}
}
