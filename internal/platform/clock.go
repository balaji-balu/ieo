// Package platform holds the seams between IEO code and its host: time today; the OCI registry
// and container runtime join in the slices that first use them (roadmap B and D).
package platform

import "time"

// Clock tells the time and makes timers. Code that waits or timestamps takes a Clock so tests can
// replace it with platformtest.FakeClock and stay deterministic (G-F4).
type Clock interface {
	// Now returns the current time.
	Now() time.Time
	// NewTimer returns a timer that sends the time on its channel once, d after now. A
	// non-positive d fires immediately.
	NewTimer(d time.Duration) Timer
}

// Timer is a single-shot timer from a Clock.
type Timer interface {
	// C returns the channel the timer sends its fire time on. It is buffered and receives at most
	// one value.
	C() <-chan time.Time
	// Stop prevents the timer from firing. It reports whether the timer was still pending.
	Stop() bool
}

// SystemClock returns the Clock of the operating system.
func SystemClock() Clock { return systemClock{} }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func (systemClock) NewTimer(d time.Duration) Timer { return systemTimer{time.NewTimer(d)} }

type systemTimer struct{ t *time.Timer }

func (t systemTimer) C() <-chan time.Time { return t.t.C }
func (t systemTimer) Stop() bool          { return t.t.Stop() }
