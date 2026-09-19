package waves

import (
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/forge"
)

// LabelClaimer is the Claimer that moves an issue from Dispatchable to
// InProgress through the tracker's labels (issue #2938).
type LabelClaimer struct {
	it              forge.IssueTracker
	label           string
	inProgressLabel string
}

// NewLabelClaimer constructs a LabelClaimer bound to the given tracker.
func NewLabelClaimer(it forge.IssueTracker, label, inProgressLabel string) *LabelClaimer {
	return &LabelClaimer{it: it, label: label, inProgressLabel: inProgressLabel}
}

// Claim moves the issue from Dispatchable to InProgress. The caller must
// treat a non-nil error as a skip, not a fatal error, because a stale
// listing racing a concurrent claimant fails here routinely.
func (c *LabelClaimer) Claim(num string) error {
	if err := c.it.TransitionState(num, forge.Dispatchable, forge.InProgress); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: could not claim (%s -> %s): %v\n", num, c.label, c.inProgressLabel, err)
		return err
	}
	return nil
}
