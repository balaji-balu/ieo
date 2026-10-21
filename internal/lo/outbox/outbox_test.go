package outbox_test

import (
	"bytes"
	"context"
	"errors"
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
	retry   = time.Minute
	wait    = 5 * time.Second        // deadline for an event that must happen
	nothing = 100 * time.Millisecond // short watch that an event has not happened (G-F4)
	digest  = contract.Digest("sha256:1111111111111111111111111111111111111111111111111111111111111111")
)

// post is one PostStatus call; the fake answers it with what is sent on reply.
type post struct {
	status contract.DeploymentStatus
	reply  chan *coapi.RequestError
}

// fakeCO hands every PostStatus call to the test and blocks until the test answers.
type fakeCO struct{ posts chan post }

func (f *fakeCO) PostStatus(ctx context.Context, s contract.DeploymentStatus) *coapi.RequestError {
	p := post{status: s, reply: make(chan *coapi.RequestError, 1)}
	select {
	case f.posts <- p:
	case <-ctx.Done():
		return &coapi.RequestError{Method: http.MethodPost, Err: ctx.Err()}
	}
	select {
	case err := <-p.reply:
		return err
	case <-ctx.Done():
		return &coapi.RequestError{Method: http.MethodPost, Err: ctx.Err()}
	}
}

type fixture struct {
	co    *fakeCO
	clock *platformtest.FakeClock
	box   *outbox.Outbox
	logs  *syncBuffer
}

func start(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		co:    &fakeCO{posts: make(chan post)},
		clock: platformtest.NewFakeClock(time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)),
		logs:  &syncBuffer{},
	}
	f.box = outbox.New(outbox.Config{
		SiteID: "site-a", Client: f.co, RetryInterval: retry, Clock: f.clock,
		Log: slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	return f
}

func (f *fixture) run(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.box.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("Run returned %v, want context.Canceled", err)
			}
		case <-time.After(wait):
			t.Error("Run did not return after its context ended")
		}
	})
}

// next returns the next PostStatus call, failing the test if none comes in time.
func (f *fixture) next(t *testing.T) post {
	t.Helper()
	select {
	case p := <-f.co.posts:
		return p
	case <-time.After(wait):
		t.Fatal("no status was sent")
		return post{}
	}
}

// none fails the test if a status is sent within a short watch.
func (f *fixture) none(t *testing.T) {
	t.Helper()
	select {
	case p := <-f.co.posts:
		t.Fatalf("status of %s sent (state %s), want none", p.status.DeploymentID, p.status.Status.State)
	case <-time.After(nothing):
	}
}

func statusOf(id uuid.UUID, state contract.ComponentState) contract.DeploymentStatus {
	return contract.DeploymentStatus{
		DeploymentID: id, DeviceID: contract.DeviceID{Site: "site-a", Host: "host-1"}, AdoptedManifestVersion: 1,
		Status:     contract.DeploymentState{State: state},
		Components: []contract.ComponentStatus{{Name: "web", State: state}},
	}
}

var (
	idA = uuid.MustParse("00000000-0000-4000-8000-00000000000a")
	idB = uuid.MustParse("00000000-0000-4000-8000-00000000000b")
)

func TestSentStatusIsDroppedAfterTheCOAcceptsIt(t *testing.T) {
	f := start(t)
	f.run(t)
	f.box.Put(context.Background(), statusOf(idA, contract.StateInstalled), digest)
	p := f.next(t)
	if p.status.DeploymentID != idA || p.status.Status.State != contract.StateInstalled {
		t.Fatalf("sent %+v, want the installed status of %s", p.status, idA)
	}
	p.reply <- nil
	f.clock.Advance(retry)
	f.none(t)
}

func TestFailedStatusIsSentAgainAfterTheRetryInterval(t *testing.T) {
	f := start(t)
	f.run(t)
	f.box.Put(context.Background(), statusOf(idA, contract.StateInstalling), digest)
	f.next(t).reply <- &coapi.RequestError{Method: http.MethodPost, Status: http.StatusServiceUnavailable}
	f.none(t) // not before the interval
	f.clock.Advance(retry)
	p := f.next(t)
	if p.status.DeploymentID != idA || p.status.Status.State != contract.StateInstalling {
		t.Fatalf("sent again %+v, want the installing status of %s", p.status, idA)
	}
	p.reply <- nil
	f.clock.Advance(retry)
	f.none(t)
	if !strings.Contains(f.logs.String(), `"deployment_id":"`+idA.String()+`"`) ||
		!strings.Contains(f.logs.String(), `"digest":"`+string(digest)+`"`) {
		t.Fatalf("logs = %s, want the failed send logged with deployment_id and digest", f.logs.String())
	}
}

// SPEC §8.8: "On `2xx` the entry is removed only if it has not been replaced since it was sent."
func TestStatusReplacedWhileSendingIsNotDropped(t *testing.T) {
	f := start(t)
	f.run(t)
	f.box.Put(context.Background(), statusOf(idA, contract.StateInstalling), digest)
	first := f.next(t)
	f.box.Put(context.Background(), statusOf(idA, contract.StateInstalled), digest)
	first.reply <- nil
	p := f.next(t)
	if p.status.Status.State != contract.StateInstalled {
		t.Fatalf("sent %+v next, want the newer installed status", p.status)
	}
	p.reply <- nil
	f.clock.Advance(retry)
	f.none(t)
}

// SPEC §8.8: "The outbox holds at most one entry per deployment; a newer status replaces an older
// one."
func TestOnlyTheLatestStatusOfADeploymentIsSent(t *testing.T) {
	f := start(t)
	f.box.Put(context.Background(), statusOf(idB, contract.StatePending), digest)
	f.box.Put(context.Background(), statusOf(idA, contract.StatePending), digest)
	f.box.Put(context.Background(), statusOf(idA, contract.StateInstalled), digest)
	f.run(t)
	got := map[uuid.UUID]contract.ComponentState{}
	for range 2 {
		p := f.next(t)
		got[p.status.DeploymentID] = p.status.Status.State
		p.reply <- nil
	}
	if got[idA] != contract.StateInstalled || got[idB] != contract.StatePending {
		t.Fatalf("sent %v, want %s installed and %s pending", got, idA, idB)
	}
	f.none(t)
}

// syncBuffer is a bytes.Buffer safe for the concurrent writes of a logger.
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
