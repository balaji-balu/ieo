// Package platformtest provides fakes of the platform seams for deterministic tests (SPEC §17,
// G-F4).
package platformtest

import (
	"slices"
	"sync"
	"time"

	"github.com/balaji-balu/ieo/internal/platform"
)

// FakeClock is a platform.Clock whose time moves only when Advance is called. It is safe for
// concurrent use.
type FakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer // pending timers
}

// NewFakeClock returns a FakeClock set to start.
func NewFakeClock(start time.Time) *FakeClock {
	return &FakeClock{now: start}
}

// Now returns the fake current time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// NewTimer returns a timer that fires when the clock reaches now+d; at once if d <= 0.
func (c *FakeClock) NewTimer(d time.Duration) platform.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{clock: c, deadline: c.now.Add(d), ch: make(chan time.Time, 1)}
	if d <= 0 {
		t.ch <- c.now
		return t
	}
	c.timers = append(c.timers, t)
	return t
}

// Advance moves the clock forward by d and fires, in deadline order, every pending timer whose
// deadline has been reached. Each fired timer receives its own deadline.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	pending := c.timers[:0]
	var due []*fakeTimer
	for _, t := range c.timers {
		if t.deadline.After(c.now) {
			pending = append(pending, t)
		} else {
			due = append(due, t)
		}
	}
	c.timers = pending
	slices.SortFunc(due, func(a, b *fakeTimer) int { return a.deadline.Compare(b.deadline) })
	for _, t := range due {
		t.ch <- t.deadline // buffered and sent at most once: never blocks
	}
}

type fakeTimer struct {
	clock    *FakeClock
	deadline time.Time
	ch       chan time.Time
}

func (t *fakeTimer) C() <-chan time.Time { return t.ch }

func (t *fakeTimer) Stop() bool {
	c := t.clock
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, p := range c.timers {
		if p == t {
			c.timers = append(c.timers[:i], c.timers[i+1:]...)
			return true
		}
	}
	return false
}
