package platformtest_test

import (
	"testing"
	"time"

	"github.com/balaji-balu/ieo/internal/platform"
	"github.com/balaji-balu/ieo/internal/platform/platformtest"
)

var _ platform.Clock = (*platformtest.FakeClock)(nil)

var start = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func fired(tm platform.Timer) (time.Time, bool) {
	select {
	case at := <-tm.C():
		return at, true
	default:
		return time.Time{}, false
	}
}

func TestFakeClockNowMovesOnlyOnAdvance(t *testing.T) {
	c := platformtest.NewFakeClock(start)
	if !c.Now().Equal(start) {
		t.Fatalf("Now() = %v, want %v", c.Now(), start)
	}
	c.Advance(90 * time.Second)
	if want := start.Add(90 * time.Second); !c.Now().Equal(want) {
		t.Fatalf("after Advance(90s): Now() = %v, want %v", c.Now(), want)
	}
}

func TestFakeClockTimerFiresAtDeadline(t *testing.T) {
	c := platformtest.NewFakeClock(start)
	tm := c.NewTimer(10 * time.Second)

	c.Advance(9 * time.Second)
	if _, ok := fired(tm); ok {
		t.Fatal("timer fired before its deadline")
	}
	c.Advance(time.Second)
	at, ok := fired(tm)
	if !ok {
		t.Fatal("timer did not fire at its deadline")
	}
	if want := start.Add(10 * time.Second); !at.Equal(want) {
		t.Errorf("timer sent %v, want its deadline %v", at, want)
	}
	if tm.Stop() {
		t.Error("Stop() after firing = true, want false")
	}
	c.Advance(time.Hour)
	if _, ok := fired(tm); ok {
		t.Error("timer fired twice")
	}
}

func TestFakeClockStoppedTimerNeverFires(t *testing.T) {
	c := platformtest.NewFakeClock(start)
	tm := c.NewTimer(time.Second)
	if !tm.Stop() {
		t.Fatal("Stop() on a pending timer = false, want true")
	}
	c.Advance(time.Minute)
	if _, ok := fired(tm); ok {
		t.Fatal("stopped timer fired")
	}
}

func TestFakeClockNonPositiveDurationFiresImmediately(t *testing.T) {
	c := platformtest.NewFakeClock(start)
	for _, d := range []time.Duration{0, -time.Second} {
		if _, ok := fired(c.NewTimer(d)); !ok {
			t.Errorf("NewTimer(%v) did not fire without Advance", d)
		}
	}
}

func TestFakeClockIsSafeForConcurrentUse(t *testing.T) {
	c := platformtest.NewFakeClock(start)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 100 {
			c.NewTimer(time.Second)
			_ = c.Now()
		}
	}()
	for range 100 {
		c.Advance(time.Millisecond)
	}
	<-done
}

func TestSystemClockTimerFires(t *testing.T) {
	tm := platform.SystemClock().NewTimer(0)
	select {
	case <-tm.C():
	case <-time.After(5 * time.Second):
		t.Fatal("system timer with zero duration did not fire")
	}
}
