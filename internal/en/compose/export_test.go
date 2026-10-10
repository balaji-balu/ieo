package compose

import "time"

// SetKillGrace shortens how long Up lets Compose run past a start timeout, for the duration of a
// test.
func SetKillGrace(d time.Duration) (restore func()) {
	old := killGrace
	killGrace = d
	return func() { killGrace = old }
}
