package waves

import (
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/forge"
)

// LabelClaimer performs the Dispatchable->InProgress label transition
// directly through the issue tracker -- the concrete Claimer both the
// one-shot Dispatch entry point and headless RunContinuous use (issue
// #2938), now that Label/InProgressLabel live on the adapter's
// constructor instead of the engine's Config.
type LabelClaimer struct {
	it              forge.IssueTracker
	label           string
	inProgressLabel string
}

// NewLabelClaimer constructs a LabelClaimer bound to it, with the two
// label names the engine's Config used to carry.
func NewLabelClaimer(it forge.IssueTracker, label, inProgressLabel string) *LabelClaimer {
	return &LabelClaimer{it: it, label: label, inProgressLabel: inProgressLabel}
}

// Claim performs the Dispatchable->InProgress transition. A failure --
// e.g. a stale listing racing a concurrent claimant -- is logged and
// returned as-is; the caller treats a non-nil error as skip, never a
// fatal error to propagate (Claimer's own doc comment, queue.go).
func (c *LabelClaimer) Claim(num string) error {
	if err := c.it.TransitionState(num, forge.Dispatchable, forge.InProgress); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not claim (%s -> %s): %v\n", num, c.label, c.inProgressLabel, err)
		return err
	}
	return nil
}
