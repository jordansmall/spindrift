package localloop

import (
	"fmt"

	"spindrift.dev/launcher/internal/forge/local"
)

// VerdictKind discriminates Verdict's two shapes (issue #1811).
type VerdictKind int

const (
	// VerdictSurfaced reports a broad ticket whose Integration branch
	// Surface just confirmed current in the operator's checkout.
	VerdictSurfaced VerdictKind = iota
	// VerdictHeld reports a broad ticket Surface could not surface this
	// sweep, naming the first unmet gate it hit.
	VerdictHeld
)

// Verdict is Surface's one-line-per-broad-ticket report (issue #1811,
// campaign #1803 C4): every broad ticket a sweep touches gets exactly one,
// so an operator reads why a branch didn't appear instead of spelunking
// Accumulation-repo internals. Kind selects which fields apply: Branch and
// SeamCount for VerdictSurfaced, Held for VerdictHeld. The Console may
// render the same value later instead of String's plain-text line.
type Verdict struct {
	// Parent is the broad ticket's stable Integration-branch key — always
	// the sanitized slug, never the title-derived surfaced name (issue
	// #1811: an edited title must never shift a ticket's identity).
	Parent local.SanitizedParent
	Kind   VerdictKind
	// Branch is the local branch Surface made current — the sanitized
	// parent for a parented ticket, or the sanitized ticket title (falling
	// back to Parent) for a parentless one. Set only for VerdictSurfaced.
	Branch string
	// SeamCount is how many seam issues make up the ticket. Set only for
	// VerdictSurfaced.
	SeamCount int
	// Held names the first unmet gate blocking this ticket, from the
	// closed set Surface always checks in order: open seam, stuck landing,
	// target branch checked out, diverged, never landed. Set only for
	// VerdictHeld.
	Held string
}

// String renders v as Surface's one printed line.
func (v Verdict) String() string {
	if v.Kind == VerdictSurfaced {
		return fmt.Sprintf("surface: %s surfaced → branch %s (%d seams)", v.Parent, v.Branch, v.SeamCount)
	}
	return fmt.Sprintf("surface: %s held — %s", v.Parent, v.Held)
}
