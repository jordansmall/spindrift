package forgejo

import (
	"time"

	"spindrift.dev/launcher/internal/forge"
)

// SetNow swaps the clock behind the undefined-label verdict's expiry.
func SetNow(t forge.IssueTracker, now func() time.Time) { t.(*forgejoClient).now = now }
