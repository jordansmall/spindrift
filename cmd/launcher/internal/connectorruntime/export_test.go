package connectorruntime

import "time"

// SetClock replaces the cooldown's clock.
func (r *Runtime) SetClock(now func() time.Time) { r.now = now }
