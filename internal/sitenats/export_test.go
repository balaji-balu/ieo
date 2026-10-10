package sitenats

import "time"

// SetDrainTimeout changes how long Subscription.Stop lets the messages already received be
// handled; a tier always uses 5 seconds (ADR 0020). Call it before Subscribe.
func (c *Conn) SetDrainTimeout(d time.Duration) { c.drainTimeout = d }
